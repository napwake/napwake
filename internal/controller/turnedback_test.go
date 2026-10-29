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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/forecast"
)

// loadHours is the test cluster's load: weekdays from 09:00 to 17:00 UTC.
func loadHours(now time.Time) bool {
	if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		return false
	}
	return now.Hour() >= 9 && now.Hour() < 17
}

// nextLoad is when the load next begins after now.
func nextLoad(now time.Time) time.Time {
	for at := time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, time.UTC); ; at = at.AddDate(0, 0, 1) {
		if at.After(now) && loadHours(at) {
			return at
		}
	}
}

// fedEngine is the real forecast, noting the peak it's fed for each hour,
// by its start in UTC.
type fedEngine struct {
	*forecast.Engine
	fed map[time.Time]float64
}

func (e *fedEngine) Observe(actual float64, hour time.Time) (float64, error) {
	e.fed[hour.UTC()] = actual
	return e.Engine.Observe(actual, hour)
}

// turnedBackRun is what turnedBackWeeks saw: the hours the forecast was fed,
// the hours a request reached the doorman in, and when the forecast held a
// pause off.
type turnedBackRun struct {
	fed       map[time.Time]float64
	requested map[time.Time]bool
	vetoes    []time.Time
}

// turnedBackWeeks runs orders/api behind the doorman for weeks from monday,
// reconciled as the operator would: at the requeue each reconcile asks for,
// and when a request stamped by the doorman wakes it, or calls off its
// pause. Its only activity source is its CPU, 7% of the 1000m it requests
// in its load hours and 0.5% otherwise, under its 10% threshold all day, so
// its idle clock runs out every idleAfter, backed off after each wrong
// pause. In its load hours, every pause is turned back by a request before
// the workload is scaled down, and one paused overnight is woken by the
// first.
func turnedBackWeeks(t *testing.T, monday time.Time, weeks int, idleAfter time.Duration) turnedBackRun {
	t.Helper()
	ctx := context.Background()
	workload := forecastWorkload()
	workload.Spec.IdlePolicy = &v1alpha1.IdlePolicySpec{IdleAfter: &metav1.Duration{Duration: idleAfter}}
	workload.Status.Activity = nil
	meta.SetStatusCondition(&workload.Status.Conditions, metav1.Condition{
		Type: conditionWakeOnRequest, Status: metav1.ConditionTrue, Reason: "DoormanRouted"})
	fr := newForecastReconciler(t, workload, nil)
	fr.now = monday
	engine := &fedEngine{Engine: forecast.NewEngine(forecast.DefaultParams(), forecast.Settings{}),
		fed: map[time.Time]float64{}}
	fr.engines = newEngineRegistry(func() forecaster { return engine })
	fr.pauser = &clockedPauser{stubPauser: stubPauser{pauseDone: true, resumeDone: true}, now: fr.Reconciler.now}
	metrics := &stubMetrics{cpuPerReplica: 1000, replicas: 1}
	fr.metrics = metrics
	recorder := events.NewFakeRecorder(100)
	fr.Recorder = recorder
	target := earlyTarget()
	routeThroughDoorman(fr.Reconciler)
	for _, obj := range []client.Object{doormanEndpoints("10.0.0.7"),
		service(apiService, apiPods, corev1.ServicePort{Name: "http", Port: 80})} {
		require.NoError(t, fr.Create(ctx, obj))
	}

	run := turnedBackRun{fed: engine.fed, requested: map[time.Time]bool{}}
	for end := monday.AddDate(0, 0, 7*weeks); fr.now.Before(end); {
		require.NoError(t, fr.Get(ctx, client.ObjectKeyFromObject(workload), workload))
		load := loadHours(fr.now)
		metrics.cpuMillis = 5
		if load {
			metrics.cpuMillis = 70
		}
		if phase := workload.Status.Phase; load && (phase == v1alpha1.PhasePausing || phase == v1alpha1.PhasePaused) {
			workload.Annotations = map[string]string{v1alpha1.AnnotationLastRequest: fr.now.Format(time.RFC3339)}
			require.NoError(t, fr.Update(ctx, workload))
			run.requested[forecast.HourStart(fr.now, nil).UTC()] = true
		}

		observed := workload.Status.DeepCopy()
		result, err := automate(ctx, fr.Reconciler, workload, target)
		require.NoError(t, err, "at %s", fr.now)
		require.NoError(t, fr.persistStatus(ctx, workload, observed))
		for len(recorder.Events) > 0 {
			if e := <-recorder.Events; strings.HasPrefix(e, "Normal "+ReasonIdleVetoed+" ") {
				run.vetoes = append(run.vetoes, fr.now)
			}
		}

		require.NotNil(t, result, "at %s", fr.now)
		next := fr.now.Add(result.RequeueAfter)
		if start := nextLoad(fr.now); workload.Status.Phase == v1alpha1.PhasePaused && start.Before(next) {
			next = start
		}
		fr.now = next
	}
	return run
}

// The soak's orders/api, P71, as a property of what the forecast learns,
// across idleAfter from the 1-minute minimum to the hour that's the
// default. Its CPU, its only activity source, stays under its threshold, and
// a request turns back every pause in its load hours. Each hour a request
// reached the doorman in, turning a pause back or waking it from zero, is
// fed as busy, and every other hour, in its load hours or not, as its CPU.
// So once the forecast acts, it holds pauses off in load hours that only
// turned-back pauses taught it are busy. It doesn't hold them all: an hour
// it holds is seen by its CPU alone, far quieter than forecast, so it
// doubts the next and lets that pause go ahead, for a request to turn back.
func TestTurnedBackPauses_TeachTheForecastTheirHours(t *testing.T) {
	if testing.Short() {
		t.Skip("simulates weeks of reconciles")
	}
	t.Parallel()
	monday := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	const weeks = 4
	busy := forecast.RequestDemand(10)
	for _, idleAfter := range []time.Duration{time.Minute, 5 * time.Minute, 20 * time.Minute, time.Hour} {
		t.Run(fmt.Sprintf("idleAfter %s", idleAfter), func(t *testing.T) {
			t.Parallel()
			run := turnedBackWeeks(t, monday, weeks, idleAfter)

			var requested int
			for hour, peak := range run.fed {
				if run.requested[hour] {
					requested++
					assert.GreaterOrEqual(t, peak, busy, "a request reached the doorman in the hour from %s", hour)
				} else {
					assert.Less(t, peak, 10.0, "no request reached the doorman in the hour from %s", hour)
				}
			}
			require.GreaterOrEqual(t, requested, 5*weeks, "at least an hour of each load day saw a request")

			for w := 1; w < weeks; w++ {
				from := monday.AddDate(0, 0, 7*w)
				var held []time.Time
				for _, at := range run.vetoes {
					if !at.Before(from) && at.Before(from.AddDate(0, 0, 7)) && loadHours(at) && at.Hour() >= 10 {
						held = append(held, at)
					}
				}
				assert.NotEmpty(t, held, "week %d: a pause is held off past the morning's first hour, "+
					"which only turned-back pauses taught it is busy; vetoes at %v", w+1, run.vetoes)
			}
		})
	}
}
