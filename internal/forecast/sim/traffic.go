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
	"time"

	"github.com/okedeji/hybernate/internal/forecast"
)

// Use is what a workload's users are doing with it at one moment.
type Use struct {
	// Active means someone is using it: a request reaches it, waking it if
	// it's paused.
	Active bool
	// Percent is its CPU use while it's awake, as a percentage of what its
	// own containers request.
	Percent float64
	// Seen means another activity source, such as an activity annotation a
	// portal stamps, sees the use, as a light-use workload's may be seen
	// only there.
	Seen bool
}

// Traffic is what a workload's users do at each moment. It's a pure function
// of time, so a scenario can be replayed and compared.
type Traffic func(t time.Time) Use

// Quiet is the CPU a workload uses when nobody is using it: its own
// background work, below any sensible activity threshold.
const Quiet = 1.0

// Working is in use from arrive to leave each day it's on, at busy percent
// CPU, in loc's clock.
func Working(loc *time.Location, on func(day time.Time) bool, arrive, leave time.Duration, busy float64) Traffic {
	return func(t time.Time) Use {
		local := t.In(loc)
		day := midnight(local)
		since := local.Sub(day)
		if on(day) && since >= arrive && since < leave {
			return Use{Active: true, Percent: busy}
		}
		return Use{Percent: Quiet}
	}
}

// Weekdays are Monday to Friday.
func Weekdays(day time.Time) bool {
	return day.Weekday() != time.Saturday && day.Weekday() != time.Sunday
}

// EveryDay is every day of the week, weekends included.
func EveryDay(time.Time) bool { return true }

// OfficeHours is the workload Hybernate exists for: in use on weekdays from
// 09:10 to 17:30 in loc, at 40% of its CPU requests, and quiet otherwise.
func OfficeHours(loc *time.Location) Traffic {
	return Working(loc, Weekdays, 9*time.Hour+10*time.Minute, 17*time.Hour+30*time.Minute, 40)
}

// Always is in use all the time, at percent CPU when active, or never in
// use, at Quiet.
func Always(active bool, percent float64) Traffic {
	if !active {
		percent = Quiet
	}
	return func(time.Time) Use { return Use{Active: active, Percent: percent} }
}

// Except is base on every day but the holidays, local dates in loc, when
// nobody uses the workload.
func Except(base Traffic, loc *time.Location, holidays ...time.Time) Traffic {
	off := map[time.Time]bool{}
	for _, h := range holidays {
		off[midnight(h.In(loc))] = true
	}
	return func(t time.Time) Use {
		if off[midnight(t.In(loc))] {
			return Use{Percent: Quiet}
		}
		return base(t)
	}
}

// Switch is before until at, and after from then on: a permanent change in
// how the workload is used, such as a team moving its hours.
func Switch(at time.Time, before, after Traffic) Traffic {
	return func(t time.Time) Use {
		if t.Before(at) {
			return before(t)
		}
		return after(t)
	}
}

// SeenElsewhere marks base's use as seen by another activity source too.
func SeenElsewhere(base Traffic) Traffic {
	return func(t time.Time) Use {
		u := base(t)
		u.Seen = u.Active
		return u
	}
}

// Noisy is office hours as people keep them: each weekday they arrive and
// leave up to jitter either side of 09:10 and 17:30, in loc, one weekday in
// absent of them nobody comes at all, and CPU wanders up to a fifth either
// side of 40%. It's deterministic for a seed.
func Noisy(loc *time.Location, seed uint64, jitter time.Duration, absent int) Traffic {
	return func(t time.Time) Use {
		local := t.In(loc)
		day := midnight(local)
		if !Weekdays(day) {
			return Use{Percent: Quiet}
		}
		key := uint64(day.Unix())
		if absent > 0 && mix(seed, key, 0)%uint64(absent) == 0 {
			return Use{Percent: Quiet}
		}
		arrive := 9*time.Hour + 10*time.Minute + spread(mix(seed, key, 1), jitter)
		leave := 17*time.Hour + 30*time.Minute + spread(mix(seed, key, 2), jitter)
		since := local.Sub(day)
		if since < arrive || since >= leave {
			return Use{Percent: Quiet}
		}
		wobble := 1 + 0.2*(2*unit(mix(seed, uint64(t.Unix()), 3))-1)
		return Use{Active: true, Percent: 40 * wobble}
	}
}

// Peaks is what traffic shows of an always-awake workload hour by hour from
// from to to, in loc's hours: each hour's highest CPU, looked at every
// step, as Prometheus history records a workload that was never paused.
func Peaks(traffic Traffic, from, to time.Time, step time.Duration, loc *time.Location) []forecast.Hour {
	var hours []forecast.Hour
	for start := forecast.HourStart(from, loc); start.Before(to); start = start.Add(time.Hour) {
		h := forecast.Hour{Start: start}
		for t := start; t.Before(start.Add(time.Hour)); t = t.Add(step) {
			h.Peak = max(h.Peak, traffic(t).Percent)
		}
		hours = append(hours, h)
	}
	return hours
}

func midnight(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// mix is splitmix64 over a seed, a key and a stream, so each draw is
// independent and the same every run.
func mix(seed, key, stream uint64) uint64 {
	z := seed ^ key*0x9e3779b97f4a7c15 ^ stream*0xbf58476d1ce4e5b9
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func unit(v uint64) float64 {
	return float64(v>>11) / (1 << 53)
}

// spread is a whole number of minutes from -d to d.
func spread(v uint64, d time.Duration) time.Duration {
	minutes := int64(d / time.Minute)
	if minutes == 0 {
		return 0
	}
	return time.Duration(int64(math.Round(unit(v)*float64(2*minutes)))-minutes) * time.Minute
}
