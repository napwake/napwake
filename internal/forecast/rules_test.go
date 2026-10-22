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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var ruleTime = time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)

// stubPredictor forecasts the hours ahead in byHour, and has no forecast
// for the others.
type stubPredictor struct {
	phase  Phase
	byHour map[int]float64
}

func (s stubPredictor) GetPhase() Phase { return s.phase }
func (s stubPredictor) Expect(h int, _ time.Time) (float64, bool) {
	v, ok := s.byHour[h]
	return v, ok
}

func TestHourStart(t *testing.T) {
	kolkata, err := time.LoadLocation("Asia/Kolkata")
	require.NoError(t, err)
	newYork, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	tests := []struct {
		name string
		loc  *time.Location
		at   time.Time
		want time.Time
	}{
		{name: "UTC", at: ruleTime.Add(10 * time.Minute), want: ruleTime},
		{name: "hours at half past in UTC", loc: kolkata, at: ruleTime.Add(10 * time.Minute),
			want: ruleTime.Add(-30 * time.Minute)},
		{name: "the first 1am of a clock change", loc: newYork, at: time.Date(2026, 11, 1, 5, 40, 0, 0, time.UTC),
			want: time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)},
		{name: "the repeated 1am", loc: newYork, at: time.Date(2026, 11, 1, 6, 40, 0, 0, time.UTC),
			want: time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HourStart(tt.at, tt.loc)
			assert.True(t, tt.want.Equal(got), "got %s", got.UTC())
		})
	}
}

// Every look in an hour must find the same hour start, so one taken from
// the real clock carries no monotonic reading: Go compares two times that
// both carry one by that reading alone, and two from time.Now() differ by
// the nanoseconds between its reads of the wall and monotonic clocks.
func TestHourStart_CarriesNoMonotonicReading(t *testing.T) {
	lagos, err := time.LoadLocation("Africa/Lagos")
	require.NoError(t, err)
	kolkata, err := time.LoadLocation("Asia/Kolkata")
	require.NoError(t, err)
	for _, loc := range []*time.Location{nil, time.UTC, lagos, kolkata} {
		start := HourStart(time.Now(), loc)
		assert.Equal(t, start.Round(0), start, "in %v: %s", loc, start)
	}
}

func TestHoldPause(t *testing.T) {
	tests := []struct {
		name          string
		predictor     stubPredictor
		wantHold      bool
		wantPredicted float64
		wantHour      time.Time
	}{
		{name: "busy this hour", predictor: stubPredictor{phase: DailyActive, byHour: map[int]float64{0: 30, 1: 5}},
			wantHold: true, wantPredicted: 30, wantHour: ruleTime},
		{name: "busy next hour", predictor: stubPredictor{phase: FullyActive, byHour: map[int]float64{0: 2, 1: 40}},
			wantHold: true, wantPredicted: 40, wantHour: ruleTime.Add(time.Hour)},
		{name: "at the threshold", predictor: stubPredictor{phase: DailyActive, byHour: map[int]float64{1: 10}},
			wantHold: true, wantPredicted: 10, wantHour: ruleTime.Add(time.Hour)},
		{name: "quiet", predictor: stubPredictor{phase: DailyActive, byHour: map[int]float64{0: 9, 1: 3}},
			wantPredicted: 9, wantHour: ruleTime},
		{name: "not yet confident", predictor: stubPredictor{phase: DailySuggesting, byHour: map[int]float64{0: 90}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hold, predicted, hour := HoldPause(tt.predictor, ruleTime.Add(20*time.Minute), nil, 10)
			assert.Equal(t, tt.wantHold, hold)
			assert.InDelta(t, tt.wantPredicted, predicted, 1e-9)
			assert.True(t, tt.wantHour.Equal(hour), "hour %s", hour)
		})
	}
}

func TestWakeAhead(t *testing.T) {
	busyNext := stubPredictor{phase: DailyActive, byHour: map[int]float64{0: 1, 1: 50}}
	tests := []struct {
		name         string
		predictor    stubPredictor
		at           time.Duration
		wantWake     bool
		wantNextHour bool
	}{
		{name: "the next hour, before the lead", predictor: busyNext, at: 44 * time.Minute},
		{name: "the next hour, at the lead", predictor: busyNext, at: 45 * time.Minute,
			wantWake: true, wantNextHour: true},
		{name: "this hour", predictor: stubPredictor{phase: FullyActive, byHour: map[int]float64{0: 20}},
			at: 10 * time.Minute, wantWake: true},
		{name: "under the threshold", predictor: stubPredictor{phase: DailyActive, byHour: map[int]float64{0: 9, 1: 9}},
			at: 50 * time.Minute},
		{name: "not yet confident", predictor: stubPredictor{phase: Observing, byHour: map[int]float64{0: 90, 1: 90}},
			at: 50 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wake, _, nextHour := WakeAhead(tt.predictor, ruleTime.Add(tt.at), nil, 10)
			assert.Equal(t, tt.wantWake, wake)
			assert.Equal(t, tt.wantNextHour, nextHour)
		})
	}
}

// The forecast brings a pause forward only once the workload has been idle
// for EarlyPauseIdle, and only while it's acting and sure every hour the
// workload would otherwise be awake for, and at least the one under way and
// the next, is quiet: under the activity threshold, the line the veto and
// autoResume draw.
func TestPauseEarly(t *testing.T) {
	quiet := func(hours int) map[int]float64 {
		byHour := map[int]float64{}
		for h := range hours {
			byHour[h] = 9
		}
		return byHour
	}
	now := ruleTime.Add(20 * time.Minute)
	tests := []struct {
		name      string
		predictor stubPredictor
		idle      time.Duration
		idleAfter time.Duration
		wantUntil time.Duration
	}{
		{name: "quiet this hour and the next", predictor: stubPredictor{phase: DailyActive, byHour: quiet(2)},
			idle: 20 * time.Minute, idleAfter: time.Hour, wantUntil: 2 * time.Hour},
		{name: "idle for less than the floor", predictor: stubPredictor{phase: DailyActive, byHour: quiet(2)},
			idle: 19 * time.Minute, idleAfter: time.Hour},
		{name: "not yet confident", predictor: stubPredictor{phase: DailySuggesting, byHour: quiet(2)},
			idle: 30 * time.Minute, idleAfter: time.Hour},
		{name: "demand at the threshold this hour", predictor: stubPredictor{phase: DailyActive,
			byHour: map[int]float64{0: 10, 1: 0}}, idle: 30 * time.Minute, idleAfter: time.Hour},
		{name: "demand the next hour", predictor: stubPredictor{phase: FullyActive,
			byHour: map[int]float64{0: 0, 1: 40}}, idle: 30 * time.Minute, idleAfter: time.Hour},
		{name: "no forecast for the next hour", predictor: stubPredictor{phase: DailyActive,
			byHour: map[int]float64{0: 0}}, idle: 30 * time.Minute, idleAfter: time.Hour},
		{name: "the clock has run out", predictor: stubPredictor{phase: DailyActive, byHour: quiet(2)},
			idle: time.Hour, idleAfter: time.Hour},
		{name: "quiet until the hour the clock would pause in", predictor: stubPredictor{phase: DailyActive,
			byHour: quiet(4)}, idle: 30 * time.Minute, idleAfter: 3*time.Hour + 30*time.Minute, wantUntil: 4 * time.Hour},
		{name: "demand before the clock would pause", predictor: stubPredictor{phase: DailyActive,
			byHour: map[int]float64{0: 0, 1: 0, 2: 0, 3: 40}}, idle: 30 * time.Minute,
			idleAfter: 3*time.Hour + 30*time.Minute},
		{name: "demand after the clock would have paused it", predictor: stubPredictor{phase: DailyActive,
			byHour: map[int]float64{0: 0, 1: 0, 2: 40}}, idle: 30 * time.Minute, idleAfter: time.Hour,
			wantUntil: 2 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			last := now.Add(-tt.idle)
			early, from, until := PauseEarly(tt.predictor, now, last, last.Add(tt.idleAfter), nil, 10)
			assert.Equal(t, tt.wantUntil > 0, early)
			if tt.wantUntil > 0 {
				assert.Equal(t, ruleTime, from)
				assert.Equal(t, ruleTime.Add(tt.wantUntil), until)
			}
		})
	}
}

// What PauseEarly brings forward, the veto doesn't hold off and autoResume
// doesn't wake straight back from: it's judged by the same rules, over
// every hour they look at.
func TestPauseEarly_AgreesWithTheVetoAndAutoResume(t *testing.T) {
	now := ruleTime.Add(50 * time.Minute)
	for _, byHour := range []map[int]float64{
		{0: 9.9, 1: 9.9}, {0: 10, 1: 0}, {0: 0, 1: 10}, {0: 0, 1: 0}, {0: 30, 1: 30},
	} {
		p := stubPredictor{phase: DailyActive, byHour: byHour}
		early, _, _ := PauseEarly(p, now, now.Add(-30*time.Minute), now.Add(30*time.Minute), nil, 10)
		hold, _, _ := HoldPause(p, now, nil, 10)
		wake, _, _ := WakeAhead(p, now, nil, 10)
		if early {
			assert.False(t, wake, "%v", byHour)
		}
		assert.Equal(t, !hold, early, "%v: the forecast either holds the pause off or may bring it forward", byHour)
	}
}

// CPU counts as use above the activity threshold, not at it.
func TestBusy(t *testing.T) {
	tests := []struct {
		percent float64
		want    bool
	}{
		{percent: 9.99, want: false},
		{percent: 10, want: false},
		{percent: 10.01, want: true},
		{percent: 64, want: true},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, Busy(tt.percent, 10), "%g%% against a threshold of 10%%", tt.percent)
	}
}

func TestAwakeDemand(t *testing.T) {
	never := func(time.Time) bool { return false }
	tests := []struct {
		name      string
		at        time.Duration
		wokeAt    time.Time
		percent   float64
		usedSince func(time.Time) bool
		want      float64
	}{
		{name: "quiet", at: 30 * time.Minute, percent: 4, usedSince: never, want: 4},
		{name: "busy", at: 30 * time.Minute, percent: 60, usedSince: never, want: 60},
		{name: "busy at a little over the threshold counts as a request does", at: 30 * time.Minute, percent: 12,
			usedSince: never, want: 20},
		{name: "light use another source saw", at: 30 * time.Minute, percent: 3,
			usedSince: func(time.Time) bool { return true }, want: 20},
		{name: "warming up after a wake", at: 30 * time.Minute, wokeAt: ruleTime.Add(20 * time.Minute), percent: 80,
			usedSince: never, want: 0},
		{name: "warmed up", at: 40 * time.Minute, wokeAt: ruleTime.Add(20 * time.Minute), percent: 80,
			usedSince: never, want: 80},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.InDelta(t, tt.want, AwakeDemand(ruleTime.Add(tt.at), tt.wokeAt, nil, tt.percent, 10, tt.usedSince), 1e-9)
		})
	}
}

// Use seen by another source counts from the later of the hour's start and
// the end of the warm-up, so use while starting up isn't demand.
func TestAwakeDemand_UseCountsFromTheWarmUp(t *testing.T) {
	var asked []time.Time
	usedSince := func(since time.Time) bool {
		asked = append(asked, since)
		return false
	}
	AwakeDemand(ruleTime.Add(50*time.Minute), ruleTime.Add(20*time.Minute), nil, 1, 10, usedSince)
	AwakeDemand(ruleTime.Add(50*time.Minute), ruleTime.Add(-30*time.Minute), nil, 1, 10, usedSince)
	assert.Equal(t, []time.Time{ruleTime.Add(35 * time.Minute), ruleTime}, asked)
}

func TestHour(t *testing.T) {
	const gap = 7 * time.Minute
	look := func(h *Hour, at time.Duration, demand float64, visible bool) (Hour, bool) {
		now := ruleTime.Add(at)
		return h.Look(HourStart(now, nil), now, demand, visible)
	}

	t.Run("seen whole, its peak is recorded once it ends", func(t *testing.T) {
		var h Hour
		for m := time.Duration(0); m < time.Hour; m += 5 * time.Minute {
			_, over := look(&h, m, float64(m/time.Minute), true)
			require.False(t, over)
		}
		ended, over := look(&h, time.Hour, 0, true)
		require.True(t, over)
		assert.True(t, ended.Whole(gap))
		assert.InDelta(t, 55, ended.Peak, 1e-9)
		assert.True(t, ruleTime.Equal(ended.Start))
		assert.True(t, ruleTime.Add(time.Hour).Equal(h.Start), "the next hour is under way")
	})

	tests := []struct {
		name    string
		looks   []time.Duration
		blindAt time.Duration
	}{
		{name: "first seen partway through", looks: []time.Duration{20 * time.Minute, 40 * time.Minute, 55 * time.Minute}},
		{name: "last seen well before its end", looks: []time.Duration{0, 10 * time.Minute, 40 * time.Minute}},
		{name: "blind to demand for a look", looks: []time.Duration{0, 30 * time.Minute, 55 * time.Minute},
			blindAt: 30 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h Hour
			for _, at := range tt.looks {
				look(&h, at, 50, at != tt.blindAt || tt.blindAt == 0)
			}
			ended, over := look(&h, time.Hour, 0, true)
			require.True(t, over)
			assert.False(t, ended.Whole(gap))
		})
	}
}
