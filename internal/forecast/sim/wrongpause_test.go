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

package sim

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ordersAPI is orders/api on the pre-launch test cluster under its load
// generator: a request every few seconds from 08:00 to 17:00 UTC on
// weekdays, at 1.5% of its CPU requests, well under the 10% threshold, so
// the idle clock never sees it; idleAfter 5m; a wake taking 80 seconds to
// Running; and its start-up below the threshold.
func ordersAPI(backOff bool) Scenario {
	settings := DefaultSettings()
	settings.IdleAfter = 5 * time.Minute
	settings.WakeTakes = 80 * time.Second
	settings.BackOff = backOff
	return Scenario{
		Name:     "orders/api",
		Start:    monday,
		Duration: 6 * week,
		Step:     15 * time.Second,
		Traffic:  Working(time.UTC, Weekdays, 8*time.Hour, 17*time.Hour, 1.5),
		Settings: settings,
		Startup:  Startup{Percent: 0.5, For: time.Minute},
	}
}

// coldStarts is how many requests found the workload paused in each
// weekday's load hours, from 08:00 to 17:00, and when it last paused each
// weekday evening.
func coldStarts(r Report) (perDay []int, lastPause []time.Time) {
	for d := monday; d.Before(r.End); d = d.Add(day) {
		if !Weekdays(d) {
			continue
		}
		from, to := d.Add(8*time.Hour), d.Add(17*time.Hour)
		n := 0
		for _, w := range r.Wakes {
			if w.Cause == WokenByRequest && w.At.After(from) && w.At.Before(to) {
				n++
			}
		}
		perDay = append(perDay, n)
		var last time.Time
		for _, p := range r.Pauses {
			if p.After(d) && p.Before(d.Add(day)) {
				last = p
			}
		}
		lastPause = append(lastPause, last)
	}
	return perDay, lastPause
}

func mean(ns []int) float64 {
	sum := 0
	for _, n := range ns {
		sum += n
	}
	return float64(sum) / float64(len(ns))
}

// Without seeing its requests, Napwake paused orders/api about 80 times
// each working day, woken by the next request each time (P29). Waiting
// twice as long after each pause a request undoes soon after, up to an
// hour more, cuts that to about eight a day: one an hour, once the wait is
// at its cap. Each evening it pauses at most an hour and five minutes after
// the last request, and nights and weekends are as they were.
func TestOrdersAPI_TheBackOffCutsColdStarts(t *testing.T) {
	today := simulate(t, ordersAPI(false))
	backedOff := simulate(t, ordersAPI(true))

	before, _ := coldStarts(today)
	after, evenings := coldStarts(backedOff)
	t.Logf("cold starts a working day, weeks 1-6: %.1f today, %.1f with the back-off; first week %.1f and %.1f",
		mean(before), mean(after), mean(before[:5]), mean(after[:5]))
	t.Logf("awake a week, last week: %s today, %s with the back-off",
		today.Awake(monday.Add(5*week), monday.Add(6*week)).Round(time.Minute),
		backedOff.Awake(monday.Add(5*week), monday.Add(6*week)).Round(time.Minute))

	assert.GreaterOrEqual(t, mean(before[:5]), 75.0, "about 80 a day today, as on GKE")
	assert.LessOrEqual(t, mean(after), 12.0, "a cold start an hour at most, once the wait is at its cap")
	assert.Less(t, mean(after), mean(before)/6)
	for i, last := range evenings {
		require.False(t, last.IsZero(), "weekday %d pauses", i)
		assert.True(t, last.Before(last.Truncate(day).Add(17*time.Hour+65*time.Minute+2*time.Minute)),
			"weekday %d pauses by an hour and five minutes after the last request, at %s", i, last)
	}
	weekendAwake := func(r Report) time.Duration {
		return r.Awake(monday.Add(5*week+5*day), monday.Add(6*week))
	}
	assert.Equal(t, weekendAwake(today), weekendAwake(backedOff), "the weekend sleeps as it did")
}

// The back-off only ever delays a pause, so a workload nobody uses pauses
// exactly when it did, and one used in office hours, which the clock sees,
// wakes and pauses as before.
func TestBackOff_ChangesNothingTheClockSees(t *testing.T) {
	for _, traffic := range []Traffic{Always(false, 0), OfficeHours(time.UTC)} {
		without := simulate(t, Scenario{Duration: 3 * week, Traffic: traffic})
		with := Scenario{Duration: 3 * week, Traffic: traffic, Settings: DefaultSettings()}
		with.Settings.BackOff = true
		assert.Equal(t, without.Pauses, simulate(t, with).Pauses)
	}
}
