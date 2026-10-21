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
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/okedeji/hybernate/internal/forecast"
)

// Recorded is a workload's recorded CPU: its utilization at each step from
// Start, as a percentage of what its own containers request, or NaN where
// none of its pods was running.
type Recorded struct {
	Start   time.Time
	Step    time.Duration
	Percent []float64
}

// Sample is one recorded utilization.
type Sample struct {
	At      time.Time
	Percent float64
}

// NewRecorded lays samples out a step apart, from the first. The step is the
// most common gap between samples; a step with no sample wasn't running.
func NewRecorded(samples []Sample) (Recorded, error) {
	if len(samples) < 2 {
		return Recorded{}, errors.New("a recording needs at least two samples")
	}
	sorted := slices.Clone(samples)
	slices.SortFunc(sorted, func(a, b Sample) int { return a.At.Compare(b.At) })
	gaps := map[time.Duration]int{}
	for i := 1; i < len(sorted); i++ {
		gaps[sorted[i].At.Sub(sorted[i-1].At)]++
	}
	var step time.Duration
	most := 0
	for gap, n := range gaps {
		if gap > 0 && (n > most || n == most && gap < step) {
			step, most = gap, n
		}
	}
	if step <= 0 {
		return Recorded{}, errors.New("the samples have no step between them")
	}
	r := Recorded{Start: sorted[0].At, Step: step}
	r.Percent = make([]float64, int(sorted[len(sorted)-1].At.Sub(r.Start)/step)+1)
	for i := range r.Percent {
		r.Percent[i] = math.NaN()
	}
	for _, s := range sorted {
		if i := int(s.At.Sub(r.Start) / step); s.At.Sub(r.Start)%step == 0 {
			r.Percent[i] = s.Percent
		}
	}
	return r, nil
}

// End is the end of the recording's last step.
func (r Recorded) End() time.Time {
	return r.Start.Add(time.Duration(len(r.Percent)) * r.Step)
}

// at is the utilization recorded for the step containing t, and whether the
// workload was running then.
func (r Recorded) at(t time.Time) (float64, bool) {
	if t.Before(r.Start) {
		return 0, false
	}
	i := int(t.Sub(r.Start) / r.Step)
	if i >= len(r.Percent) || math.IsNaN(r.Percent[i]) {
		return 0, false
	}
	return r.Percent[i], true
}

// Traffic replays the recording as traffic: the workload was in use
// whenever its CPU was above cpuThreshold, the activity clock's measure,
// and quiet whenever none of its pods ran.
func (r Recorded) Traffic(cpuThreshold int) Traffic {
	return func(t time.Time) Use {
		percent, running := r.at(t)
		if !running {
			return Use{}
		}
		return Use{Active: percent > float64(cpuThreshold), Percent: percent}
	}
}

// Hours is the recording hour by hour, in loc's hours, as the forecast
// learns history: each hour's peak, from the first hour the workload ran,
// with hours none of its pods ran in as quiet.
func (r Recorded) Hours(loc *time.Location) []forecast.Hour {
	first := -1
	for i, p := range r.Percent {
		if !math.IsNaN(p) {
			first = i
			break
		}
	}
	if first < 0 {
		return nil
	}
	from := r.Start.Add(time.Duration(first) * r.Step)
	var hours []forecast.Hour
	for start := forecast.HourStart(from, loc); start.Before(r.End()); start = start.Add(time.Hour) {
		h := forecast.Hour{Start: start}
		for t := start; t.Before(start.Add(time.Hour)); t = t.Add(r.Step) {
			if p, ok := r.at(t); ok {
				h.Peak = max(h.Peak, p)
			}
		}
		hours = append(hours, h)
	}
	return hours
}

// Replay simulates Hybernate managing the recorded workload with settings,
// over the whole recording: what it would have paused, woken and
// forecast.
func (r Recorded) Replay(name string, settings Settings) (Report, error) {
	if r.Step > LookGap || time.Hour%r.Step != 0 {
		return Report{}, fmt.Errorf("a recording %s a step apart can't be replayed: the step must divide an hour "+
			"and be at most %s", r.Step, LookGap)
	}
	return Run(Scenario{Name: name, Start: r.Start, Duration: r.End().Sub(r.Start), Step: r.Step,
		Traffic: r.Traffic(settings.CPUThreshold), Settings: settings})
}
