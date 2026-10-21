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
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/okedeji/hybernate/internal/forecast"
)

const (
	day  = 24 * time.Hour
	week = 7 * day
)

// monday is a Monday at midnight UTC, with no clock change for months.
var monday = time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)

// startup is the CPU a typical workload burns as it starts, enough to look
// busy: the warm-up must keep the forecast from learning it.
var startup = Startup{Percent: 80, For: 3 * time.Minute}

func simulate(t *testing.T, sc Scenario) Report {
	t.Helper()
	r, err := Run(withDefaults(sc))
	require.NoError(t, err)
	return r
}

func withDefaults(sc Scenario) Scenario {
	if sc.Start.IsZero() {
		sc.Start = monday
	}
	if sc.Settings == (Settings{}) {
		sc.Settings = DefaultSettings()
	}
	if sc.Startup == (Startup{}) {
		sc.Startup = startup
	}
	return sc
}

// simulateAll runs the scenarios at once, a goroutine each: they share
// nothing, and the measurements over many seeds take dozens of them.
func simulateAll(t *testing.T, scs []Scenario) []Report {
	t.Helper()
	reports := make([]Report, len(scs))
	errs := make([]error, len(scs))
	var wg sync.WaitGroup
	for i := range scs {
		sc := withDefaults(scs[i])
		wg.Go(func() { reports[i], errs[i] = Run(sc) })
	}
	wg.Wait()
	require.NoError(t, errors.Join(errs...))
	return reports
}

func local(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	require.NoError(t, err)
	return loc
}

// Once the forecast has learned office hours, autoResume has the workload
// up at 08:45 every weekday, 25 minutes before people arrive, the activity
// clock pauses it an idleAfter after they leave, and weekends are left
// asleep: the operator's own six-week controller test says the same.
func TestOfficeHours_ReadyEveryMorningOnceLearned(t *testing.T) {
	r := simulate(t, Scenario{Duration: 8 * week, Traffic: OfficeHours(time.UTC)})

	settled := monday.Add(4 * week)
	s := r.Summarize(settled)
	assert.Equal(t, 20, s.UsedDays)
	assert.Equal(t, 20, s.Ready, "every weekday of the last four weeks")
	assert.Equal(t, 25*time.Minute, s.MedianLead)
	assert.Zero(t, s.WastedWakes)
	assert.Zero(t, s.Vetoes)
	for _, w := range r.Wakes {
		if w.At.Before(settled) || w.PausedAt.IsZero() {
			continue
		}
		assert.Equal(t, WokenByForecast, w.Cause, "%s", w.At)
		assert.Equal(t, "08:45", w.At.Format("15:04"), "%s", w.At)
		assert.Equal(t, "18:29", w.PausedAt.Format("15:04"), "paused an hour after the last busy minute, %s", w.At)
		assert.True(t, Weekdays(midnight(w.At)), "woken on a %s", w.At.Weekday())
	}

	first := r.Summarize(monday)
	assert.Zero(t, first.Missed, "the forecast never sleeps through a morning it acts on")
	var cold []string
	for _, d := range r.Days {
		if d.Outcome == Cold {
			cold = append(cold, d.Date.Format("Mon 02"))
		}
	}
	assert.Equal(t, []string{"Mon 16", "Tue 17", "Wed 18", "Thu 19", "Fri 20", "Mon 23"}, cold,
		"people wake it themselves the first week, and on the second Monday, before the forecast has scored a day "+
			"of its forecasts")
}

// Once the forecast acts on office hours, at any setting, every morning is
// ready, Mondays too, and weekends are left asleep: a weekend's quiet,
// learned in the first week, doesn't cost the forecast its confidence
// before the Monday after.
func TestOfficeHours_MondaysReadyOnceActing(t *testing.T) {
	t.Parallel()
	scs := make([]Scenario, 0, len(forecast.Modes))
	for _, mode := range forecast.Modes {
		set := DefaultSettings()
		set.Confidence = mode.Threshold()
		scs = append(scs, Scenario{Name: string(mode), Duration: 5 * week, Traffic: OfficeHours(time.UTC),
			Settings: set})
	}
	for _, r := range simulateAll(t, scs) {
		acts, ok := r.Reached(forecast.DailyActive)
		require.True(t, ok, r.Name)
		s := r.Summarize(monday.Add(acts))
		assert.Equal(t, s.UsedDays, s.Ready, "%s: every morning from %s", r.Name, monday.Add(acts).Format("Mon 02"))
		assert.Zero(t, s.IdleDayWakes, "%s: wakes on weekends", r.Name)
		for _, d := range r.Days {
			if d.Date.Weekday() == time.Monday && d.Date.After(monday.Add(acts)) {
				assert.Equal(t, Ready, d.Outcome, "%s: %s", r.Name, d.Date.Format("Mon 02 Jan"))
			}
		}
	}
}

// The first use of each day in the week before the forecast acts finds the
// workload paused, and wakes it, as without a forecast at all.
func TestOfficeHours_LearningDoesNotCostAvailability(t *testing.T) {
	r := simulate(t, Scenario{Duration: week, Traffic: OfficeHours(time.UTC)})
	s := r.Summarize(monday)
	assert.Equal(t, 5, s.Cold)
	assert.Zero(t, s.ForecastWakes)
	assert.Equal(t, 5, len(r.Wakes))
	for _, w := range r.Wakes {
		assert.Equal(t, WokenByRequest, w.Cause)
	}
}

// A workload busy all the time is never paused, and the forecast acts once
// it has seen the week and scored a day of it, as for any pattern.
func TestAlwaysBusy(t *testing.T) {
	r := simulate(t, Scenario{Duration: 2 * week, Traffic: Always(true, 40)})
	assert.Empty(t, r.Pauses)
	acts, ok := r.Reached(forecast.DailyActive)
	require.True(t, ok)
	assert.InDelta(t, 8, acts.Hours()/24, 0.1)
}

// A workload nobody uses is paused after idleAfter and left asleep: the
// forecast it learns, of no demand, never wakes it or holds off a pause.
func TestAlwaysIdle(t *testing.T) {
	r := simulate(t, Scenario{Duration: 4 * week, Traffic: Always(false, 0)})
	assert.Len(t, r.Pauses, 1)
	assert.Empty(t, r.Wakes)
	assert.Empty(t, r.Vetoes)
	_, ok := r.Reached(forecast.FullyActive)
	assert.True(t, ok)
}

// A weekday holiday costs one wake for nothing, kept awake while the
// forecast still expects the day, until the regime change the holiday's
// anomalies set off lets the activity clock pause it. The morning after,
// the first request wakes it, since the regime change the holiday's
// anomalies set off has the forecast earning its confidence again. The
// weekly pattern doesn't learn the holiday, because the day after it is
// busy again: the same weekday a week later, and every morning after the
// holiday's next, is ready.
func TestHoliday(t *testing.T) {
	holiday := monday.Add(6*week + 2*day)
	r := simulate(t, Scenario{Duration: 9 * week, Traffic: Except(OfficeHours(time.UTC), time.UTC, holiday)})

	assert.Len(t, r.RegimeChanges, 1)
	h := r.Summarize(holiday)
	next := r.Summarize(holiday.Add(day))
	assert.Equal(t, 1, h.WastedWakes-next.WastedWakes, "woken for the holiday")
	assert.Equal(t, 1, h.IdleDayWakes-next.IdleDayWakes)
	assert.LessOrEqual(t, h.WastedHours-next.WastedHours, 3.5, "hours awake for the holiday")
	var cold []time.Time
	for _, d := range r.Days {
		if d.Outcome == Cold && d.Date.After(holiday) {
			cold = append(cold, d.Date)
		}
	}
	assert.Equal(t, []time.Time{holiday.Add(day)}, cold, "only the morning after the holiday")
	for _, d := range r.Days {
		if d.Date.Equal(holiday.Add(week)) {
			assert.Equal(t, Ready, d.Outcome, "the same weekday a week later")
		}
	}
	after := r.Summarize(holiday.Add(2 * day))
	assert.Equal(t, after.UsedDays, after.Ready, "ready every morning from the second day after")
}

// People who come in on one Saturday don't have the forecast wake the
// workload the Saturday after: the Sunday is quiet again, so the Saturday
// was a one-off, and the weekly pattern doesn't learn it. Like a holiday,
// it costs the next working morning, while the forecast earns back the
// confidence the regime change discarded.
func TestOneSaturdayAtWork(t *testing.T) {
	saturday := monday.Add(5*week + 5*day)
	atWork := Working(time.UTC, func(d time.Time) bool { return d.Equal(saturday) },
		9*time.Hour+10*time.Minute, 17*time.Hour+30*time.Minute, 40)
	office := OfficeHours(time.UTC)
	traffic := func(t time.Time) Use {
		if u := atWork(t); u.Active {
			return u
		}
		return office(t)
	}
	r := simulate(t, Scenario{Duration: 8 * week, Traffic: traffic})

	s := r.Summarize(saturday.Add(day))
	assert.Zero(t, s.IdleDayWakes, "weekend wakes after the Saturday at work")
	assert.Zero(t, s.WastedWakes)
	assert.Equal(t, s.UsedDays-1, s.Ready, "every morning but the Monday after")
	after := r.Summarize(saturday.Add(3 * day))
	assert.Equal(t, after.UsedDays, after.Ready)
}

// A workload nobody uses any more is left asleep within about a week: the
// first quiet day is a regime change, and the weekly pattern learns the
// quiet from the second.
func TestRetiredWorkload(t *testing.T) {
	retired := monday.Add(6 * week)
	r := simulate(t, Scenario{Duration: 10 * week, Traffic: Switch(retired, OfficeHours(time.UTC), Always(false, 0))})

	require.Len(t, r.RegimeChanges, 1)
	assert.WithinDuration(t, retired, r.RegimeChanges[0], day, "noticed within the first day")
	s := r.Summarize(retired)
	assert.LessOrEqual(t, s.WastedWakes, 2, "wakes for the old hours")
	assert.LessOrEqual(t, s.WastedHours, 3.5)
	for _, w := range r.Wakes {
		assert.False(t, w.At.After(retired.Add(8*day)), "woken %s", w.At.Format("Mon 02 Jan 15:04"))
	}
}

// A team that moves its hours from 09:00 to 13:00 for good is relearned:
// a regime change, a week of people waking it themselves, a few wakes for
// the old hours, and within two weeks every afternoon ready again.
func TestPermanentPatternChange(t *testing.T) {
	change := monday.Add(6 * week)
	afternoons := Working(time.UTC, Weekdays, 13*time.Hour, 21*time.Hour, 40)
	r := simulate(t, Scenario{Duration: 10 * week, Traffic: Switch(change, OfficeHours(time.UTC), afternoons)})

	require.Len(t, r.RegimeChanges, 1)
	assert.WithinDuration(t, change, r.RegimeChanges[0], day, "noticed within the first day")
	during := r.Summarize(change)
	after := r.Summarize(change.Add(2 * week))
	assert.LessOrEqual(t, during.WastedWakes-after.WastedWakes, 5, "wakes for the old hours")
	assert.Equal(t, after.UsedDays, after.Ready, "every afternoon ready from the third week")
	assert.Zero(t, after.WastedWakes)
}

// With --timezone set to the users' zone, the clocks changing doesn't move
// anything: the workload is up at 08:45 local time through the switch.
func TestDaylightSaving(t *testing.T) {
	for _, tt := range []struct {
		zone   string
		start  time.Time
		change time.Time
	}{
		{zone: "America/New_York", start: time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC),
			change: time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC)},
		{zone: "Europe/London", start: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC),
			change: time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)},
	} {
		t.Run(tt.zone, func(t *testing.T) {
			loc := local(t, tt.zone)
			start := time.Date(tt.start.Year(), tt.start.Month(), tt.start.Day(), 0, 0, 0, 0, loc)
			set := DefaultSettings()
			set.Location = loc
			r := simulate(t, Scenario{Start: start, Duration: 9 * week, Traffic: OfficeHours(loc), Settings: set})

			from := tt.change.Add(-week)
			s := r.Summarize(from)
			assert.Equal(t, s.UsedDays, s.Ready, "every morning around the switch")
			assert.Zero(t, s.WastedWakes)
			for _, w := range r.Wakes {
				if !w.At.Before(from) {
					assert.Equal(t, "08:45", w.At.In(loc).Format("15:04"), "%s", w.At)
				}
			}
		})
	}
}

// Left at UTC, the operator's clock is an hour off its users' after the
// switch, and the forecast has to relearn their mornings. That's what
// --timezone is for.
func TestDaylightSaving_WrongTimezoneCostsMornings(t *testing.T) {
	ny := local(t, "America/New_York")
	start := time.Date(2026, 2, 2, 0, 0, 0, 0, ny)
	r := simulate(t, Scenario{Start: start, Duration: 9 * week, Traffic: OfficeHours(ny)})

	switched := time.Date(2026, 3, 9, 0, 0, 0, 0, ny)
	s := r.Summarize(switched)
	assert.Positive(t, s.Missed, "mornings the forecast slept through after the switch")
	later := r.Summarize(switched.Add(2 * week))
	assert.Equal(t, later.UsedDays, later.Ready, "relearned within two weeks")
}

// The operator restarting every day, at a different time each day, loses
// the hour each restart lands in and nothing else: the forecast is taken up
// from the state it saved, and starts acting within hours of when it
// would have without the restarts.
func TestOperatorRestarts(t *testing.T) {
	restarts := make([]Restart, 0, 56)
	for d := range 56 {
		at := monday.Add(time.Duration(d)*day + time.Duration(d*97%1440)*time.Minute)
		restarts = append(restarts, Restart{At: at, Down: 2 * time.Minute})
	}
	steady := simulate(t, Scenario{Duration: 8 * week, Traffic: OfficeHours(time.UTC)})
	r := simulate(t, Scenario{Duration: 8 * week, Traffic: OfficeHours(time.UTC), Restarts: restarts})

	assert.Less(t, r.Hours, steady.Hours, "the hours the restarts landed in aren't learned")
	assert.Greater(t, r.Hours, steady.Hours-56, "only those")
	acts, ok := r.Reached(forecast.DailyActive)
	require.True(t, ok)
	steadyActs, _ := steady.Reached(forecast.DailyActive)
	assert.InDelta(t, steadyActs.Hours(), acts.Hours(), 12)
	s := r.Summarize(monday.Add(4 * week))
	assert.Equal(t, s.UsedDays, s.Ready)
}

// Without the doorman, a paused workload's hours can't be seen, so its
// nights are never learned and the forecast never acts: every morning
// someone wakes it by hand.
func TestWithoutTheDoorman(t *testing.T) {
	set := DefaultSettings()
	set.WakeOnRequest = false
	r := simulate(t, Scenario{Duration: 6 * week, Traffic: OfficeHours(time.UTC), Settings: set,
		HandWake: 5 * time.Minute})

	_, ok := r.Reached(forecast.DailySuggesting)
	assert.False(t, ok, "never sees the whole day")
	s := r.Summarize(monday)
	assert.Equal(t, s.UsedDays, s.Cold)
	for _, w := range r.Wakes {
		assert.Equal(t, WokenByHand, w.Cause)
	}
}

// A light-use workload, used at 3% of its request while people are on it,
// is ready every morning when its use is seen another way, such as an
// activity annotation its portal stamps: the operator's own controller
// test checks the same.
func TestLightUseSeenElsewhere(t *testing.T) {
	light := SeenElsewhere(Working(time.UTC, Weekdays, 9*time.Hour+10*time.Minute, 17*time.Hour+30*time.Minute, 3))
	r := simulate(t, Scenario{Duration: 6 * week, Traffic: light})
	s := r.Summarize(monday.Add(5 * week))
	assert.Equal(t, 5, s.Ready)
}

// People who keep office hours loosely, arriving up to 20 minutes either
// side of 09:10 and staying away one weekday in ten, still find it ready on
// most mornings once it's learned, and it's woken for nothing about once a
// fortnight, never for long.
func TestNoisyOfficeHours(t *testing.T) {
	t.Parallel()
	var ready, used, wasted int
	var wastedHours float64
	scs := make([]Scenario, 0, 10)
	for seed := uint64(1); seed <= 10; seed++ {
		scs = append(scs, Scenario{Duration: 8 * week, Traffic: Noisy(time.UTC, seed, 20*time.Minute, 10)})
	}
	for _, r := range simulateAll(t, scs) {
		s := r.Summarize(monday.Add(4 * week))
		ready += s.Ready
		used += s.UsedDays
		wasted += s.WastedWakes
		wastedHours += s.WastedHours
	}
	assert.Greater(t, float64(ready)/float64(used), 0.7, "%d of %d mornings ready", ready, used)
	assert.LessOrEqual(t, float64(wasted)/10, 2.0, "wasted wakes a month")
	assert.LessOrEqual(t, wastedHours/float64(max(wasted, 1)), 3.0, "hours each kept it awake")
}

// A forecast learned from four weeks of history, as from Prometheus when a
// workload is opted in, acts from the first morning.
func TestWarmStart(t *testing.T) {
	hours := Peaks(OfficeHours(time.UTC), monday.Add(-4*week), monday, 5*time.Minute, time.UTC)
	e := forecast.NewEngine(forecast.DefaultParams(), forecast.Settings{Threshold: 75})
	_, err := e.Learn(hours, 10)
	require.NoError(t, err)
	state, err := e.Export()
	require.NoError(t, err)

	r := simulate(t, Scenario{Duration: 2 * week, Traffic: OfficeHours(time.UTC), State: state})
	s := r.Summarize(monday)
	assert.Equal(t, 10, s.UsedDays)
	assert.Equal(t, 10, s.Ready, "ready from the first morning")
	assert.Zero(t, s.WastedWakes)
}

// What the outlook projects in the first week, before the forecast acts, is
// when it does start acting, to within the day, and to within the hour once
// it has seen a weekend.
func TestOutlookProjectsTheStart(t *testing.T) {
	acted := simulate(t, Scenario{Duration: 3 * week, Traffic: OfficeHours(time.UTC)})
	acts, ok := acted.Reached(forecast.DailyActive)
	require.True(t, ok)

	for d := 2; d <= 7; d++ {
		r := simulate(t, Scenario{Duration: time.Duration(d) * day, Traffic: OfficeHours(time.UTC)})
		e, err := forecast.ImportEngine(r.State, forecast.Settings{Threshold: 75})
		require.NoError(t, err)
		o := e.Outlook()
		require.False(t, o.Acting, "day %d", d)
		assert.WithinDuration(t, monday.Add(acts), o.StartsActing, day, "projected on day %d", d)
		if d >= 6 {
			assert.WithinDuration(t, monday.Add(acts), o.StartsActing, time.Hour, "projected on day %d", d)
		}
	}
}

func TestScenarioValidation(t *testing.T) {
	_, err := Run(Scenario{Start: monday, Duration: day, Step: 15 * time.Minute, Traffic: Always(false, 0)})
	require.Error(t, err, "a 15-minute step never sees an hour whole")
	_, err = Run(Scenario{Start: monday, Duration: day})
	require.Error(t, err)
	_, err = Run(Scenario{Start: monday, Duration: day, Traffic: Always(false, 0), State: "not state"})
	require.Error(t, err)
}

// The simulation is fast enough to run months of it in every test run.
func BenchmarkOfficeHoursQuarter(b *testing.B) {
	for b.Loop() {
		if _, err := Run(Scenario{Start: monday, Duration: 13 * week, Traffic: OfficeHours(time.UTC),
			Settings: DefaultSettings(), Startup: startup}); err != nil {
			b.Fatal(err)
		}
	}
}
