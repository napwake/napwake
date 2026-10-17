/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
	"github.com/okedeji/hybernate/internal/discovery"
)

// relearnAfter is how long learned dependencies stand before they're
// learned again. A changed pod template or ignore annotation relearns them
// at once; this catches a ConfigMap changed since.
const relearnAfter = time.Hour

// A dependency learned from a wake is forgotten once no request has
// confirmed it for wakeLearnedFor, so a one-off call, such as someone's curl
// from a pod, doesn't tie two workloads together for good. A confirmation
// is written at most once per wakeConfirmEvery, not on every wake.
const (
	wakeLearnedFor   = 14 * 24 * time.Hour
	wakeConfirmEvery = 24 * time.Hour
)

// learnDependencies finds the workloads this one's environment points at,
// the same way the scan does, and records them, so they're held and woken
// like dependsOn without anyone writing it.
func (r *Reconciler) learnDependencies(ctx context.Context, workload *v1alpha1.ManagedWorkload,
	target client.Object) error {
	ignored := ignoredDependencies(workload, target)
	from := learnedFrom(target, ignored)
	now := r.clockTime()
	if l := workload.Status.LearnedDependencies; l != nil && l.From == from && now.Sub(l.At.Time) < relearnAfter {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, dependencyTimeout)
	defer cancel()

	spec := podTemplateOf(target).Spec
	configMaps, err := r.configMapsFor(ctx, workload.Namespace, spec)
	if err != nil {
		return err
	}
	byNamespace := map[string]map[string]corev1.Service{}
	var targets []discovery.Target
	for _, ns := range r.namespacesNamed(workload.Namespace, spec, configMaps) {
		found, err := r.addServices(ctx, ns, byNamespace)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		in, err := r.dependencyTargets(ctx, ns)
		if err != nil {
			return err
		}
		targets = append(targets, in...)
	}

	found, _ := discovery.DependenciesOf(workload.Namespace, workload.Spec.Target.Kind, workload.Spec.Target.Name,
		spec, configMaps, byNamespace, targets)
	learned := make([]v1alpha1.LearnedDependency, 0, len(found))
	for _, d := range found {
		if ignored[d.Namespace+"/"+d.Name] {
			continue
		}
		learned = append(learned, v1alpha1.LearnedDependency{Namespace: d.Namespace, Kind: d.Kind, Name: d.Name,
			Source: v1alpha1.LearnedFromEnvironment, Via: d.Via, Address: d.Address})
	}
	before := workload.Status.LearnedDependencies
	// What wakes taught stays until it goes unconfirmed: the environment
	// can't confirm or deny it.
	stamped := false
	if before != nil {
		for _, d := range before.Dependencies {
			if d.Source != v1alpha1.LearnedFromWake || ignored[d.Namespace+"/"+d.Name] ||
				slices.ContainsFunc(learned, sameTarget(d)) {
				continue
			}
			if d.LastSeen == nil {
				d.LastSeen = &now
				stamped = true
			} else if now.Sub(d.LastSeen.Time) > wakeLearnedFor {
				continue
			}
			learned = append(learned, d)
		}
	}

	workload.Status.LearnedDependencies = &v1alpha1.LearnedDependencies{From: from, At: now, Dependencies: learned}
	changed := before == nil || !sameDependencies(before.Dependencies, learned)
	if !changed && !stamped {
		return nil
	}
	// Written before it's announced: a write that fails is learned again,
	// and would otherwise be announced again.
	if err := r.Status().Update(ctx, workload); err != nil {
		return fmt.Errorf("recording learned dependencies: %w", err)
	}
	if !changed {
		return nil
	}
	r.emitEvent(workload, false, "Normal", ReasonDependenciesLearned, actionLearnDependencies,
		"%s", learnedMessage(learned))
	return nil
}

// configMapsFor reads the ConfigMaps a pod takes variables from, one by one
// and uncached: caching every ConfigMap in the cluster to read a few would
// cost the operator more memory than anything else it holds.
func (r *Reconciler) configMapsFor(ctx context.Context, namespace string, spec corev1.PodSpec) (
	map[string]map[string]string, error) {
	reader := r.PodReader
	if reader == nil {
		reader = r.Client
	}
	out := map[string]map[string]string{}
	for _, name := range discovery.ConfigMapsReferenced(spec) {
		var cm corev1.ConfigMap
		err := reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &cm)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("reading ConfigMap %s/%s: %w", namespace, name, err)
		}
		out[name] = cm.Data
	}
	return out, nil
}

// maxNamedNamespaces bounds how many namespaces an environment can have
// listed one by one. An environment naming more, such as a ConfigMap
// holding a whole config file, has the cluster listed once instead.
const maxNamedNamespaces = 32

// namespacesNamed are the namespaces the pod's environment could address a
// Service in: its own, which a bare name resolves in, and every word of its
// values that could be a namespace, since an address names one as a label
// of its host, as in postgres.data or postgres.data.svc.cluster.local. That
// is more than the addresses name, and never fewer. Nil means every
// namespace. In a namespaced install, only the namespaces it watches can be
// read.
func (r *Reconciler) namespacesNamed(own string, spec corev1.PodSpec, configMaps map[string]map[string]string) []string {
	named := map[string]bool{own: true}
	addWords := func(value string) {
		for word := range strings.FieldsFuncSeq(strings.ToLower(value), func(c rune) bool {
			return (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-'
		}) {
			if len(validation.IsDNS1123Label(word)) == 0 {
				named[word] = true
			}
		}
	}
	for _, c := range append(slices.Clone(spec.InitContainers), spec.Containers...) {
		for _, env := range c.Env {
			addWords(env.Value)
		}
	}
	for _, data := range configMaps {
		for _, value := range data {
			addWords(value)
		}
	}
	if len(r.WatchNamespaces) > 0 {
		for ns := range named {
			if !slices.Contains(r.WatchNamespaces, ns) {
				delete(named, ns)
			}
		}
	} else if len(named) > maxNamedNamespaces {
		return []string{metav1.NamespaceAll}
	}
	return slices.Sorted(maps.Keys(named))
}

// addServices adds the Services in a namespace to byNamespace, and reports
// whether there were any. They're read from the cache without copying:
// dependencies are learned only by reading them.
func (r *Reconciler) addServices(ctx context.Context, namespace string,
	byNamespace map[string]map[string]corev1.Service) (bool, error) {
	var services corev1.ServiceList
	if err := r.List(ctx, &services, client.InNamespace(namespace), client.UnsafeDisableDeepCopy); err != nil {
		return false, fmt.Errorf("listing Services in %q: %w", namespace, err)
	}
	for _, svc := range services.Items {
		if byNamespace[svc.Namespace] == nil {
			byNamespace[svc.Namespace] = map[string]corev1.Service{}
		}
		byNamespace[svc.Namespace][svc.Name] = svc
	}
	return len(services.Items) > 0, nil
}

// dependencyTargets are the Deployments and StatefulSets in a namespace a
// dependency can resolve to.
func (r *Reconciler) dependencyTargets(ctx context.Context, namespace string) ([]discovery.Target, error) {
	var deployments appsv1.DeploymentList
	if err := r.List(ctx, &deployments, client.InNamespace(namespace), client.UnsafeDisableDeepCopy); err != nil {
		return nil, fmt.Errorf("listing Deployments in %q: %w", namespace, err)
	}
	var statefulSets appsv1.StatefulSetList
	if err := r.List(ctx, &statefulSets, client.InNamespace(namespace), client.UnsafeDisableDeepCopy); err != nil {
		return nil, fmt.Errorf("listing StatefulSets in %q: %w", namespace, err)
	}
	targets := make([]discovery.Target, 0, len(deployments.Items)+len(statefulSets.Items))
	for _, d := range deployments.Items {
		targets = append(targets, discovery.Target{Namespace: d.Namespace, Kind: v1alpha1.TargetKindDeployment,
			Name: d.Name, Template: d.Spec.Template})
	}
	for _, s := range statefulSets.Items {
		targets = append(targets, discovery.Target{Namespace: s.Namespace, Kind: v1alpha1.TargetKindStatefulSet,
			Name: s.Name, Template: s.Spec.Template})
	}
	return targets, nil
}

func podTemplateOf(target client.Object) corev1.PodTemplateSpec {
	switch t := target.(type) {
	case *appsv1.Deployment:
		return t.Spec.Template
	case *appsv1.StatefulSet:
		return t.Spec.Template
	}
	return corev1.PodTemplateSpec{}
}

// ignoredDependencies are the dependencies hybernate.io/ignore-dependencies
// names on the workload or its ManagedWorkload, as namespace/name.
func ignoredDependencies(workload *v1alpha1.ManagedWorkload, target client.Object) map[string]bool {
	ignored := map[string]bool{}
	for _, raw := range []string{workload.Annotations[v1alpha1.AnnotationIgnoreDependencies],
		target.GetAnnotations()[v1alpha1.AnnotationIgnoreDependencies]} {
		for entry := range strings.SplitSeq(raw, ",") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			if !strings.Contains(entry, "/") {
				entry = workload.Namespace + "/" + entry
			}
			ignored[entry] = true
		}
	}
	return ignored
}

// learnedFrom identifies the pod template and ignore list dependencies are
// learned from.
func learnedFrom(target client.Object, ignored map[string]bool) string {
	names := make([]string, 0, len(ignored))
	for name := range ignored {
		names = append(names, name)
	}
	slices.Sort(names)
	sum := sha256.Sum256([]byte(podTemplateHash(target) + "\n" + strings.Join(names, ",")))
	return hex.EncodeToString(sum[:8])
}

func sameTarget(d v1alpha1.LearnedDependency) func(v1alpha1.LearnedDependency) bool {
	return func(other v1alpha1.LearnedDependency) bool {
		return other.Namespace == d.Namespace && other.Kind == d.Kind && other.Name == d.Name
	}
}

func sameDependencies(a, b []v1alpha1.LearnedDependency) bool {
	return slices.EqualFunc(a, b, func(x, y v1alpha1.LearnedDependency) bool {
		return x.Namespace == y.Namespace && x.Kind == y.Kind && x.Name == y.Name
	})
}

func learnedMessage(learned []v1alpha1.LearnedDependency) string {
	if len(learned) == 0 {
		return "no dependencies found in its environment"
	}
	parts := make([]string, 0, len(learned))
	for _, d := range learned {
		how := d.Via
		if d.Source == v1alpha1.LearnedFromWake {
			how = "a request that woke it"
		}
		parts = append(parts, fmt.Sprintf("%s/%s (%s)", d.Namespace, d.Name, how))
	}
	return "depends on " + strings.Join(parts, ", ") + "; held and woken with it"
}

// dependencyRefs are the workloads a ManagedWorkload depends on: those its
// dependsOn names, and those Hybernate learned.
func dependencyRefs(workload *v1alpha1.ManagedWorkload) []v1alpha1.DependencyRef {
	refs := slices.Clone(workload.Spec.DependsOn)
	l := workload.Status.LearnedDependencies
	if l == nil {
		return refs
	}
	for _, d := range l.Dependencies {
		ref := v1alpha1.DependencyRef{Namespace: d.Namespace, Kind: d.Kind, Name: d.Name}
		if !slices.ContainsFunc(refs, func(have v1alpha1.DependencyRef) bool {
			return dependencyID(workload, have) == dependencyID(workload, ref)
		}) {
			refs = append(refs, ref)
		}
	}
	return refs
}

// podIPField finds pods by their IP, which the API server indexes.
const podIPField = "status.podIP"

// learnFromWake learns that the workload whose request woke this one
// depends on it. Only a workload Hybernate manages counts: a request through
// an ingress controller comes from the controller's pod, which depends on
// nothing.
func (r *Reconciler) learnFromWake(ctx context.Context, workload *v1alpha1.ManagedWorkload) error {
	ip := workload.Annotations[v1alpha1.AnnotationLastRequestFrom]
	if ip == "" || wakeSource(workload) != v1alpha1.ActivitySourceRequest {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, dependencyTimeout)
	defer cancel()

	pod, found, err := r.podAt(ctx, ip)
	if err != nil || !found {
		return err
	}
	dependent, target, found, err := r.managing(ctx, pod)
	if err != nil || !found || dependent.UID == workload.UID {
		return err
	}
	now := r.clockTime()
	learned := v1alpha1.LearnedDependency{Namespace: workload.Namespace, Kind: workload.Spec.Target.Kind,
		Name: workload.Spec.Target.Name, Source: v1alpha1.LearnedFromWake, LastSeen: &now}
	if ignoredDependencies(dependent, target)[learned.Namespace+"/"+learned.Name] {
		return nil
	}
	l := dependent.Status.LearnedDependencies
	if i := slices.IndexFunc(learnedList(l), sameTarget(learned)); i >= 0 {
		return r.confirmLearnedFromWake(ctx, dependent, i, now)
	}
	if l == nil {
		// No From matches a pod template, so the dependent still learns
		// from its environment on its next reconcile, keeping this.
		l = &v1alpha1.LearnedDependencies{At: now}
		dependent.Status.LearnedDependencies = l
	}
	l.Dependencies = append(l.Dependencies, learned)
	if err := r.Status().Update(ctx, dependent); err != nil {
		return fmt.Errorf("recording that %s/%s depends on %s/%s: %w", dependent.Namespace, dependent.Name,
			learned.Namespace, learned.Name, err)
	}
	r.emitEvent(dependent, false, "Normal", ReasonDependenciesLearned, actionLearnDependencies,
		"depends on %s/%s, learned from a request it sent that woke it; held and woken with it",
		learned.Namespace, learned.Name)
	return nil
}

// confirmLearnedFromWake records that a request confirmed the dependent's
// i-th learned dependency again, which keeps one learned from a wake from
// being forgotten. One learned from the environment needs no confirming.
func (r *Reconciler) confirmLearnedFromWake(ctx context.Context, dependent *v1alpha1.ManagedWorkload, i int,
	now metav1.Time) error {
	d := &dependent.Status.LearnedDependencies.Dependencies[i]
	if d.Source != v1alpha1.LearnedFromWake || d.LastSeen != nil && now.Sub(d.LastSeen.Time) < wakeConfirmEvery {
		return nil
	}
	d.LastSeen = &now
	if err := r.Status().Update(ctx, dependent); err != nil {
		return fmt.Errorf("confirming that %s/%s depends on %s/%s: %w", dependent.Namespace, dependent.Name,
			d.Namespace, d.Name, err)
	}
	return nil
}

func learnedList(l *v1alpha1.LearnedDependencies) []v1alpha1.LearnedDependency {
	if l == nil {
		return nil
	}
	return l.Dependencies
}

// podAt is the pod with the IP, and false for none, or for pods on the
// node's network, which share the node's IP and so can't be told apart.
func (r *Reconciler) podAt(ctx context.Context, ip string) (*corev1.Pod, bool, error) {
	reader := r.PodReader
	if reader == nil {
		reader = r.Client
	}
	// Listing pods across namespaces needs a ClusterRole, which a
	// namespaced install doesn't have; the watched namespaces are searched
	// one by one instead.
	namespaces := r.WatchNamespaces
	if len(namespaces) == 0 {
		namespaces = []string{metav1.NamespaceAll}
	}
	for _, ns := range namespaces {
		var pods corev1.PodList
		if err := reader.List(ctx, &pods, client.InNamespace(ns), client.MatchingFields{podIPField: ip}); err != nil {
			return nil, false, fmt.Errorf("finding the pod at %s: %w", ip, err)
		}
		if p, ok := podWithIP(pods.Items, ip); ok {
			return p, true, nil
		}
	}
	if addr, err := netip.ParseAddr(ip); err != nil || addr.Unmap().Is4() {
		return nil, false, nil
	}
	// A dual-stack pod's IPv6 address, in a cluster whose pods are IPv4
	// first, isn't its status.podIP, and the API server matches
	// status.podIPs only as the whole list. Such a pod is found by reading
	// pods a page at a time.
	for _, ns := range namespaces {
		for page := ""; ; {
			var pods corev1.PodList
			if err := reader.List(ctx, &pods, client.InNamespace(ns), client.Limit(podPageSize),
				client.Continue(page)); err != nil {
				return nil, false, fmt.Errorf("finding the pod at %s: %w", ip, err)
			}
			if p, ok := podWithIP(pods.Items, ip); ok {
				return p, true, nil
			}
			if page = pods.Continue; page == "" {
				break
			}
		}
	}
	return nil, false, nil
}

// podPageSize is how many pods a search by IPv6 address reads at once.
const podPageSize = 500

// podWithIP is the pod in pods with the IP among its addresses that isn't
// on its node's network or being deleted.
func podWithIP(pods []corev1.Pod, ip string) (*corev1.Pod, bool) {
	for i := range pods {
		p := &pods[i]
		if p.Spec.HostNetwork || p.DeletionTimestamp != nil {
			continue
		}
		if p.Status.PodIP == ip || slices.ContainsFunc(p.Status.PodIPs, func(a corev1.PodIP) bool { return a.IP == ip }) {
			return p, true
		}
	}
	return nil, false
}

// managing is the ManagedWorkload whose target the pod belongs to, and the
// target.
func (r *Reconciler) managing(ctx context.Context, pod *corev1.Pod) (*v1alpha1.ManagedWorkload, client.Object,
	bool, error) {
	var list v1alpha1.ManagedWorkloadList
	if err := r.List(ctx, &list, client.InNamespace(pod.Namespace)); err != nil {
		return nil, nil, false, fmt.Errorf("listing managed workloads in %s: %w", pod.Namespace, err)
	}
	for i := range list.Items {
		mw := &list.Items[i]
		target, err := r.getWorkload(ctx, targetID(mw))
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, nil, false, fmt.Errorf("getting the target of %s/%s: %w", mw.Namespace, mw.Name, err)
		}
		if selects(target, pod) {
			return mw, target, true, nil
		}
	}
	return nil, nil, false, nil
}

// selects says the workload's selector picks the pod.
func selects(target client.Object, pod *corev1.Pod) bool {
	var selector *metav1.LabelSelector
	switch t := target.(type) {
	case *appsv1.Deployment:
		selector = t.Spec.Selector
	case *appsv1.StatefulSet:
		selector = t.Spec.Selector
	}
	if selector == nil {
		return false
	}
	s, err := metav1.LabelSelectorAsSelector(selector)
	return err == nil && !s.Empty() && s.Matches(labels.Set(pod.Labels))
}
