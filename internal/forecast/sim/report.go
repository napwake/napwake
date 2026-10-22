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
	"slices"
	"time"

	"github.com/okedeji/hybernate/internal/forecast"
)

// Reached is how long after the start the forecast first reached phase, or
// a later one.
func (r Report) Reached(phase forecast.Phase) (time.Duration, bool) {
	for _, c := range r.Phases {
		if c.Phase >= phase {
			return c.At.Sub(r.Start), true
		}
	}
	return 0, false
}

// ActingAt reports whether the forecast was driving decisions at t.
func (r Report) ActingAt(t time.Time) bool {
	acting := false
	for _, c := range r.Phases {
		if c.At.After(t) {
			break
		}
		acting = c.Phase >= forecast.DailyActive
	}
	return acting
}

// Awake is how long the workload was awake from from to to: from the
// start, until it was first paused, and from each wake until the pause
// after it.
func (r Report) Awake(from, to time.Time) time.Duration {
	type change struct {
		at    time.Time
		awake bool
	}
	changes := make([]change, 0, len(r.Pauses)+len(r.Wakes)+1)
	changes = append(changes, change{at: r.Start, awake: true})
	for _, p := range r.Pauses {
		changes = append(changes, change{at: p})
	}
	for _, w := range r.Wakes {
		changes = append(changes, change{at: w.At, awake: true})
	}
	slices.SortStableFunc(changes, func(a, b change) int { return a.at.Compare(b.at) })
	var awake time.Duration
	for i, c := range changes {
		end := r.End
		if i+1 < len(changes) {
			end = changes[i+1].at
		}
		if !c.awake {
			continue
		}
		if start, stop := later(c.at, from), earlier(end, to); stop.After(start) {
			awake += stop.Sub(start)
		}
	}
	return awake
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// Summary counts what a simulation measured over the days from From.
type Summary struct {
	From time.Time
	// UsedDays had someone use the workload, and Ready, Awake and Cold are
	// how their first use found it.
	UsedDays int
	Ready    int
	Awake    int
	Cold     int
	// Missed are the Cold days the forecast was acting on, with autoResume
	// on: mornings it should have had the workload ready.
	Missed int
	// MedianLead is how long before the first use autoResume typically woke
	// the workload, on Ready days.
	MedianLead time.Duration
	// ForecastWakes are autoResume's wakes; WastedWakes those nobody used
	// before the workload was paused again, and IdleDayWakes those of them
	// on days nobody used it at all, such as weekends and holidays.
	ForecastWakes int
	WastedWakes   int
	IdleDayWakes  int
	// WastedHours is how long the wasted wakes kept the workload awake.
	WastedHours float64
	// Vetoes are pauses the forecast held off, and UnusedVetoes those
	// nobody used the workload during; VetoHours is how long they lasted.
	Vetoes       int
	UnusedVetoes int
	VetoHours    float64
	// EarlyPauses are pauses the forecast brought forward; ColdEarly those
	// a use found paused when the workload would otherwise have been awake,
	// and SavedHours how long they had it paused that it would otherwise
	// have been awake for.
	EarlyPauses int
	ColdEarly   int
	SavedHours  float64
}

// Summarize counts the report's days, wakes and vetoes from from on.
func (r Report) Summarize(from time.Time) Summary {
	sum := Summary{From: from}
	var leads []time.Duration
	used := map[time.Time]bool{}
	for _, d := range r.Days {
		if d.Date.Before(midnight(from.In(d.Date.Location()))) || d.Arrival.IsZero() {
			continue
		}
		used[d.Date] = true
		sum.UsedDays++
		switch d.Outcome {
		case Ready:
			sum.Ready++
			leads = append(leads, d.Lead)
		case Awake:
			sum.Awake++
		case Cold:
			sum.Cold++
			if d.Acting && r.Settings.AutoResume {
				sum.Missed++
			}
		}
	}
	if len(leads) > 0 {
		slices.Sort(leads)
		sum.MedianLead = leads[len(leads)/2]
	}
	for _, w := range r.Wakes {
		if w.Cause != WokenByForecast || w.At.Before(from) {
			continue
		}
		sum.ForecastWakes++
		if !w.Wasted {
			continue
		}
		sum.WastedWakes++
		sum.WastedHours += w.PausedAt.Sub(w.At).Hours()
		if !used[midnight(w.At.In(r.loc()))] {
			sum.IdleDayWakes++
		}
	}
	for _, v := range r.Vetoes {
		if v.From.Before(from) {
			continue
		}
		sum.Vetoes++
		sum.VetoHours += v.To.Sub(v.From).Hours()
		if !v.Used {
			sum.UnusedVetoes++
		}
	}
	for _, p := range r.EarlyPauses {
		if p.At.Before(from) {
			continue
		}
		sum.EarlyPauses++
		sum.SavedHours += p.Saved(r.End).Hours()
		if p.Cold {
			sum.ColdEarly++
		}
	}
	return sum
}

func (r Report) loc() *time.Location {
	if r.Settings.Location == nil {
		return time.UTC
	}
	return r.Settings.Location
}
