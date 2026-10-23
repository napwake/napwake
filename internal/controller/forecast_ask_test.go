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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
	"github.com/okedeji/hybernate/internal/forecast"
)

func asks(w *v1alpha1.ManagedWorkload) {
	w.Spec.Wake = &v1alpha1.WakeSpec{Mode: v1alpha1.WakeOnAsk}
}

func routedThroughTheDoorman(w *v1alpha1.ManagedWorkload) {
	meta.SetStatusCondition(&w.Status.Conditions, metav1.Condition{Type: conditionWakeOnRequest,
		Status: metav1.ConditionTrue, Reason: "DoormanRouted"})
}

// With wake: ask, only people wake a paused workload, so behind the doorman
// an hour it spends paused is one nobody needed it in: if someone had,
// they'd have clicked or asked. An hour a click, a wake request or an
// activity annotation woke it in is busy, as an hour a request wakes it in
// is with wake: request. Without the doorman nobody can click, so a look
// that finds it still paused can't say nobody needed it, and the hour isn't
// learned, as with wake: request, which is unchanged.
func TestPausedHour_PeoplesWakesInAsk(t *testing.T) {
	tests := []struct {
		name    string
		ask     bool
		routed  bool
		wakeBy  string
		wakeVal string
		want    []float64
	}{
		{name: "ask, routed: a quiet night", ask: true, routed: true, want: []float64{0}},
		{name: "ask, routed: a morning wake request", ask: true, routed: true,
			wakeBy: v1alpha1.AnnotationWakeRequested, wakeVal: wakeToken, want: []float64{20}},
		{name: "ask, routed: a click", ask: true, routed: true, wakeBy: v1alpha1.AnnotationLastRequest,
			want: []float64{20}},
		{name: "ask, routed: an activity annotation", ask: true, routed: true,
			wakeBy: v1alpha1.AnnotationLastActivity, want: []float64{20}},
		{name: "ask, not routed: a quiet night, unseen", ask: true},
		{name: "ask, not routed: a wake request, the looks before it unseen", ask: true,
			wakeBy: v1alpha1.AnnotationWakeRequested, wakeVal: wakeToken},
		{name: "request, not routed: unseen, as before"},
		{name: "request, routed: a quiet night", routed: true, want: []float64{0}},
		{name: "request, not routed: a wake request", wakeBy: v1alpha1.AnnotationWakeRequested, wakeVal: wakeToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workload := pausedForecastWorkload()
			workload.Spec.IdlePolicy.AutoResume = false
			if tt.ask {
				asks(workload)
			}
			if tt.routed {
				routedThroughTheDoorman(workload)
			}
			engine := &stubForecaster{phase: forecast.Observing}
			r := newAutomationReconciler(t, workload, engine, automationOpts{
				metrics: &stubMetrics{cpuMillis: 1, cpuPerReplica: 100}, pauser: &stubPauser{resumeDone: true}})

			for now := fixedTime; !now.After(fixedTime.Add(time.Hour)); now = now.Add(time.Minute) {
				r.clock = func() time.Time { return now }
				if tt.wakeBy != "" && now.Equal(fixedTime.Add(10*time.Minute)) {
					value := tt.wakeVal
					if value == "" {
						value = now.Format(time.RFC3339)
					}
					workload.Annotations = map[string]string{tt.wakeBy: value}
				}
				_, err := r.reconcileAutomation(context.Background(), workload, nil)
				require.NoError(t, err)
			}

			assert.Equal(t, tt.want, engine.observed)
		})
	}
}

// With wake: ask, a paused worker's queries failing blinds its hours even
// behind the doorman: the doorman sees only people, not the activity the
// queries measure, so an outage of Prometheus mustn't teach quiet hours.
// The query's condition says so.
func TestPausedHour_AskIsBlindedByAFailingQuery(t *testing.T) {
	queue, prom := newQueuePrometheus(t)
	queue.down.Store(true)
	worker := queueWorker(v1alpha1.PhasePaused, fixedTime)
	worker.Status.Pause = &v1alpha1.PauseStatus{PreviousReplicas: 1,
		PausedAt: ptr.To(metav1.NewTime(fixedTime.Add(-time.Hour)))}
	asks(worker)
	routedThroughTheDoorman(worker)
	engine := &stubForecaster{phase: forecast.Observing}
	r := newAutomationReconciler(t, worker, engine, automationOpts{})
	r.prometheus = prom

	for now := fixedTime; !now.After(fixedTime.Add(time.Hour)); now = now.Add(time.Minute) {
		r.clock = func() time.Time { return now }
		_, err := r.reconcileAutomation(context.Background(), worker, nil)
		require.NoError(t, err)
		require.Equal(t, v1alpha1.PhasePaused, worker.Status.Phase, "a query that fails never wakes it")
	}

	assert.Zero(t, engine.observeCalls, "the hour isn't learned")
	c := meta.FindStatusCondition(worker.Status.Conditions, conditionPrometheusAvailable)
	require.NotNil(t, c)
	assert.Contains(t, c.Message, "so the queries can't wake the paused workload, and the forecast doesn't learn "+
		"the time it's paused, until Prometheus answers; people can still wake it")
}

// A worker its queries watch learns its paused hours from them with wake:
// ask as with wake: request, though no Service routes requests to it: the
// queries show the demand there is, and people wake it as they need.
func TestPausedHour_AskWorkerLearnsFromItsQueries(t *testing.T) {
	_, prom := newQueuePrometheus(t)
	worker := queueWorker(v1alpha1.PhasePaused, fixedTime)
	worker.Status.Pause = &v1alpha1.PauseStatus{PreviousReplicas: 1,
		PausedAt: ptr.To(metav1.NewTime(fixedTime.Add(-time.Hour)))}
	asks(worker)
	engine := &stubForecaster{phase: forecast.Observing}
	r := newAutomationReconciler(t, worker, engine, automationOpts{})
	r.prometheus = prom

	for now := fixedTime; !now.After(fixedTime.Add(time.Hour)); now = now.Add(time.Minute) {
		r.clock = func() time.Time { return now }
		_, err := r.reconcileAutomation(context.Background(), worker, nil)
		require.NoError(t, err)
	}

	assert.Equal(t, []float64{0}, engine.observed, "a quiet hour")
}

// With wake: ask, the forecast's message says it learns from people waking
// the workload while it's paused, behind the doorman, where they can, and
// why it doesn't learn the hours it's paused where they can't, or its
// queries are failing.
func TestForecastOutlook_InAsk(t *testing.T) {
	learning := forecast.Outlook{Waiting: forecast.WaitingForDay, HoursOfDaySeen: 20}
	const prefix = "learning: it has seen 20 of the 24 hours of the day, and can't act until it has seen them all; " +
		"while it's paused, it learns from "
	queries := func(w *v1alpha1.ManagedWorkload) {
		w.Spec.IdlePolicy = &v1alpha1.IdlePolicySpec{Activity: &v1alpha1.ActivitySpec{
			Prometheus: []v1alpha1.PrometheusActivity{{PromQL: `sum(rabbitmq_queue_messages{queue="tasks"})`}}}}
	}
	failing := func(w *v1alpha1.ManagedWorkload) {
		queries(w)
		meta.SetStatusCondition(&w.Status.Conditions, metav1.Condition{Type: conditionPrometheusAvailable,
			Status: metav1.ConditionFalse, Reason: "QueryFailed"})
	}
	noServices := func(w *v1alpha1.ManagedWorkload) {
		meta.SetStatusCondition(&w.Status.Conditions, metav1.Condition{Type: conditionWakeOnRequest,
			Status: metav1.ConditionFalse, Reason: "NoServices"})
	}
	tests := []struct {
		name       string
		workload   []func(*v1alpha1.ManagedWorkload)
		wantReason string
		want       string
	}{
		{name: "routed through the doorman", workload: []func(*v1alpha1.ManagedWorkload){routedThroughTheDoorman},
			wantReason: v1alpha1.ForecastObserving,
			want:       prefix + "people waking it (a click on its asleep page, a wake request or an activity annotation)"},
		{name: "with its queries", workload: []func(*v1alpha1.ManagedWorkload){routedThroughTheDoorman, queries},
			wantReason: v1alpha1.ForecastObserving,
			want: prefix + "people waking it (a click on its asleep page, a wake request or an activity annotation) " +
				"and its Prometheus activity queries"},
		{name: "a worker its queries watch", workload: []func(*v1alpha1.ManagedWorkload){noServices, queries},
			wantReason: v1alpha1.ForecastObserving, want: prefix + "its Prometheus activity queries"},
		{name: "not routed", workload: []func(*v1alpha1.ManagedWorkload){noServices},
			wantReason: v1alpha1.ForecastNotLearning,
			want: "not learning while paused: no Service routes requests to it; nobody can click Wake it on its " +
				"asleep page, so an hour nobody woke it in can't be told from one they couldn't, and it learns only " +
				"the hours it's awake. To learn the hours it's paused too, give it a Service, or, for a workload no " +
				"request reaches, such as a queue worker, a Prometheus activity query that sees its demand: " +
				"https://okedeji.io/hybernate/concepts/wake-on-request/#when-a-workload-isnt-routed"},
		{name: "its queries failing", workload: []func(*v1alpha1.ManagedWorkload){routedThroughTheDoorman, failing},
			wantReason: v1alpha1.ForecastNotLearning,
			want: "not learning while paused: its Prometheus activity queries can't be evaluated; they can't show " +
				"its demand until Prometheus answers, and PrometheusAvailable says why: " +
				"https://okedeji.io/hybernate/guides/prometheus-signals/#when-a-query-cant-be-evaluated"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workload := forecastWorkload()
			workload.Spec.IdlePolicy = nil
			workload.Status.Phase = v1alpha1.PhasePaused
			asks(workload)
			for _, f := range tt.workload {
				f(workload)
			}

			reason, message := forecastOutlook(workload, learning)

			assert.Equal(t, tt.wantReason, reason)
			assert.Equal(t, tt.want, message)
		})
	}
}
