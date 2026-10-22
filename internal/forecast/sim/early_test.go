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

package sim

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pausingEarly() Settings {
	set := DefaultSettings()
	set.PauseEarly = true
	return set
}

// Once the forecast acts on office hours, it brings each weekday's pause
// forward from 18:29, an idleAfter after the last busy minute, to 18:00:
// the hour people leave in is busy, so it can't pause before that hour is
// over, and 18:00 to 20:00 is quiet. Mornings are ready as before, nobody
// finds it paused who wouldn't have, and the forecast learns every hour it
// learned without pausing early.
func TestEarlyPause_OfficeHours(t *testing.T) {
	without := simulate(t, Scenario{Duration: 8 * week, Traffic: OfficeHours(time.UTC)})
	r := simulate(t, Scenario{Duration: 8 * week, Traffic: OfficeHours(time.UTC), Settings: pausingEarly()})

	settled := monday.Add(4 * week)
	require.NotEmpty(t, r.EarlyPauses)
	for _, p := range r.EarlyPauses {
		assert.Equal(t, "18:00", p.At.Format("15:04"), "%s", p.At)
		assert.Equal(t, "18:29", p.ClockAt.Format("15:04"), "the clock would have paused it then")
		assert.Equal(t, "20:00", p.QuietUntil.Format("15:04"))
		assert.False(t, p.Cold, "%s", p.At)
	}
	early := r.Summarize(settled)
	assert.Equal(t, 20, early.EarlyPauses, "every weekday of the last four weeks")
	assert.Equal(t, without.Summarize(settled).Ready, early.Ready, "mornings ready")
	assert.Equal(t, early.UsedDays, early.Ready)
	assert.Equal(t, without.Hours, r.Hours, "the hours paused behind the doorman are learned as quiet")
	assert.Equal(t, without.Phases, r.Phases)
	saved := without.Awake(settled, r.End) - r.Awake(settled, r.End)
	assert.Equal(t, 20*29*time.Minute, saved)
}

// The forecast brings no pause forward on the days it can go wrong: a
// holiday, which it doubts from its first quiet hour, and the first two
// days of new hours, while it relearns. Those pauses are the clock's, as
// without pausing early, and nobody finds the workload paused who
// wouldn't have.
func TestEarlyPause_NotWhileInDoubt(t *testing.T) {
	holiday := monday.Add(6*week + 2*day)
	change := monday.Add(6 * week)
	afternoons := Working(time.UTC, Weekdays, 13*time.Hour, 21*time.Hour, 40)
	for _, tt := range []struct {
		name    string
		traffic Traffic
		doubted []time.Time
	}{
		{name: "holiday", traffic: Except(OfficeHours(time.UTC), time.UTC, holiday),
			doubted: []time.Time{holiday}},
		{name: "change of hours", traffic: Switch(change, OfficeHours(time.UTC), afternoons),
			doubted: []time.Time{change, change.Add(day)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			without := simulate(t, Scenario{Duration: 9 * week, Traffic: tt.traffic})
			r := simulate(t, Scenario{Duration: 9 * week, Traffic: tt.traffic, Settings: pausingEarly()})
			for _, d := range tt.doubted {
				for _, p := range r.EarlyPauses {
					assert.False(t, midnight(p.At).Equal(d), "brought forward on %s", p.At.Format("Mon 02 Jan 15:04"))
				}
				assert.Equal(t, pausesOn(without, d), pausesOn(r, d), "%s", d.Format("Mon 02 Jan"))
			}
			assert.Zero(t, r.Summarize(monday).ColdEarly)
		})
	}
}

func pausesOn(r Report, d time.Time) []time.Time {
	var on []time.Time
	for _, p := range r.Pauses {
		if midnight(p).Equal(d) {
			on = append(on, p)
		}
	}
	return on
}

// The cost of pausing early is someone using the workload in an hour the
// forecast expected to be quiet, within idleAfter of the last use: coming
// back after 18:00 for one last thing, they find it paused, and their
// request waits for it to wake, where it would have been up.
func TestEarlyPause_ComingBackFindsItPaused(t *testing.T) {
	r := simulate(t, Scenario{Duration: 8 * week, Traffic: ComingBack(time.UTC, 1, 3), Settings: pausingEarly()})

	s := r.Summarize(monday.Add(2 * week))
	require.Positive(t, s.ColdEarly)
	for _, p := range r.EarlyPauses {
		if !p.Cold {
			continue
		}
		assert.True(t, p.WokeAt.Before(p.ClockAt), "woken before the clock would have paused it")
		assert.Equal(t, 18, p.WokeAt.Hour(), "coming back after 18:00, within an hour of leaving")
	}
}

// Without the doorman, a request wouldn't wake a paused workload, so the
// forecast never brings a pause forward.
func TestEarlyPause_NeedsTheDoorman(t *testing.T) {
	set := pausingEarly()
	set.WakeOnRequest = false
	r := simulate(t, Scenario{Duration: 4 * week, Traffic: OfficeHours(time.UTC), Settings: set, HandWake: 5 * time.Minute})
	assert.Empty(t, r.EarlyPauses)
}
