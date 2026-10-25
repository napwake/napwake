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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/forecast"
)

// A workload's forecast goes on learning once it has taken up its history,
// one more hour for each hour it's seen whole, by a clock that reads like
// the real one, in a zone ahead of UTC. Taken from time.Now(), each look's
// hour start carried its own monotonic reading, no two looks were in the
// same hour, and on GKE no workload learned a live hour after its history.
func TestForecast_LearnsEveryLiveHourAfterItsHistory(t *testing.T) {
	lagos, err := time.LoadLocation("Africa/Lagos")
	require.NoError(t, err)
	created := time.Date(2026, 10, 21, 22, 43, 0, 0, time.UTC)
	tests := []struct {
		name      string
		policy    v1alpha1.IdlePolicySpec
		cpuMillis float64
		// requestEvery is how often a request reaches the workload while
		// it's paused, through the doorman; never when zero.
		requestEvery time.Duration
	}{
		{name: "awake", policy: v1alpha1.IdlePolicySpec{}, cpuMillis: 40},
		{name: "paused when idle, and woken by a request every few minutes",
			policy:    v1alpha1.IdlePolicySpec{IdleAfter: &metav1.Duration{Duration: 5 * time.Minute}},
			cpuMillis: 2, requestEvery: 20 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, wakes := liveDataPoints(t, created, lagos, tt.policy, tt.cpuMillis, tt.requestEvery, 6*time.Hour)
			if tt.requestEvery > 0 {
				assert.GreaterOrEqual(t, wakes, 15, "paused and woken through the doorman again and again")
			} else {
				assert.Zero(t, wakes, "awake throughout")
			}

			want := map[string]int{}
			for h := 1; h <= 6; h++ {
				hour := forecast.HourStart(created, lagos).Add(time.Duration(h) * time.Hour)
				// Twenty hours of history; the hour it was created in, seen
				// only in part, isn't learned.
				want[hour.In(lagos).Format("15:04 MST")] = 20 + h - 1
			}
			assert.Equal(t, want, got, "the data points saved by the first look in each hour")
		})
	}
}

// liveDataPoints runs a workload created at created, behind the doorman,
// with twenty hours of quiet CPU history in Prometheus, for hours, in the
// forecast time zone loc. Its history is learned and taken up as the
// operator does it, and it's reconciled at each requeue it asks for, and
// when the doorman stamps a request, every requestEvery while it's paused.
// It returns the forecast's data points, as saved in the workload's status,
// at the first look in each hour, keyed by the hour in loc, and how many
// times a request woke the workload.
func liveDataPoints(t *testing.T, created time.Time, loc *time.Location, policy v1alpha1.IdlePolicySpec,
	cpuMillis float64, requestEvery, hours time.Duration) (dataPoints map[string]int, wakes int) {
	t.Helper()
	ctx := context.Background()
	workload := forecastWorkload()
	workload.Spec.IdlePolicy = &policy
	workload.Status.Activity = nil
	workload.Status.Prediction = nil
	meta.SetStatusCondition(&workload.Status.Conditions, metav1.Condition{
		Type: conditionWakeOnRequest, Status: metav1.ConditionTrue, Reason: "DoormanRouted"})
	c := fakeClientBuilder(testScheme(t)).WithStatusSubresource(&v1alpha1.ManagedWorkload{}).
		WithObjects(workload, historyTarget()).Build()
	key := client.ObjectKeyFromObject(workload)

	fr := newForecastReconciler(t, workload, c)
	fr.now = created
	fr.Timezone = loc
	recorder := events.NewFakeRecorder(1000)
	fr.Recorder = recorder
	metrics := &stubMetrics{cpuPerReplica: 100, cpuMillis: cpuMillis, replicas: 1}
	fr.metrics = metrics
	fr.pauser = &clockedPauser{stubPauser: stubPauser{pauseDone: true, resumeDone: true}, now: fr.Reconciler.now}
	peaks := map[int64]float64{}
	for at := forecast.HourStart(created, loc).Add(-20 * time.Hour); at.Before(forecast.HourStart(created, loc)); at = at.Add(time.Hour) {
		peaks[at.Unix()] = 0.001
	}
	fr.history = &stubHistory{peaks: peaks}
	learner := &historyLearner{client: c, history: fr.history, metrics: metrics, engines: fr.engines,
		timezone: loc, clock: fr.Reconciler.now}
	learnHistory(t, learner, workload)
	target := clockTarget("app:v1", nil)

	saved := map[string]int{}
	firstWholeHour := forecast.HourStart(created, loc).Add(time.Hour)
	nextRequest := created.Add(requestEvery)
	for end := created.Add(hours); fr.now.Before(end); {
		require.NoError(t, c.Get(ctx, key, workload))
		if requestEvery > 0 && !fr.now.Before(nextRequest) {
			if workload.Status.Phase == v1alpha1.PhasePaused {
				workload.Annotations = map[string]string{v1alpha1.AnnotationLastRequest: fr.now.Format(time.RFC3339)}
				require.NoError(t, c.Update(ctx, workload))
			}
			nextRequest = nextRequest.Add(requestEvery)
		}

		observed := workload.Status.DeepCopy()
		result, err := fr.reconcileAutomation(ctx, workload, target)
		require.NoError(t, err, "at %s", fr.now)
		require.NoError(t, fr.persistStatus(ctx, workload, observed))

		hour := forecast.HourStart(fr.now, loc).In(loc).Format("15:04 MST")
		if _, seen := saved[hour]; !seen && !fr.now.Before(firstWholeHour) {
			saved[hour] = savedDataPoints(t, c, key)
		}

		require.NotNil(t, result, "at %s", fr.now)
		next := fr.now.Add(result.RequeueAfter)
		if workload.Status.Phase == v1alpha1.PhasePaused && requestEvery > 0 && nextRequest.Before(next) {
			next = nextRequest
		}
		fr.now = next
	}
	return saved, countEvents(recorder, ReasonWokeByActivity)
}

// savedDataPoints is how many hours the forecast saved in the workload's
// status has learned.
func savedDataPoints(t *testing.T, c client.Client, key client.ObjectKey) int {
	t.Helper()
	var stored v1alpha1.ManagedWorkload
	require.NoError(t, c.Get(context.Background(), key, &stored))
	require.NotNil(t, stored.Status.Prediction)
	engine, err := forecast.ImportEngine(stored.Status.Prediction.State, forecast.Settings{})
	require.NoError(t, err)
	return engine.GetDataPoints()
}
