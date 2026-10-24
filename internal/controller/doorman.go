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
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/doorman"
	opmetrics "github.com/napwake/napwake/internal/metrics"
)

const (
	conditionWakeOnRequest = "WakeOnRequest"
	actionRouteDoorman     = "RouteDoorman"
	reasonRoutingFailed    = "RoutingFailed"
	reasonNotPaused        = "NotPaused"

	labelServiceProxyName = "service.kubernetes.io/service-proxy-name"

	// doormanRetryMin and doormanRetryMax bound how soon a workload whose
	// doorman routing failed is reconciled again, doubling with each failure
	// in a row.
	doormanRetryMin = 5 * time.Second
	doormanRetryMax = 5 * time.Minute

	// portDrain is how long a doorman port stays out of reach of other
	// workloads once its route is gone: the doorman's own drain, plus the
	// status write and the doorman's next sync that start it.
	portDrain = 2 * doorman.RouteDrain

	// portsReload is how often allocation re-reads every workload's routes,
	// letting go of ports claimed by workloads that went without releasing
	// them.
	portsReload = 30 * time.Minute

	// servedRecheck is how soon a paused workload is reconciled again while
	// a Service that selects it is left to other Ready pods. Those pods'
	// endpoints aren't cached, so nothing reports them going, and until the
	// workload is reconciled, requests through the Service fail rather than
	// wake it.
	servedRecheck = 30 * time.Second

	// endpointsReadTimeout bounds each uncached read of a Service's own
	// EndpointSlices.
	endpointsReadTimeout = 10 * time.Second

	// staleCacheRetry is how soon a slice write the cache was too stale for
	// is repeated.
	staleCacheRetry = time.Second

	// sliceSettle is how long after a phase change a doorman slice written
	// for the phase before may still be missing from the cache.
	sliceSettle = time.Minute
)

// doormanEligible reports whether doorman routes should exist now. They're
// added as a workload starts pausing, before its pods stop, so a request
// that comes while they do is held rather than refused, and kept through
// Resuming, so connections arriving mid-wake are held too; they're removed
// once it's Running, which its resume reaches with some pods Ready, or,
// past its progress deadline, with none, when holding requests would only
// fail them later.
func doormanEligible(workload *v1alpha1.ManagedWorkload) bool {
	switch workload.Status.Phase {
	case v1alpha1.PhasePausing, v1alpha1.PhasePaused:
		return true
	case v1alpha1.PhaseResuming:
		return len(workload.Status.Doorman) > 0
	default:
		return false
	}
}

// requestWouldWake reports whether, were the awake workload paused now, a
// request to any Service that selects its pods would wake it: its doorman
// doesn't ask a person first, the doorman is on and has a Ready pod, the workload's last pause found nothing wrong
// with its routing, and the plan its pause would be routed by routes at
// least one of those Services, every port of each but those
// napwake.dev/doorman-ignore-ports lists, and takes no more doorman ports
// than the workload and its namespace may hold. Otherwise it says why not.
// What it can't check counts against it.
func (r *Reconciler) requestWouldWake(ctx context.Context, workload *v1alpha1.ManagedWorkload,
	target client.Object) (bool, string, error) {
	switch {
	case r.DoormanService == "" || target == nil:
		return false, "the doorman is off", nil
	case workload.Spec.Wake.Asks():
		return false, "the doorman asks a person before waking it, so a request doesn't", nil
	}
	if c := meta.FindStatusCondition(workload.Status.Conditions, conditionWakeOnRequest); c != nil &&
		c.Status == metav1.ConditionFalse && c.Reason != reasonNotPaused {
		return false, fmt.Sprintf("its routing has a problem (WakeOnRequest reason %s)", c.Reason), nil
	}
	endpoints, err := r.doormanEndpoints(ctx)
	if err != nil {
		return false, "", err
	}
	if len(endpoints) == 0 {
		return false, "no doorman pod is Ready", nil
	}
	plan, err := r.planDoorman(ctx, workload, target, endpoints, true)
	if err != nil {
		return false, "", err
	}
	if left := plan.unroutable; len(left) > 0 {
		return false, "Services the doorman can't route select its pods: " + strings.Join(left, ", "), nil
	}
	if skipped := plan.skipped.unsupported; len(skipped) > 0 {
		return false, "Services behind GKE container-native load balancing, which can't use the doorman without " +
			"its forwarder, select its pods: " + strings.Join(skipped, ", "), nil
	}
	if notes := plan.skipped.notes(); notes != "" {
		return false, notes, nil
	}
	ports := 0
	for _, s := range plan.services {
		ports += len(s.ports)
	}
	if ports == 0 {
		return false, "no Service routes TCP traffic to it", nil
	}
	reader := routeReader{reader: r.apiReader(), namespaces: r.WatchNamespaces}
	holder := portHolder{uid: workload.UID, namespace: workload.Namespace}
	err = r.doormanPorts.fits(ctx, reader, r.now(), holder, r.portLimits(), ports)
	if tooMany := (*tooManyPortsError)(nil); errors.As(err, &tooMany) {
		return false, tooMany.Error(), nil
	}
	if err != nil {
		return false, "", err
	}
	return true, "", nil
}

// routeDoorman brings the workload's doorman routing up to date. Routing is
// only ever a convenience on top of the lifecycle, so a failure is reported
// on the WakeOnRequest condition and retried, and never stops the reconcile.
// It returns how soon to reconcile again, to retry or to recheck, or zero.
func (r *Reconciler) routeDoorman(ctx context.Context, workload *v1alpha1.ManagedWorkload, target client.Object) time.Duration {
	recordTurnedAway(workload)
	failing := conditionFalseWith(workload, conditionWakeOnRequest, reasonRoutingFailed)
	recheck, err := r.reconcileDoorman(ctx, workload, target)
	if err == nil {
		r.doormanFailures.reset(workload.UID)
		return recheck
	}
	log.FromContext(ctx).Error(err, "routing requests through the doorman",
		"workload", workload.Name, "namespace", workload.Namespace)
	r.setCondition(workload, conditionWakeOnRequest, metav1.ConditionFalse, reasonRoutingFailed,
		fmt.Sprintf("requests may not wake the workload; retrying: %v", err))
	if !failing {
		r.announceRouting(ctx, workload, reasonRoutingFailed, "requests may not wake the workload: %v", err)
	}
	return r.doormanFailures.next(workload.UID)
}

// recordTurnedAway brings status.turnedAway up to date for a workload whose
// doorman asks before waking it: each pause starts with none turned away,
// and the doorman's count, in its annotation, is copied in only when it's
// of the pause status counts, the current one or, once the workload is
// awake, the last. Anything else in the annotation, such as a count of an
// earlier pause written late, or one edited by hand without a pause or
// with one still to come, is left out: status needs the pause it counts,
// and a write without one would fail every status write after it.
func recordTurnedAway(workload *v1alpha1.ManagedWorkload) {
	if !workload.Spec.Wake.Asks() {
		workload.Status.TurnedAway = nil
		return
	}
	current := workload.Status.TurnedAway
	if p := workload.Status.Pause; p != nil && p.PausedAt != nil {
		pausedAt := metav1.NewTime(p.PausedAt.Truncate(time.Second))
		if current == nil || !current.PausedAt.Truncate(time.Second).Equal(pausedAt.Time) {
			current = &v1alpha1.TurnedAway{PausedAt: pausedAt}
		}
	}
	if current == nil {
		return
	}
	if counted, ok := turnedAwayIn(workload.Annotations); ok &&
		counted.PausedAt.Truncate(time.Second).Equal(current.PausedAt.Truncate(time.Second)) {
		current = &v1alpha1.TurnedAway{PausedAt: current.PausedAt, Requests: counted.Requests}
	}
	workload.Status.TurnedAway = current
}

// turnedAwayIn is the doorman's count in a workload's annotations, if
// there's one that names its pause.
func turnedAwayIn(annotations map[string]string) (v1alpha1.TurnedAway, bool) {
	var counted v1alpha1.TurnedAway
	value, ok := annotations[v1alpha1.AnnotationTurnedAway]
	if !ok || json.Unmarshal([]byte(value), &counted) != nil {
		return counted, false
	}
	return counted, counted.Requests >= 0 && !counted.PausedAt.IsZero()
}

// announceRouting warns about a change in the workload's routing once the
// condition reporting it is written. A pause or wake in progress is retried
// every few seconds without writing status, so a change announced but left
// unwritten would be announced again on every retry. If the write fails,
// the change is announced when a later reconcile finds it again.
func (r *Reconciler) announceRouting(ctx context.Context, workload *v1alpha1.ManagedWorkload, reason, msgFmt string, args ...any) {
	if err := r.Status().Update(ctx, workload); err != nil {
		log.FromContext(ctx).Error(err, "recording a change in doorman routing",
			"workload", workload.Name, "namespace", workload.Namespace, "reason", reason)
		return
	}
	r.emitEvent(workload, false, "Warning", reason, actionRouteDoorman, msgFmt, args...)
}

// reconcileDoorman makes the workload's doorman EndpointSlices and routes
// match its phase. It returns how soon to look again, or zero when only a
// change the operator watches can alter them.
func (r *Reconciler) reconcileDoorman(ctx context.Context, workload *v1alpha1.ManagedWorkload, target client.Object) (time.Duration, error) {
	if r.DoormanService == "" {
		r.clearCondition(workload, conditionWakeOnRequest, "DoormanDisabled")
		return 0, r.removeDoorman(ctx, workload)
	}
	if !doormanEligible(workload) || target == nil {
		r.keepRoutingProblem(workload)
		if err := r.removeDoorman(ctx, workload); err != nil {
			return 0, err
		}
		return r.forwarderRecheck(workload), nil
	}

	endpoints, err := r.doormanEndpoints(ctx)
	if err != nil {
		return 0, err
	}
	if len(endpoints) == 0 {
		r.setCondition(workload, conditionWakeOnRequest, metav1.ConditionFalse, "DoormanUnavailable",
			"no doorman pods are ready, so requests to this paused workload fail until one is")
		return 0, r.removeDoorman(ctx, workload)
	}

	plan, err := r.planDoorman(ctx, workload, target, endpoints, false)
	if err != nil {
		return 0, err
	}
	var recheck time.Duration
	if len(plan.skipped.served) > 0 {
		recheck = servedRecheck
	}
	current, err := r.listDoormanSlices(ctx, r.Client, workload)
	if err != nil {
		return 0, err
	}
	routes, err := r.allocateRoutes(ctx, workload, plan.services, current)
	if errors.Is(err, doorman.ErrNoFreePort) {
		r.setCondition(workload, conditionWakeOnRequest, metav1.ConditionFalse, "NoFreePort", err.Error())
		return recheck, nil
	}
	var tooMany *tooManyPortsError
	if errors.As(err, &tooMany) {
		r.setCondition(workload, conditionWakeOnRequest, metav1.ConditionFalse, "TooManyPorts",
			"requests don't wake the workload: "+err.Error())
		return shortest(recheck, servedRecheck), nil
	}
	if err != nil {
		return 0, err
	}
	// Status is written after this reconcile, so a slice can send traffic
	// to a port before the doorman serves it, and is refused meanwhile, as
	// it would be without the doorman. A reconcile that fails before the
	// write keeps the ports its slices already use, so they aren't moved
	// on every retry.
	r.recordDrains(workload, routes)
	workload.Status.Doorman = routes
	if wait, err := r.untilDoormanReady(ctx, workload, endpoints); err != nil || wait > 0 {
		return wait, err
	}
	fwd, fwdErr := r.reconcileForwarder(ctx, workload, target, plan.services, routes)
	recheck = shortest(recheck, fwd.recheck)

	keep := map[string]bool{}
	routed := map[string]bool{}
	var errs []error
	if fwdErr != nil {
		errs = append(errs, fwdErr)
	}
	for _, s := range plan.services {
		svcRoutes := routesFor(routes, s.svc.Name)
		want := doormanSlice{ports: slicePorts(svcRoutes, nil), endpoints: endpoints}
		if s.forwarded {
			want = doormanSlice{ports: slicePorts(svcRoutes, fwd.ports), endpoints: fwd.endpoints, forwarded: true}
			if want.doormanPorts, err = doormanPortsAnnotation(svcRoutes); err != nil {
				return 0, err
			}
		}
		for _, family := range s.families {
			if len(want.endpoints[family]) == 0 || len(want.ports) == 0 {
				continue
			}
			name := doorman.SliceName(s.svc.Name, workload.Name, family)
			keep[name] = true
			routed[s.svc.Name] = true
			err := r.applyDoormanSlice(ctx, workload, s.svc, name, family, want)
			if apierrors.IsAlreadyExists(err) {
				err = r.claimedSlice(ctx, workload.Namespace, name, err)
			}
			switch {
			case err == nil:
			// The cache hasn't caught up with a slice written moments
			// ago: not a failure, just a write to repeat once it has.
			case apierrors.IsAlreadyExists(err) || apierrors.IsConflict(err):
				recheck = shortest(recheck, staleCacheRetry)
			default:
				errs = append(errs, err)
			}
		}
		if s.forwarded && !routed[s.svc.Name] {
			plan.skipped.forwarding = append(plan.skipped.forwarding, s.svc.Name)
			recheck = shortest(recheck, forwarderRecheckInterval)
		}
	}
	if err := r.deleteDoormanSlices(ctx, current, keep); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return 0, errors.Join(errs...)
	}
	plan.skipped.forwarder = fwd.unready
	fronted, err := r.reportRouted(ctx, workload, target, routes, routed, plan.skipped)
	return shortest(recheck, fronted), err
}

// reportRouted reports the routes the doorman's slices now stand in front
// of, those of the Services in routed, and, for a workload pausing, records
// it fronted once any do.
func (r *Reconciler) reportRouted(ctx context.Context, workload *v1alpha1.ManagedWorkload, target client.Object,
	routes []v1alpha1.DoormanRoute, routed map[string]bool, skipped skippedServices) (time.Duration, error) {
	live := slices.DeleteFunc(slices.Clone(routes), func(rt v1alpha1.DoormanRoute) bool { return !routed[rt.Service] })
	r.reportDoorman(ctx, workload, target, live, skipped)
	if len(live) == 0 {
		return 0, nil
	}
	return r.recordFronted(ctx, workload)
}

// lastingRoutingProblems are the WakeOnRequest reasons that come from how the
// workload's Services are set up, which the next pause meets again unless
// someone changes them.
var lastingRoutingProblems = map[string]bool{
	"UnsupportedLoadBalancer": true,
	"ServedByOtherPods":       true,
	"NodeLocalTraffic":        true,
	"UnsupportedIPFamily":     true,
	"TooManyPorts":            true,
}

const foundAtLastPause = "at its last pause: "

// keepRoutingProblem clears WakeOnRequest for a workload the doorman doesn't
// route once it isn't paused, except for a problem its last pause found
// with its Services, which stays, so it can be fixed before the next pause
// rather than found by someone whose request doesn't wake it.
func (r *Reconciler) keepRoutingProblem(workload *v1alpha1.ManagedWorkload) {
	c := meta.FindStatusCondition(workload.Status.Conditions, conditionWakeOnRequest)
	if c == nil || c.Status != metav1.ConditionFalse || !lastingRoutingProblems[c.Reason] {
		r.clearCondition(workload, conditionWakeOnRequest, reasonNotPaused)
		return
	}
	if !strings.HasPrefix(c.Message, foundAtLastPause) {
		r.setCondition(workload, conditionWakeOnRequest, metav1.ConditionFalse, c.Reason, foundAtLastPause+c.Message)
	}
}

// shortest is the sooner of two requeue delays, where zero means none.
func shortest(a, b time.Duration) time.Duration {
	if a == 0 || (b != 0 && b < a) {
		return b
	}
	return a
}

// removeDoorman stops routing the workload through the doorman: its slices
// are deleted, not left to garbage collection, which an orphaning delete
// would skip. Only once they're gone do its ports leave status and start
// draining: a port another workload could be given while a slice still
// sends traffic to it would deliver that traffic to the wrong workload.
//
// A slice written moments ago may not be in the cache yet, and would be
// left sending traffic to the doorman, so the slices are listed from the
// API server whenever one may have been: see mayMissSlices. Otherwise, as
// for every Running workload on every reconcile, the cache is enough.
func (r *Reconciler) removeDoorman(ctx context.Context, workload *v1alpha1.ManagedWorkload) error {
	reader := client.Reader(r.Client)
	if r.mayMissSlices(workload) {
		reader = r.apiReader()
	}
	current, err := r.listDoormanSlices(ctx, reader, workload)
	if err != nil {
		return err
	}
	if err := r.deleteDoormanSlices(ctx, current, nil); err != nil {
		return err
	}
	r.doormanPorts.release(workload.UID, r.now())
	r.recordDrains(workload, nil)
	// Routes taken down before the scale-down are stored, listened on and
	// stood in front of afresh should they come back.
	if pause := workload.Status.Pause; len(workload.Status.Doorman) > 0 && pause != nil && pause.PausedAt == nil {
		pause.RoutesWrittenAt, pause.RoutedAt = nil, nil
	}
	workload.Status.Doorman = nil
	return r.retireForwarder(ctx, workload)
}

// mayMissSlices reports whether the cache may not have a doorman slice of
// the workload's yet: while status names routes; just after a phase change,
// since a reconcile whose status write failed leaves slices status doesn't
// name; and for a workload being deleted, which is never reconciled again
// once its finalizer goes, until its slices have been listed from the API
// server and removed since the deletion began. A drain that started after
// the deletion records that, and nothing writes slices for a workload being
// deleted, so the reconciles while its ports drain read the cache. A
// restart in between starts the cache afresh, with every slice in it.
func (r *Reconciler) mayMissSlices(workload *v1alpha1.ManagedWorkload) bool {
	if len(workload.Status.Doorman) > 0 {
		return true
	}
	if !workload.DeletionTimestamp.IsZero() {
		return !slices.ContainsFunc(workload.Status.DoormanDraining, func(d v1alpha1.DrainingPort) bool {
			return !d.Until.Add(-portDrain).Before(workload.DeletionTimestamp.Time)
		})
	}
	return r.sinceTransition(workload) < sliceSettle
}

// recordDrains notes in status the ports the workload's routes are about to
// give up, which drain before anyone else may have them, and forgets drains
// that are over or whose port the workload has taken back. The allocator
// keeps the same in memory; status is what a leader that takes over reads.
func (r *Reconciler) recordDrains(workload *v1alpha1.ManagedWorkload, routes []v1alpha1.DoormanRoute) {
	now := r.now()
	kept := map[int32]bool{}
	for _, route := range routes {
		kept[route.DoormanPort] = true
	}
	var draining []v1alpha1.DrainingPort
	for _, d := range workload.Status.DoormanDraining {
		if now.Before(d.Until.Time) && !kept[d.Port] {
			draining = append(draining, d)
		}
	}
	until := metav1.NewTime(now.Add(portDrain))
	for _, route := range workload.Status.Doorman {
		if !kept[route.DoormanPort] && !slices.ContainsFunc(draining, func(d v1alpha1.DrainingPort) bool {
			return d.Port == route.DoormanPort
		}) {
			draining = append(draining, v1alpha1.DrainingPort{Port: route.DoormanPort, Until: until})
		}
	}
	workload.Status.DoormanDraining = draining
}

// skippedServices are the Services that select the workload but aren't
// routed, by why.
type skippedServices struct {
	unsupported []string
	served      []string
	nodeLocal   []string
	family      []string

	// forwarding are those behind GKE container-native load balancing
	// whose forwarder has no Ready pod yet, and forwarder says why.
	forwarding []string
	forwarder  string
}

func (s skippedServices) notes() string {
	var notes []string
	if len(s.forwarding) > 0 {
		notes = append(notes, fmt.Sprintf("not routed until the forwarder is Ready (%s): %s",
			s.forwarder, strings.Join(s.forwarding, ", ")))
	}
	if len(s.served) > 0 {
		notes = append(notes, "served by other Ready pods, so not routed: "+strings.Join(s.served, ", "))
	}
	if len(s.nodeLocal) > 0 {
		notes = append(notes, "internalTrafficPolicy is Local, so not routed: "+strings.Join(s.nodeLocal, ", "))
	}
	if len(s.family) > 0 {
		notes = append(notes, "no doorman pod has an address in the IP family of: "+strings.Join(s.family, ", "))
	}
	return strings.Join(notes, "; ")
}

const (
	reasonNoReadinessProbe = "NoReadinessProbe"

	// noReadinessNote is what the WakeOnRequest condition adds for a routed
	// workload whose pods have no readiness probe.
	noReadinessNote = "its pods have no readiness probe, so held requests reach them as soon as they start, " +
		"which may be before the app can answer them"
)

// hasReadinessCheck reports whether a pod template tells Kubernetes when
// its app can serve: a readiness or startup probe on one of its containers,
// or a readiness gate. Without one, a pod is Ready as soon as its
// containers start, and the doorman passes held requests to it then.
// Injected sidecars aren't in the template, and their probes say nothing of
// the app's.
func hasReadinessCheck(spec corev1.PodSpec) bool {
	return len(spec.ReadinessGates) > 0 || slices.ContainsFunc(spec.Containers, func(c corev1.Container) bool {
		return c.ReadinessProbe != nil || c.StartupProbe != nil
	})
}

// reportDoorman sets the WakeOnRequest condition. Requests through a Service
// left unrouted because of its load balancer fail while the workload is
// paused, so a warning event says so whenever that list changes, as one
// does when a routed workload's pods have no readiness probe, which the
// requests that wake it may reach before its app can answer them.
func (r *Reconciler) reportDoorman(ctx context.Context, workload *v1alpha1.ManagedWorkload, target client.Object,
	routes []v1alpha1.DoormanRoute, skipped skippedServices) {
	// While the workload wakes, the Ready pods serving a Service are its
	// own: the Service leaving the doorman is the wake finishing, and what
	// its pause found stands until it's awake.
	if workload.Status.Phase == v1alpha1.PhaseResuming && len(skipped.served) > 0 {
		skipped.served = nil
		if len(routes) == 0 {
			return
		}
	}
	status, reason, message := metav1.ConditionTrue, "DoormanRouted", "requests are held and wake the workload"
	if workload.Spec.Wake.Asks() {
		message = "the doorman asks a person's browser before waking the workload, and turns away other requests"
	}
	switch {
	case len(routes) > 0:
	case len(skipped.forwarding) > 0:
		status, reason, message = metav1.ConditionFalse, reasonForwarderNotReady,
			"requests through GKE's load balancer reach the doorman through a forwarder beside the workload, which isn't Ready yet"
	case len(skipped.unsupported) > 0:
		status, reason, message = metav1.ConditionFalse, "UnsupportedLoadBalancer", "no Service can be routed through the doorman"
	case len(skipped.served) > 0:
		status, reason, message = metav1.ConditionFalse, "ServedByOtherPods",
			"every Service that selects the workload has other Ready pods, so requests go to them and don't wake it"
	case len(skipped.nodeLocal) > 0:
		status, reason, message = metav1.ConditionFalse, "NodeLocalTraffic",
			"the workload's Services keep traffic on the caller's node, so only callers on a doorman pod's node could reach the doorman"
	case len(skipped.family) > 0:
		status, reason, message = metav1.ConditionFalse, "UnsupportedIPFamily",
			"no doorman pod has an address in the IP family of the workload's Services"
	default:
		status, reason, message = metav1.ConditionFalse, "NoServices", "no Service routes TCP traffic to this workload"
	}
	if len(skipped.unsupported) > 0 {
		message = fmt.Sprintf("%s; not routed, since GKE container-native load balancing (NEGs) can't use the doorman: %s",
			message, strings.Join(skipped.unsupported, ", "))
	}
	if notes := skipped.notes(); notes != "" {
		message += "; " + notes
	}
	unready := len(routes) > 0 && !hasReadinessCheck(podTemplateOf(target).Spec)
	if unready {
		message += "; " + noReadinessNote
	}
	previous := meta.FindStatusCondition(workload.Status.Conditions, conditionWakeOnRequest).DeepCopy()
	r.setCondition(workload, conditionWakeOnRequest, status, reason, message)
	if len(skipped.unsupported) > 0 && (previous == nil || previous.Message != message) {
		r.announceRouting(ctx, workload, "UnsupportedLoadBalancer",
			"requests to Services %s won't wake the workload: GKE container-native load balancing (NEGs) can't use the doorman",
			strings.Join(skipped.unsupported, ", "))
	}
	if unready && (previous == nil || !strings.Contains(previous.Message, noReadinessNote)) {
		r.announceRouting(ctx, workload, reasonNoReadinessProbe,
			"its pods have no readiness probe, so a request that wakes it reaches them as soon as they start, "+
				"which may be before the app can answer it; give it a readiness probe that passes once it can serve: %s",
			readinessProbesGuide)
	}
}

const readinessProbesGuide = "https://docs.napwake.dev/concepts/wake-on-request/#readiness-probes"

func (r *Reconciler) doormanEndpoints(ctx context.Context) (map[discoveryv1.AddressType][]discoveryv1.Endpoint, error) {
	return readDoormanEndpoints(ctx, r.Client, r.DoormanNamespace, r.DoormanService)
}

// readDoormanEndpoints returns an endpoint for each Ready doorman pod, by
// address family, from the doorman Service's own EndpointSlices. A pod
// that's terminating is left out at once, whatever its readiness says, since
// it stops accepting connections within seconds. Each keeps its pod's node,
// zone, and pod reference: load balancers that register pods directly skip
// endpoints without a pod, and traffic policies of Local skip those without
// a node.
func readDoormanEndpoints(ctx context.Context, reader client.Reader, namespace, service string) (
	map[discoveryv1.AddressType][]discoveryv1.Endpoint, error) {
	var list discoveryv1.EndpointSliceList
	if err := reader.List(ctx, &list, client.InNamespace(namespace),
		client.MatchingLabels{discoveryv1.LabelServiceName: service}); err != nil {
		return nil, fmt.Errorf("listing doorman endpoints: %w", err)
	}
	out := map[discoveryv1.AddressType][]discoveryv1.Endpoint{}
	for _, slice := range list.Items {
		if slice.AddressType != discoveryv1.AddressTypeIPv4 && slice.AddressType != discoveryv1.AddressTypeIPv6 {
			continue
		}
		for _, ep := range slice.Endpoints {
			if len(ep.Addresses) == 0 || (ep.Conditions.Ready != nil && !*ep.Conditions.Ready) ||
				(ep.Conditions.Terminating != nil && *ep.Conditions.Terminating) {
				continue
			}
			out[slice.AddressType] = append(out[slice.AddressType], discoveryv1.Endpoint{
				Addresses:  ep.Addresses[:1],
				Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(true)},
				NodeName:   ep.NodeName,
				Zone:       ep.Zone,
				TargetRef:  ep.TargetRef,
			})
		}
	}
	for family, eps := range out {
		slices.SortFunc(eps, func(a, b discoveryv1.Endpoint) int { return cmp.Compare(a.Addresses[0], b.Addresses[0]) })
		out[family] = slices.CompactFunc(eps, func(a, b discoveryv1.Endpoint) bool { return a.Addresses[0] == b.Addresses[0] })
	}
	return out, nil
}

// doormanService is a Service to route through the doorman: the address
// families to route it for, and the ports. A forwarded one is behind GKE
// container-native load balancing, and routed through the workload's
// forwarder (see reconcileForwarder).
type doormanService struct {
	svc       corev1.Service
	families  []discoveryv1.AddressType
	ports     []corev1.ServicePort
	forwarded bool
}

type doormanPlan struct {
	services []doormanService
	skipped  skippedServices
	// unroutable are the Services that select an awake workload's pods
	// which the doorman can't route at all, or not on every port, each with
	// why, planned ahead of its pause. A paused workload's routing leaves
	// them out as it always does.
	unroutable []string
}

// planDoorman picks the Services in the workload's namespace to route
// through the doorman: those that select its pods and have nowhere else to
// send traffic.
//
// Services without a selector manage their own endpoints, and headless and
// ExternalName Services hand callers addresses directly, so none of those
// is routed. Nor is a Service with internalTrafficPolicy Local: kube-proxy
// sends a caller only to endpoints on its own node, and the doorman's pods
// are on one or two, so most callers would get nothing at all. A Service with other Ready pods, such as one shared by a
// canary and a stable Deployment of which only one is paused, is left to
// them: requests reach a running workload, and waking the paused one for
// them would be no use. Once every workload behind it is paused, it's
// routed by each, so a request wakes one of them. For an awake workload,
// planned ahead of its pause, and one pausing, its own pods serve its
// Services until they stop, and aren't counted among those other pods; nor
// are a paused one's while its target is held at zero, as those are still
// stopping, and can be listed Ready for a moment after the scale-down.
func (r *Reconciler) planDoorman(ctx context.Context, workload *v1alpha1.ManagedWorkload, target client.Object,
	endpoints map[discoveryv1.AddressType][]discoveryv1.Endpoint, awake bool) (doormanPlan, error) {
	var own client.Object
	if awake || workload.Status.Phase == v1alpha1.PhasePausing ||
		(workload.Status.Phase == v1alpha1.PhasePaused && replicasFromTarget(target) == 0) {
		own = target
	}
	podLabels := labels.Set(podTemplateLabels(target))
	var list corev1.ServiceList
	if err := r.List(ctx, &list, client.InNamespace(workload.Namespace)); err != nil {
		return doormanPlan{}, fmt.Errorf("listing services: %w", err)
	}
	var ingresses networkingv1.IngressList
	if err := r.List(ctx, &ingresses, client.InNamespace(workload.Namespace), client.UnsafeDisableDeepCopy); err != nil {
		return doormanPlan{}, fmt.Errorf("listing ingresses: %w", err)
	}
	var plan doormanPlan
	for _, svc := range list.Items {
		if len(svc.Spec.Selector) == 0 || svc.Spec.Type == corev1.ServiceTypeExternalName ||
			!labels.SelectorFromSet(svc.Spec.Selector).Matches(podLabels) {
			continue
		}
		if why := unroutable(svc); awake && why != "" {
			plan.unroutable = append(plan.unroutable, fmt.Sprintf("%s (%s)", svc.Name, why))
			continue
		}
		if svc.Spec.ClusterIP == corev1.ClusterIPNone {
			continue
		}
		forwarded := usesGKENEG(svc, ingresses.Items)
		if forwarded && r.NEGForwarder == nil {
			plan.skipped.unsupported = append(plan.skipped.unsupported, svc.Name)
			continue
		}
		ports := doorman.RoutablePorts(svc)
		if len(ports) == 0 {
			continue
		}
		if svc.Spec.InternalTrafficPolicy != nil && *svc.Spec.InternalTrafficPolicy == corev1.ServiceInternalTrafficPolicyLocal {
			plan.skipped.nodeLocal = append(plan.skipped.nodeLocal, svc.Name)
			continue
		}
		served, err := r.servedByOthers(ctx, svc, own)
		if err != nil {
			return doormanPlan{}, err
		}
		if served {
			plan.skipped.served = append(plan.skipped.served, svc.Name)
			continue
		}
		var families []discoveryv1.AddressType
		for _, family := range serviceFamilies(svc) {
			if len(endpoints[family]) > 0 {
				families = append(families, family)
			} else {
				plan.skipped.family = append(plan.skipped.family, fmt.Sprintf("%s (%s)", svc.Name, family))
			}
		}
		if len(families) > 0 {
			plan.services = append(plan.services, doormanService{svc: svc, families: families, ports: ports, forwarded: forwarded})
		}
	}
	slices.SortFunc(plan.services, func(a, b doormanService) int { return cmp.Compare(a.svc.Name, b.svc.Name) })
	slices.Sort(plan.skipped.unsupported)
	slices.Sort(plan.skipped.served)
	slices.Sort(plan.skipped.nodeLocal)
	slices.Sort(plan.skipped.family)
	slices.Sort(plan.unroutable)
	return plan, nil
}

// unroutable says why the doorman can't route a Service that selects a
// workload's pods, or not on every port: one that's headless hands callers
// the pods' addresses directly, and UDP and SCTP it can't hold. Empty when
// it can route every port but those napwake.dev/doorman-ignore-ports
// lists, which are meant to fail while the workload is paused.
func unroutable(svc corev1.Service) string {
	if svc.Spec.ClusterIP == corev1.ClusterIPNone {
		return "headless"
	}
	ports := doorman.UnroutablePorts(svc)
	if len(ports) == 0 {
		return ""
	}
	names := make([]string, 0, len(ports))
	for _, p := range ports {
		names = append(names, fmt.Sprintf("%s port %d", p.Protocol, p.Port))
	}
	return strings.Join(names, ", ")
}

// servedByOthers reports whether a Service's own EndpointSlices have a
// Ready pod, which can only be another workload's while this one is paused,
// or, for an awake one, is one that own, its target, doesn't run.
// They're read from the API server: the operator caches only the doorman's
// slices, since caching every Service's would cost memory in proportion to
// every pod in the cluster. An endpoint counts only once the pod it names,
// in the Service's namespace, has its address: anyone who may write
// EndpointSlices could otherwise keep a Service from being routed.
func (r *Reconciler) servedByOthers(ctx context.Context, svc corev1.Service, own client.Object) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, endpointsReadTimeout)
	defer cancel()
	var list discoveryv1.EndpointSliceList
	if err := r.apiReader().List(ctx, &list, client.InNamespace(svc.Namespace),
		client.MatchingLabels{discoveryv1.LabelServiceName: svc.Name}); err != nil {
		return false, fmt.Errorf("listing endpoints of service %s: %w", svc.Name, err)
	}
	for _, ep := range doorman.ReadyPodEndpoints(list.Items) {
		if ep.Namespace != svc.Namespace {
			continue
		}
		pod, err := r.podWithAddress(ctx, types.NamespacedName{Namespace: svc.Namespace, Name: ep.Pod}, ep.Addr)
		if err != nil {
			return false, err
		}
		if pod != nil && (own == nil || !opmetrics.RunBy(own, pod)) {
			return true, nil
		}
	}
	return false, nil
}

// podWithAddress is the pod of name, if it has addr; nil otherwise.
func (r *Reconciler) podWithAddress(ctx context.Context, name types.NamespacedName, addr netip.Addr) (*corev1.Pod, error) {
	var pod corev1.Pod
	if err := r.apiReader().Get(ctx, name, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading pod %s: %w", name, err)
	}
	for _, ip := range pod.Status.PodIPs {
		if parsed, err := netip.ParseAddr(ip.IP); err == nil && parsed.Unmap() == addr {
			return &pod, nil
		}
	}
	return nil, nil
}

// serviceFamilies are the address families a Service has cluster IPs in,
// and so the EndpointSlices kube-proxy reads for it.
func serviceFamilies(svc corev1.Service) []discoveryv1.AddressType {
	families := svc.Spec.IPFamilies
	if len(families) == 0 {
		families = []corev1.IPFamily{corev1.IPv4Protocol}
		if ip, err := netip.ParseAddr(svc.Spec.ClusterIP); err == nil && ip.Is6() {
			families = []corev1.IPFamily{corev1.IPv6Protocol}
		}
	}
	out := make([]discoveryv1.AddressType, 0, len(families))
	for _, f := range families {
		switch f {
		case corev1.IPv4Protocol:
			out = append(out, discoveryv1.AddressTypeIPv4)
		case corev1.IPv6Protocol:
			out = append(out, discoveryv1.AddressTypeIPv6)
		}
	}
	return out
}

func podTemplateLabels(target client.Object) map[string]string {
	switch t := target.(type) {
	case *appsv1.Deployment:
		return t.Spec.Template.Labels
	case *appsv1.StatefulSet:
		return t.Spec.Template.Labels
	default:
		return nil
	}
}

// allocateRoutes gives each Service port a doorman port, keeping the one
// it has in status or, failing that, in the workload's current slices.
func (r *Reconciler) allocateRoutes(ctx context.Context, workload *v1alpha1.ManagedWorkload, services []doormanService,
	current []discoveryv1.EndpointSlice) ([]v1alpha1.DoormanRoute, error) {
	keys := make([]routeKey, 0, len(services))
	for _, s := range services {
		for _, port := range s.ports {
			keys = append(keys, routeKey{service: s.svc.Name, portName: port.Name})
		}
	}
	existing := map[routeKey]int32{}
	for i := range current {
		for name, port := range doormanPortsOf(&current[i]) {
			existing[routeKey{service: current[i].Labels[discoveryv1.LabelServiceName], portName: name}] = port
		}
	}
	for _, route := range append(r.forwarderRoutes(ctx, workload), workload.Status.Doorman...) {
		existing[routeKey{service: route.Service, portName: route.PortName}] = route.DoormanPort
	}
	reader := routeReader{reader: r.apiReader(), namespaces: r.WatchNamespaces}
	holder := portHolder{uid: workload.UID, namespace: workload.Namespace}
	return r.doormanPorts.assign(ctx, reader, r.now(), holder, r.portLimits(), existing, keys)
}

// portLimits are the caps on doorman ports, the defaults where unset.
func (r *Reconciler) portLimits() portLimits {
	return portLimits{
		perWorkload:  cmp.Or(r.DoormanPortsPerWorkload, DefaultDoormanPortsPerWorkload),
		perNamespace: cmp.Or(r.DoormanPortsPerNamespace, DefaultDoormanPortsPerNamespace),
	}
}

// apiReader reads from the API server what the cache leaves out, or may
// not have caught up with.
func (r *Reconciler) apiReader() client.Reader {
	if r.PodReader != nil {
		return r.PodReader
	}
	return r.Client
}

// routeReader lists the ManagedWorkloads and doorman EndpointSlices the
// operator can see: in each watched namespace, which is all its Roles
// allow, or in every namespace.
type routeReader struct {
	reader     client.Reader
	namespaces []string
}

// portsInUse is every workload's routes and drains, and every doorman
// slice, which may hold a port its workload's status doesn't: one written
// by a reconcile whose status write failed.
type portsInUse struct {
	workloads []v1alpha1.ManagedWorkload
	slices    []discoveryv1.EndpointSlice
}

func (rr routeReader) list(ctx context.Context) (portsInUse, error) {
	namespaces := rr.namespaces
	if len(namespaces) == 0 {
		namespaces = []string{metav1.NamespaceAll}
	}
	var inUse portsInUse
	for _, ns := range namespaces {
		var workloads v1alpha1.ManagedWorkloadList
		if err := rr.reader.List(ctx, &workloads, client.InNamespace(ns)); err != nil {
			return portsInUse{}, fmt.Errorf("listing managed workloads%s: %w", inNamespace(ns), err)
		}
		var sliceList discoveryv1.EndpointSliceList
		if err := rr.reader.List(ctx, &sliceList, client.InNamespace(ns),
			client.MatchingLabels{discoveryv1.LabelManagedBy: doorman.ManagedBy}); err != nil {
			return portsInUse{}, fmt.Errorf("listing doorman slices%s: %w", inNamespace(ns), err)
		}
		inUse.workloads = append(inUse.workloads, workloads.Items...)
		inUse.slices = append(inUse.slices, sliceList.Items...)
	}
	return inUse, nil
}

func inNamespace(ns string) string {
	if ns == metav1.NamespaceAll {
		return ""
	}
	return " in " + ns
}

type routeKey struct {
	service, portName string
}

// The default caps on the doorman ports one workload, and one namespace,
// may hold. Every port of every Service selecting a paused workload takes
// one of the 10,000 the whole cluster shares, so without them one tenant
// could leave none for the rest.
const (
	DefaultDoormanPortsPerWorkload  = 32
	DefaultDoormanPortsPerNamespace = 256
)

type portLimits struct {
	perWorkload, perNamespace int
}

// tooManyPortsError means routing a workload would take more doorman ports
// than it, or its namespace, may hold.
type tooManyPortsError struct {
	holder      string
	want, limit int
}

func (e *tooManyPortsError) Error() string {
	return fmt.Sprintf("%s would hold %d doorman ports, more than its limit of %d", e.holder, e.want, e.limit)
}

// portHolder is the workload a port is claimed for.
type portHolder struct {
	uid       types.UID
	namespace string
}

type portClaim struct {
	owner     types.UID
	namespace string
	// at is when the claim was last made, or for a draining port, when
	// the drain ends.
	at time.Time
}

// portAllocator hands out doorman ports. One doorman serves the whole
// cluster, so two routes on one port would send one workload's traffic to
// another. Reconciles run in parallel, and a status written moments ago may
// not be in the cache yet, so ports aren't chosen from the cache: the
// allocator, which only the leader runs, remembers every claim it makes,
// starting from, and every so often reconciling with, a read of every
// workload's routes and doorman slices from the API server. That read
// spans the cluster, so it's made without holding mu, which would hold up
// every routing reconcile behind it.
type portAllocator struct {
	mu       sync.Mutex
	claims   map[int32]portClaim
	draining map[int32]portClaim
	loadedAt time.Time

	// pick chooses a free port. Tests make it predictable.
	pick func(used func(int32) bool) (int32, error)
}

// assign gives each key a port, keeping the existing ports where they're
// still the owner's, and claims them for it, unless that's more than limits
// allow. Ports it no longer needs start draining.
func (a *portAllocator) assign(ctx context.Context, reader routeReader, now time.Time, holder portHolder,
	limits portLimits, existing map[routeKey]int32, keys []routeKey) ([]v1alpha1.DoormanRoute, error) {
	if err := a.load(ctx, reader, now); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for port, d := range a.draining {
		if !now.Before(d.at) {
			delete(a.draining, port)
		}
	}
	if err := a.withinLimits(holder, limits, len(keys)); err != nil {
		return nil, err
	}
	owner := holder.uid

	chosen := map[int32]bool{}
	used := func(port int32) bool { return chosen[port] || !a.free(port, owner) }
	pick := a.pick
	if pick == nil {
		pick = doorman.AllocatePort
	}
	routes := make([]v1alpha1.DoormanRoute, 0, len(keys))
	for _, key := range keys {
		port, ok := existing[key]
		if !ok || !doorman.InRange(port) || used(port) {
			var err error
			if port, err = pick(used); err != nil {
				return nil, err
			}
		}
		chosen[port] = true
		routes = append(routes, v1alpha1.DoormanRoute{Service: key.service, PortName: key.portName, DoormanPort: port})
	}

	a.releaseLocked(owner, now, chosen)
	for port := range chosen {
		a.claims[port] = portClaim{owner: owner, namespace: holder.namespace, at: now}
		delete(a.draining, port)
	}
	return routes, nil
}

// fits reports, with a *tooManyPortsError, whether holder could be given n
// ports now without going over limits, as assign would find.
func (a *portAllocator) fits(ctx context.Context, reader routeReader, now time.Time, holder portHolder,
	limits portLimits, n int) error {
	if err := a.load(ctx, reader, now); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.withinLimits(holder, limits, n)
}

// withinLimits reports whether holder may hold n ports. a.mu must be held.
func (a *portAllocator) withinLimits(holder portHolder, limits portLimits, n int) error {
	if n > limits.perWorkload {
		return &tooManyPortsError{holder: "the workload", want: n, limit: limits.perWorkload}
	}
	inNamespace := n
	for _, c := range a.claims {
		if c.namespace == holder.namespace && c.owner != holder.uid {
			inNamespace++
		}
	}
	if inNamespace > limits.perNamespace {
		return &tooManyPortsError{holder: "namespace " + holder.namespace, want: inNamespace, limit: limits.perNamespace}
	}
	return nil
}

func (a *portAllocator) free(port int32, owner types.UID) bool {
	if c, ok := a.claims[port]; ok && c.owner != owner {
		return false
	}
	if d, ok := a.draining[port]; ok && d.owner != owner {
		return false
	}
	return true
}

// release lets go of every port the workload holds. They drain before
// another workload may have them.
func (a *portAllocator) release(owner types.UID, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.releaseLocked(owner, now, nil)
}

func (a *portAllocator) releaseLocked(owner types.UID, now time.Time, keep map[int32]bool) {
	for port, c := range a.claims {
		if c.owner == owner && !keep[port] {
			delete(a.claims, port)
			if a.draining == nil {
				a.draining = map[int32]portClaim{}
			}
			a.draining[port] = portClaim{owner: owner, at: now.Add(portDrain)}
		}
	}
}

func (a *portAllocator) loadDue(now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.claims == nil || now.Sub(a.loadedAt) >= portsReload
}

// load reads every workload's routes and doorman slices from the API
// server, the first time and every portsReload after, and merges them into
// the claims. Claims made since the last load are kept, since their status
// may not be written yet.
func (a *portAllocator) load(ctx context.Context, reader routeReader, now time.Time) error {
	if !a.loadDue(now) {
		return nil
	}
	inUse, err := reader.list(ctx)
	if err != nil {
		return fmt.Errorf("reading doorman ports in use: %w", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.claims != nil && now.Sub(a.loadedAt) < portsReload {
		return nil
	}
	a.merge(inUse, now)
	return nil
}

// merge replaces the claims with those in inUse, read at now, and the
// claims made since the last load. A port a slice holds is its owner's,
// unless a status claims it first. a.mu must be held.
func (a *portAllocator) merge(inUse portsInUse, now time.Time) {
	if a.draining == nil {
		a.draining = map[int32]portClaim{}
	}
	claims := map[int32]portClaim{}
	claim := func(port int32, owner types.UID, namespace string) {
		if _, taken := claims[port]; taken {
			return
		}
		// Given up while the read was under way: it's draining now.
		if d, ok := a.draining[port]; ok && d.owner == owner {
			return
		}
		claims[port] = portClaim{owner: owner, namespace: namespace, at: now}
	}
	for _, w := range inUse.workloads {
		for _, route := range w.Status.Doorman {
			claim(route.DoormanPort, w.UID, w.Namespace)
		}
		for _, d := range w.Status.DoormanDraining {
			if _, known := a.draining[d.Port]; !known && now.Before(d.Until.Time) {
				a.draining[d.Port] = portClaim{owner: w.UID, at: d.Until.Time}
			}
		}
	}
	for _, slice := range inUse.slices {
		owner := metav1.GetControllerOf(&slice)
		if owner == nil {
			continue
		}
		for _, port := range doormanPortsOf(&slice) {
			claim(port, owner.UID, slice.Namespace)
		}
	}
	for port, c := range a.claims {
		if _, ok := claims[port]; !ok && c.at.After(a.loadedAt) {
			claims[port] = c
		}
	}
	a.claims, a.loadedAt = claims, now
}

// failureBackoff counts each workload's doorman failures in a row.
type failureBackoff struct {
	mu       sync.Mutex
	failures map[types.UID]int
}

// next records a failure and returns how long to wait before retrying.
func (b *failureBackoff) next(uid types.UID) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failures == nil {
		b.failures = map[types.UID]int{}
	}
	n := b.failures[uid]
	b.failures[uid] = n + 1
	return min(doormanRetryMin<<min(n, 10), doormanRetryMax)
}

func (b *failureBackoff) reset(uid types.UID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.failures, uid)
}

func routesFor(routes []v1alpha1.DoormanRoute, service string) []v1alpha1.DoormanRoute {
	var out []v1alpha1.DoormanRoute
	for _, route := range routes {
		if route.Service == service {
			out = append(out, route)
		}
	}
	return out
}

// doormanSlice is what a Service's doorman slices say, in each address
// family.
type doormanSlice struct {
	ports     []discoveryv1.EndpointPort
	endpoints map[discoveryv1.AddressType][]discoveryv1.Endpoint

	// forwarded points the Service at the workload's forwarder, with
	// doormanPorts recording the doorman's ports (see
	// annotationDoormanPorts).
	forwarded    bool
	doormanPorts string
}

// applyDoormanSlice creates or updates the EndpointSlice that points one
// address family of a Service at the doorman. Each port keeps its Service
// port name, which is how kube-proxy matches a slice port to a Service port,
// so traffic to the Service port reaches the doorman port allocated for it.
func (r *Reconciler) applyDoormanSlice(ctx context.Context, workload *v1alpha1.ManagedWorkload, svc corev1.Service,
	name string, family discoveryv1.AddressType, want doormanSlice) error {
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: workload.Namespace},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, slice, func() error {
		if slice.ResourceVersion != "" && slice.Labels[discoveryv1.LabelManagedBy] != doorman.ManagedBy {
			return fmt.Errorf("endpointslice %s exists and isn't the doorman's", slice.Name)
		}
		slice.Labels = map[string]string{
			discoveryv1.LabelServiceName: svc.Name,
			discoveryv1.LabelManagedBy:   doorman.ManagedBy,
			doorman.LabelManagedWorkload: doorman.WorkloadLabel(workload.Name),
		}
		// Proxies use this to pick the Services they serve, so the doorman's
		// slice must carry it whenever the Service's own slices do.
		if proxy, ok := svc.Labels[labelServiceProxyName]; ok {
			slice.Labels[labelServiceProxyName] = proxy
		}
		if r.OperatorNamespace != "" {
			slice.Labels[v1alpha1.LabelOperator] = r.OperatorNamespace
		}
		delete(slice.Annotations, annotationDoormanPorts)
		if want.forwarded {
			slice.Labels[labelForwarded] = v1alpha1.True
			metav1.SetMetaDataAnnotation(&slice.ObjectMeta, annotationDoormanPorts, want.doormanPorts)
		}
		slice.AddressType = family
		slice.Endpoints = want.endpoints[family]
		slice.Ports = want.ports
		return controllerutil.SetControllerReference(workload, slice, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("applying doorman slice for service %s: %w", svc.Name, err)
	}
	return nil
}

// claimedSlice tells apart the two reasons creating a doorman slice the
// cache doesn't have finds one already there. The cache holds only the
// doorman's slices, so the one there is either the doorman's, written
// moments ago, and createErr is returned for the write to be repeated; or
// someone else's under the same name, which the doorman never overwrites,
// and routing that Service has failed.
func (r *Reconciler) claimedSlice(ctx context.Context, namespace, name string, createErr error) error {
	ctx, cancel := context.WithTimeout(ctx, endpointsReadTimeout)
	defer cancel()
	var slice discoveryv1.EndpointSlice
	if err := r.apiReader().Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &slice); err != nil {
		if apierrors.IsNotFound(err) {
			return createErr
		}
		return fmt.Errorf("reading endpointslice %s: %w", name, err)
	}
	if slice.Labels[discoveryv1.LabelManagedBy] != doorman.ManagedBy {
		return fmt.Errorf("endpointslice %s exists and isn't the doorman's", name)
	}
	return createErr
}

// listDoormanSlices lists the doorman slices the workload has written.
func (r *Reconciler) listDoormanSlices(ctx context.Context, reader client.Reader,
	workload *v1alpha1.ManagedWorkload) ([]discoveryv1.EndpointSlice, error) {
	ctx, cancel := context.WithTimeout(ctx, endpointsReadTimeout)
	defer cancel()
	var list discoveryv1.EndpointSliceList
	if err := reader.List(ctx, &list, client.InNamespace(workload.Namespace), client.MatchingLabels{
		discoveryv1.LabelManagedBy:   doorman.ManagedBy,
		doorman.LabelManagedWorkload: doorman.WorkloadLabel(workload.Name),
	}); err != nil {
		return nil, fmt.Errorf("listing doorman slices: %w", err)
	}
	return list.Items, nil
}

// deleteDoormanSlices removes the slices except those in keep, so a
// workload that's awake again routes only to its own pods.
func (r *Reconciler) deleteDoormanSlices(ctx context.Context, current []discoveryv1.EndpointSlice, keep map[string]bool) error {
	for i := range current {
		slice := &current[i]
		if keep[slice.Name] {
			continue
		}
		if err := r.Delete(ctx, slice); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("deleting doorman slice %s: %w", slice.Name, err)
		}
	}
	return nil
}

// EndpointSliceCache is the operator's cache of EndpointSlices: the doorman
// slices it writes into workloads' namespaces, the watched ones or all, and
// every slice in doormanNamespace, where the doorman Service's own are.
// Empty doormanNamespace means the doorman is disabled. Every other slice,
// which on a large cluster is most of them, is left out: a Service's own
// slices are read from the API server when a paused workload is routed.
func EndpointSliceCache(watched []string, doormanNamespace string) cache.ByObject {
	doormanSlices := cache.Config{
		LabelSelector: labels.SelectorFromSet(labels.Set{discoveryv1.LabelManagedBy: doorman.ManagedBy}),
	}
	namespaces := map[string]cache.Config{}
	if len(watched) == 0 {
		namespaces[cache.AllNamespaces] = doormanSlices
	}
	for _, ns := range watched {
		namespaces[ns] = doormanSlices
	}
	if doormanNamespace != "" {
		namespaces[doormanNamespace] = cache.Config{LabelSelector: labels.Everything()}
	}
	return cache.ByObject{Namespaces: namespaces}
}

// isDoormanEndpoints reports whether obj is one of the doorman Service's own
// EndpointSlices. Of the slices the operator caches, only those say anything
// about where a paused workload's Services should send traffic.
func (r *Reconciler) isDoormanEndpoints(obj client.Object) bool {
	return obj.GetNamespace() == r.DoormanNamespace && obj.GetLabels()[discoveryv1.LabelServiceName] == r.DoormanService
}

// findWorkloadsForDoorman re-queues every paused or resuming workload when
// the IP families the doorman has Ready pods in change: one that has none
// left stops being routed, and one that has some again is routed anew. A
// change of pods within them, as in a rolling update, is the
// doormanSliceSync's, which moves every slice without a full reconcile.
func (r *Reconciler) findWorkloadsForDoorman(ctx context.Context, _ client.Object) []reconcile.Request {
	endpoints, err := r.doormanEndpoints(ctx)
	if err != nil {
		log.FromContext(ctx).Error(err, "reading doorman endpoints for doorman change")
	} else if !r.doormanFamilies.changed(endpoints) {
		return nil
	}
	var list v1alpha1.ManagedWorkloadList
	if err := r.List(ctx, &list, client.UnsafeDisableDeepCopy); err != nil {
		log.FromContext(ctx).Error(err, "listing managed workloads for doorman change")
		return nil
	}
	var requests []reconcile.Request
	for i := range list.Items {
		if w := &list.Items[i]; w.Status.Phase == v1alpha1.PhasePaused || w.Status.Phase == v1alpha1.PhaseResuming {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(w)})
		}
	}
	return requests
}

// findPausedWorkloadsInNamespace re-queues paused workloads when a Service
// in their namespace changes, since it may now select their pods or be
// behind GKE's NEGs, or an Ingress there does, which may put one of their
// Services behind NEGs or take it out.
func (r *Reconciler) findPausedWorkloadsInNamespace(ctx context.Context, obj client.Object) []reconcile.Request {
	var list v1alpha1.ManagedWorkloadList
	if err := r.List(ctx, &list, client.InNamespace(obj.GetNamespace()), client.UnsafeDisableDeepCopy); err != nil {
		log.FromContext(ctx).Error(err, "listing managed workloads for service change", "namespace", obj.GetNamespace())
		return nil
	}
	var requests []reconcile.Request
	for i := range list.Items {
		if w := &list.Items[i]; w.Status.Phase == v1alpha1.PhasePaused {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(w)})
		}
	}
	return requests
}

// findWorkloadsInNamespace requeues every ManagedWorkload in a namespace
// whose labels changed, such as being protected.
func (r *Reconciler) findWorkloadsInNamespace(ctx context.Context, obj client.Object) []reconcile.Request {
	var list v1alpha1.ManagedWorkloadList
	if err := r.List(ctx, &list, client.InNamespace(obj.GetName()), client.UnsafeDisableDeepCopy); err != nil {
		log.FromContext(ctx).Error(err, "listing managed workloads for namespace change", "namespace", obj.GetName())
		return nil
	}
	requests := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return requests
}

const (
	// doormanListenWait is how long after a workload starts pausing its
	// Services are routed through the doorman: the routes reach every
	// doorman replica through status, which each loads every
	// doorman.RouteSyncInterval, and a slice sending connections to a port
	// it doesn't listen on yet would refuse them while the pods still serve.
	doormanListenWait = 2*doorman.RouteSyncInterval + time.Second

	// doormanTakeover is how long a pause waits, once the doorman's slices
	// stand beside the pods, before it scales them down: kube-proxy and
	// ingress controllers pick up endpoint changes about once a second, and
	// until they do, a connection sent to a pod that has stopped is refused.
	doormanTakeover = 5 * time.Second

	// doormanFrontLimit bounds the wait for the doorman before a pause
	// scales down, from when the pause began: whatever keeps the doorman
	// from fronting the workload, the pause goes on as it would without it.
	doormanFrontLimit = time.Minute
)
