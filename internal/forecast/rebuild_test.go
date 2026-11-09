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

package forecast

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// historyBefore is four weeks of office hours up to the hour takenOn falls
// in, and what Prometheus shows after it, while Napwake paused the
// workload: nothing, up to until.
func historyBefore(takenOn, until time.Time) []Hour {
	var hours []Hour
	start := HourStart(takenOn, nil)
	for t := start.Add(-4 * WeeklySeason * time.Hour); t.Before(until); t = t.Add(time.Hour) {
		peak := 0.0
		if t.Before(start) {
			peak = officeHours(t)
		}
		hours = append(hours, Hour{Start: t, Peak: peak})
	}
	return hours
}

// liveFrom is an engine that has learned live from takenOn for hours
// hours: office hours, 20% where a request woke it, with the hours its
// gaps left unseen skipped, and the hours it observed, as it observed them.
func liveFrom(t *testing.T, takenOn time.Time, hours int) (*Engine, []Hour) {
	t.Helper()
	e := NewEngine(DefaultParams(), Settings{Threshold: 75})
	var seen []Hour
	for t0, i := HourStart(takenOn, nil).Add(time.Hour), 0; i < hours; t0, i = t0.Add(time.Hour), i+1 {
		if i%11 == 5 {
			continue
		}
		demand := officeHours(t0)
		if t0.Hour() == 9 && demand > 0 {
			demand = 20
		}
		_, err := e.Observe(demand, t0)
		require.NoError(t, err)
		seen = append(seen, Hour{Start: t0, Peak: demand})
	}
	return e, seen
}

// Rebuilt beneath what it learned live, a forecast is the one that learned
// the history before the hour it was taken on in first, and then the same
// hours live, in their order, at the demand each showed it, requests
// included: the same state, to the last value.
func TestRebuild_IsTheHistoryThenTheHoursLearnedLive(t *testing.T) {
	takenOn := testEpoch.Add(3*24*time.Hour + 10*time.Hour + 20*time.Minute)
	live, seen := liveFrom(t, takenOn, 3*DailySeason)
	history := historyBefore(takenOn, takenOn)

	rebuilt, learned, err := Rebuild(history, live, takenOn, 10)
	require.NoError(t, err)

	want := NewEngine(DefaultParams(), Settings{Threshold: 75})
	require.True(t, history[len(history)-1].Start.Equal(HourStart(takenOn, nil)), "the history has the hour of take-on")
	_, err = want.Learn(history[:len(history)-1], 10)
	require.NoError(t, err)
	for _, h := range seen {
		_, err := want.Observe(h.Peak, h.Start)
		require.NoError(t, err)
	}
	assert.Equal(t, 4*WeeklySeason, learned)
	got, err := rebuilt.Export()
	require.NoError(t, err)
	wanted, err := want.Export()
	require.NoError(t, err)
	assert.Equal(t, wanted, got)
	assert.True(t, rebuilt.Active(), "four weeks of office hours, and it acts")
	assert.False(t, live.Active(), "where three days live don't")
}

// The hours Prometheus has from the hour Napwake took the workload on in,
// in which it may have paused it, never reach the forecast, wherever in the
// week and the hour it was taken on: only the four weeks before do, then
// the hours it learned live.
func TestRebuild_NoHourFromTakeOnIsLearned(t *testing.T) {
	for offset := range WeeklySeason {
		takenOn := testEpoch.Add(4*WeeklySeason*time.Hour + time.Duration(offset)*time.Hour +
			time.Duration(offset%60)*time.Minute)
		t.Run(fmt.Sprintf("taken on %s", takenOn.Format("Mon 15:04")), func(t *testing.T) {
			live, seen := liveFrom(t, takenOn, 2*DailySeason)
			rebuilt, learned, err := Rebuild(historyBefore(takenOn, takenOn.Add(2*24*time.Hour)), live, takenOn, 10)
			require.NoError(t, err)

			assert.Equal(t, 4*WeeklySeason, learned, "only the four weeks before the hour it was taken on in")
			assert.Equal(t, 4*WeeklySeason+len(seen), rebuilt.GetDataPoints())
			for _, h := range seen {
				slot := weeklyIndex(h.Start)
				assert.InDelta(t, h.Peak, float64(rebuilt.recent[slot]), 1e-9,
					"the hour from %s is what live learned", h.Start)
			}
		})
	}
}

// A forecast that has learned live for a week, or that saw an hour before
// it was taken on, is refused: what it keeps of each hour of the week no
// longer holds every hour it learned, so they can't be replayed.
func TestRebuild_RefusesAWeekLearnedLive(t *testing.T) {
	takenOn := testEpoch.Add(4 * WeeklySeason * time.Hour)
	history := historyBefore(takenOn, takenOn)
	for _, tt := range []struct {
		name  string
		hours int
		since time.Time
		ok    bool
	}{
		{name: "a day short of a week", hours: WeeklySeason - DailySeason, since: takenOn, ok: true},
		{name: "to the last hour of its first week", hours: WeeklySeason - 1, since: takenOn, ok: true},
		{name: "a week", hours: WeeklySeason, since: takenOn},
		{name: "two weeks", hours: 2 * WeeklySeason, since: takenOn},
		{name: "an hour seen before it was taken on", hours: DailySeason, since: takenOn.Add(2 * time.Hour)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			live, _ := liveFrom(t, takenOn, tt.hours)
			_, _, err := Rebuild(history, live, tt.since, 10)
			if tt.ok {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, ErrLearnedLiveTooLong)
		})
	}
}

// A forecast that has learned nothing live yet is the history alone.
func TestRebuild_NothingLearnedLive(t *testing.T) {
	takenOn := testEpoch.Add(4 * WeeklySeason * time.Hour)
	rebuilt, learned, err := Rebuild(historyBefore(takenOn, takenOn), NewEngine(DefaultParams(), Settings{}),
		takenOn, 10)
	require.NoError(t, err)
	assert.Equal(t, 4*WeeklySeason, learned)
	assert.Equal(t, 4*WeeklySeason, rebuilt.GetDataPoints())
}
