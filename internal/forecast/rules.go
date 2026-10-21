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

import "time"

// AutoResumeLead is how far ahead of an hour the forecast expects to be busy
// autoResume wakes a paused workload, so it's Ready when people arrive
// rather than starting as they do.
const AutoResumeLead = 15 * time.Minute

// ResumeWarmup is how long after a wake an awake workload's usage isn't
// counted as demand. An autoResume wake lands at most AutoResumeLead before
// the hour it's for, so that long covers what such a wake brings about.
const ResumeWarmup = AutoResumeLead

// HourStart is when the forecast's hour containing t began, counted in loc
// (UTC when nil), where an hour may begin at half or quarter past the hour
// in UTC. Stepping back to the local hour's start, rather than building it
// with time.Date, stays exact in the hour a clock change repeats.
func HourStart(t time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	local := t.In(loc)
	into := time.Duration(local.Minute())*time.Minute +
		time.Duration(local.Second())*time.Second +
		time.Duration(local.Nanosecond())
	return t.Add(-into)
}

// Predictor is what the decision rules read of a forecast.
type Predictor interface {
	GetPhase() Phase
	Predict(h int, now time.Time) float64
}

// HoldPause reports whether a confident forecast expects demand at or
// above threshold, the activity threshold, in the hour under way or the
// next. It covers every hour WakeAhead would wake the workload for, so a
// workload is never paused only to be woken straight back. It returns the
// busier of the two hours' forecasts, and when that hour begins.
func HoldPause(p Predictor, now time.Time, loc *time.Location, threshold int) (hold bool, predicted float64,
	hour time.Time) {
	if p.GetPhase() < DailyActive {
		return false, 0, time.Time{}
	}
	hour = HourStart(now, loc)
	predicted = p.Predict(0, now)
	if next := p.Predict(1, now); next > predicted {
		predicted, hour = next, hour.Add(time.Hour)
	}
	return predicted >= float64(threshold), predicted, hour
}

// WakeAhead reports whether autoResume wakes a paused workload at now: a
// confident forecast expects demand at or above threshold in the hour under
// way, or, within AutoResumeLead of the next hour, in that one. It returns
// the forecast it acted on, and whether that was the next hour's.
func WakeAhead(p Predictor, now time.Time, loc *time.Location, threshold int) (wake bool, predicted float64,
	nextHour bool) {
	if p.GetPhase() < DailyActive {
		return false, 0, false
	}
	predicted = p.Predict(0, now)
	if HourStart(now, loc).Add(time.Hour).Sub(now) <= AutoResumeLead {
		if next := p.Predict(1, now); next > predicted {
			predicted, nextHour = next, true
		}
	}
	return predicted >= float64(threshold), predicted, nextHour
}

// RequestDemand is the demand a request that wakes a paused workload shows,
// however little the workload then uses: twice the activity threshold. The
// forecast approaches a pattern it is learning from below, so an hour seen
// at the threshold itself would never quite be forecast to reach it.
func RequestDemand(threshold int) float64 {
	return 2 * float64(threshold)
}

// AwakeDemand is the demand one look at an awake workload shows, at
// utilization percent of its requests. Starting up, filling caches and
// compiling hot paths isn't anyone asking for it, so for ResumeWarmup after
// it woke at wokeAt it shows none. Counted, an autoResume wake would teach
// the forecast that the hour it woke in is busy, and a week later it would
// wake an hour earlier, and earlier again the week after. A wake by request
// shows its demand anyway, in RequestDemand.
//
// Once warmed up, a workload seen in use counts as at least RequestDemand,
// by its utilization above threshold or by usedSince, which reports whether
// another activity source saw it in use since the hour began or it warmed
// up, whichever is later. A light-use workload's use may show only in
// another source; taught its own light utilization on the days autoResume
// had it ready, and a request's demand on the days people woke it, the
// forecast would have it ready only every other morning.
func AwakeDemand(now, wokeAt time.Time, loc *time.Location, percent float64, threshold int,
	usedSince func(since time.Time) bool) float64 {
	since := HourStart(now, loc)
	if !wokeAt.IsZero() {
		warm := wokeAt.Add(ResumeWarmup)
		if now.Before(warm) {
			return 0
		}
		if warm.After(since) {
			since = warm
		}
	}
	return Demand(percent, threshold, usedSince(since))
}

// Busy reports whether a workload at utilization percent of its CPU
// requests is in use by its CPU: above threshold, the activity threshold.
// The activity clock, the forecast and the scan's replay of the clock all
// count use this way, so the scan predicts what the operator does.
func Busy(percent float64, threshold int) bool {
	return percent > float64(threshold)
}

// Demand is the demand a workload at utilization percent shows: at least
// RequestDemand once it's Busy, or once inUse says another activity source
// saw it in use, and percent otherwise.
func Demand(percent float64, threshold int, inUse bool) float64 {
	if Busy(percent, threshold) || inUse {
		return max(percent, RequestDemand(threshold))
	}
	return percent
}

// Hour is what has been seen of a workload's demand in one hour of the
// forecast's clock.
type Hour struct {
	Start time.Time
	// Peak is the highest demand any one look found. The peak, not the
	// mean, is what the forecast's decisions need: the activity clock counts
	// a workload as active for any minute above the threshold, so an hour
	// busy for its last ten minutes is an hour it must be awake for.
	Peak        float64
	first, last time.Time
	// blind is set by a look that couldn't see demand: the workload was
	// paused where a request wouldn't wake it.
	blind bool
}

// Look adds a look at demand made at now, in the hour that began at start,
// to h. visible is false for a look that couldn't see demand. When the look
// is the first since the hour h held ended, h starts afresh and the hour
// that ended is returned.
func (h *Hour) Look(start, now time.Time, demand float64, visible bool) (ended Hour, over bool) {
	prev := *h
	if prev.Start.IsZero() || !prev.Start.Equal(start) {
		*h = Hour{Start: start, first: now}
	}
	h.last = now
	h.Peak = max(h.Peak, demand)
	h.blind = h.blind || !visible
	return prev, !prev.Start.IsZero() && prev.Start.Before(start)
}

// Whole reports whether the hour was seen from its start to its end, with
// no look that was blind to demand. Looks that began more than gap after
// it started, or ended more than gap before it ended, may have missed its
// busiest minutes, as from an operator that started partway through it,
// and the forecast would learn a busy hour as a quiet one. A gap within the
// hour isn't held against it: a slow wake or pause leaves one, and the
// looks either side of it show the demand around it.
func (h Hour) Whole(gap time.Duration) bool {
	return !h.blind && h.first.Sub(h.Start) <= gap && h.Start.Add(time.Hour).Sub(h.last) <= gap
}
