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
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
	"github.com/okedeji/hybernate/internal/forecast"
	"github.com/okedeji/hybernate/internal/signal"
)

// queuePrometheus is a Prometheus whose answer to a queue-length activity
// query the test sets: the messages waiting, or a failure.
type queuePrometheus struct {
	waiting atomic.Int64
	down    atomic.Bool
}

func newQueuePrometheus(t *testing.T) (*queuePrometheus, *signal.Prometheus) {
	t.Helper()
	q := &queuePrometheus{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if q.down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(promValue(strconv.FormatInt(q.waiting.Load(), 10))))
	}))
	t.Cleanup(srv.Close)
	return q, signal.NewPrometheus(srv.URL)
}

// queueWorker is a queue worker with no Service, which a query of its
// queue's length keeps awake, last active two hours before now, with an
// hour's idle clock.
func queueWorker(phase v1alpha1.WorkloadPhase, now time.Time) *v1alpha1.ManagedWorkload {
	w := lifecycleWorkload("api", phase)
	w.Spec.IdlePolicy = &v1alpha1.IdlePolicySpec{
		IdleAfter: &metav1.Duration{Duration: time.Hour},
		Activity: &v1alpha1.ActivitySpec{Prometheus: []v1alpha1.PrometheusActivity{
			{PromQL: `sum(rabbitmq_queue_messages_ready{queue="orders"})`}}},
	}
	w.Status.LastTransitionTime = ptr.To(metav1.NewTime(now.Add(-3 * time.Hour)))
	w.Status.Activity = &v1alpha1.ActivityStatus{
		LastActivityTime:   metav1.NewTime(now.Add(-2 * time.Hour)),
		LastActivitySource: v1alpha1.ActivitySourcePrometheus,
		LastEvaluatedTime:  ptr.To(metav1.NewTime(now.Add(-30 * time.Second))),
	}
	return w
}

// A queue worker paused when its queue went quiet is woken by its query as
// soon as messages wait again, looked at every minute as when it's awake,
// and the wake is recorded as the query's.
func TestPausedQueries_WakeAWorkerPausedWhenIdle(t *testing.T) {
	queue, prom := newQueuePrometheus(t)
	r := lifecycleReconciler(t, queueWorker(v1alpha1.PhaseRunning, time.Now()), 3, interceptor.Funcs{})
	// The real clock, which the lifecycle stamps pausedAt with.
	r.clock = time.Now
	r.prometheus = prom

	reconcileUntilSettled(t, r)
	paused := getWorkload(t, r, "api")
	require.Equal(t, v1alpha1.PhasePaused, paused.Status.Phase, "idle for longer than idleAfter")
	assert.False(t, paused.Status.Pause.QueryActiveSincePause, "the query was quiet as the pause began")
	result, err := r.Reconcile(context.Background(), reconcileFor("api"))
	require.NoError(t, err)
	assert.Equal(t, activityCheckInterval, result.RequeueAfter, "looked at every minute, as when awake")
	assert.Equal(t, v1alpha1.PhasePaused, getWorkload(t, r, "api").Status.Phase, "nothing waits on its queue")
	drainEvents(t, r)

	queue.waiting.Store(12)
	_, err = r.Reconcile(context.Background(), reconcileFor("api"))
	require.NoError(t, err)
	// Emptied by the worker, so what the resume recorded stands.
	queue.waiting.Store(0)
	reconcileUntilSettled(t, r)

	woken := getWorkload(t, r, "api")
	assert.Equal(t, v1alpha1.PhaseRunning, woken.Status.Phase)
	assert.Equal(t, int32(3), targetReplicas(t, r))
	assert.Equal(t, v1alpha1.ActivitySourcePrometheus, woken.Status.Activity.LastActivitySource)
	assert.Contains(t, drainEvents(t, r), "Normal WokeByActivity api: "+queryWakeMessage)
}

// A pause requested while the query shows activity isn't undone by it: the
// query wakes the workload only once a check has found it quiet, and it
// shows activity again.
func TestPausedQueries_DontUndoARequestedPause(t *testing.T) {
	queue, prom := newQueuePrometheus(t)
	queue.waiting.Store(40)
	r := lifecycleReconciler(t, withPauseRequested(queueWorker(v1alpha1.PhaseRunning, time.Now())), 3,
		interceptor.Funcs{})
	r.clock = time.Now
	r.prometheus = prom

	reconcileUntilSettled(t, r)
	paused := getWorkload(t, r, "api")
	require.Equal(t, v1alpha1.PhasePaused, paused.Status.Phase, "paused as requested, with 40 messages waiting")
	assert.True(t, paused.Status.Pause.QueryActiveSincePause)

	queue.waiting.Store(41)
	reconcileUntilSettled(t, r)
	assert.Equal(t, v1alpha1.PhasePaused, getWorkload(t, r, "api").Status.Phase,
		"still showing activity, as when the pause began")

	queue.waiting.Store(0)
	reconcileUntilSettled(t, r)
	quiet := getWorkload(t, r, "api")
	assert.Equal(t, v1alpha1.PhasePaused, quiet.Status.Phase)
	assert.False(t, quiet.Status.Pause.QueryActiveSincePause, "quiet since the pause")

	queue.waiting.Store(3)
	_, err := r.Reconcile(context.Background(), reconcileFor("api"))
	require.NoError(t, err)
	queue.waiting.Store(0)
	reconcileUntilSettled(t, r)
	woken := getWorkload(t, r, "api")
	assert.Equal(t, v1alpha1.PhaseRunning, woken.Status.Phase)
	assert.Equal(t, v1alpha1.ActivitySourcePrometheus, woken.Status.Activity.LastActivitySource)
}

// A query that can't be evaluated never wakes a paused workload. It's
// reported as it is while the workload is awake, worded for one that's
// paused, and warned about once.
func TestPausedQueries_AFailingQueryNeverWakes(t *testing.T) {
	queue, prom := newQueuePrometheus(t)
	r := lifecycleReconciler(t, queueWorker(v1alpha1.PhaseRunning, fixedTime), 3, interceptor.Funcs{})
	r.prometheus = prom
	reconcileUntilSettled(t, r)
	require.Equal(t, v1alpha1.PhasePaused, getWorkload(t, r, "api").Status.Phase)
	drainEvents(t, r)

	queue.down.Store(true)
	reconcileUntilSettled(t, r)

	got := getWorkload(t, r, "api")
	assert.Equal(t, v1alpha1.PhasePaused, got.Status.Phase)
	c := meta.FindStatusCondition(got.Status.Conditions, conditionPrometheusAvailable)
	require.NotNil(t, c)
	assert.Equal(t, metav1.ConditionFalse, c.Status)
	assert.Equal(t, "QueryFailed", c.Reason)
	assert.Contains(t, c.Message, "so the queries can't wake the paused workload, and the forecast doesn't "+
		"learn the time it's paused, until Prometheus answers")
	assert.Equal(t, 1, strings.Count(drainEvents(t, r), "Warning QueryFailed"))
}

// A query wake is a wake like any other: the workload's dependencies are
// woken with it.
func TestPausedQueries_WakeDependencies(t *testing.T) {
	queue, prom := newQueuePrometheus(t)
	queue.waiting.Store(5)
	pausedAt := metav1.NewTime(fixedTime.Add(-time.Hour))
	worker := depWorkload("default", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhasePaused, postgresRef())
	worker.Spec.IdlePolicy.Activity = queueWorker(v1alpha1.PhasePaused, fixedTime).Spec.IdlePolicy.Activity
	worker.Status.Pause = &v1alpha1.PauseStatus{PreviousReplicas: 1, PausedAt: &pausedAt}
	postgres := depWorkload("default", "postgres", v1alpha1.TargetKindStatefulSet, v1alpha1.PhasePaused)
	r := depReconciler(t, &stubPauser{resumeDone: true}, worker, postgres, postgresTarget(1, 1))
	r.prometheus = prom

	_, err := r.reconcileAutomation(context.Background(), worker, nil)
	require.NoError(t, err)

	assert.Equal(t, v1alpha1.PhaseRunning, worker.Status.Phase)
	assert.Equal(t, fixedTime.UTC().Format(time.RFC3339),
		fetch(t, r, "postgres").Annotations[v1alpha1.AnnotationLastActivity], "its dependency is woken with it")
}

// The hours a paused worker's query watches are learned, quiet while it
// finds nothing, so a worker with no Service, paused every night, learns
// whole days. Paused hours a query couldn't be read in aren't learned.
func TestPausedQueries_TheForecastLearnsTheHoursTheyWatch(t *testing.T) {
	tests := []struct {
		name     string
		down     bool
		wantHour bool
	}{
		{name: "watched by its query", wantHour: true},
		{name: "with Prometheus down", down: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queue, prom := newQueuePrometheus(t)
			queue.down.Store(tt.down)
			worker := queueWorker(v1alpha1.PhasePaused, fixedTime)
			worker.Status.Pause = &v1alpha1.PauseStatus{PreviousReplicas: 1,
				PausedAt: ptr.To(metav1.NewTime(fixedTime.Add(-time.Hour)))}
			engine := &stubForecaster{phase: forecast.Observing}
			r := newAutomationReconciler(t, worker, engine, automationOpts{})
			r.prometheus = prom

			for now := fixedTime; !now.After(fixedTime.Add(time.Hour)); now = now.Add(time.Minute) {
				r.clock = func() time.Time { return now }
				_, err := r.reconcileAutomation(context.Background(), worker, nil)
				require.NoError(t, err)
				require.Equal(t, v1alpha1.PhasePaused, worker.Status.Phase)
			}

			if !tt.wantHour {
				assert.Zero(t, engine.observeCalls)
				return
			}
			assert.Equal(t, []float64{0}, engine.observed, "a quiet hour")
			assert.Equal(t, []time.Time{fixedTime}, engine.observedHours)
		})
	}
}

// In dry-run, a pause requested while a query shows activity is measured
// for as long as the real pause would last: the query ends it only once a
// check has found it quiet and it shows activity again, as it would wake the
// workload, and not at the next check.
func TestPausedQueries_DontEndARequestedWouldBePause(t *testing.T) {
	queue, prom := newQueuePrometheus(t)
	queue.waiting.Store(40)
	workload := withPauseRequested(queueWorker(v1alpha1.PhaseRunning, time.Now()))
	workload.Spec.DryRun = true
	r := lifecycleReconciler(t, workload, 3, interceptor.Funcs{})
	r.clock = time.Now
	r.prometheus = prom

	reconcileUntilSettled(t, r)
	measuring := getWorkload(t, r, "api")
	require.Equal(t, v1alpha1.PhaseIdle, measuring.Status.Phase, "a would-be pause, with 40 messages waiting")
	require.NotNil(t, measuring.Status.DryRun)
	assert.True(t, measuring.Status.DryRun.QueryActiveSincePause)

	queue.waiting.Store(41)
	reconcileUntilSettled(t, r)
	assert.Equal(t, v1alpha1.PhaseIdle, getWorkload(t, r, "api").Status.Phase,
		"still showing activity, as when the pause was asked for")

	queue.waiting.Store(0)
	reconcileUntilSettled(t, r)
	quiet := getWorkload(t, r, "api")
	assert.Equal(t, v1alpha1.PhaseIdle, quiet.Status.Phase)
	assert.False(t, quiet.Status.DryRun.QueryActiveSincePause, "quiet since the request")

	queue.waiting.Store(3)
	reconcileUntilSettled(t, r)
	ended := getWorkload(t, r, "api")
	assert.Equal(t, v1alpha1.PhaseRunning, ended.Status.Phase, "the query would have woken it")
	assert.Equal(t, int32(1), ended.Status.DryRun.Pauses)
	assert.Contains(t, drainEvents(t, r), "ActivityResumed [dry-run] api: activity resumed (prometheus)")
}
