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
	"strings"
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

// earlyPauseRow is one row of the forecasting page's table of what pausing
// early saves and costs: a pattern, and the weeks it's measured over.
type earlyPauseRow struct {
	label   string
	traffic func(seed uint64) Traffic
	seeds   int
	weeks   int
	// from and to bound what's measured, from the start of the run.
	from, to time.Duration
	// idleAfter is the workload's, an hour unless set.
	idleAfter time.Duration
}

// earlyPauseRows are the patterns the forecasting page's table measures.
func earlyPauseRows() []earlyPauseRow {
	office := OfficeHours(time.UTC)
	holiday := monday.Add(6*week + 2*day)
	change := monday.Add(6 * week)
	return []earlyPauseRow{
		{"Busy on weekdays", func(uint64) Traffic { return office }, 1, 8, 2 * week, 8 * week, 0},
		{"The same, with `idleAfter: 2h`", func(uint64) Traffic { return office }, 1, 8, 2 * week, 8 * week, 2 * time.Hour},
		{"The same hours every day", func(uint64) Traffic {
			return Working(time.UTC, EveryDay, 9*time.Hour+10*time.Minute, 17*time.Hour+30*time.Minute, 40)
		}, 1, 8, 2 * week, 8 * week, 0},
		{"Office hours, people arriving and leaving up to 20 minutes", func(seed uint64) Traffic {
			return Noisy(time.UTC, seed, 20*time.Minute, 10)
		}, 20, 8, 2 * week, 8 * week, 0},
		{"Office hours, people arriving and leaving up to an hour", func(seed uint64) Traffic {
			return Noisy(time.UTC, seed, 60*time.Minute, 4)
		}, 20, 8, 2 * week, 8 * week, 0},
		{"Office hours, the week of a holiday", func(uint64) Traffic { return Except(office, time.UTC, holiday) },
			1, 9, 6 * week, 7 * week, 0},
		{"Office hours moving to 13:00", func(uint64) Traffic {
			return Switch(change, office, Working(time.UTC, Weekdays, 13*time.Hour, 21*time.Hour, 40))
		}, 1, 10, 6 * week, 8 * week, 0},
		{"Office hours, someone coming back", func(seed uint64) Traffic { return ComingBack(time.UTC, seed, 3) },
			20, 8, 2 * week, 8 * week, 0},
		{"Office hours, someone working in the evening", func(seed uint64) Traffic { return EveningWork(time.UTC, seed, 3) },
			20, 8, 2 * week, 8 * week, 0},
		{"Always busy", func(uint64) Traffic { return Always(true, 40) }, 1, 8, 2 * week, 8 * week, 0},
		{"Always idle", func(uint64) Traffic { return Always(false, 0) }, 1, 8, 2 * week, 8 * week, 0},
	}
}

// measure runs the row's pattern at balanced without and with pausing
// early, and returns the hours a week the workload was awake each way, and
// the cold starts a week pausing early added: uses that found it paused
// when it would otherwise have been up. Each is averaged over the row's
// seeds.
func (row earlyPauseRow) measure(t *testing.T) (off, on, cold float64) {
	t.Helper()
	runs := make([]Scenario, 0, 2*row.seeds)
	for seed := uint64(1); seed <= uint64(row.seeds); seed++ {
		sc := Scenario{Duration: time.Duration(row.weeks) * week, Traffic: row.traffic(seed), Settings: DefaultSettings()}
		if row.idleAfter > 0 {
			sc.Settings.IdleAfter = row.idleAfter
		}
		runs = append(runs, sc)
		sc.Settings.PauseEarly = true
		runs = append(runs, sc)
	}
	reports := simulateAll(t, runs)
	from, to := monday.Add(row.from), monday.Add(row.to)
	for i := 0; i < len(reports); i += 2 {
		off += reports[i].Awake(from, to).Hours()
		on += reports[i+1].Awake(from, to).Hours()
		for _, p := range reports[i+1].EarlyPauses {
			if p.Cold && !p.At.Before(from) && p.At.Before(to) {
				cold++
			}
		}
	}
	n := float64(row.seeds) * (row.to - row.from).Hours() / week.Hours()
	return off / n, on / n, cold / n
}

// The forecasting page's table of what pausing early saves and costs is
// what the simulator measures at balanced: for each pattern, the hours a
// week the workload is awake without and with pausing early, and the cold
// starts a week pausing early adds, averaged over twenty seeds for traffic
// drawn from one.
func TestDocs_PausingEarly(t *testing.T) {
	t.Parallel()
	rows := earlyPauseRows()
	table := readDocsTable(t, "### What It Saves and Costs", "Hours awake a week")
	require.Len(t, table.rows, len(rows), "a row for each pattern the test measures")
	for _, row := range rows {
		var cells [][]float64
		for label, values := range table.rows {
			if strings.HasPrefix(label, row.label) {
				cells = values
			}
		}
		require.Len(t, cells, 2, "the page has a row starting %q", row.label)
		off, on, cold := row.measure(t)
		require.Len(t, cells[0], 2, "%s: hours awake without and with", row.label)
		require.Len(t, cells[1], 1, "%s: extra cold starts", row.label)
		assert.InDelta(t, off, cells[0][0], 0.15, "%s: hours awake a week without pausing early", row.label)
		assert.InDelta(t, on, cells[0][1], 0.15, "%s: hours awake a week pausing early", row.label)
		assert.InDelta(t, cold, cells[1][0], 0.15, "%s: extra cold starts a week", row.label)
	}
}
