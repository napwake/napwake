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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

const (
	// conditionWakeRequest says what came of the last wake request, and
	// when: True, reason Woken or AlreadyAwake, when it woke the workload or
	// found it awake already, and False, with why, when Napwake couldn't
	// act on it. Like PauseRequest, it answers that request until the next
	// one, through every pause and wake meanwhile, so it says what happened,
	// in the past tense.
	conditionWakeRequest = "WakeRequest"

	ReasonWakeRequested = "WakeRequested"

	reasonWoken        = "Woken"
	reasonAlreadyAwake = "AlreadyAwake"
	reasonBeingDeleted = "BeingDeleted"

	reasonPredatesStatus = "PredatesStatus"

	wakeRequestedMessage = "wake requested, waking"
)

// pendingWakeRequest is the napwake.dev/wake-requested value Napwake
// hasn't acted on yet, if there is one.
func pendingWakeRequest(workload *v1alpha1.ManagedWorkload) (string, bool) {
	token := workload.Annotations[v1alpha1.AnnotationWakeRequested]
	return token, token != "" && token != workload.Status.LastWakeRequest
}

// answerWakeRequest marks the pending wake request handled, with what came
// of it, for the caller to write.
func (r *Reconciler) answerWakeRequest(workload *v1alpha1.ManagedWorkload, status metav1.ConditionStatus,
	reason, message string) {
	token, _ := pendingWakeRequest(workload)
	workload.Status.LastWakeRequest = token
	r.setCondition(workload, conditionWakeRequest, status, reason, message)
}

// refuseWakeRequest answers a pending wake request that Napwake can't act
// on, saying why. It's answered rather than left pending, so it isn't tried
// again on every reconcile, and the event is sent once the answer is
// written, so a failed write doesn't send it twice. Without a pending
// request it does nothing.
func (r *Reconciler) refuseWakeRequest(ctx context.Context, workload *v1alpha1.ManagedWorkload,
	eventType, reason, why string) error {
	if _, ok := pendingWakeRequest(workload); !ok {
		return nil
	}
	r.answerWakeRequest(workload, metav1.ConditionFalse, reason, fmt.Sprintf("not woken at %s: %s",
		answeredAt(r.now()), why))
	if err := r.Status().Update(ctx, workload); err != nil {
		return fmt.Errorf("answering the wake request: %w", err)
	}
	r.emitEvent(workload, false, eventType, reason, actionResume, "wake requested, but %s", why)
	return nil
}

// refuseRequests answers the pending pause and wake requests of a workload
// Napwake isn't managing as it stands, saying why, with a warning.
func (r *Reconciler) refuseRequests(ctx context.Context, workload *v1alpha1.ManagedWorkload, reason, why string) error {
	if err := r.refusePauseRequest(ctx, workload, "Warning", reason, why); err != nil {
		return err
	}
	return r.refuseWakeRequest(ctx, workload, "Warning", reason, why)
}

// predatesStatus is why a request already on a ManagedWorkload as its
// status starts afresh isn't acted on.
const predatesStatus = "the request was on the ManagedWorkload before Napwake's record of it began, as when " +
	"it's restored without its status, so it may have been answered already; set a new value to ask again"

// answerEarlierPauseRequest answers, without acting on it, a pause request
// a ManagedWorkload carries as its status starts afresh. Status is
// Napwake's only record of which requests it has answered, and
// annotations outlive it: a backup restored without status, kubectl
// replace --force or an exported manifest applied again brings back
// requests answered long ago, which would otherwise pause the workload
// again. The answer is for the caller to write, and the event it returns,
// to send once it's written.
func (r *Reconciler) answerEarlierPauseRequest(workload *v1alpha1.ManagedWorkload) (announce func()) {
	if _, ok := pendingPauseRequest(workload); !ok {
		return func() {}
	}
	r.answerPauseRequest(workload, metav1.ConditionFalse, reasonPredatesStatus,
		fmt.Sprintf("not paused at %s: %s", answeredAt(r.now()), predatesStatus))
	return func() {
		r.emitEvent(workload, false, "Normal", reasonPredatesStatus, actionPause, "pause requested, but %s",
			predatesStatus)
	}
}

// answerEarlierWakeRequest answers, without acting on it, a wake request
// a ManagedWorkload carries as its pause is taken up again after its status
// was lost, as answerEarlierPauseRequest does a pause request, so an old
// request doesn't wake it.
func (r *Reconciler) answerEarlierWakeRequest(workload *v1alpha1.ManagedWorkload) (announce func()) {
	if _, ok := pendingWakeRequest(workload); !ok {
		return func() {}
	}
	r.answerWakeRequest(workload, metav1.ConditionFalse, reasonPredatesStatus,
		fmt.Sprintf("not woken at %s: %s", answeredAt(r.now()), predatesStatus))
	return func() {
		r.emitEvent(workload, false, "Normal", reasonPredatesStatus, actionResume, "wake requested, but %s",
			predatesStatus)
	}
}

// answerAwakeWakeRequest answers a wake request that finds the workload
// awake, and restarts its idle clock: whoever asked means to use it, as
// kubectl napwake wake has always restarted it. Under dry-run that ends a
// would-be pause, as activity does. A paused workload's request is the
// wake's to answer, with the look at its demand that sees it, one pausing
// is answered as it calls the pause off, and one waking is answered once
// that's done, by the reconcile that finds it Running.
func (r *Reconciler) answerAwakeWakeRequest(workload *v1alpha1.ManagedWorkload) {
	if _, ok := pendingWakeRequest(workload); !ok {
		return
	}
	switch workload.Status.Phase {
	case v1alpha1.PhaseRunning, v1alpha1.PhaseIdle:
	default:
		return
	}
	now := r.clockTime()
	r.answerWakeRequest(workload, metav1.ConditionTrue, reasonAlreadyAwake, "already awake at "+answeredAt(now.Time))
	if a := workload.Status.Activity; a != nil {
		recordActivity(a, now.Time, v1alpha1.ActivitySourceAnnotation)
	}
}

// takeWakeRequest answers a paused workload's pending wake request as
// woken, for the wake it asks for to write, and reports whether there was
// one.
func (r *Reconciler) takeWakeRequest(workload *v1alpha1.ManagedWorkload) bool {
	if _, ok := pendingWakeRequest(workload); !ok {
		return false
	}
	r.answerWakeRequest(workload, metav1.ConditionTrue, reasonWoken, "woken as requested at "+answeredAt(r.now()))
	return true
}
