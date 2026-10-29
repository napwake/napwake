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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

// A workload no longer used, its load stopped for good while the forecast
// holds its pauses in its old hours, which it learns as unseen while its
// last wrong pause, a request's, is under 7 days old. After that, each hour
// it's held in is its CPU again, which the forecast reads as far quieter
// than it expected: within the week after, it stops holding the old hours,
// and it's paused through them from then on. From the hour after the last
// wrong pause is 7 days old, every old hour is learned, none is unseen,
// and from a week after that none is held, and it's awake in none.
func TestHeldHours_StopForAWorkloadNoLongerUsed(t *testing.T) {
	if testing.Short() {
		t.Skip("simulates weeks of reconciles")
	}
	t.Parallel()
	monday := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	retired, end := monday.AddDate(0, 0, 14), monday.AddDate(0, 0, 35)
	run := soakWeeks(t, monday, soak{idleAfter: 5 * time.Minute, weeks: 5, cpu: variedCPU, zero: 0.2,
		load: switched(retired, loadHours, never)})

	wrong := run.last.Status.IdleWait.LastWrongPause
	require.NotNil(t, wrong)
	require.True(t, wrong.PausedAt.Time.Before(retired), "its last wrong pause, at %s, came while it was used",
		wrong.PausedAt.UTC())
	lapsed := wrong.PausedAt.UTC().Add(heldBlindFor)
	week := lapsed.AddDate(0, 0, 7)

	var unseen int
	var awake, afterLapse time.Duration
	for hour := retired; hour.Before(end); hour = hour.Add(time.Hour) {
		if !loadHours(hour) {
			continue
		}
		_, fed := run.engine.fed[hour]
		awake += run.awake[hour]
		switch {
		case hour.Add(time.Hour).Before(lapsed):
			if run.held[hour] && !fed {
				unseen++
			}
		case hour.Before(week):
			afterLapse += run.awake[hour]
			assert.True(t, fed || hour.Before(lapsed), "the old hour from %s is learned, not unseen", hour)
		default:
			assert.True(t, fed, "the old hour from %s is learned, not unseen", hour)
			assert.False(t, run.held[hour], "the forecast holds a pause off in the old hour from %s", hour)
			assert.Zero(t, run.awake[hour], "awake in the old hour from %s", hour)
		}
	}
	t.Logf("last wrong pause %s; once retired, awake %s in its old hours, %d held unseen, and %s from the hour "+
		"%s is in", wrong.PausedAt.UTC(), awake, unseen, afterLapse, lapsed)
	assert.Positive(t, unseen, "once retired, held in its old hours, unseen, until its last wrong pause is 7 days old")
	assert.LessOrEqual(t, afterLapse, 5*time.Hour, "then awake an hour or so an old load day, as the forecast "+
		"relearns it")
	assert.Equal(t, v1alpha1.PhasePaused, run.last.Status.Phase)
}
