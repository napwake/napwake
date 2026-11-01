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
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/forecast"
)

// ReasonPauseCancelled says a pause under way was called off: something
// that would have woken the workload came as it paused.
const ReasonPauseCancelled = "PauseCancelled"

// A pause of a workload the doorman routes goes in three steps, so that no
// request is refused while its pods stop:
//
//  1. As it starts pausing, its routes are stored in status, and every
//     doorman replica opens their ports. Nothing is sent there yet. Routes
//     whose write fails are written again by the next reconcile.
//  2. doormanListenWait after they're stored, the doorman's EndpointSlices
//     stand beside the pods' own. The doorman passes what it's sent
//     straight to a Ready pod.
//  3. doormanTakeover after the slices first stand, once proxies send to
//     the doorman too, the workload is scaled to zero. A request that comes
//     as its pods stop is held, as for any paused workload.
//
// A workload the doorman doesn't route, or can't now, pauses at once, as it
// always has, and none waits more than doormanFrontLimit for the doorman to
// front it. Once it does, the takeover is always waited.

// routeAsPauseBegins stores the routes of a workload that has just started
// pausing in status, for the doorman to listen on, and reports whether
// there are any, which the scale-down then waits for the doorman to front,
// and how soon to look again.
func (r *Reconciler) routeAsPauseBegins(ctx context.Context, workload *v1alpha1.ManagedWorkload,
	target client.Object) (bool, time.Duration) {
	if r.DoormanService == "" {
		return false, 0
	}
	recheck := r.routeDoorman(ctx, workload, target)
	return len(workload.Status.Doorman) > 0, shortest(doormanListenWait, recheck)
}

// untilDoormanListens is how long the doorman's slices for a workload that
// has started pausing, and whose pods still run, wait for every doorman
// replica to listen on its routes: doormanListenWait from when they were
// stored in status, where the doorman reads them. Routes not yet stored are
// written now, and timed from then. A write that conflicts, as one from a
// cache that hasn't seen the move to Pausing does, is tried again soon.
func (r *Reconciler) untilDoormanListens(ctx context.Context, workload *v1alpha1.ManagedWorkload) (time.Duration, error) {
	pause := workload.Status.Pause
	if workload.Status.Phase != v1alpha1.PhasePausing || pause == nil || scaledDownByNapwake(workload) ||
		len(workload.Status.Doorman) == 0 {
		return 0, nil
	}
	if pause.RoutesWrittenAt == nil {
		now := r.clockTime()
		pause.RoutesWrittenAt = &now
		err := r.Status().Update(ctx, workload)
		switch {
		case apierrors.IsConflict(err):
			pause.RoutesWrittenAt = nil
			return staleCacheRetry, nil
		case err != nil:
			pause.RoutesWrittenAt = nil
			return 0, fmt.Errorf("storing the doorman's routes as the pause begins: %w", err)
		}
		return doormanListenWait, nil
	}
	return max(doormanListenWait-r.now().Sub(pause.RoutesWrittenAt.Time), 0), nil
}

// untilDoormanReady is how long a pausing workload's first doorman slices
// wait: for every doorman replica to have had time to read its routes, then
// for each to answer on them.
func (r *Reconciler) untilDoormanReady(ctx context.Context, workload *v1alpha1.ManagedWorkload,
	endpoints map[discoveryv1.AddressType][]discoveryv1.Endpoint) (time.Duration, error) {
	if wait, err := r.untilDoormanListens(ctx, workload); err != nil || wait > 0 {
		return wait, err
	}
	return r.untilDoormanAnswers(ctx, workload, endpoints), nil
}

// doormanDialTimeout is how long a doorman replica is given to accept a
// connection on a route's port before it's taken as not listening yet.
const doormanDialTimeout = 500 * time.Millisecond

// untilDoormanAnswers is how long a pausing workload's first doorman slices
// wait for every Ready doorman replica to accept connections on the
// workload's routes. The listen wait gives the replicas time to read the
// routes, and this checks each did: a slow one, as on a busy node, would
// refuse what its slice is sent while the pods still serve. A connection
// that sends nothing is a TCP check to the doorman, which wakes nothing and
// calls no pause off. Passed to a Ready pod, it isn't counted in the
// doorman's metrics. With none of the workload's pods Ready, it counts as
// ignored. It waits no longer than the scale-down does, which untilFronted
// lets go on after doormanFrontLimit whatever this finds.
func (r *Reconciler) untilDoormanAnswers(ctx context.Context, workload *v1alpha1.ManagedWorkload,
	endpoints map[discoveryv1.AddressType][]discoveryv1.Endpoint) time.Duration {
	pause := workload.Status.Pause
	if r.DialDoorman == nil || workload.Status.Phase != v1alpha1.PhasePausing || pause == nil ||
		pause.RoutedAt != nil || scaledDownByNapwake(workload) {
		return 0
	}
	for _, family := range []discoveryv1.AddressType{discoveryv1.AddressTypeIPv4, discoveryv1.AddressTypeIPv6} {
		for _, ep := range endpoints[family] {
			for _, route := range workload.Status.Doorman {
				addr := net.JoinHostPort(ep.Addresses[0], strconv.Itoa(int(route.DoormanPort)))
				if err := r.DialDoorman(ctx, addr); err != nil {
					log.FromContext(ctx).V(1).Info("a doorman replica doesn't answer on a pausing workload's route yet",
						"workload", workload.Name, "namespace", workload.Namespace, "address", addr, "error", err.Error())
					return staleCacheRetry
				}
			}
		}
	}
	return 0
}

// DialTCP opens a TCP connection to addr and closes it, sending nothing.
func DialTCP(ctx context.Context, addr string) error {
	d := net.Dialer{Timeout: doormanDialTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return conn.Close()
}

// untilFronted is how long a pausing workload's scale-down waits for the
// doorman to stand in front of its Services, or zero once it may go ahead:
// it's scaled down already, isn't routed, the doorman can't front it after
// all, or it has waited doormanFrontLimit for its routes to be listened on.
// Once its routes are, the reconcile that puts the slices in place records
// it in status.pause.routedAt, and the pause always waits doormanTakeover
// from then, so neither a restart nor a late reconcile cuts the wait short.
func (r *Reconciler) untilFronted(workload *v1alpha1.ManagedWorkload) time.Duration {
	pause := workload.Status.Pause
	if pause == nil || pause.PausedAt != nil || len(workload.Status.Doorman) == 0 {
		return 0
	}
	if pause.RoutedAt != nil {
		return max(doormanTakeover-r.now().Sub(pause.RoutedAt.Time), 0)
	}
	listening := doormanListenWait
	if pause.RoutesWrittenAt != nil {
		listening = max(doormanListenWait-r.now().Sub(pause.RoutesWrittenAt.Time), 0)
	}
	frontLimit := max(doormanFrontLimit-r.sinceTransition(workload), 0)
	switch {
	case listening > 0:
		return min(listening, frontLimit)
	case meta.IsStatusConditionFalse(workload.Status.Conditions, conditionWakeOnRequest):
		return 0
	}
	// Listened on, but this reconcile's routing came before the listen wait
	// ended, or couldn't record the slices: the next puts them in place.
	return min(staleCacheRetry, frontLimit)
}

// recordFronted records in status.pause.routedAt that the doorman's slices
// now stand in front of a pausing workload, still up, for the first time
// this pause, from which the scale-down waits doormanTakeover. A write that
// conflicts is tried again soon.
func (r *Reconciler) recordFronted(ctx context.Context, workload *v1alpha1.ManagedWorkload) (time.Duration, error) {
	pause := workload.Status.Pause
	if workload.Status.Phase != v1alpha1.PhasePausing || pause == nil || pause.RoutedAt != nil ||
		scaledDownByNapwake(workload) {
		return 0, nil
	}
	now := r.clockTime()
	pause.RoutedAt = &now
	err := r.Status().Update(ctx, workload)
	switch {
	case apierrors.IsConflict(err):
		pause.RoutedAt = nil
		return staleCacheRetry, nil
	case err != nil:
		pause.RoutedAt = nil
		return 0, fmt.Errorf("recording the doorman in front of the workload: %w", err)
	}
	return doormanTakeover, nil
}

// heldAsItPauses says what calls off a pause about to scale the workload to
// zero because its dependents hold it now, or nil. Its hold was decided as
// the pause began, from what the graph had then, and nothing re-checked it
// while the doorman's takeover was waited: a dependent may since have
// learned it depends on the workload, or woken without the graph showing
// it yet. So the dependents are read from the API server once more, as the
// last step before anything is scaled.
func (r *Reconciler) heldAsItPauses(ctx context.Context, workload *v1alpha1.ManagedWorkload) (*phaseEvent, error) {
	holds, err := r.heldByDependenciesNow(ctx, workload)
	if err != nil || !holds.held() {
		return nil, err
	}
	what := "dependsOn formed a cycle through it"
	switch n := len(holds.holders); {
	case n == 1:
		what = "its dependent " + holds.holders[0] + " is awake"
	case n > 1:
		what = "its dependents " + listNames(holds.holders) + " are awake"
	}
	why := what + " as it paused"
	if workload.Status.Pause.Requested {
		r.setCondition(workload, conditionPauseRequest, metav1.ConditionFalse, ReasonPauseCancelled,
			fmt.Sprintf("not paused at %s: %s", answeredAt(r.now()), why))
	}
	// No cause: it was its dependents' use, not its own, so the pause isn't
	// judged wrong.
	return &phaseEvent{reason: ReasonPauseCancelled, message: why + "; the pause is cancelled"}, nil
}

// pauseCancelled says what calls off a workload's pause under way, or nil
// if nothing does: whatever would wake it once paused, come since the pause
// began, which shows it's in use. That's a request the doorman held or
// passed on, a person asking for a workload that asks first, a wake
// request, or an activity annotation, which a waking dependent stamps. The
// wake request, and the pause request the pause was for, are answered here,
// for the wake to write.
func (r *Reconciler) pauseCancelled(ctx context.Context, workload *v1alpha1.ManagedWorkload,
	target client.Object) (*phaseEvent, error) {
	pause := workload.Status.Pause
	if pause == nil {
		return nil, nil
	}
	var what string
	var cause v1alpha1.WakeCause
	switch {
	case r.takeWakeRequest(workload):
		what, cause = "a wake request came", v1alpha1.WokenByWakeRequest
	case !r.wokenByActivity(workload, target):
		return nil, nil
	case requestedSince(workload, pause):
		cause = requestCause(workload)
		what = wakeCauses[cause]
	default:
		cause = v1alpha1.WokenByAnnotation
		holds, err := r.heldByDependencies(ctx, workload)
		if err != nil {
			return nil, err
		}
		what = "an activity annotation came"
		switch n := len(holds.holders); {
		case n == 1:
			what = "its dependent " + holds.holders[0] + " woke"
		case n > 1:
			what = "its dependents " + listNames(holds.holders) + " woke"
		}
	}
	why := what + " as it paused"
	if pause.Requested {
		r.setCondition(workload, conditionPauseRequest, metav1.ConditionFalse, ReasonPauseCancelled,
			fmt.Sprintf("not paused at %s: %s", answeredAt(r.now()), why))
	}
	return &phaseEvent{reason: ReasonPauseCancelled, message: why + "; the pause is cancelled", cause: cause}, nil
}

// seeTurnedBack is a look at the demand of a workload whose pause why calls
// off. A request through the doorman shows forecast.RequestDemand, in the
// hour of the look, as one that wakes it from zero does in
// seePausedDemand. No look is made while a workload pauses, and the wake's
// warm-up hides the request from the looks at its CPU after, so without
// this a workload whose use shows only in requests, its CPU under the
// threshold, would learn the hours its pauses are turned back in from its
// CPU, and the forecast wouldn't hold its pauses there. Anything else that
// calls a pause off, a click for a workload that asks first included, is
// learned from what the workload does once awake, as before.
func (r *Reconciler) seeTurnedBack(ctx context.Context, workload *v1alpha1.ManagedWorkload, why *phaseEvent) {
	if why.cause != v1alpha1.WokenByRequest {
		return
	}
	r.seeDemand(ctx, workload, r.forecastEngine(workload), forecast.RequestDemand(cpuThresholdFor(workload)), true)
}
