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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	opmetrics "github.com/napwake/napwake/internal/metrics"
)

const (
	// conditionPauseRequest says what came of the last pause request, and
	// when: True, reason Paused or AlreadyPaused, when it paused the
	// workload or found it paused already, and False, with why, when it
	// didn't. It answers that request until the next one, through every
	// wake and idle pause meanwhile, so it says what happened, in the past
	// tense, rather than what's happening.
	conditionPauseRequest = "PauseRequest"

	ReasonPauseRequested = "PauseRequested"

	reasonAlreadyPaused = "AlreadyPaused"
	reasonActiveUntil   = "ActiveUntil"
	reasonDryRun        = "DryRun"

	reasonForecastExpectsDemand = "ForecastExpectsDemand"
)

// answeredAt is when a pause request was answered, as its condition says
// it: to the minute, in UTC, which kubectl describe shows as it is.
func answeredAt(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

// pendingPauseRequest is the napwake.dev/pause-requested value Napwake
// hasn't acted on yet, if there is one.
func pendingPauseRequest(workload *v1alpha1.ManagedWorkload) (string, bool) {
	token := workload.Annotations[v1alpha1.AnnotationPauseRequested]
	return token, token != "" && token != workload.Status.LastPauseRequest
}

// answerPauseRequest marks the pending pause request handled, with what
// came of it, for the caller to write.
func (r *Reconciler) answerPauseRequest(workload *v1alpha1.ManagedWorkload, status metav1.ConditionStatus,
	reason, message string) {
	token, _ := pendingPauseRequest(workload)
	workload.Status.LastPauseRequest = token
	r.setCondition(workload, conditionPauseRequest, status, reason, message)
}

// refusePauseRequest answers a pending pause request that Napwake won't
// act on, saying why. It's answered rather than left pending, so it isn't
// tried again on every reconcile, and the event is sent once the answer is
// written, so a failed write doesn't send it twice. Without a pending
// request it does nothing.
func (r *Reconciler) refusePauseRequest(ctx context.Context, workload *v1alpha1.ManagedWorkload,
	eventType, reason, why string) error {
	if _, ok := pendingPauseRequest(workload); !ok {
		return nil
	}
	r.answerPauseRequest(workload, metav1.ConditionFalse, reason, fmt.Sprintf("not paused at %s: %s",
		answeredAt(r.now()), why))
	if err := r.Status().Update(ctx, workload); err != nil {
		return fmt.Errorf("answering the pause request: %w", err)
	}
	r.emitEvent(workload, r.dryRun(workload), eventType, reason, actionPause, "pause requested, but %s", why)
	return nil
}

// reconcilePauseRequest acts on a pause asked for with the
// napwake.dev/pause-requested annotation, as kubectl napwake pause sets
// it. The request runs the idle clock out now, and the workload is paused as
// an idle one is, to wake on a request, on activity, or by autoResume. The
// rules an idle pause keeps still apply: an active-until hold, awake
// dependents and dry-run all stop it. The forecast expecting demand within
// the hour declines it too, saying when, unless the request overrides the
// forecast: the person asking decides, knowing it would likely be woken
// straight back. The hour Napwake waits after a GitOps tool undid its last
// pause doesn't stop it, because someone asked.
//
// The callers before it answer a request for a workload Napwake isn't
// managing, and one scaled to zero already. One that arrives while the
// workload is waking is answered once it's Running, and one that arrives
// while it's pausing once it's Paused.
func (r *Reconciler) reconcilePauseRequest(ctx context.Context, workload *v1alpha1.ManagedWorkload,
	target client.Object) (*ctrl.Result, error) {
	if _, ok := pendingPauseRequest(workload); !ok {
		return nil, nil
	}
	phase := workload.Status.Phase
	switch phase {
	case v1alpha1.PhasePaused:
		r.answerPauseRequest(workload, metav1.ConditionTrue, reasonAlreadyPaused,
			"already paused at "+answeredAt(r.now()))
		return nil, nil
	case v1alpha1.PhaseRunning, v1alpha1.PhaseIdle:
	default:
		return nil, nil
	}

	if until, by := r.heldAwakeUntil(ctx, workload, target); !until.IsZero() {
		return nil, r.refusePauseRequest(ctx, workload, "Warning", reasonActiveUntil, fmt.Sprintf(
			"the %s annotation on %s keeps it awake until %s; remove it to pause now",
			v1alpha1.AnnotationActiveUntil, by, until.UTC().Format(time.RFC3339)))
	}
	holds, err := r.heldByDependencies(ctx, workload)
	if err != nil {
		return nil, err
	}
	if holds.held() {
		eventType := "Normal"
		if holds.cycle {
			eventType = "Warning"
		}
		return nil, r.refusePauseRequest(ctx, workload, eventType, holds.condition(), holds.message())
	}

	if r.dryRun(workload) {
		return r.pauseRequestedInDryRun(ctx, workload)
	}

	if workload.Annotations[v1alpha1.AnnotationPauseOverridesForecast] != v1alpha1.True {
		if vetoed, predicted, hour := r.forecastVeto(workload, r.forecastEngine(workload)); vetoed {
			return nil, r.refusePauseRequest(ctx, workload, "Normal", reasonForecastExpectsDemand, fmt.Sprintf(
				"the forecast expects demand at %.0f%% of requests in the hour from %s; ask again overriding the forecast to pause anyway",
				predicted, hour.UTC().Format("15:04 UTC")))
		}
	}

	r.clearIdleVeto(workload)
	r.answerPauseRequest(workload, metav1.ConditionTrue, ReasonPaused, "paused as requested at "+answeredAt(r.now()))
	return r.handlePause(ctx, workload, target, &phaseEvent{reason: ReasonPauseRequested,
		message: "pause requested, pausing"})
}

// heldAwakeUntil is when the napwake.dev/active-until hold on the
// ManagedWorkload or its target ends, and which of them sets it, or zero
// when neither holds it awake now.
func (r *Reconciler) heldAwakeUntil(ctx context.Context, workload *v1alpha1.ManagedWorkload,
	target client.Object) (time.Time, string) {
	now := r.now()
	var until time.Time
	var by string
	for _, obj := range []client.Object{workload, target} {
		if obj == nil {
			continue
		}
		if _, activeUntil := r.activityAnnotations(ctx, obj, now); activeUntil.After(now) && activeUntil.After(until) {
			until = activeUntil
			by = "the ManagedWorkload"
			if obj == target {
				by = fmt.Sprintf("%s %s", workload.Spec.Target.Kind, workload.Spec.Target.Name)
			}
		}
	}
	return until, by
}

// pauseRequestedInDryRun counts the pause dry-run holds back as a would-be
// pause, as the idle clock running out does: the workload is Idle, and
// stays up, until activity after the request ends it. One already Idle is
// in a would-be pause already.
func (r *Reconciler) pauseRequestedInDryRun(ctx context.Context, workload *v1alpha1.ManagedWorkload) (*ctrl.Result, error) {
	reason, because := r.whyMeasured()
	if workload.Status.Phase == v1alpha1.PhaseIdle {
		r.answerPauseRequest(workload, metav1.ConditionFalse, reason, fmt.Sprintf(
			"not paused at %s: %s, and a would-be pause was already under way", answeredAt(r.now()), because))
		return nil, nil
	}
	r.beginWouldBePause(ctx, workload)
	workload.Status.DryRun.QueryActiveSincePause = r.queryActiveAtPause(ctx, workload)
	r.answerPauseRequest(workload, metav1.ConditionFalse, reason, fmt.Sprintf(
		"not paused at %s: %s, so Napwake counted a would-be pause and left the workload running",
		answeredAt(r.now()), because))
	if err := r.transition(ctx, workload, v1alpha1.PhaseIdle, ReasonPauseRequested); err != nil {
		return nil, err
	}
	opmetrics.DryrunActions.WithLabelValues("requested_pause").Inc()
	r.emitEvent(workload, true, "Normal", ReasonPauseRequested, actionPause, "pause requested; would pause")
	return &ctrl.Result{RequeueAfter: activityCheckInterval}, nil
}
