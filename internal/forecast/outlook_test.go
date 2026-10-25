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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutlook_NewEngineWaitsForTheDay(t *testing.T) {
	o := NewEngine(DefaultParams(), Settings{Threshold: 75}).Outlook()
	assert.False(t, o.Acting)
	assert.Equal(t, WaitingForDay, o.Waiting)
	assert.Zero(t, o.HoursOfDaySeen)
	assert.True(t, o.StartsActing.IsZero(), "nothing seen, nothing to project")
	assert.Equal(t, 75, o.Threshold)
}

// A few days into a steady pattern, the projection says when the engine
// will start acting, and it's when the engine does.
func TestOutlook_ProjectsWhenItStartsActing(t *testing.T) {
	everyDay := func(_ int, at time.Time) float64 {
		if at.Hour() >= 9 && at.Hour() < 17 {
			return 50
		}
		return 0
	}
	e := NewEngine(DefaultParams(), Settings{Threshold: 75})
	run(t, e, 0, 3*DailySeason, everyDay)
	o := e.Outlook()
	require.False(t, o.Acting)
	assert.Equal(t, WaitingForDay, o.Waiting, "it scores an hour of the week from the second time it sees it")
	assert.Equal(t, DailySeason, o.HoursOfDaySeen)
	assert.Equal(t, 3*DailySeason, o.HoursOfWeekSeen)
	require.False(t, o.StartsActing.IsZero(), "a steady pattern starts acting within the horizon")

	acted := -1
	for i := 3 * DailySeason; i < 6*WeeklySeason && acted < 0; i++ {
		run(t, e, i, 1, everyDay)
		if e.Active() {
			acted = i + 1
		}
	}
	require.GreaterOrEqual(t, acted, 0)
	assert.WithinDuration(t, hourAt(acted), o.StartsActing, 3*time.Hour,
		"projected %s, acted %s", o.StartsActing, hourAt(acted))
	assert.True(t, e.Outlook().StartsActing.IsZero(), "nothing to project once it's acting")
}

func TestOutlook_ActingEngine(t *testing.T) {
	e := NewEngine(DefaultParams(), Settings{Threshold: 75})
	run(t, e, 0, 4*WeeklySeason, office)
	o := e.Outlook()
	assert.True(t, o.Acting)
	assert.Empty(t, o.Waiting)
	assert.True(t, o.WeekSeen)
	assert.GreaterOrEqual(t, o.Confidence, 75)
}

func TestOutlook_RegimeChangeRelearns(t *testing.T) {
	e := NewEngine(DefaultParams(), Settings{Threshold: 75})
	run(t, e, 0, 6*WeeklySeason, office)
	evenings := func(_ int, at time.Time) float64 { return officeHours(at.Add(8 * time.Hour)) }
	log := run(t, e, 6*WeeklySeason, 3*DailySeason, evenings)
	require.NotEmpty(t, log.regimeChanges)
	if e.Active() {
		t.Skip("relearned within the window")
	}
	assert.Equal(t, WaitingToRelearn, e.Outlook().Waiting)
}

// Hours never seen on any day, as a workload paused every night without
// the doorman leaves them, are never assumed: it never sees the whole day,
// so it's never projected to act.
func TestOutlook_UnseenNightsAreNotProjected(t *testing.T) {
	e := NewEngine(DefaultParams(), Settings{Threshold: 50})
	for i := range 3 * WeeklySeason {
		at := hourAt(i)
		if at.Hour() < 9 || at.Hour() >= 17 {
			continue
		}
		_, err := e.Observe(officeHours(at), at)
		require.NoError(t, err)
	}
	o := e.Outlook()
	assert.Equal(t, WaitingForDay, o.Waiting)
	assert.Equal(t, 8, o.HoursOfDaySeen)
	assert.True(t, o.StartsActing.IsZero())
}

// History learned in one go is learned as live observation would have
// learned it: four weeks of office hours start the forecast acting at once.
func TestLearn_HistoryActsAtOnce(t *testing.T) {
	hours := make([]Hour, 0, 4*WeeklySeason)
	for i := range 4 * WeeklySeason {
		hours = append(hours, Hour{Start: hourAt(i), Peak: officeHours(hourAt(i))})
	}
	e := NewEngine(DefaultParams(), Settings{Threshold: 75})
	learned, err := e.Learn(hours, 10)
	require.NoError(t, err)
	assert.Equal(t, 4*WeeklySeason, learned)
	assert.True(t, e.Active())

	again, err := e.Learn(hours, 10)
	require.NoError(t, err)
	assert.Zero(t, again, "hours already learned are skipped")
}

// An hour's peak above the activity threshold is learned as a request's
// demand at least, as live.
func TestLearn_PeakAboveTheThresholdIsUse(t *testing.T) {
	e := NewEngine(DefaultParams(), Settings{Threshold: 75})
	_, err := e.Learn([]Hour{{Start: hourAt(0), Peak: 12}}, 10)
	require.NoError(t, err)
	assert.InDelta(t, 20, e.Model.level, 1e-9)
}
