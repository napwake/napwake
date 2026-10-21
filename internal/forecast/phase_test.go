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

package forecast

import (
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestEngine() *Engine {
	return NewEngine(DefaultParams(), Settings{Threshold: 85})
}

// phaseLog records every phase an engine moves through, by hour.
type phaseLog struct {
	changes       map[int]Phase
	regimeChanges []int
	firstFully    int
}

func run(t *testing.T, e *Engine, from, hours int, demand func(i int, at time.Time) float64) phaseLog {
	t.Helper()
	log := phaseLog{changes: map[int]Phase{}, firstFully: -1}
	prev := e.Phase
	for i := from; i < from+hours; i++ {
		at := hourAt(i)
		_, err := e.Observe(demand(i, at), at)
		require.NoError(t, err, "observing hour %d", i)
		if e.Phase != prev {
			log.changes[i] = e.Phase
			prev = e.Phase
		}
		if e.RegimeChanged() {
			log.regimeChanges = append(log.regimeChanges, i)
		}
		if e.Phase == FullyActive && log.firstFully < 0 {
			log.firstFully = i
		}
	}
	return log
}

func office(_ int, at time.Time) float64 { return officeHours(at) }

func TestEngine_RealisticTraces(t *testing.T) {
	noise := rand.New(rand.NewSource(1))
	tests := []struct {
		name      string
		demand    func(i int, at time.Time) float64
		fullyBy   int // weeks
		stableAt  int // weeks, after which the phase never changes
		thisHour  func(e *Engine, at time.Time) float64
		wantRange [2]float64
	}{
		{
			name:     "office hours",
			demand:   office,
			fullyBy:  5,
			stableAt: 5,
		},
		{
			name: "office hours with 10% noise",
			demand: func(_ int, at time.Time) float64 {
				return officeHours(at)*(0.9+0.2*noise.Float64()) + 0.3*noise.Float64()
			},
			fullyBy:  5,
			stableAt: 6,
		},
		{
			name:     "always on",
			demand:   func(int, time.Time) float64 { return 30 + 3*noise.NormFloat64() },
			fullyBy:  2,
			stableAt: 2,
		},
		{
			name:     "always idle",
			demand:   func(int, time.Time) float64 { return 0 },
			fullyBy:  2,
			stableAt: 2,
		},
		{
			name:     "idle with a little noise",
			demand:   func(int, time.Time) float64 { return 0.3 * noise.Float64() },
			fullyBy:  2,
			stableAt: 2,
		},
		{
			name:     "daily cycle",
			demand:   func(i int, _ time.Time) float64 { return 20 + 10*math.Sin(2*math.Pi*float64(i%24)/24) },
			fullyBy:  2,
			stableAt: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEngine()
			log := run(t, e, 0, 10*WeeklySeason, tt.demand)

			require.GreaterOrEqual(t, log.firstFully, 0, "never became FullyActive")
			assert.Less(t, log.firstFully, tt.fullyBy*WeeklySeason, "became FullyActive at hour %d", log.firstFully)
			for hour, phase := range log.changes {
				assert.Less(t, hour, tt.stableAt*WeeklySeason, "phase changed to %s at hour %d on a stable pattern", phase, hour)
			}
			assert.Empty(t, log.regimeChanges, "a stable pattern is never a regime change")
			assert.Equal(t, FullyActive, e.Phase)
		})
	}
}

func TestEngine_OfficeHoursForecast(t *testing.T) {
	e := newTestEngine()
	run(t, e, 0, 8*WeeklySeason, office)

	monday := hourAt(8 * WeeklySeason)
	assert.InDelta(t, 50, e.Predict(10, monday), 5, "Monday 10am")
	assert.Less(t, e.Predict(3, monday), 2.5, "Monday 3am")
	assert.Less(t, e.Predict(24+20, monday), 2.5, "Tuesday 8pm")
	saturday := monday.Add(5 * 24 * time.Hour)
	assert.Less(t, e.Predict(10, saturday), 2.5, "Saturday 10am")
}

func TestEngine_AlwaysIdleForecastsNothing(t *testing.T) {
	e := newTestEngine()
	run(t, e, 0, 3*WeeklySeason, func(int, time.Time) float64 { return 0 })

	require.Equal(t, FullyActive, e.Phase, "confident that nothing is coming")
	for h := range DailySeason {
		assert.Zero(t, e.Predict(h, hourAt(3*WeeklySeason)))
	}
}

// TestEngine_ConfidenceIsNotFooledByZeros: the old MAPE scorer counted a
// clamped zero forecast of zero demand as a perfect hour, so a model that
// forecast nothing at all read as 91-95% confident every Monday.
func TestEngine_ConfidenceIsNotFooledByZeros(t *testing.T) {
	e := newTestEngine()
	for i := range dailyWindow {
		at := hourAt(i)
		e.Scorer.Record(0, officeHours(at))
	}
	e.scale = 50 * 8.0 / 24

	assert.Zero(t, e.DailyConfidence(), "forecasting nothing for a day of office hours")
}

func TestEngine_DailyAndWeeklyConfidenceDiffer(t *testing.T) {
	e := newTestEngine()
	noise := rand.New(rand.NewSource(1))
	differ := 0
	for i := range 4 * WeeklySeason {
		at := hourAt(i)
		_, err := e.Observe(officeHours(at)*(0.8+0.4*noise.Float64()), at)
		require.NoError(t, err)
		if i >= 2*WeeklySeason && e.DailyConfidence() != e.WeeklyConfidence() {
			differ++
		}
	}
	assert.Greater(t, differ, WeeklySeason, "the weekly season is scored over the week, not the last day")
}

func TestScorer_WindowsAreIndependent(t *testing.T) {
	var s Scorer
	for range weeklyWindow - dailyWindow {
		s.Record(0, 100)
	}
	for range dailyWindow {
		s.Record(100, 100)
	}

	assert.Equal(t, 1.0, s.Confidence(dailyWindow, 1), "the last day was perfect")
	assert.InDelta(t, float64(dailyWindow)/weeklyWindow, s.Confidence(weeklyWindow, 1), 1e-9,
		"the week was mostly missed")
}

func TestScorer_ZeroDemandIsJudgedAgainstTheFloor(t *testing.T) {
	var s Scorer
	for range dailyWindow {
		s.Record(5, 0)
	}

	assert.InDelta(t, 0.5, s.Confidence(dailyWindow, 10), 1e-9, "5 off with a floor of 10")
	assert.False(t, s.Ready(weeklyWindow))
	assert.Zero(t, s.Confidence(weeklyWindow, 10))
}

func TestEngine_SingleSpikeIsNotARegimeChange(t *testing.T) {
	e := newTestEngine()
	spikeAt := 4*WeeklySeason + 10
	log := run(t, e, 0, 7*WeeklySeason, func(i int, _ time.Time) float64 {
		if i == spikeAt {
			return 200
		}
		return 0
	})

	assert.Empty(t, log.regimeChanges)
	assert.Less(t, e.Predict(10, hourAt(7*WeeklySeason)), 2.5,
		"one spike isn't learned as a Monday 10am pattern")
	assert.Equal(t, FullyActive, e.Phase, "confidence is regained once the spike leaves the window")
}

// TestEngine_RegimeChangeDemotesOnce is the cascade the old engine had: the
// anomalies stayed in the window, so it demoted every hour, warned every
// hour for most of a day, and then promoted itself back within hours
// because nothing it had learned was reset.
func TestEngine_RegimeChangeDemotesOnce(t *testing.T) {
	// The threshold is low enough that three anomalous hours don't cost the
	// daily season its confidence on their own, so what demotes is the
	// regime change alone.
	e := NewEngine(DefaultParams(), Settings{Threshold: 60})
	steady := func(i int, _ time.Time) float64 { return 10 + 0.1*float64(i%3) }
	run(t, e, 0, 3*WeeklySeason, steady)
	require.Equal(t, FullyActive, e.Phase)

	start := 3 * WeeklySeason
	log := run(t, e, start, 3, func(int, time.Time) float64 { return 45 })
	require.Equal(t, []int{start + 2}, log.regimeChanges)
	assert.Equal(t, WeeklySuggesting, e.Phase, "demoted one step")
	assert.False(t, e.Scorer.Ready(dailyWindow), "confidence is earned again")
	assert.False(t, e.Active(), "before the forecast drives decisions again")
	assert.Zero(t, e.Predict(1, hourAt(start+3)))
	assert.Equal(t, DailySuggesting, e.GetPhase(), "reported as not acting while it isn't")

	log = run(t, e, start+3, DailySeason, steady)
	assert.Empty(t, log.regimeChanges, "the regime change is reported once")
	assert.Equal(t, WeeklySuggesting, e.Phase)
	require.True(t, e.Active())
	assert.Equal(t, WeeklySuggesting, e.GetPhase(), "acting again once a day is scored")

	log = run(t, e, start+3+DailySeason, 2*WeeklySeason, steady)
	assert.Empty(t, log.regimeChanges)
	require.GreaterOrEqual(t, log.firstFully, 0)
	assert.GreaterOrEqual(t, log.firstFully, start+weeklyWindow,
		"FullyActive again only after a full week of evidence")
}

// The anomalies leading up to a regime change can cost the engine its
// confidence before they add up to one. The regime change doesn't demote it
// again for them, and nor does it promote it back to one level below where
// it was before them, on the evidence it has just discarded.
func TestEngine_RegimeChangeDemotesOneLevelInAll(t *testing.T) {
	e := newTestEngine()
	run(t, e, 0, 6*WeeklySeason, office)
	require.Equal(t, FullyActive, e.Phase)

	evenings := func(_ int, at time.Time) float64 { return officeHours(at.Add(8 * time.Hour)) }
	regimeChanges := 0
	for i := 6 * WeeklySeason; i < 7*WeeklySeason; i++ {
		before := e.Phase
		_, err := e.Observe(evenings(i, hourAt(i)), hourAt(i))
		require.NoError(t, err)
		if !e.RegimeChanged() {
			continue
		}
		regimeChanges++
		assert.LessOrEqual(t, e.Phase, before, "a regime change never raises the phase")
		assert.LessOrEqual(t, e.Phase, WeeklySuggesting, "at least one level below FullyActive")
		assert.False(t, e.Active(), "nothing is scored once the evidence is discarded")
	}
	assert.Equal(t, 1, regimeChanges)
}

// TestEngine_HolidayDoesNotWakeIntoTheHoliday: a weekday holiday costs the
// daily season its confidence before its anomalies add up to a regime
// change. The regime change must not switch the forecast back on, or a
// paused workload would be woken for the busy hours the holiday doesn't
// have.
func TestEngine_HolidayDoesNotWakeIntoTheHoliday(t *testing.T) {
	e := NewEngine(DefaultParams(), Settings{Threshold: 75})
	run(t, e, 0, 6*WeeklySeason, office)
	require.Equal(t, FullyActive, e.Phase)

	holiday := 6*WeeklySeason + 2*DailySeason
	run(t, e, 6*WeeklySeason, 2*DailySeason, office)
	regimeChanged := false
	acting := 0
	for i := holiday; i < holiday+DailySeason; i++ {
		_, err := e.Observe(0, hourAt(i))
		require.NoError(t, err)
		regimeChanged = regimeChanged || e.RegimeChanged()
		if regimeChanged && e.Predict(1, hourAt(i)) > 0 {
			acting++
		}
	}
	require.True(t, regimeChanged)
	assert.Zero(t, acting, "hours of the holiday after the regime change the forecast acts on")
}

// A weekday holiday is a regime change, but the weekly pattern doesn't
// learn it: the next day is busy again, so the holiday was a day off, not
// a new pattern, and the same weekday a week later is forecast as busy as
// before.
func TestEngine_HolidayIsNotLearnedIntoTheWeek(t *testing.T) {
	e := NewEngine(DefaultParams(), Settings{Threshold: 75})
	run(t, e, 0, 6*WeeklySeason, office)
	holiday := 6*WeeklySeason + 2*DailySeason
	wednesday10 := hourAt(holiday + 10)
	before := e.Model.forecast(wednesday10, 1)
	require.InDelta(t, 50, before, 5)

	run(t, e, 6*WeeklySeason, 2*DailySeason, office)
	log := run(t, e, holiday, DailySeason, func(int, time.Time) float64 { return 0 })
	require.NotEmpty(t, log.regimeChanges)
	run(t, e, holiday+DailySeason, 6*DailySeason, office)

	nextWednesday := wednesday10.Add(7 * 24 * time.Hour)
	assert.InDelta(t, before, e.Model.forecast(nextWednesday, 1), 5, "the Wednesday after is expected as busy")
	assert.InDelta(t, 0, e.Model.forecast(nextWednesday.Add(10*time.Hour), 1), 2.5, "and its evening as quiet")
}

// A lasting change is learned into the week from the day after it shows:
// the weekly change held on its first day is made once the second day
// breaks from the old pattern the same way.
func TestEngine_LastingChangeIsLearnedIntoTheWeek(t *testing.T) {
	e := NewEngine(DefaultParams(), Settings{Threshold: 75})
	run(t, e, 0, 6*WeeklySeason, office)
	monday10 := hourAt(6*WeeklySeason + 10)
	busy := e.Model.forecast(monday10, 1)

	held := NewEngine(DefaultParams(), Settings{Threshold: 75})
	run(t, held, 0, 6*WeeklySeason, office)
	quiet := func(int, time.Time) float64 { return 0 }
	run(t, held, 6*WeeklySeason, DailySeason, quiet)
	run(t, held, 6*WeeklySeason+DailySeason, DailySeason, office)
	run(t, e, 6*WeeklySeason, 2*DailySeason, quiet)

	nextMonday := monday10.Add(7 * 24 * time.Hour)
	assert.Less(t, e.Model.forecast(nextMonday, 1), busy/2+5,
		"two quiet days in a row: next Monday is expected at most half as busy")
	assert.Greater(t, held.Model.forecast(nextMonday, 1), e.Model.forecast(nextMonday, 1)+10,
		"one quiet Monday, then a busy Tuesday: next Monday is expected busier")
}

// A held change whose next day's hour isn't observed can't be settled, and
// is dropped rather than settled a week later.
func TestEngine_HeldChangeLapsesOverAGap(t *testing.T) {
	e := NewEngine(DefaultParams(), Settings{Threshold: 75})
	run(t, e, 0, 6*WeeklySeason, office)
	run(t, e, 6*WeeklySeason, DailySeason, func(int, time.Time) float64 { return 0 })
	require.NotZero(t, e.held[10], "Monday 10am's change is held")

	_, err := e.Observe(0, hourAt(6*WeeklySeason+2*DailySeason))
	require.NoError(t, err)
	assert.Zero(t, e.held[10], "Tuesday 10am went unobserved")
}

// The first time it sees an hour of the week, the forecast learns it, and
// doesn't score itself on it: it had nothing to forecast the hour from. An
// hour of the week it hasn't seen isn't acted on.
func TestEngine_FirstWeekIsLearnedNotScored(t *testing.T) {
	e := NewEngine(DefaultParams(), Settings{Threshold: 75})
	tuesday10 := DailySeason + 10
	skipTuesday10 := func(_ int, at time.Time) float64 { return officeHours(at) }
	for i := range 3 * WeeklySeason {
		if i%WeeklySeason == tuesday10 {
			continue
		}
		if i == WeeklySeason-1 {
			assert.Zero(t, e.Scorer.count, "nothing scored in the first week")
			assert.Equal(t, Observing, e.Phase)
		}
		run(t, e, i, 1, skipTuesday10)
	}
	require.True(t, e.Active())
	monday := hourAt(3 * WeeklySeason)
	assert.Zero(t, e.Predict(DailySeason+10, monday), "Tuesday 10am, never seen")
	assert.InDelta(t, 50, e.Predict(DailySeason+11, monday), 2.5, "Tuesday 11am")
}

// A large spike that recurs every week is a pattern, not an outlier. It is
// learned within weeks rather than a few percent at a time, and stops
// knocking the engine's confidence once it is.
func TestEngine_RecurringWeeklySpikeIsLearned(t *testing.T) {
	tuesdayBatch := func(_ int, at time.Time) float64 {
		if at.Weekday() == time.Tuesday && at.Hour() >= 2 && at.Hour() < 4 {
			return 500
		}
		return officeHours(at)
	}
	e := newTestEngine()
	log := run(t, e, 0, 10*WeeklySeason, tuesdayBatch)

	require.GreaterOrEqual(t, log.firstFully, 0, "never became FullyActive")
	assert.Less(t, log.firstFully, 6*WeeklySeason, "became FullyActive at hour %d", log.firstFully)
	for hour, phase := range log.changes {
		assert.Less(t, hour, 6*WeeklySeason, "phase changed to %s at hour %d after the spike was learned", phase, hour)
	}
	assert.Empty(t, log.regimeChanges, "a weekly batch job is not a regime change")
	monday := hourAt(10 * WeeklySeason)
	assert.InDelta(t, 500, e.Predict(24+2, monday), 25, "Tuesday 2am")
	assert.Less(t, e.Predict(24+5, monday), 5.0, "Tuesday 5am")
}

func TestEngine_PatternShiftIsRelearned(t *testing.T) {
	e := newTestEngine()
	run(t, e, 0, 6*WeeklySeason, office)
	require.Equal(t, FullyActive, e.Phase)

	evenings := func(_ int, at time.Time) float64 { return officeHours(at.Add(8 * time.Hour)) }
	log := run(t, e, 6*WeeklySeason, 6*WeeklySeason, evenings)

	assert.Len(t, log.regimeChanges, 1)
	assert.Equal(t, FullyActive, e.Phase)
	monday := hourAt(12 * WeeklySeason)
	assert.InDelta(t, 50, e.Predict(3, monday), 5, "Monday 3am is the new busy hour")
	assert.Less(t, e.Predict(10, monday), 2.5, "Monday 10am is quiet now")
}

func TestEngine_ThresholdGatesPromotion(t *testing.T) {
	e := NewEngine(DefaultParams(), Settings{Threshold: 100})
	noisy := rand.New(rand.NewSource(2))
	demand := func(int, time.Time) float64 { return 30 + 3*noisy.NormFloat64() }
	run(t, e, 0, 2*WeeklySeason, demand)
	require.Equal(t, DailySuggesting, e.Phase, "a noisy workload is never 100% predictable")

	e.Configure(Settings{Threshold: 85})
	run(t, e, 2*WeeklySeason, 1, demand)
	assert.Equal(t, DailyActive, e.Phase, "a lowered threshold applies to the engine already running")
}

func TestEngine_PhasesNeedCalendarCoverage(t *testing.T) {
	e := newTestEngine()
	nineToFive := 0
	for day := range 14 {
		for hour := 9; hour < 17; hour++ {
			at := hourAt(day*24 + hour)
			_, err := e.Observe(50, at)
			require.NoError(t, err)
			nineToFive++
		}
	}
	require.Greater(t, nineToFive, DailySeason)

	assert.Equal(t, Observing, e.Phase, "hours of the day never seen can't be forecast")
}

func TestEngine_ObservesEachHourOnce(t *testing.T) {
	e := newTestEngine()
	at := hourAt(10).Add(5 * time.Minute)
	_, err := e.Observe(100, at)
	require.NoError(t, err)

	assert.True(t, e.Observed(at.Add(50*time.Minute)))
	_, err = e.Observe(100, at.Add(50*time.Minute))
	require.ErrorIs(t, err, ErrAlreadyObserved)
	assert.Equal(t, 1, e.GetDataPoints())
	assert.False(t, e.Observed(at.Add(55*time.Minute)), "the next hour is due")
}

// TestEngine_GapsKeepSlotsAligned: a gap, from a pause or the operator being
// down, must not shift what the engine learned onto the wrong hours.
func TestEngine_GapsKeepSlotsAligned(t *testing.T) {
	e := newTestEngine()
	run(t, e, 0, 5*WeeklySeason, office)

	downFrom := 5*WeeklySeason + 8
	_, err := e.Observe(0, hourAt(downFrom))
	require.NoError(t, err)
	resumed := downFrom + 31
	run(t, e, resumed, 2*WeeklySeason, office)

	nextMonday := hourAt((resumed + 2*WeeklySeason + WeeklySeason - 1) / WeeklySeason * WeeklySeason)
	assert.InDelta(t, 50, e.Model.forecast(nextMonday.Add(10*time.Hour), 1), 7.5, "Monday 10am")
	assert.Less(t, e.Model.forecast(nextMonday.Add(7*time.Hour), 1), 5.0, "Monday 7am")
	assert.Equal(t, FullyActive, e.Phase)
}

func TestEngine_RejectsInvalidObservations(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1, maxDemand * 2} {
		e := newTestEngine()
		run(t, e, 0, 30, func(int, time.Time) float64 { return 100 })
		before, err := e.Export()
		require.NoError(t, err)

		_, err = e.Observe(v, hourAt(30))
		require.ErrorIs(t, err, ErrInvalidObservation, "observing %g", v)

		after, err := e.Export()
		require.NoError(t, err, "a rejected observation leaves the engine exportable")
		assert.Equal(t, before, after, "a rejected observation leaves the engine unchanged")
		_, err = e.Observe(100, hourAt(30))
		assert.NoError(t, err, "the hour can still be observed")
	}
}

func TestEngine_PredictsNothingUntilConfident(t *testing.T) {
	e := newTestEngine()
	run(t, e, 0, 10, func(int, time.Time) float64 { return 50 })

	assert.Equal(t, Observing, e.Phase)
	assert.Zero(t, e.Predict(1, hourAt(10)))
}

func TestEngine_SeasonsFollowLocalTimeThroughDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	e := NewEngine(DefaultParams(), Settings{Threshold: 85, Location: ny})

	// US clocks go forward on 2026-03-08; this runs from six weeks before
	// to two weeks after, through the change, in local office hours.
	start := time.Date(2026, 1, 26, 0, 0, 0, 0, ny)
	end := time.Date(2026, 3, 23, 0, 0, 0, 0, ny)
	for at := start; at.Before(end); at = at.Add(time.Hour) {
		_, err := e.Observe(officeHours(at.In(ny)), at)
		require.NoError(t, err, "observing %s", at)
	}

	monday := end
	assert.InDelta(t, 50, e.Predict(9, monday), 7.5, "local 9am stays busy after the clocks change")
	assert.Less(t, e.Predict(8, monday), 5.0, "local 8am stays quiet")
	assert.Less(t, e.Predict(17, monday), 5.0, "local 5pm stays quiet")
	assert.Equal(t, FullyActive, e.Phase)
}

// TestEngine_LastHourFromAFastClockDoesNotFreezeLearning: one observation
// stamped by a clock a week ahead must not stop the engine learning until
// real time catches up with it.
func TestEngine_LastHourFromAFastClockDoesNotFreezeLearning(t *testing.T) {
	e := newTestEngine()
	run(t, e, 0, 48, func(int, time.Time) float64 { return 100 })
	_, err := e.Observe(100, hourAt(48+WeeklySeason))
	require.NoError(t, err)

	for i := 48; i < 52; i++ {
		require.False(t, e.Observed(hourAt(i)), "hour %d", i)
		_, err := e.Observe(100, hourAt(i))
		require.NoError(t, err, "hour %d", i)
	}
	assert.True(t, e.Observed(hourAt(51)))
	assert.Equal(t, 48+1+4, e.GetDataPoints())
}

// The hour a clock change repeats is observed once, not twice.
func TestEngine_RepeatedHourIsNotAFastClock(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	e := NewEngine(DefaultParams(), Settings{Threshold: 85, Location: ny})
	firstOneAM := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)
	_, err = e.Observe(100, firstOneAM)
	require.NoError(t, err)

	assert.True(t, e.Observed(firstOneAM.Add(time.Hour)), "1am again, after the clocks went back")
}
