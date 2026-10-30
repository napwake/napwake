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
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/doorman"
	"github.com/napwake/napwake/internal/human"
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

	// F5's NGINX Ingress Controller takes ingress-nginx's class name, nginx,
	// but reads its own annotations, such as nginx.org/proxy-read-timeout.
	annotationF5Prefix     = "nginx.org/"
	annotationF5PlusPrefix = "nginx.com/"

	// nginxReadTimeoutDefault is ingress-nginx's own proxy-read-timeout, which
	// its ConfigMap can change.
	nginxReadTimeoutDefault = 60 * time.Second
)

// servedByNginx reports whether ingress-nginx serves an Ingress, as far as
// the Ingress tells: every class it names, by spec.ingressClassName or the
// kubernetes.io/ingress.class annotation, has nginx in its name, or it names
// none and carries an nginx.ingress.kubernetes.io/ annotation. One whose
// class is named otherwise isn't counted whatever its annotations, such as
// a Traefik one with an ingress-nginx annotation left over. Which
// controller an IngressClass names is cluster-scoped, which the operator
// isn't granted to read, so one with no class and no such annotation isn't
// counted either, though ingress-nginx may serve it as the cluster's
// default class. Nor is one with an annotation of F5's NGINX Ingress
// Controller, which also takes the class nginx: ingress-nginx's annotation
// does nothing there, so advising it would be wrong, and it would clear
// the warning all the same.
func servedByNginx(ing networkingv1.Ingress) bool {
	for key := range ing.Annotations {
		if strings.HasPrefix(key, annotationF5Prefix) || strings.HasPrefix(key, annotationF5PlusPrefix) {
			return false
		}
	}
	var classes []string
	if name := ing.Spec.IngressClassName; name != nil && *name != "" {
		classes = append(classes, *name)
	}
	if class := ing.Annotations[annotationIngressClass]; class != "" {
		classes = append(classes, class)
	}
	if len(classes) > 0 {
		return !slices.ContainsFunc(classes, func(class string) bool { return !namesNginx(class) })
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
// Ingress, as ingress-nginx v1.15 reads its proxy-read-timeout annotation:
// that many seconds, by strconv.Atoi, and its default without one, or for
// one Atoi refuses, which it ignores (GetIntAnnotation's error, in
// internal/ingress/annotations/proxy/main.go). A whole number at or below
// zero, which it would apply as written, isn't a timeout Napwake judges by,
// so such an Ingress isn't counted.
func nginxReadTimeout(ing v1alpha1.NginxIngress) (time.Duration, bool) {
	if ing.ProxyReadTimeout == "" || ing.TimeoutIgnored() {
		return nginxReadTimeoutDefault, true
	}
	seconds, err := strconv.Atoi(ing.ProxyReadTimeout)
	if err != nil || seconds <= 0 {
		return 0, false
	}
	return time.Duration(min(seconds, math.MaxInt32)) * time.Second, true
}

// routedService is a Service the doorman routes for a workload, and the
// ports of it that it routes: those doorman.RoutablePorts gives, less those
// napwake.dev/doorman-ignore-ports lists.
type routedService struct {
	name  string
	ports []corev1.ServicePort
}

// sendsTo reports whether an Ingress sends any of its traffic to one of the
// ports the doorman routes of svc, by the Service port's name or number. A
// request to another port isn't held, so ingress-nginx's timeout there
// doesn't matter to a wake.
func sendsTo(ing networkingv1.Ingress, svc routedService) bool {
	routes := func(b *networkingv1.IngressBackend) bool {
		if b == nil || b.Service == nil || b.Service.Name != svc.name {
			return false
		}
		port := b.Service.Port
		return slices.ContainsFunc(svc.ports, func(p corev1.ServicePort) bool {
			if port.Name != "" {
				return p.Name == port.Name
			}
			return p.Port == port.Number
		})
	}
	if routes(ing.Spec.DefaultBackend) {
		return true
	}
	for _, rule := range ing.Spec.Rules {
		if rule.HTTP == nil {
			continue
		}
		for i := range rule.HTTP.Paths {
			if routes(&rule.HTTP.Paths[i].Backend) {
				return true
			}
		}
	}
	return false
}

// nginxIngressesFor are the Ingresses ingress-nginx serves that send traffic
// to a port the doorman routes of any of services. ingresses are read only:
// they may be the cache's own.
func nginxIngressesFor(ingresses []networkingv1.Ingress, services []routedService) []v1alpha1.NginxIngress {
	var out []v1alpha1.NginxIngress
	for i := range ingresses {
		ing := ingresses[i]
		if !servedByNginx(ing) {
			continue
		}
		var fronted []string
		for _, svc := range services {
			if sendsTo(ing, svc) {
				fronted = append(fronted, svc.name)
			}
		}
		if len(fronted) == 0 {
			continue
		}
		entry := v1alpha1.NginxIngress{Name: ing.Name, Services: fronted,
			ProxyReadTimeout: ing.Annotations[annotationNginxReadTimeout]}
		timeout, ok := nginxReadTimeout(entry)
		if !ok {
			continue
		}
		entry.ReadTimeout = metav1.Duration{Duration: timeout}
		out = append(out, entry)
	}
	slices.SortFunc(out, func(a, b v1alpha1.NginxIngress) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// recordNginxIngresses records the Ingresses ingress-nginx serves in front of
// the ports the doorman now routes for a workload pausing or paused: those
// of the planned Services it routed. ingresses are those in its namespace. A
// wake removes its routes as its pods come up, so what its pause recorded
// is kept through it, for the wake to be measured against.
func recordNginxIngresses(workload *v1alpha1.ManagedWorkload, plan doormanPlan, routed map[string]bool) {
	if workload.Status.Phase != v1alpha1.PhasePausing && workload.Status.Phase != v1alpha1.PhasePaused {
		return
	}
	var services []routedService
	for _, s := range plan.services {
		if routed[s.svc.Name] {
			services = append(services, routedService{name: s.svc.Name, ports: s.ports})
		}
	}
	workload.Status.NginxIngresses = nginxIngressesFor(plan.ingresses, services)
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
// pause routed, on the ports the doorman would route of them now, so a
// timeout raised, or an Ingress removed, while it's awake clears
// IngressTimeoutShort without waiting for its next pause. A paused
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
		if fresh, err := r.readNginxIngresses(ctx, workload.Namespace, recorded); err != nil {
			log.FromContext(ctx).Info("couldn't read the Ingresses in front of the workload", "workload", workload.Name,
				"namespace", workload.Namespace, "error", err.Error())
		} else {
			workload.Status.NginxIngresses = fresh
		}
	}
	r.judgeIngressTimeouts(workload)
}

// readNginxIngresses reads from the cache the Ingresses ingress-nginx serves
// in front of the Services recorded ones are, in namespace, on the ports
// the doorman routes of them. A Service that's gone has none.
func (r *Reconciler) readNginxIngresses(ctx context.Context, namespace string,
	recorded []v1alpha1.NginxIngress) ([]v1alpha1.NginxIngress, error) {
	names := make([]string, 0, len(recorded))
	for _, ing := range recorded {
		names = append(names, ing.Services...)
	}
	slices.Sort(names)
	var services []routedService
	for _, name := range slices.Compact(names) {
		var svc corev1.Service
		err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &svc)
		switch {
		case apierrors.IsNotFound(err):
			continue
		case err != nil:
			return nil, fmt.Errorf("reading service %s: %w", name, err)
		}
		services = append(services, routedService{name: name, ports: doorman.RoutablePorts(svc)})
	}
	var list networkingv1.IngressList
	if err := r.List(ctx, &list, client.InNamespace(namespace), client.UnsafeDisableDeepCopy); err != nil {
		return nil, fmt.Errorf("listing ingresses: %w", err)
	}
	return nginxIngressesFor(list.Items, services), nil
}

// shortTimeouts are the Ingresses of ingresses whose timeout is under
// maxWait.
func shortTimeouts(ingresses []v1alpha1.NginxIngress, maxWait time.Duration) []v1alpha1.NginxIngress {
	var short []v1alpha1.NginxIngress
	for _, ing := range ingresses {
		if ing.ReadTimeout.Duration < maxWait {
			short = append(short, ing)
		}
	}
	return short
}

// judgeIngressTimeouts sets IngressTimeoutShort from the workload's last slow
// wake and the Ingresses in front of it now: True while one waits less than
// wake.maxWait for an answer, and False, with the slow wake forgotten, once
// none does. It reports whether that just made it True. Before any slow
// wake it says nothing, and once one is forgotten, a timeout under maxWait
// that comes back removes the condition, as before any: False would say
// none is, and nothing has taken that long since.
func (r *Reconciler) judgeIngressTimeouts(workload *v1alpha1.ManagedWorkload) bool {
	slow := workload.Status.LastSlowWake
	maxWait := workload.Spec.Wake.HoldFor()
	short := shortTimeouts(workload.Status.NginxIngresses, maxWait)
	if slow == nil {
		c := meta.FindStatusCondition(workload.Status.Conditions, conditionIngressTimeoutShort)
		if c != nil && c.Status == metav1.ConditionFalse && len(short) > 0 {
			meta.RemoveStatusCondition(&workload.Status.Conditions, conditionIngressTimeoutShort)
		}
		return false
	}
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
// waits for an answer, while that's under wake.maxWait. Only a wake a
// request, or a person's click, began is timed, from that request. One the
// forecast, a wake request or an activity annotation began holds any
// request that comes during it all the same, but no request began it to
// time it from. It returns what to announce once the status is written.
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
		return ing.ReadTimeout.Duration < maxWait && took > ing.ReadTimeout.Duration
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
// write held longer with a 504, and what to set on each. It names setting,
// what sets maxWait, only where the user may have set it, maxWait being
// other than the default, or to say to raise it when the wake outlasted
// that too: a workload Napwake made from a label carries maxWait whether
// or not its annotation was set. ingress-nginx's ConfigMap
// can raise the default timeout, which Napwake can't see, so for an Ingress
// on the default it says the annotation settles it, and for one on the
// default because ingress-nginx ignores its annotation, why.
func ingressTimeoutMessage(slow v1alpha1.SlowWake, short []v1alpha1.NginxIngress, maxWait time.Duration,
	setting string) string {
	took := slow.Took.Duration
	var outlasted, under, ignored []string
	defaulted := false
	for _, ing := range short {
		if ing.ProxyReadTimeout == "" || ing.TimeoutIgnored() {
			defaulted = true
		}
		if ing.TimeoutIgnored() {
			ignored = append(ignored, fmt.Sprintf("Ingress %s's proxy-read-timeout %q isn't a whole number of "+
				"seconds, so ingress-nginx ignores it", ing.Name, ing.ProxyReadTimeout))
		}
		switch {
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
		fmt.Fprintf(&b, ". %s also %s less than its maxWait (%s)", ingressNames(under),
			plural(len(under), "waits", "wait"), seconds(maxWait))
	}
	for _, s := range ignored {
		b.WriteString(". " + s)
	}
	where := "it"
	if len(short) > 1 {
		where = "each"
	}
	inSeconds := setting
	if maxWait == v1alpha1.DefaultWakeMaxWait {
		inSeconds = "its maxWait (" + human.Duration(maxWait) + ", the default)"
	}
	fmt.Fprintf(&b, ". Set %s: %q on %s, %s in seconds", annotationNginxReadTimeout,
		strconv.Itoa(int(maxWait/time.Second)), where, inSeconds)
	if took > maxWait {
		fmt.Fprintf(&b, ". The wake took longer than its maxWait (%s) too: raise %s as well, and the annotation "+
			"to match", seconds(maxWait), setting)
	}
	if defaulted {
		b.WriteString(". ingress-nginx's ConfigMap may already set a longer proxy-read-timeout, which Napwake " +
			"can't see: the annotation settles it")
	}
	return b.String()
}

// waitsFor says how long ingress-nginx waits through an Ingress, and
// whether that's its default.
func waitsFor(ing v1alpha1.NginxIngress) string {
	if ing.ProxyReadTimeout == "" || ing.TimeoutIgnored() {
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
