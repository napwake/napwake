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
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/forecast/sim"
)

// The simulator stands in for the operator only if it decides what the
// operator decides. Six weeks of office hours, run through the reconciler
// against a fake API server and through the simulator, wake and pause the
// workload at the same minutes for the same reasons, the forecast bringing
// the same pauses forward with pauseEarly.
func TestSimulatorMatchesTheReconciler(t *testing.T) {
	if testing.Short() {
		t.Skip("simulates six weeks of reconciles")
	}
	t.Parallel()
	require.Equal(t, unobservedGap, sim.LookGap, "an hour is seen whole on the same terms")

	monday := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	const days = 42
	for _, pauseEarly := range []bool{false, true} {
		t.Run(fmt.Sprintf("pauseEarly %t", pauseEarly), func(t *testing.T) {
			t.Parallel()
			reconciled := doormanDays(t, monday, days, v1alpha1.IdlePolicySpec{AutoResume: true, PauseEarly: pauseEarly},
				func(now time.Time, _ *v1alpha1.ManagedWorkload, metrics *stubMetrics) bool {
					if !officeDay(now) {
						return false
					}
					metrics.cpuMillis = 400
					return true
				})

			settings := sim.DefaultSettings()
			settings.Confidence = forecastWorkload().Spec.Prediction.Confidence
			settings.PauseEarly = pauseEarly
			r, err := sim.Run(sim.Scenario{
				Start:    monday,
				Duration: days * 24 * time.Hour,
				Traffic: func(now time.Time) sim.Use {
					if officeDay(now) {
						return sim.Use{Active: true, Percent: 40}
					}
					return sim.Use{Percent: 0.5}
				},
				Settings: settings,
				Startup:  sim.Startup{Percent: 80, For: 3 * time.Minute},
			})
			require.NoError(t, err)
			simulated := make([]lifecycleEvent, 0, len(r.Wakes)+len(r.EarlyPauses)+len(r.Pauses))
			for _, w := range r.Wakes {
				reason := ReasonWokeByActivity
				if w.Cause == sim.WokenByForecast {
					reason = ReasonAutoResume
				}
				simulated = append(simulated, lifecycleEvent{at: w.At, reason: reason})
			}
			for _, p := range r.EarlyPauses {
				simulated = append(simulated, lifecycleEvent{at: p.At, reason: ReasonPausedEarly})
			}
			for _, p := range r.Pauses {
				simulated = append(simulated, lifecycleEvent{at: p, reason: ReasonPaused})
			}
			slices.SortStableFunc(simulated, func(a, b lifecycleEvent) int { return a.at.Compare(b.at) })

			assert.Equal(t, reconciled, simulated)
			if pauseEarly {
				assert.NotEmpty(t, r.EarlyPauses)
			}
		})
	}
}
