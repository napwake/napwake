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

// A workload no longer used, at its worst: its load stops for good right
// after the wrong pauses a lapse of the 7 days brings, so its last wrong
// pause, a request's, is as fresh as it gets. The forecast holds its pauses
// off in its old hours, unseen, until that pause is 7 days old: at a full
// working day's load, up to about 46 hours awake in that week, as the
// forecasting page says. After that, each hour it's held in is its CPU
// again, which the forecast reads as far quieter than it expected: within
// the week after, it stops holding them, a few hours awake in all, and
// every old hour is learned. From the third week, none is held, and it's
// awake in none.
func TestHeldHours_StopForAWorkloadNoLongerUsed(t *testing.T) {
	if testing.Short() {
		t.Skip("simulates weeks of reconciles")
	}
	t.Parallel()
	monday := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	// The 7 days from the last wrong pause of its second week lapse on the
	// Monday evening of its third, and requests undo 3 pauses the next
	// morning, the last at 10:30.
	retired, end := time.Date(2026, 3, 31, 11, 0, 0, 0, time.UTC), monday.AddDate(0, 0, 35)
	run := soakWeeks(t, monday, soak{idleAfter: 5 * time.Minute, weeks: 5, cpu: variedCPU, zero: 0.2,
		load: switched(retired, loadHours, never)})

	wrong := run.last.Status.IdleWait.LastWrongPause
	require.NotNil(t, wrong)
	require.Equal(t, v1alpha1.WokenByRequest, wrong.By)
	last := wrong.PausedAt.UTC()
	assert.True(t, last.Before(retired) && retired.Sub(last) < time.Hour,
		"its last wrong pause, at %s, came right before it was retired", last)
	day := retired.Truncate(24 * time.Hour)
	var before time.Time
	for _, at := range append(append([]time.Time{}, run.turned...), run.atZero...) {
		if at.Before(day) && at.After(before) {
			before = at
		}
	}
	assert.Greater(t, day.Sub(before), 7*24*time.Hour, "the 7 days from the wrong pause before, at %s, had "+
		"lapsed", before)

	lapsed := last.Add(7 * 24 * time.Hour)
	// Its old hours, and the quarter-hour ahead of them autoResume woke it
	// in.
	old := weekdays(8, 18)
	var unseen int
	var held, after time.Duration
	for hour := retired; hour.Before(end); hour = hour.Add(time.Hour) {
		if !old(hour) {
			continue
		}
		_, fed := run.engine.fed[hour]
		switch {
		case hour.Before(lapsed):
			held += run.awake[hour]
			if run.held[hour] && hour.Add(time.Hour).Before(lapsed) {
				assert.False(t, fed, "held in the old hour from %s, its last wrong pause under 7 days old, and fed",
					hour)
				unseen++
			}
		case hour.Before(lapsed.AddDate(0, 0, 7)):
			after += run.awake[hour]
			assert.True(t, fed, "the old hour from %s is learned, not unseen", hour)
		default:
			assert.True(t, fed, "the old hour from %s is learned, not unseen", hour)
			assert.False(t, run.held[hour], "the forecast holds a pause off in the old hour from %s", hour)
			assert.Zero(t, run.awake[hour], "awake in the old hour from %s", hour)
		}
	}
	t.Logf("last wrong pause %s, retired %s: awake %s in its old hours, %d held unseen, until %s, and %s in the "+
		"week after", last, retired, held, unseen, lapsed, after)
	assert.Positive(t, unseen, "once retired, held in its old hours, unseen, until its last wrong pause is 7 days old")
	assert.Greater(t, held, 40*time.Hour, "at its worst, held awake through nearly all its old hours")
	assert.LessOrEqual(t, held, 46*time.Hour+30*time.Minute, "up to about 46 hours, the forecasting page says")
	assert.LessOrEqual(t, after, 5*time.Hour, "then a few hours in the week after, as the forecast relearns them")
	assert.Equal(t, v1alpha1.PhasePaused, run.last.Status.Phase)
}
