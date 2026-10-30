/*
Copyright 2026 The Napwake Authors.

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
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

// ingress-nginx waits proxy-read-timeout for an answer from the doorman,
// then gives up: it tries a GET again, but answers a POST or PATCH 504. So a
// write held through a wake that takes longer fails, though the doorman
// would have held it for wake.maxWait. IngressTimeoutShort says so once a
// wake by a request has taken that long, while the timeout is under
// maxWait: most wakes take seconds, so a timeout under maxWait alone, which
// ingress-nginx's default is, isn't worth a warning.
const (
	conditionIngressTimeoutShort = "IngressTimeoutShort"

	ReasonIngressTimeoutShort  = "IngressTimeoutShort"
	reasonWakeOutlastedTimeout = "WakeOutlastedTimeout"
	reasonTimeoutsCoverMaxWait = "TimeoutsCoverMaxWait"
	reasonNoNginxIngress       = "NoNginxIngress"
	reasonDoormanDisabled      = "DoormanDisabled"

	annotationNginxPrefix      = "nginx.ingress.kubernetes.io/"
	annotationNginxReadTimeout = annotationNginxPrefix + "proxy-read-timeout"

	// nginxReadTimeoutDefault is ingress-nginx's own proxy-read-timeout, which
	// its ConfigMap can change.
	nginxReadTimeoutDefault = 60 * time.Second
)

// servedByNginx reports whether ingress-nginx serves an Ingress, as far as
// the Ingress tells: its class, by spec.ingressClassName or the
// kubernetes.io/ingress.class annotation, has nginx in its name, or it
// carries an nginx.ingress.kubernetes.io/ annotation. Which controller an
// IngressClass names is cluster-scoped, which the operator isn't granted
// to read, so an Ingress with neither isn't counted, though ingress-nginx
// may serve it as the cluster's default class.
func servedByNginx(ing networkingv1.Ingress) bool {
	if name := ing.Spec.IngressClassName; name != nil && namesNginx(*name) {
		return true
	}
	if namesNginx(ing.Annotations[annotationIngressClass]) {
		return true
	}
	for key := range ing.Annotations {
		if strings.HasPrefix(key, annotationNginxPrefix) {
			return true
		}
	}
	return false
}

func namesNginx(class string) bool {
	return strings.Contains(strings.ToLower(class), "nginx")
}

// nginxReadTimeout is how long ingress-nginx waits for an answer through an
// Ingress whose proxy-read-timeout annotation is annotation: that many
// seconds, or its default without one. A value that isn't a whole number
// of seconds above zero isn't one Napwake can tell, so it says so rather
// than guess what ingress-nginx makes of it.
func nginxReadTimeout(annotation string) (time.Duration, bool) {
	if annotation == "" {
		return nginxReadTimeoutDefault, true
	}
	seconds, err := strconv.ParseInt(annotation, 10, 32)
	if err != nil || seconds <= 0 {
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
}

// nginxIngressesFor are the Ingresses ingress-nginx serves that send traffic
// to any of services, by name. ingresses are read only: they may be the
// cache's own.
func nginxIngressesFor(ingresses []networkingv1.Ingress, services []string) []v1alpha1.NginxIngress {
	var out []v1alpha1.NginxIngress
	for i := range ingresses {
		ing := ingresses[i]
		if !servedByNginx(ing) {
			continue
		}
		var fronted []string
		for _, svc := range services {
			if backsOnto(ing, svc) {
				fronted = append(fronted, svc)
			}
		}
		if len(fronted) == 0 {
			continue
		}
		entry := v1alpha1.NginxIngress{Name: ing.Name, Services: fronted,
			ProxyReadTimeout: ing.Annotations[annotationNginxReadTimeout]}
		if timeout, ok := nginxReadTimeout(entry.ProxyReadTimeout); ok {
			entry.ReadTimeout = &metav1.Duration{Duration: timeout}
		}
		out = append(out, entry)
	}
	slices.SortFunc(out, func(a, b v1alpha1.NginxIngress) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// recordNginxIngresses records the Ingresses ingress-nginx serves in front of
// the Services the doorman now routes for a workload pausing or paused,
// from ingresses, those in its namespace. A wake removes its routes as its
// pods come up, so what its pause recorded is kept through it, for the
// wake to be measured against.
func recordNginxIngresses(workload *v1alpha1.ManagedWorkload, ingresses []networkingv1.Ingress, routed map[string]bool) {
	if workload.Status.Phase != v1alpha1.PhasePausing && workload.Status.Phase != v1alpha1.PhasePaused {
		return
	}
	services := make([]string, 0, len(routed))
	for name := range routed {
		services = append(services, name)
	}
	slices.Sort(services)
	workload.Status.NginxIngresses = nginxIngressesFor(ingresses, services)
}

// forgetNginxIngresses drops what's recorded of the Ingresses in front of a
// workload the doorman no longer holds requests for, and clears
// IngressTimeoutShort, for reason.
func (r *Reconciler) forgetNginxIngresses(workload *v1alpha1.ManagedWorkload, reason string) {
	workload.Status.NginxIngresses, workload.Status.LastSlowWake = nil, nil
	r.clearCondition(workload, conditionIngressTimeoutShort, reason)
}

// reviewIngressTimeouts brings what's recorded of the Ingresses in front of
// an awake workload up to date from the cache, for the Services its last
// pause routed, so a timeout raised, or an Ingress removed, while it's awake
// clears IngressTimeoutShort without waiting for its next pause. A paused
// workload's were recorded as it was routed. Neither fails the reconcile:
// the Ingresses are read again at the next.
func (r *Reconciler) reviewIngressTimeouts(ctx context.Context, workload *v1alpha1.ManagedWorkload) {
	if r.DoormanService == "" {
		r.forgetNginxIngresses(workload, reasonDoormanDisabled)
		return
	}
	phase := workload.Status.Phase
	if recorded := workload.Status.NginxIngresses; len(recorded) > 0 &&
		(phase == v1alpha1.PhaseRunning || phase == v1alpha1.PhaseIdle) {
		var services []string
		for _, ing := range recorded {
			services = append(services, ing.Services...)
		}
		slices.Sort(services)
		var list networkingv1.IngressList
		if err := r.List(ctx, &list, client.InNamespace(workload.Namespace), client.UnsafeDisableDeepCopy); err != nil {
			log.FromContext(ctx).Info("couldn't read the Ingresses in front of the workload", "workload", workload.Name,
				"namespace", workload.Namespace, "error", err.Error())
		} else {
			workload.Status.NginxIngresses = nginxIngressesFor(list.Items, slices.Compact(services))
		}
	}
	r.judgeIngressTimeouts(workload)
}

// shortTimeouts are the Ingresses of ingresses whose timeout is under
// maxWait, or can't be told.
func shortTimeouts(ingresses []v1alpha1.NginxIngress, maxWait time.Duration) []v1alpha1.NginxIngress {
	var short []v1alpha1.NginxIngress
	for _, ing := range ingresses {
		if ing.ReadTimeout == nil || ing.ReadTimeout.Duration < maxWait {
			short = append(short, ing)
		}
	}
	return short
}

// judgeIngressTimeouts sets IngressTimeoutShort from the workload's last slow
// wake and the Ingresses in front of it now: True while one waits less than
// wake.maxWait for an answer, or can't be told, and False, with the slow
// wake forgotten, once none does. It reports whether that just made it
// True. Before any slow wake, it sets nothing.
func (r *Reconciler) judgeIngressTimeouts(workload *v1alpha1.ManagedWorkload) bool {
	slow := workload.Status.LastSlowWake
	if slow == nil {
		return false
	}
	maxWait := workload.Spec.Wake.HoldFor()
	short := shortTimeouts(workload.Status.NginxIngresses, maxWait)
	if len(short) == 0 {
		reason := reasonTimeoutsCoverMaxWait
		if len(workload.Status.NginxIngresses) == 0 {
			reason = reasonNoNginxIngress
		}
		workload.Status.LastSlowWake = nil
		r.clearCondition(workload, conditionIngressTimeoutShort, reason)
		return false
	}
	was := meta.IsStatusConditionTrue(workload.Status.Conditions, conditionIngressTimeoutShort)
	r.setCondition(workload, conditionIngressTimeoutShort, metav1.ConditionTrue, reasonWakeOutlastedTimeout,
		ingressTimeoutMessage(*slow, short, maxWait, maxWaitSetting(workload)))
	return !was
}

// maxWaitSetting is what sets the workload's wake.maxWait: its own field, or
// for one Napwake made from a label, the annotation.
func maxWaitSetting(workload *v1alpha1.ManagedWorkload) string {
	if workload.Labels[v1alpha1.LabelFromLabel] == v1alpha1.True {
		return v1alpha1.AnnotationWakeMaxWait
	}
	return "wake.maxWait"
}

// judgeWakeTime measures, as a resume first sees one of the workload's
// replicas Ready at firstReady, how long a request held through the wake
// waited, and records the wake if that's longer than an Ingress in front
// waits for an answer, while that's under wake.maxWait. Only a wake by a
// request, or a person's click, held one: the forecast's, a wake request's
// or an activity annotation's didn't. It returns what to announce once the
// status is written.
func (r *Reconciler) judgeWakeTime(workload *v1alpha1.ManagedWorkload, firstReady time.Time) func() {
	nothing := func() {}
	pause := workload.Status.Pause
	if pause == nil || r.DoormanService == "" || !requestedSince(workload, pause) {
		return nothing
	}
	began, ok := wakeBegan(workload, pause, firstReady)
	if !ok {
		return nothing
	}
	took := firstReady.Truncate(time.Second).Sub(began.Truncate(time.Second))
	maxWait := workload.Spec.Wake.HoldFor()
	if !slices.ContainsFunc(workload.Status.NginxIngresses, func(ing v1alpha1.NginxIngress) bool {
		return ing.ReadTimeout != nil && ing.ReadTimeout.Duration < maxWait && took > ing.ReadTimeout.Duration
	}) {
		return nothing
	}
	workload.Status.LastSlowWake = &v1alpha1.SlowWake{Began: metav1.NewTime(began.Truncate(time.Second)),
		Took: metav1.Duration{Duration: took}}
	if !r.judgeIngressTimeouts(workload) {
		return nothing
	}
	message := meta.FindStatusCondition(workload.Status.Conditions, conditionIngressTimeoutShort).Message
	return func() {
		r.emitEvent(workload, false, "Warning", ReasonIngressTimeoutShort, actionResume, "%s", message)
	}
}

// wakeBegan is when a request held through the wake under way began to
// wait: when the doorman took the request that woke the workload, its
// napwake.dev/last-request stamp. That's the first request held, and
// ingress-nginx's wait for an answer starts as it's sent. The resume's
// scale-up comes later, after any wait for its dependencies, so it would
// leave out what holds a request longest. A stamp that can't be read, or
// one after firstReady, as a doorman clock ahead of the operator's could
// give, falls back to the scale-up.
func wakeBegan(workload *v1alpha1.ManagedWorkload, pause *v1alpha1.PauseStatus, firstReady time.Time) (time.Time, bool) {
	stamped, err := time.Parse(time.RFC3339Nano, workload.Annotations[v1alpha1.AnnotationLastRequest])
	if err == nil && !stamped.After(firstReady) {
		return stamped, true
	}
	if pause.ResumedAt != nil {
		return pause.ResumedAt.Time, true
	}
	return time.Time{}, false
}

// ingressTimeoutMessage says how long the slow wake took, which Ingresses in
// front wait less than maxWait for an answer, that ingress-nginx answers a
// write held longer with a 504, and what to set on each, with setting, what
// sets maxWait, when the wake outlasted that too. ingress-nginx's ConfigMap
// can raise the default timeout, which Napwake can't see, so for an Ingress
// on the default it says the annotation settles it.
func ingressTimeoutMessage(slow v1alpha1.SlowWake, short []v1alpha1.NginxIngress, maxWait time.Duration,
	setting string) string {
	took := slow.Took.Duration
	var outlasted, under, unreadable []string
	defaulted := false
	for _, ing := range short {
		if ing.ProxyReadTimeout == "" {
			defaulted = true
		}
		switch {
		case ing.ReadTimeout == nil:
			unreadable = append(unreadable, fmt.Sprintf("Ingress %s's proxy-read-timeout %q isn't a whole number of "+
				"seconds, so how long it waits can't be told", ing.Name, ing.ProxyReadTimeout))
		case ing.ReadTimeout.Duration < took:
			outlasted = append(outlasted, ing.Name+" ("+waitsFor(ing)+")")
		default:
			under = append(under, ing.Name+" ("+waitsFor(ing)+")")
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "a wake by a request took %s to its first Ready replica", seconds(took))
	if len(outlasted) > 0 {
		fmt.Fprintf(&b, ", longer than ingress-nginx waits for an answer through %s, so a write held that long gets "+
			"a 504 from ingress-nginx", ingressNames(outlasted))
	} else {
		b.WriteString(", and ingress-nginx answers a write held longer than it waits with a 504")
	}
	if len(under) > 0 {
		fmt.Fprintf(&b, ". %s also %s less than %s (%s)", ingressNames(under), plural(len(under), "waits", "wait"),
			setting, seconds(maxWait))
	}
	for _, s := range unreadable {
		b.WriteString(". " + s)
	}
	where := "it"
	if len(short) > 1 {
		where = "each"
	}
	fmt.Fprintf(&b, ". Set %s: %q on %s, %s in seconds", annotationNginxReadTimeout,
		strconv.Itoa(int(maxWait/time.Second)), where, setting)
	if took > maxWait {
		fmt.Fprintf(&b, ". The wake took longer than %s (%s) too: raise it as well, and the annotation to match",
			setting, seconds(maxWait))
	}
	if defaulted {
		b.WriteString(". ingress-nginx's ConfigMap may already set a longer proxy-read-timeout, which Napwake " +
			"can't see: the annotation settles it")
	}
	return b.String()
}

// waitsFor says how long ingress-nginx waits through an Ingress whose
// timeout can be told, and whether that's its default.
func waitsFor(ing v1alpha1.NginxIngress) string {
	if ing.ProxyReadTimeout == "" {
		return seconds(ing.ReadTimeout.Duration) + ", its default"
	}
	return seconds(ing.ReadTimeout.Duration)
}

// ingressNames names one Ingress or several.
func ingressNames(names []string) string {
	if len(names) == 1 {
		return "Ingress " + names[0]
	}
	return "Ingresses " + strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// seconds writes d in whole seconds, the unit of ingress-nginx's timeout.
func seconds(d time.Duration) string {
	return plural(int(d/time.Second), "1 second", fmt.Sprintf("%d seconds", int(d/time.Second)))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// findWorkloadsBehindIngress requeues, when an Ingress changes, the paused
// workloads in its namespace, as findPausedWorkloadsInNamespace does, and
// those of any phase that record it in front of them, so a timeout raised
// while one is awake clears IngressTimeoutShort at once.
func (r *Reconciler) findWorkloadsBehindIngress(ctx context.Context, obj client.Object) []reconcile.Request {
	var list v1alpha1.ManagedWorkloadList
	if err := r.List(ctx, &list, client.InNamespace(obj.GetNamespace()), client.UnsafeDisableDeepCopy); err != nil {
		log.FromContext(ctx).Error(err, "listing managed workloads for ingress change", "namespace", obj.GetNamespace())
		return nil
	}
	var requests []reconcile.Request
	for i := range list.Items {
		w := &list.Items[i]
		if w.Status.Phase == v1alpha1.PhasePaused || slices.ContainsFunc(w.Status.NginxIngresses,
			func(ing v1alpha1.NginxIngress) bool { return ing.Name == obj.GetName() }) {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(w)})
		}
	}
	return requests
}
