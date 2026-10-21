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
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// record samples traffic every five minutes, as Prometheus history does,
// leaving out the steps the workload wasn't running.
func record(traffic Traffic, from, to time.Time, running func(time.Time) bool) []Sample {
	var samples []Sample
	for t := from; t.Before(to); t = t.Add(5 * time.Minute) {
		if running == nil || running(t) {
			samples = append(samples, Sample{At: t, Percent: traffic(t).Percent})
		}
	}
	return samples
}

func TestNewRecorded(t *testing.T) {
	samples := []Sample{
		{At: monday.Add(10 * time.Minute), Percent: 3},
		{At: monday, Percent: 1},
		{At: monday.Add(5 * time.Minute), Percent: 2},
		{At: monday.Add(25 * time.Minute), Percent: 6},
	}
	r, err := NewRecorded(samples)
	require.NoError(t, err)
	assert.Equal(t, 5*time.Minute, r.Step)
	assert.True(t, monday.Equal(r.Start))
	require.Len(t, r.Percent, 6)
	assert.Equal(t, []float64{1, 2, 3}, r.Percent[:3])
	assert.True(t, math.IsNaN(r.Percent[3]) && math.IsNaN(r.Percent[4]), "steps without a sample didn't run")
	assert.True(t, monday.Add(30*time.Minute).Equal(r.End()))

	_, err = NewRecorded(samples[:1])
	require.Error(t, err)
	_, err = NewRecorded([]Sample{{At: monday}, {At: monday}})
	require.Error(t, err)
}

func TestRecorded_TrafficAndHours(t *testing.T) {
	r, err := NewRecorded([]Sample{
		{At: monday.Add(time.Hour), Percent: 4},
		{At: monday.Add(time.Hour + 5*time.Minute), Percent: 30},
		{At: monday.Add(3*time.Hour + 55*time.Minute), Percent: 2},
	})
	require.NoError(t, err)

	tr := r.Traffic(10)
	assert.Equal(t, Use{Percent: 4}, tr(monday.Add(time.Hour+2*time.Minute)))
	assert.Equal(t, Use{Active: true, Percent: 30}, tr(monday.Add(time.Hour+5*time.Minute)))
	assert.Equal(t, Use{}, tr(monday.Add(2*time.Hour)), "not running is quiet")

	hours := r.Hours(time.UTC)
	require.Len(t, hours, 3, "from the first hour it ran")
	assert.InDelta(t, 30, hours[0].Peak, 1e-9)
	assert.Zero(t, hours[1].Peak, "an hour without its pods is quiet")
	assert.InDelta(t, 2, hours[2].Peak, 1e-9)
}

// Replaying recorded office hours is replaying the office hours themselves:
// after a few weeks the workload is ready every morning, though the
// recording, five minutes a step, sees people arrive at 09:10 only to the
// step.
func TestRecorded_ReplaysOfficeHours(t *testing.T) {
	r, err := NewRecorded(record(OfficeHours(time.UTC), monday, monday.Add(6*week), nil))
	require.NoError(t, err)
	report, err := r.Replay("recorded", DefaultSettings())
	require.NoError(t, err)
	s := report.Summarize(monday.Add(4 * week))
	assert.Equal(t, 10, s.UsedDays)
	assert.Equal(t, 10, s.Ready)
	assert.Equal(t, 25*time.Minute, s.MedianLead)
}

func TestRecorded_ReplayNeedsASteadyStep(t *testing.T) {
	samples := make([]Sample, 0, 10)
	for i := range 10 {
		samples = append(samples, Sample{At: monday.Add(time.Duration(i) * 15 * time.Minute)})
	}
	r, err := NewRecorded(samples)
	require.NoError(t, err)
	_, err = r.Replay("coarse", DefaultSettings())
	require.Error(t, err)
}
