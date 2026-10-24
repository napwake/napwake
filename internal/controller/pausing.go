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
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

// ReasonPauseCancelled says a pause under way was called off: something
// that would have woken the workload came as it paused.
const ReasonPauseCancelled = "PauseCancelled"

// A pause of a workload the doorman routes goes in three steps, so that no
// request is refused while its pods stop:
//
//  1. As it starts pausing, its routes are written to status, and every
//     doorman replica opens their ports. Nothing is sent there yet.
//  2. doormanListenWait later, the doorman's EndpointSlices stand beside the
//     pods' own. The doorman passes what it's sent straight to a Ready pod.
//  3. doormanTakeover later, once proxies send to the doorman too, the
//     workload is scaled to zero. A request that comes as its pods stop is
//     held, as for any paused workload.
//
// A workload the doorman doesn't route, or can't now, pauses at once, as it
// always has, and none waits more than doormanFrontLimit for the doorman.

// routeAsPauseBegins writes the routes of a workload that has just started
// pausing to status, for the doorman to listen on, and reports whether
// there are any, which the scale-down then waits for the doorman to front.
func (r *Reconciler) routeAsPauseBegins(ctx context.Context, workload *v1alpha1.ManagedWorkload,
	target client.Object) (bool, error) {
	if r.DoormanService == "" {
		return false, nil
	}
	r.routeDoorman(ctx, workload, target)
	if len(workload.Status.Doorman) == 0 {
		return false, nil
	}
	if err := r.Status().Update(ctx, workload); err != nil {
		return false, fmt.Errorf("recording the doorman's routes as the pause begins: %w", err)
	}
	return true, nil
}

// untilDoormanListens is how long the doorman's slices for a workload that
// has started pausing, and whose pods still run, wait for every doorman
// replica to listen on its routes.
func (r *Reconciler) untilDoormanListens(workload *v1alpha1.ManagedWorkload) time.Duration {
	if workload.Status.Phase != v1alpha1.PhasePausing || scaledDownByNapwake(workload) {
		return 0
	}
	return max(doormanListenWait-r.sinceTransition(workload), 0)
}

// untilFronted is how long a pausing workload's scale-down waits for the
// doorman to stand in front of its Services, or zero once it may go ahead:
// it's scaled down already, isn't routed, the doorman doesn't front it after
// all, or it has waited doormanFrontLimit. The doorman fronts it from when
// its slices are first in place, which is recorded in status, so a restart
// in between doesn't cut the wait short.
func (r *Reconciler) untilFronted(ctx context.Context, workload *v1alpha1.ManagedWorkload) (time.Duration, error) {
	pause := workload.Status.Pause
	if pause == nil || pause.PausedAt != nil || len(workload.Status.Doorman) == 0 {
		return 0, nil
	}
	since := r.sinceTransition(workload)
	switch {
	case since >= doormanFrontLimit:
		return 0, nil
	case since < doormanListenWait:
		return doormanListenWait - since, nil
	case !meta.IsStatusConditionTrue(workload.Status.Conditions, conditionWakeOnRequest):
		return 0, nil
	}
	if pause.RoutedAt == nil {
		now := r.clockTime()
		pause.RoutedAt = &now
		if err := r.Status().Update(ctx, workload); err != nil {
			return 0, fmt.Errorf("recording the doorman in front of the workload: %w", err)
		}
		return doormanTakeover, nil
	}
	return max(doormanTakeover-r.now().Sub(pause.RoutedAt.Time), 0), nil
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
