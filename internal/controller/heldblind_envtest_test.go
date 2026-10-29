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
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/forecast"
	"github.com/napwake/napwake/internal/signal"
)

// change changes the workload's spec or annotations, as its owner would.
func (tc *turnedBackCase) change(t *testing.T, edit func(*v1alpha1.ManagedWorkload)) {
	t.Helper()
	var w v1alpha1.ManagedWorkload
	require.NoError(t, tc.admin.Get(tc.ctx, tc.key, &w))
	edit(&w)
	require.NoError(t, tc.admin.Update(tc.ctx, &w))
}

// pauseAgain watches the workload once a minute until it's pausing again,
// then on until the doorman fronts it.
func (tc *turnedBackCase) pauseAgain(t *testing.T) {
	t.Helper()
	for range 30 {
		tc.now = tc.now.Add(time.Minute)
		if tc.reconcile(t).Status.Phase == v1alpha1.PhasePausing {
			tc.now = tc.now.Add(doormanListenWait)
			tc.reconcile(t)
			return
		}
	}
	t.Fatalf("not pausing again by %s", tc.now)
}

// The soak's orders/api, P71, once the forecast acts: its CPU, its only
// activity source, is under its threshold, so its pauses are turned back
// by requests through the doorman, and the forecast learns those hours are
// busy and holds its pauses off there. Nothing sees its requests while
// it's held awake, so for 7 days after a wrong pause a request undid, the
// hour it's held in isn't learned: from its CPU, 7%, far quieter than
// forecast, the forecast would doubt itself and stop holding. After 7
// days, or once a wrong pause has another cause, the hour is its CPU
// again. So is it for a workload whose requests something else sees, a
// Prometheus query, or that asks a person first, and in dry-run.
func TestHeldHour_IsUnseenFor7DaysAfterARequestUndidAPause(t *testing.T) {
	env := startTurnedBackEnv(t)
	start := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	quiet := prometheusServer(t, http.StatusOK, promValue("0"))
	tests := []struct {
		name string
		// then is what changes after a request turns its pause back, at
		// 10:00 on a Monday.
		then func(tc *turnedBackCase, t *testing.T)
		// days is how many days later the forecast holds it awake, from
		// a minute before 11:00.
		days   int
		unseen bool
	}{
		{name: "6 days after", days: 6, unseen: true},
		{name: "7 days after", days: 7},
		{name: "6 days after, a wrong pause since undone by an activity annotation", days: 6,
			then: func(tc *turnedBackCase, t *testing.T) {
				tc.pauseAgain(t)
				tc.change(t, func(w *v1alpha1.ManagedWorkload) {
					metav1.SetMetaDataAnnotation(&w.ObjectMeta, v1alpha1.AnnotationLastActivity,
						tc.now.UTC().Format(time.RFC3339))
				})
				tc.now = tc.now.Add(time.Second)
				w := tc.reconcile(t)
				require.Equal(t, v1alpha1.PhaseRunning, w.Status.Phase, "the annotation turned the pause back")
				require.Equal(t, v1alpha1.WokenByAnnotation, w.Status.IdleWait.LastWrongPause.By)
			}},
		{name: "6 days after, with a Prometheus activity query since", days: 6,
			then: func(tc *turnedBackCase, t *testing.T) {
				tc.r.prometheus = signal.NewPrometheus(quiet)
				tc.change(t, func(w *v1alpha1.ManagedWorkload) {
					w.Spec.IdlePolicy.Activity = &v1alpha1.ActivitySpec{Prometheus: []v1alpha1.PrometheusActivity{
						{PromQL: `sum(rate(nginx_ingress_controller_requests{service="api"}[5m]))`}}}
				})
			}},
		{name: "6 days after, asking a person first since", days: 6,
			then: func(tc *turnedBackCase, t *testing.T) {
				tc.change(t, func(w *v1alpha1.ManagedWorkload) {
					w.Spec.Wake = &v1alpha1.WakeSpec{Mode: v1alpha1.WakeOnAsk}
				})
			}},
		{name: "6 days after, in dry-run since", days: 6, then: (*turnedBackCase).setDryRun},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc := env.create(t, fmt.Sprintf("held-%d", i), func(w *v1alpha1.ManagedWorkload) {
				w.Spec.IdlePolicy.IdleAfter = &metav1.Duration{Duration: 5 * time.Minute}
			})
			tc.pauseUntilFronted(t)
			tc.stamp(t)
			tc.now = tc.now.Add(time.Second)
			w := tc.reconcile(t)
			require.Equal(t, v1alpha1.PhaseRunning, w.Status.Phase, "the request turned the pause back")
			require.Equal(t, v1alpha1.WokenByRequest, w.Status.IdleWait.LastWrongPause.By)
			if tt.then != nil {
				tt.then(tc, t)
			}

			tc.forecast.phase, tc.forecast.predictValue = forecast.DailyActive, 20
			held := start.AddDate(0, 0, tt.days).Add(time.Hour)
			tc.now = monotonic(held.Add(-time.Minute))
			tc.reconcile(t)
			tc.watchUntil(t, held.Add(time.Hour+30*time.Second))

			w = tc.reconcile(t)
			require.Equal(t, v1alpha1.PhaseRunning, w.Status.Phase)
			require.True(t, meta.IsStatusConditionTrue(w.Status.Conditions, conditionIdleVetoed),
				"the forecast holds its pause off")
			require.False(t, meta.IsStatusConditionTrue(w.Status.Conditions, conditionWakeOnRequest),
				"awake, it isn't WakeOnRequest=True: its last pause's routing is all there is")
			fed := slices.IndexFunc(tc.forecast.observedHours, held.Equal)
			if tt.unseen {
				assert.Negative(t, fed, "the hour from %s it's held in is unseen: fed %v", held.Format("Mon 15:04"),
					tc.forecast.observed)
				return
			}
			require.GreaterOrEqual(t, fed, 0, "the hour from %s it's held in is fed: fed hours %v",
				held.Format("Mon 15:04"), tc.forecast.observedHours)
			assert.InDelta(t, 7, tc.forecast.observed[fed], 0.5, "as its CPU")
		})
	}
}

// pauseToZero has the operator go on with a pause under way, as often as the
// doorman's takeover waits, until the workload is scaled to zero.
func (tc *turnedBackCase) pauseToZero(t *testing.T) {
	t.Helper()
	for range 10 {
		tc.now = tc.now.Add(doormanTakeover)
		if tc.reconcile(t).Status.Phase == v1alpha1.PhasePaused {
			return
		}
	}
	t.Fatalf("not paused by %s", tc.now)
}

// The soak's orders/api, P71, once a request has undone a pause, so the
// hours the forecast holds it in are unseen: one look while held makes the
// whole hour unseen. But a request that wakes it from zero early in an
// hour, as one an hour earlier than its usual load does, is a look that
// saw the hour busy, and the forecast then holds it awake in that hour
// expecting it busy. The hour is learned as busy, as the request showed:
// unseen, autoResume would never learn to wake it ahead of that hour.
func TestHeldHour_SeenBusyBeforeTheHoldIsLearned(t *testing.T) {
	env := startTurnedBackEnv(t)
	start := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	tc := env.create(t, "held-busy", func(w *v1alpha1.ManagedWorkload) {
		w.Spec.IdlePolicy.IdleAfter = &metav1.Duration{Duration: 5 * time.Minute}
	})
	tc.pauseUntilFronted(t)
	tc.stamp(t)
	tc.now = tc.now.Add(time.Second)
	w := tc.reconcile(t)
	require.Equal(t, v1alpha1.PhaseRunning, w.Status.Phase, "the request turned the pause back")
	require.Equal(t, v1alpha1.WokenByRequest, w.Status.IdleWait.LastWrongPause.By)
	tc.pauseAgain(t)
	tc.pauseToZero(t)

	woken := start.Add(time.Hour)
	tc.watchUntil(t, woken.Add(30*time.Second))
	tc.now = monotonic(woken.Add(100 * time.Second))
	tc.stamp(t)
	tc.now = tc.now.Add(time.Second)
	require.NotEqual(t, v1alpha1.PhasePaused, tc.reconcile(t).Status.Phase, "a request woke it from zero")

	tc.forecast.phase, tc.forecast.predictValue = forecast.DailyActive, 20
	tc.watchUntil(t, woken.Add(30*time.Minute+30*time.Second))
	w = tc.reconcile(t)
	require.Equal(t, v1alpha1.PhaseRunning, w.Status.Phase)
	require.True(t, meta.IsStatusConditionTrue(w.Status.Conditions, conditionIdleVetoed),
		"the forecast holds its pause off in the hour the request woke it in")
	require.Equal(t, v1alpha1.WokenByRequest, w.Status.IdleWait.LastWrongPause.By,
		"a request undid its last wrong pause, under 7 days ago")

	tc.watchUntil(t, woken.Add(time.Hour+30*time.Second))
	assert.GreaterOrEqual(t, tc.fed(t, woken), forecast.RequestDemand(10),
		"the hour from %s, which a request woke it in before it was held, is busy", woken.Format("15:04"))
}
