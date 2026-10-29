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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/forecast"
)

// loadHours is the test cluster's load: weekdays from 09:00 to 17:59 UTC.
func loadHours(now time.Time) bool {
	if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		return false
	}
	return now.Hour() >= 9 && now.Hour() < 18
}

// loadgenCalls reports whether the test cluster's load generator is calling
// at now: for 270 seconds of every 5 minutes through the load hours.
func loadgenCalls(now time.Time) bool {
	return loadHours(now) && now.Sub(now.Truncate(5*time.Minute)) < 270*time.Second
}

// nextCall is when the load generator next starts calling after now.
func nextCall(now time.Time) time.Time {
	for at := now.Truncate(5 * time.Minute); ; at = at.Add(5 * time.Minute) {
		if at.After(now) && loadHours(at) {
			return at
		}
	}
}

// soakEngine is the real forecast, noting the peak it's fed for each hour,
// by its start in UTC, and whether it acts once fed each.
type soakEngine struct {
	*forecast.Engine
	fed    map[time.Time]float64
	acting []fedHour
}

type fedHour struct {
	start  time.Time
	acting bool
}

func (e *soakEngine) Observe(actual float64, hour time.Time) (float64, error) {
	predicted, err := e.Engine.Observe(actual, hour)
	if err == nil {
		e.fed[hour.UTC()] = actual
		e.acting = append(e.acting, fedHour{start: hour.UTC(), acting: e.GetPhase() >= forecast.DailyActive})
	}
	return predicted, err
}

// soak is how the test cluster's orders/api is used, from the Monday a
// soakWeeks run starts on: behind the doorman with wake.mode Request, its
// only activity source its CPU, under its 10% threshold all day, autoResume
// on and the forecast at the balanced confidence, 75, as on the cluster.
type soak struct {
	idleAfter time.Duration
	weeks     int
	// cpu is its CPU through its load hours, as a percentage of what it
	// requests. It's 0.5% otherwise.
	cpu func(now time.Time) float64
	// zero is the share of its pauses in its load hours no call reaches
	// before it's scaled down, so the next wakes it from zero.
	zero float64
	// retired is when its load stops for good, or zero for never.
	retired time.Time
}

// steadyCPU is a CPU of 7% through the load hours.
func steadyCPU(time.Time) float64 { return 7 }

// variedCPU is a CPU between 6 and 9.9% through the load hours, changing
// each hour, as the test cluster's orders/api's.
func variedCPU(now time.Time) float64 {
	return 6 + 3.9*spread(uint64(now.Unix()/3600))
}

// spread maps n to [0, 1), evenly enough for a simulation to draw from.
func spread(n uint64) float64 {
	return float64((n*2654435761)%1000) / 1000
}

// soakRun is what a soakWeeks run saw, each hour by its start in UTC.
type soakRun struct {
	engine *soakEngine
	// requested are the hours a call reached the doorman in, as the
	// workload paused or was paused.
	requested map[time.Time]bool
	// held are the hours the forecast held a pause off in, and blind those
	// in which it did while the workload's last wrong pause, undone by a
	// request, was under heldBlindFor old.
	held, blind map[time.Time]bool
	// turned and atZero are when each wrong pause was undone, as it paused
	// or once it was scaled to zero.
	turned, atZero []time.Time
	// awake is how long the workload wasn't paused in each hour.
	awake map[time.Time]time.Duration
	// mornings is, for each load day by its first hour, whether the
	// workload was awake for the load generator's first call: woken ahead.
	mornings map[time.Time]bool
	// last is the workload as the run ended.
	last *v1alpha1.ManagedWorkload
}

// operate reconciles the workload as Reconcile does once it has read the
// target: the doorman's routing brought up to date, then a pause under way
// gone on with, or automation.
func operate(ctx context.Context, r *Reconciler, workload *v1alpha1.ManagedWorkload,
	target client.Object) (*ctrl.Result, error) {
	r.routeDoorman(ctx, workload, target)
	if workload.Status.Phase == v1alpha1.PhasePausing {
		return r.resumeTransition(ctx, workload, target)
	}
	return r.reconcileAutomation(ctx, workload, target)
}

// soakWeeks runs the test cluster's orders/api, as s has it, for s.weeks
// from monday, reconciled as the operator would: at the requeue each
// reconcile asks for, and when a call stamped by the doorman wakes it. In
// its load hours, a call calls off a pause under way, and wakes it once
// paused.
func soakWeeks(t *testing.T, monday time.Time, s soak) soakRun {
	t.Helper()
	ctx := context.Background()
	workload := forecastWorkload()
	workload.Spec.IdlePolicy = &v1alpha1.IdlePolicySpec{IdleAfter: &metav1.Duration{Duration: s.idleAfter},
		AutoResume: true}
	workload.Spec.Prediction.Confidence = 0
	workload.Status.Activity = nil
	fr := newForecastReconciler(t, workload, nil)
	fr.now = monday
	engine := &soakEngine{Engine: forecast.NewEngine(forecast.DefaultParams(), forecast.Settings{}),
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

	run := soakRun{engine: engine, requested: map[time.Time]bool{}, held: map[time.Time]bool{},
		blind: map[time.Time]bool{}, awake: map[time.Time]time.Duration{}, mornings: map[time.Time]bool{}}
	for end := monday.AddDate(0, 0, 7*s.weeks); fr.now.Before(end); {
		require.NoError(t, fr.Get(ctx, client.ObjectKeyFromObject(workload), workload))
		retired := !s.retired.IsZero() && !fr.now.Before(s.retired)
		hour := forecast.HourStart(fr.now, nil)
		load, calls := loadHours(fr.now) && !retired, loadgenCalls(fr.now) && !retired
		metrics.cpuMillis = 5
		if load {
			metrics.cpuMillis = s.cpu(fr.now) * 10
		}
		phase := workload.Status.Phase
		if morning := fr.now.Truncate(24 * time.Hour).Add(9 * time.Hour); load {
			if _, seen := run.mornings[morning]; !seen {
				run.mornings[morning] = phase != v1alpha1.PhasePaused && phase != v1alpha1.PhasePausing
			}
		}
		missed := calls && phase == v1alpha1.PhasePausing &&
			spread(uint64(workload.Status.LastTransitionTime.Unix())) < s.zero
		if calls && !missed && (phase == v1alpha1.PhasePausing || phase == v1alpha1.PhasePaused) {
			workload.Annotations = map[string]string{v1alpha1.AnnotationLastRequest: fr.now.Format(time.RFC3339)}
			require.NoError(t, fr.Update(ctx, workload))
			run.requested[hour] = true
		}

		observed := workload.Status.DeepCopy()
		result, err := operate(ctx, fr.Reconciler, workload, target)
		require.NoError(t, err, "at %s", fr.now)
		require.NoError(t, fr.persistStatus(ctx, workload, observed))
		for len(recorder.Events) > 0 {
			e := <-recorder.Events
			switch {
			case !strings.HasPrefix(e, "Warning "+ReasonPausedWhileInUse+" "):
			case strings.Contains(e, " as it paused"):
				run.turned = append(run.turned, fr.now)
			default:
				run.atZero = append(run.atZero, fr.now)
			}
		}
		if meta.IsStatusConditionTrue(workload.Status.Conditions, conditionIdleVetoed) {
			run.held[hour] = true
			if wrong := workload.Status.IdleWait.LastWrongPause; wrong != nil && wrong.By == v1alpha1.WokenByRequest &&
				fr.now.Sub(wrong.PausedAt.Time) < heldBlindFor {
				run.blind[hour] = true
			}
		}

		require.NotNil(t, result, "at %s", fr.now)
		next := fr.now.Add(result.RequeueAfter)
		if workload.Status.Phase == v1alpha1.PhasePaused && !retired {
			if call := nextCall(fr.now); call.Before(next) {
				next = call
			}
			if missed && loadgenCalls(fr.now.Add(20*time.Second)) {
				next = fr.now.Add(20 * time.Second)
			}
		}
		if workload.Status.Phase != v1alpha1.PhasePaused {
			run.awake[hour] += next.Sub(fr.now)
		}
		fr.now = next
	}
	run.last = workload
	return run
}

// wrongPerDay is how many pauses were wrong each load day from from until
// until, on average.
func (run soakRun) wrongPerDay(from, until time.Time) float64 {
	wrong := 0
	for _, at := range append(append([]time.Time{}, run.turned...), run.atZero...) {
		if !at.Before(from) && at.Before(until) {
			wrong++
		}
	}
	return float64(wrong) / float64(max(run.loadDays(from, until), 1))
}

func (run soakRun) loadDays(from, until time.Time) int {
	days := 0
	for day := from; day.Before(until); day = day.AddDate(0, 0, 1) {
		if loadHours(day.Add(9 * time.Hour)) {
			days++
		}
	}
	return days
}

// wokenAhead is how many load days' mornings from from until until found
// the workload awake, of how many there were.
func (run soakRun) wokenAhead(from, until time.Time) (woken, days int) {
	for morning, awake := range run.mornings {
		if morning.Before(from) || !morning.Before(until) {
			continue
		}
		days++
		if awake {
			woken++
		}
	}
	return woken, days
}

// leftActing is when the forecast stopped acting, from from until until:
// it acted once fed one hour and not once fed the next.
func (run soakRun) leftActing(from, until time.Time) []time.Time {
	var left []time.Time
	for i := 1; i < len(run.engine.acting); i++ {
		h := run.engine.acting[i]
		if !h.start.Before(from) && h.start.Before(until) && run.engine.acting[i-1].acting && !h.acting {
			left = append(left, h.start)
		}
	}
	return left
}

// heldShare is how many of the load hours from from until until the
// forecast held a pause off in, of how many there were.
func (run soakRun) heldShare(from, until time.Time) (held, load int) {
	for hour := from; hour.Before(until); hour = hour.Add(time.Hour) {
		if loadHours(hour) {
			load++
			if run.held[hour] {
				held++
			}
		}
	}
	return held, load
}

// The soak's orders/api, P71, as a property of what the forecast learns,
// across idleAfter from the 1-minute minimum to the hour that's the
// default, at the test cluster's settings. Its CPU, its only activity
// source, stays under its threshold, and a call through the doorman turns
// back every pause in its load hours. Each hour a call reached the doorman
// in, turning a pause back or waking it from zero, is fed as busy, and each
// hour the forecast held a pause off in, within 7 days of a wrong pause a
// request undid, isn't fed at all: nothing sees the calls while it's held
// awake. Every other hour is fed its CPU. So once the forecast acts, it
// holds pauses off through the load days, and wrong pauses become rare.
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
			run := soakWeeks(t, monday, soak{idleAfter: idleAfter, weeks: weeks, cpu: steadyCPU})

			var requested int
			for hour, peak := range run.engine.fed {
				assert.False(t, run.blind[hour], "the hour from %s was held, blind, and fed %.1f", hour, peak)
				if run.requested[hour] {
					requested++
					assert.GreaterOrEqual(t, peak, busy, "a call reached the doorman in the hour from %s", hour)
				} else {
					assert.Less(t, peak, 10.0, "no call reached the doorman in the hour from %s", hour)
				}
			}
			require.GreaterOrEqual(t, requested, 5*weeks, "at least an hour of each load day saw a call")

			from, until := monday.AddDate(0, 0, 14), monday.AddDate(0, 0, 7*weeks)
			held, load := run.heldShare(from, until)
			woken, days := run.wokenAhead(from, until)
			t.Logf("in its last two weeks, held %d of %d load hours, %.2f wrong pauses a load day, woken ahead %d of "+
				"%d mornings", held, load, run.wrongPerDay(from, until), woken, days)
			assert.GreaterOrEqual(t, held, load*3/4, "in its last two weeks, the forecast holds pauses off in "+
				"most load hours")
			assert.LessOrEqual(t, run.wrongPerDay(from, until), 2.5, "wrong pauses a load day in its last two weeks")
			assert.GreaterOrEqual(t, woken, days*3/4, "mornings autoResume woke it ahead in its last two weeks")
		})
	}
}
