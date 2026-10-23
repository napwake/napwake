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

// With wake: ask, only people wake a paused workload, so an hour it spends
// paused is one nobody needed it in, routed through the doorman or not: if
// someone had, they'd have clicked or asked. An hour a click, a wake request
// or an activity annotation woke it in is busy, as an hour a request wakes
// it in is with wake: request, which is unchanged: there, without the
// doorman, the hour can't be seen.
func TestPausedHour_PeoplesWakesInAsk(t *testing.T) {
	tests := []struct {
		name    string
		ask     bool
		routed  bool
		wakeBy  string
		wakeVal string
		want    []float64
	}{
		{name: "ask: a quiet night", ask: true, want: []float64{0}},
		{name: "ask, routed: a quiet night", ask: true, routed: true, want: []float64{0}},
		{name: "ask: a morning wake request", ask: true, wakeBy: v1alpha1.AnnotationWakeRequested,
			wakeVal: wakeToken, want: []float64{20}},
		{name: "ask, routed: a click", ask: true, routed: true, wakeBy: v1alpha1.AnnotationLastRequest,
			want: []float64{20}},
		{name: "ask: an activity annotation", ask: true, wakeBy: v1alpha1.AnnotationLastActivity,
			want: []float64{20}},
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

// People's wakes don't depend on Prometheus, so with wake: ask a paused
// worker's query failing doesn't blind its hours: they're learned from
// people waking it, and the query's condition says so.
func TestPausedHour_AskIsntBlindedByAFailingQuery(t *testing.T) {
	queue, prom := newQueuePrometheus(t)
	queue.down.Store(true)
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
		require.Equal(t, v1alpha1.PhasePaused, worker.Status.Phase, "a query that fails never wakes it")
	}

	assert.Equal(t, []float64{0}, engine.observed, "a quiet hour")
	assert.Equal(t, []time.Time{fixedTime}, engine.observedHours)
	c := meta.FindStatusCondition(worker.Status.Conditions, conditionPrometheusAvailable)
	require.NotNil(t, c)
	assert.Contains(t, c.Message, "so the queries can't wake the paused workload until Prometheus answers; people "+
		"still can, and the forecast learns the time it's paused from whether they do")
}

// With wake: ask, the forecast's message says it learns from people waking
// the workload while it's paused, never that it can't learn.
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
	tests := []struct {
		name     string
		workload []func(*v1alpha1.ManagedWorkload)
		want     string
	}{
		{name: "routed through the doorman", workload: []func(*v1alpha1.ManagedWorkload){routedThroughTheDoorman},
			want: "people waking it (a click on its asleep page, a wake request or an activity annotation)"},
		{name: "not routed",
			want: "people waking it (a wake request or an activity annotation)"},
		{name: "with its queries", workload: []func(*v1alpha1.ManagedWorkload){routedThroughTheDoorman, queries},
			want: "people waking it (a click on its asleep page, a wake request or an activity annotation) and " +
				"its Prometheus activity queries"},
		{name: "its queries failing", workload: []func(*v1alpha1.ManagedWorkload){failing},
			want: "people waking it (a wake request or an activity annotation), its Prometheus activity queries " +
				"failing"},
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

			assert.Equal(t, v1alpha1.ForecastObserving, reason)
			assert.Equal(t, prefix+tt.want, message)
		})
	}
}
