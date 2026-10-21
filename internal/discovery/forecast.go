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

package discovery

import (
	"math"
	"time"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
	"github.com/okedeji/hybernate/internal/forecast"
	"github.com/okedeji/hybernate/internal/forecast/sim"
)

// ForecastOptions has a scan replay Hybernate's forecast over each
// workload's history.
type ForecastOptions struct {
	// Confidence is the confidence threshold the forecast of a workload
	// Hybernate doesn't manage acts at; managed ones use their own.
	Confidence int
	// Location is the operator's --timezone, whose hours and weekdays the
	// forecast learns.
	Location *time.Location
}

// minForecastHistory is the least history worth replaying the forecast
// over: a day to learn from and a day to replay.
const minForecastHistory = 2 * 24 * time.Hour

// ForecastReplay is what Hybernate's forecast would make of a workload's
// history: whether it would act were the workload opted in now, and how it
// would have done over the last half of the history, had it learned the
// first, waking the workload ahead of demand with autoResume.
type ForecastReplay struct {
	// Threshold is the confidence it acts at.
	Threshold int `json:"threshold"`
	// IfOptedIn is the forecast the operator would start a new
	// ManagedWorkload with, having learned the whole history at once.
	IfOptedIn ForecastOutlook `json:"ifOptedIn"`
	// Live is the operator's own forecast's, for a workload Hybernate
	// manages.
	Live *ForecastOutlook `json:"live,omitempty"`
	// Replay is the last half of the history, replayed.
	Replay *ForecastRun `json:"replay,omitempty"`
}

// ForecastOutlook is whether a forecast acts, and if not, what it waits
// for, as in status.prediction.
type ForecastOutlook struct {
	Acting bool `json:"acting"`
	// Confidence is its forecasts' accuracy over the last day, as a
	// percentage.
	Confidence int `json:"confidence"`
	// Waiting is what it needs before it acts: Day, Confidence or Relearn;
	// or for a managed workload, the reason in its status.
	Waiting string `json:"waiting,omitempty"`
	// StartsActing is when it's expected to start acting, if the coming days
	// repeat the last week.
	StartsActing *time.Time `json:"startsActing,omitempty"`
	// Message is a managed workload's status.prediction.message.
	Message string `json:"message,omitempty"`
}

// ForecastRun is Hybernate managing a workload over part of its history,
// with autoResume.
type ForecastRun struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	// UsedDays had someone use the workload, CPU above its threshold, and
	// Ready, Awake and Cold are how its first use each day found it: woken
	// ahead by the forecast, awake anyway, or paused, so the use woke it.
	UsedDays int `json:"usedDays"`
	Ready    int `json:"ready"`
	Awake    int `json:"awake"`
	Cold     int `json:"cold"`
	// Missed are Cold days the forecast was acting on.
	Missed int `json:"missed"`
	// WastedWakes are wakes ahead of demand that didn't come, and
	// WastedHours how long they kept the workload awake.
	WastedWakes int     `json:"wastedWakes"`
	WastedHours float64 `json:"wastedHours"`
	// MedianLead is how long before the first use the forecast typically
	// woke the workload, on Ready days.
	MedianLead time.Duration `json:"medianLead"`
	Days       []ForecastDay `json:"days"`
}

// ForecastDay is one day of a ForecastRun, in the operator's clock.
type ForecastDay struct {
	Date     time.Time  `json:"date"`
	FirstUse *time.Time `json:"firstUse,omitempty"`
	// Found is how the first use found the workload: ready, awake or cold.
	Found string `json:"found,omitempty"`
	// WokeAt is when the forecast woke it ahead of that use.
	WokeAt *time.Time `json:"wokeAt,omitempty"`
	// Acting says the forecast was driving decisions then.
	Acting      bool `json:"acting"`
	WastedWakes int  `json:"wastedWakes,omitempty"`
}

// recording is the replay's history as utilization at each step, from its
// start, with the steps none of the workload's pods ran in left out.
func (r replay) recording() sim.Recorded {
	rec := sim.Recorded{Start: r.start, Step: r.step}
	for t := r.start; !t.After(r.end); t = t.Add(r.step) {
		pods := r.pods[t.Unix()]
		percent := math.NaN()
		if pods > 0 && r.requestCores > 0 {
			percent = r.usedCores[t.Unix()] / (float64(pods) * r.requestCores) * 100
		}
		rec.Percent = append(rec.Percent, percent)
	}
	return rec
}

// replayForecast replays the forecast over the workload's history, with
// threshold its confidence threshold and mw its ManagedWorkload, if
// Hybernate manages it.
func replayForecast(r replay, threshold int, mw *v1alpha1.ManagedWorkload, opts ForecastOptions) *ForecastReplay {
	if r.end.Sub(r.start) < minForecastHistory || r.step > sim.LookGap || time.Hour%r.step != 0 {
		return nil
	}
	loc := opts.Location
	if loc == nil {
		loc = time.UTC
	}
	rec := r.recording()
	settings := forecast.Settings{Threshold: threshold, Location: loc}
	fr := &ForecastReplay{Threshold: threshold, IfOptedIn: outlookOf(learnedEngine(rec.Hours(loc), settings, r.threshold))}
	if mw != nil {
		fr.Live = liveOutlook(mw)
	}

	half := forecast.HourStart(r.start.Add(r.end.Sub(r.start)/2), loc)
	var first []forecast.Hour
	for _, h := range rec.Hours(loc) {
		if h.Start.Before(half) {
			first = append(first, h)
		}
	}
	state, err := learnedEngine(first, settings, r.threshold).Export()
	if err != nil {
		return fr
	}
	report, err := sim.Run(sim.Scenario{Start: half, Duration: r.end.Sub(half), Step: r.step,
		Traffic: rec.Traffic(r.threshold), State: state, Settings: sim.Settings{Confidence: threshold,
			CPUThreshold: r.threshold, IdleAfter: r.idleAfter, AutoResume: true, WakeOnRequest: true, Location: loc}})
	if err != nil {
		return fr
	}
	fr.Replay = forecastRun(report)
	return fr
}

func learnedEngine(hours []forecast.Hour, settings forecast.Settings, cpuThreshold int) *forecast.Engine {
	e := forecast.NewEngine(forecast.DefaultParams(), settings)
	// History parsed from Prometheus is finite and never negative, which is
	// all an observation is checked for.
	_, _ = e.Learn(hours, cpuThreshold)
	return e
}

func outlookOf(e *forecast.Engine) ForecastOutlook {
	o := e.Outlook()
	out := ForecastOutlook{Acting: o.Acting, Confidence: o.Confidence, Waiting: string(o.Waiting)}
	if !o.StartsActing.IsZero() {
		at := o.StartsActing.UTC()
		out.StartsActing = &at
	}
	return out
}

func liveOutlook(mw *v1alpha1.ManagedWorkload) *ForecastOutlook {
	p := mw.Status.Prediction
	if p == nil {
		return nil
	}
	out := &ForecastOutlook{Acting: p.Acting, Confidence: p.DailyConfidence, Waiting: p.Reason, Message: p.Message}
	if p.StartsActing != nil {
		at := p.StartsActing.UTC()
		out.StartsActing = &at
	}
	return out
}

func forecastRun(report sim.Report) *ForecastRun {
	s := report.Summarize(report.Start)
	run := &ForecastRun{From: report.Start.UTC(), To: report.End.UTC(), UsedDays: s.UsedDays, Ready: s.Ready,
		Awake: s.Awake, Cold: s.Cold, Missed: s.Missed, WastedWakes: s.WastedWakes,
		WastedHours: math.Round(s.WastedHours*10) / 10, MedianLead: s.MedianLead}
	for _, d := range report.Days {
		day := ForecastDay{Date: d.Date, Found: string(d.Outcome), Acting: d.Acting, WastedWakes: d.WastedWakes}
		if !d.Arrival.IsZero() {
			first := d.Arrival.UTC()
			day.FirstUse = &first
		}
		if d.Outcome == sim.Ready {
			woke := d.Arrival.Add(-d.Lead).UTC()
			day.WokeAt = &woke
		}
		run.Days = append(run.Days, day)
	}
	return run
}

// confidenceFor is the confidence threshold a workload's forecast acts at:
// its own when Hybernate manages it, otherwise the scan's.
func confidenceFor(mw *v1alpha1.ManagedWorkload, opts ForecastOptions) int {
	if mw != nil {
		return forecast.Threshold(forecast.Mode(mw.Spec.Prediction.Mode), mw.Spec.Prediction.Confidence)
	}
	return forecast.Threshold(forecast.Balanced, opts.Confidence)
}
