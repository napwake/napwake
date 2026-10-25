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
	"errors"
	"fmt"
	"time"
)

// Waiting is what a forecast that isn't acting yet needs before it does.
type Waiting string

const (
	// WaitingForDay means it hasn't scored a day of its forecasts yet. It
	// scores its forecast of an hour of the week from the second time it
	// sees that hour, so a new forecast observes through its first week,
	// and one that hasn't seen every hour of the day can't score a day.
	WaitingForDay Waiting = "Day"
	// WaitingForConfidence means its forecasts of the last day aren't yet
	// as right as its threshold asks.
	WaitingForConfidence Waiting = "Confidence"
	// WaitingToRelearn means demand broke from what it had learned, and it
	// discarded the evidence its confidence rested on.
	WaitingToRelearn Waiting = "Relearn"
)

// ProjectionHorizon is how far ahead StartsActing is projected.
const ProjectionHorizon = 4 * 7 * 24 * time.Hour

// Outlook is whether the forecast is acting, and if it isn't, what it's
// waiting for and when it's expected to start.
type Outlook struct {
	Acting bool
	// Waiting is what it needs before it acts; empty while it's acting.
	Waiting Waiting
	// HoursOfDaySeen is how many of the 24 hours of the day it has seen
	// since it started or last relearned.
	HoursOfDaySeen int
	// WeekSeen says it has seen every hour of the week since then.
	WeekSeen bool
	// HoursOfWeekSeen is how many of the 168 hours of the week it has ever
	// seen.
	HoursOfWeekSeen int
	// Confidence is its daily confidence, and Threshold what it acts at.
	Confidence int
	Threshold  int
	// StartsActing is when it's expected to start acting if the coming days
	// repeat the last week it saw; zero while it's acting, or when it isn't
	// expected to within ProjectionHorizon.
	StartsActing time.Time
}

// Outlook says whether the forecast is acting, and if not, why, and when
// it's expected to start: the engine is run on, on a copy, through the
// demand it last saw in each hour of the week, until it would act.
func (e *Engine) Outlook() Outlook {
	o := Outlook{
		Acting:          e.Active(),
		HoursOfDaySeen:  e.hoursOfDayCovered(),
		WeekSeen:        e.coveredWeek(),
		HoursOfWeekSeen: e.recentSeen.count(),
		Confidence:      e.DailyConfidence(),
		Threshold:       e.settings.Threshold,
	}
	if o.Acting {
		return o
	}
	switch {
	case e.relearning():
		o.Waiting = WaitingToRelearn
	case e.Phase == Observing:
		o.Waiting = WaitingForDay
	default:
		o.Waiting = WaitingForConfidence
	}
	o.StartsActing, _ = e.projectStart(ProjectionHorizon)
	return o
}

// relearning reports whether a regime change discarded the forecast's
// scored errors: without one, every observation of an hour of the week seen
// before is scored, up to a week of them.
func (e *Engine) relearning() bool {
	return e.Scorer.count < min(e.Model.n-e.recentSeen.count(), weeklyWindow)
}

// projectStart runs a copy of the engine through the demand it last saw in
// each hour of the week, from the hour after its last observation, and
// returns when the copy would start acting: the end of the hour whose
// observation made it act.
func (e *Engine) projectStart(within time.Duration) (time.Time, bool) {
	if e.lastHour == 0 {
		return time.Time{}, false
	}
	c := e.clone()
	last := time.Unix(e.lastHour, 0)
	for h := 1; h <= int(within/time.Hour); h++ {
		t := e.local(last.Add(time.Duration(h) * time.Hour))
		demand, ok := e.expected(t)
		if !ok {
			continue
		}
		if _, err := c.Observe(demand, t); err != nil {
			if errors.Is(err, ErrAlreadyObserved) {
				continue
			}
			return time.Time{}, false
		}
		if c.Active() {
			return t.Add(time.Hour), true
		}
	}
	return time.Time{}, false
}

// expected is the demand a projection assumes at t: what the same hour of
// the week last showed, or for an hour of the week not yet seen, the same
// hour on the nearest day before it that was. An hour of the day never
// seen on any day isn't assumed at all, as for a workload paused every
// night without the doorman, whose nights stay unseen.
func (e *Engine) expected(t time.Time) (float64, bool) {
	slot := weeklyIndex(t)
	for back := range daysPerWeek {
		s := (slot - back*DailySeason + WeeklySeason) % WeeklySeason
		if e.recentSeen.has(s) {
			return float64(e.recent[s]), true
		}
	}
	return 0, false
}

func (e *Engine) clone() *Engine {
	c := *e
	model, scorer, anomaly := *e.Model, *e.Scorer, *e.Anomaly
	c.Model, c.Scorer, c.Anomaly = &model, &scorer, &anomaly
	return &c
}

// Learn feeds the engine hours of a workload's history, oldest first, as
// live observation would have fed them: an hour's demand is its peak
// utilization by Demand, with cpuThreshold the activity threshold. Hours up
// to the last one observed are skipped: unlike a clock that has gone back,
// history is meant to be older. It returns how many hours it learned.
func (e *Engine) Learn(hours []Hour, cpuThreshold float64) (int, error) {
	learned := 0
	for _, h := range hours {
		if e.lastHour != 0 && hourStart(e.local(h.Start)) <= e.lastHour {
			continue
		}
		if _, err := e.Observe(Demand(h.Peak, cpuThreshold, false), h.Start); err != nil {
			return learned, fmt.Errorf("learning the hour from %s: %w", h.Start.UTC().Format(time.RFC3339), err)
		}
		learned++
	}
	return learned, nil
}
