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

// Package sim runs the forecast engine and the operator's decision rules
// through simulated time: the activity clock pausing an idle workload, the
// forecast holding off a pause and autoResume waking ahead of demand, the
// doorman, an activity query or people waking it, and each hour's peak fed
// to the forecast, as the operator does for a ManagedWorkload, a look at a
// time. Months of traffic take milliseconds, so what the forecast does with
// a pattern, and how long it takes to start, can be measured rather than
// guessed.
package sim

import (
	"errors"
	"fmt"
	"time"

	"github.com/okedeji/hybernate/internal/forecast"
)

// LookGap is how far from an hour's start and end the looks at it may begin
// and end for it to be seen whole: the operator's unobservedGap, two missed
// activity checks and a status flush.
const LookGap = 7 * time.Minute

// Settings are a ManagedWorkload's, and the operator's, as they bear on the
// forecast.
type Settings struct {
	// Confidence is the confidence threshold the forecast acts at.
	Confidence int
	// CPUThreshold is the CPU use, as a percentage of requests, above which
	// the workload is active, and the demand the decision rules act on.
	CPUThreshold int
	IdleAfter    time.Duration
	AutoResume   bool
	// PauseEarly lets a confident forecast that expects quiet hours pause
	// the workload before IdleAfter runs out, when WakeOnRequest has a
	// request wake it.
	PauseEarly bool
	// WakeOnRequest routes the workload's Services to the doorman while it's
	// paused, so a request wakes it and an hour paused is seen as quiet.
	WakeOnRequest bool
	// WakeOnQuery gives the workload a Prometheus activity query that sees
	// its use, as a queue's length shows a worker's: awake, the query keeps
	// it awake, and paused, it wakes it, and an hour paused is seen as
	// quiet.
	WakeOnQuery bool
	// WakeOnAsk has only people wake the workload while it's paused, as
	// wake.mode Ask does: the doorman shows a browser an asleep page, and a
	// use wakes it as the person clicks Wake it, or asks with a wake
	// request. Nothing else can wake it, so an hour paused that nobody woke
	// it in is seen as quiet. It's instead of WakeOnRequest.
	WakeOnAsk bool
	// Location is the operator's --timezone, the clock the forecast's hours
	// and weekdays are counted in.
	Location *time.Location
}

// DefaultSettings are a ManagedWorkload's defaults, with autoResume on.
func DefaultSettings() Settings {
	return Settings{Confidence: 75, CPUThreshold: 10, IdleAfter: time.Hour, AutoResume: true, WakeOnRequest: true,
		Location: time.UTC}
}

// Restart is the operator stopping at At and starting again Down later, as
// for an upgrade or a change of leader. What the forecast has learned is
// restored from the state it last saved; the hour under way is lost.
type Restart struct {
	At   time.Time
	Down time.Duration
}

// Startup is the CPU a workload uses for a while after it wakes, before
// anyone has used it.
type Startup struct {
	Percent float64
	For     time.Duration
}

// Scenario is one simulation: a workload's traffic, and how Hybernate is set
// up for it.
type Scenario struct {
	Name     string
	Start    time.Time
	Duration time.Duration
	// Step is how often the workload is looked at, every minute by default,
	// as the activity clock looks at an awake workload. It must divide an
	// hour and be at most LookGap.
	Step     time.Duration
	Traffic  Traffic
	Settings Settings
	Restarts []Restart
	Startup  Startup
	// HandWake is how long people take to wake a paused workload by hand,
	// without the doorman, once they find it down.
	HandWake time.Duration
	// State is a forecast to start from, as exported, instead of a new one.
	State string
}

// WakeCause is what woke a paused workload.
type WakeCause string

const (
	WokenByForecast WakeCause = "autoResume"
	WokenByRequest  WakeCause = "request"
	WokenByQuery    WakeCause = "query"
	WokenByPeople   WakeCause = "people"
	WokenByHand     WakeCause = "hand"
)

// Wake is one wake of a paused workload.
type Wake struct {
	At    time.Time
	Cause WakeCause
	// Wasted means nobody used the workload before it was paused again: a
	// wake ahead of demand that didn't come.
	Wasted bool
	// PausedAt is when it was paused again; zero if it was still awake when
	// the simulation ended.
	PausedAt time.Time
}

// Outcome is how the first use of a day found the workload.
type Outcome string

const (
	// Ready means autoResume had woken it ahead of the use.
	Ready Outcome = "ready"
	// Awake means it was awake for another reason, such as never having
	// been paused since the day before.
	Awake Outcome = "awake"
	// Cold means it was paused, so the use waited for it to start.
	Cold Outcome = "cold"
)

// Day is one day of the simulation, in the operator's clock.
type Day struct {
	Date time.Time
	// Arrival is the first use of the day; zero for a day nobody used it.
	Arrival time.Time
	Outcome Outcome `json:",omitempty"`
	// Lead is how long before Arrival autoResume woke the workload, for a
	// Ready day.
	Lead time.Duration
	// Acting means the forecast was driving decisions at the day's first
	// use, or at its end for a day nobody used it.
	Acting bool
	// WastedWakes are autoResume wakes that day that nobody used before the
	// workload was paused again.
	WastedWakes int
}

// PhaseChange is the forecast reaching a phase, as the operator reports it.
type PhaseChange struct {
	At    time.Time
	Phase forecast.Phase
}

// EarlyPause is a pause the forecast brought forward, before the activity
// clock would have paused the workload.
type EarlyPause struct {
	At time.Time
	// ClockAt is when the activity clock would have paused it.
	ClockAt time.Time
	// QuietUntil is when the hours the forecast expected to be quiet end.
	QuietUntil time.Time
	// WokeAt is when it next woke, and Cold says a request, or people by
	// hand, woke it before ClockAt: a use that found it paused when it
	// would otherwise have been awake. Zero if it was still paused when
	// the simulation ended.
	WokeAt time.Time
	Cold   bool
}

// Saved is how long the early pause had the workload paused that it would
// otherwise have been awake for, up to end, when the simulation ended.
func (p EarlyPause) Saved(end time.Time) time.Duration {
	until := p.ClockAt
	if !p.WokeAt.IsZero() && p.WokeAt.Before(until) {
		until = p.WokeAt
	}
	if end.Before(until) {
		until = end
	}
	return max(until.Sub(p.At), 0)
}

// Veto is a stretch the forecast held off a pause the activity clock had
// decided.
type Veto struct {
	From, To time.Time
	// Used means someone used the workload during it, so the pause it held
	// off would have been woken straight back.
	Used bool
}

// Report is what a simulation measured.
type Report struct {
	Name          string
	Start, End    time.Time
	Settings      Settings
	Phases        []PhaseChange
	RegimeChanges []time.Time
	Days          []Day
	Wakes         []Wake
	Pauses        []time.Time
	// EarlyPauses are the pauses, among Pauses, that the forecast brought
	// forward.
	EarlyPauses []EarlyPause
	Vetoes      []Veto
	// Hours is how many hours the forecast was fed.
	Hours int
	// AwakeFor and PausedFor sum the time the workload spent each way.
	AwakeFor, PausedFor time.Duration
	// State is what the forecast had learned when the simulation ended.
	State string
}

// Run simulates sc.
func Run(sc Scenario) (Report, error) {
	s, err := newRun(sc)
	if err != nil {
		return Report{}, err
	}
	end := sc.Start.Add(sc.Duration)
	restarts := sc.Restarts
	for t := sc.Start; t.Before(end); t = t.Add(s.step) {
		for len(restarts) > 0 && !restarts[0].At.After(t) {
			if err := s.restart(restarts[0]); err != nil {
				return Report{}, err
			}
			restarts = restarts[1:]
		}
		if err := s.look(t); err != nil {
			return Report{}, err
		}
	}
	return s.finish(end)
}

type run struct {
	sc     Scenario
	step   time.Duration
	engine *forecast.Engine
	set    forecast.Settings
	hour   forecast.Hour
	report Report

	awake bool
	// wokeAt is the last wake; zero before any.
	wokeAt time.Time
	// lastActivity is the activity clock's last activity, and lastWasUse
	// whether that was use, rather than the clock restarting.
	lastActivity time.Time
	lastWasUse   bool
	// downUntil is when the operator is back from a restart.
	downUntil time.Time
	restarted bool
	// wantedFrom is when people first found the workload paused, without
	// the doorman to wake it.
	wantedFrom time.Time
	phase      forecast.Phase
	veto       *Veto
	day        *Day
	// usedSinceWake says someone used the workload since it last woke.
	usedSinceWake bool
}

func newRun(sc Scenario) (*run, error) {
	step := sc.Step
	if step == 0 {
		step = time.Minute
	}
	if step > LookGap || time.Hour%step != 0 {
		return nil, fmt.Errorf("step %s must divide an hour and be at most %s", step, LookGap)
	}
	if sc.Traffic == nil {
		return nil, errors.New("a scenario needs traffic")
	}
	if sc.Settings.WakeOnRequest && sc.Settings.WakeOnAsk {
		return nil, errors.New("a workload wakes on any request, or asks people first, not both")
	}
	set := forecast.Settings{Threshold: sc.Settings.Confidence, Location: sc.Settings.Location}
	engine := forecast.NewEngine(forecast.DefaultParams(), set)
	if sc.State != "" {
		e, err := forecast.ImportEngine(sc.State, set)
		if err != nil {
			return nil, fmt.Errorf("restoring the scenario's forecast: %w", err)
		}
		engine = e
	}
	s := &run{sc: sc, step: step, engine: engine, set: set, awake: true, lastActivity: sc.Start,
		phase: engine.GetPhase()}
	s.report = Report{Name: sc.Name, Start: sc.Start, Settings: sc.Settings}
	if sc.State != "" {
		s.report.Phases = append(s.report.Phases, PhaseChange{At: sc.Start, Phase: s.phase})
	}
	return s, nil
}

func (s *run) loc() *time.Location {
	if s.sc.Settings.Location == nil {
		return time.UTC
	}
	return s.sc.Settings.Location
}

// restart stops the operator: what it remembers in memory is lost, and the
// forecast is taken up from the state it last saved, as from the
// ManagedWorkload's status.
func (s *run) restart(r Restart) error {
	state, err := s.engine.Export()
	if err != nil {
		return fmt.Errorf("saving the forecast at %s: %w", r.At, err)
	}
	engine, err := forecast.ImportEngine(state, s.set)
	if err != nil {
		return fmt.Errorf("restoring the forecast at %s: %w", r.At, err)
	}
	s.engine = engine
	s.hour = forecast.Hour{}
	s.downUntil = r.At.Add(r.Down)
	s.restarted = true
	return nil
}

func (s *run) look(t time.Time) error {
	use := s.sc.Traffic(t)
	s.account(t, use)
	if t.Before(s.downUntil) {
		return nil
	}
	if s.restarted {
		s.restarted = false
		// Without the clock in memory, the gap since its last written
		// evaluation is longer than a restarted operator trusts, so the
		// clock restarts.
		s.lastActivity, s.lastWasUse = t, false
	}
	if s.awake {
		return s.lookAwake(t, use)
	}
	return s.lookPaused(t, use)
}

// account keeps the days and the time spent awake and paused, whether or
// not the operator is up to look.
func (s *run) account(t time.Time, use Use) {
	if s.awake {
		s.report.AwakeFor += s.step
	} else {
		s.report.PausedFor += s.step
	}
	date := midnight(t.In(s.loc()))
	if s.day == nil || !s.day.Date.Equal(date) {
		s.closeDay()
		s.report.Days = append(s.report.Days, Day{Date: date})
		s.day = &s.report.Days[len(s.report.Days)-1]
	}
	if !use.Active {
		return
	}
	if s.awake {
		s.usedSinceWake = true
		if s.veto != nil {
			s.veto.Used = true
		}
	}
	if !s.day.Arrival.IsZero() {
		return
	}
	s.day.Arrival = t
	s.day.Acting = s.engine.GetPhase() >= forecast.DailyActive
	switch {
	case !s.awake:
		s.day.Outcome = Cold
	case s.lastWakeCause() == WokenByForecast:
		s.day.Outcome, s.day.Lead = Ready, t.Sub(s.wokeAt)
	default:
		s.day.Outcome = Awake
	}
}

func (s *run) closeDay() {
	if s.day != nil && s.day.Arrival.IsZero() {
		s.day.Acting = s.engine.GetPhase() >= forecast.DailyActive
	}
}

func (s *run) lastWakeCause() WakeCause {
	if len(s.report.Wakes) == 0 {
		return ""
	}
	return s.report.Wakes[len(s.report.Wakes)-1].Cause
}

func (s *run) lookAwake(t time.Time, use Use) error {
	set := s.sc.Settings
	percent := use.Percent
	if !s.wokeAt.IsZero() && t.Sub(s.wokeAt) < s.sc.Startup.For {
		percent = max(percent, s.sc.Startup.Percent)
	}
	demand := forecast.AwakeDemand(t, s.wokeAt, set.Location, percent, set.CPUThreshold, func(since time.Time) bool {
		return s.lastWasUse && !s.lastActivity.Before(since)
	})
	if err := s.see(t, demand, true); err != nil {
		return err
	}
	if forecast.Busy(percent, set.CPUThreshold) || use.Seen || use.Active && set.WakeOnQuery {
		s.lastActivity, s.lastWasUse = t, true
	}

	if clockAt := s.lastActivity.Add(set.IdleAfter); t.Before(clockAt) {
		s.endVeto(t)
		if !set.PauseEarly || !set.WakeOnRequest {
			return nil
		}
		early, _, until := forecast.PauseEarly(s.engine, t, s.lastActivity, clockAt, set.Location, set.CPUThreshold)
		if early {
			s.pause(t)
			s.report.EarlyPauses = append(s.report.EarlyPauses, EarlyPause{At: t, ClockAt: clockAt, QuietUntil: until})
		}
		return nil
	}
	if hold, _, _ := forecast.HoldPause(s.engine, t, set.Location, set.CPUThreshold); hold {
		if s.veto == nil {
			s.report.Vetoes = append(s.report.Vetoes, Veto{From: t})
			s.veto = &s.report.Vetoes[len(s.report.Vetoes)-1]
		}
		s.veto.To = t
		return nil
	}
	s.endVeto(t)
	s.pause(t)
	return nil
}

func (s *run) endVeto(t time.Time) {
	if s.veto != nil {
		s.veto.To = t
		s.veto = nil
	}
}

func (s *run) pause(t time.Time) {
	s.awake = false
	s.report.Pauses = append(s.report.Pauses, t)
	if n := len(s.report.Wakes); n > 0 && s.report.Wakes[n-1].PausedAt.IsZero() {
		w := &s.report.Wakes[n-1]
		w.PausedAt = t
		w.Wasted = !s.usedSinceWake
		if w.Wasted && w.Cause == WokenByForecast {
			s.dayOf(w.At).WastedWakes++
		}
	}
	s.wantedFrom = time.Time{}
}

func (s *run) dayOf(t time.Time) *Day {
	date := midnight(t.In(s.loc()))
	for i := len(s.report.Days) - 1; i >= 0; i-- {
		if s.report.Days[i].Date.Equal(date) {
			return &s.report.Days[i]
		}
	}
	return s.day
}

func (s *run) lookPaused(t time.Time, use Use) error {
	set := s.sc.Settings
	cause := WakeCause("")
	switch {
	case use.Active && set.WakeOnRequest:
		cause = WokenByRequest
	case use.Active && set.WakeOnAsk:
		cause = WokenByPeople
	case use.Active && set.WakeOnQuery:
		cause = WokenByQuery
	case use.Active:
		if s.wantedFrom.IsZero() {
			s.wantedFrom = t
		}
		if t.Sub(s.wantedFrom) >= s.sc.HandWake {
			cause = WokenByHand
		}
	default:
		s.wantedFrom = time.Time{}
	}
	var demand float64
	if cause != "" {
		demand = forecast.RequestDemand(set.CPUThreshold)
	}
	if err := s.see(t, demand, set.WakeOnRequest || set.WakeOnQuery || set.WakeOnAsk || cause != ""); err != nil {
		return err
	}
	if cause != "" {
		s.wake(t, cause)
		return nil
	}
	if !set.AutoResume {
		return nil
	}
	if wake, _, _ := forecast.WakeAhead(s.engine, t, set.Location, set.CPUThreshold); wake {
		s.wake(t, WokenByForecast)
	}
	return nil
}

func (s *run) wake(t time.Time, cause WakeCause) {
	if n := len(s.report.EarlyPauses); n > 0 && s.report.EarlyPauses[n-1].WokeAt.IsZero() {
		p := &s.report.EarlyPauses[n-1]
		p.WokeAt = t
		p.Cold = cause != WokenByForecast && t.Before(p.ClockAt)
	}
	s.awake = true
	s.wokeAt = t
	s.lastActivity, s.lastWasUse = t, false
	s.usedSinceWake = cause != WokenByForecast
	s.report.Wakes = append(s.report.Wakes, Wake{At: t, Cause: cause})
}

// see adds a look to the hour under way and feeds the forecast an hour once
// it's over, as the operator does.
func (s *run) see(t time.Time, demand float64, visible bool) error {
	ended, over := s.hour.Look(forecast.HourStart(t, s.sc.Settings.Location), t, demand, visible)
	if !over || !ended.Whole(LookGap) || s.engine.Observed(ended.Start) {
		return nil
	}
	if _, err := s.engine.Observe(ended.Peak, ended.Start); err != nil {
		return fmt.Errorf("feeding the forecast the hour from %s: %w", ended.Start, err)
	}
	s.report.Hours++
	if s.engine.RegimeChanged() {
		s.report.RegimeChanges = append(s.report.RegimeChanges, t)
	}
	if phase := s.engine.GetPhase(); phase != s.phase {
		s.phase = phase
		s.report.Phases = append(s.report.Phases, PhaseChange{At: t, Phase: phase})
	}
	return nil
}

func (s *run) finish(end time.Time) (Report, error) {
	s.closeDay()
	s.endVeto(end)
	state, err := s.engine.Export()
	if err != nil {
		return Report{}, fmt.Errorf("saving the forecast: %w", err)
	}
	s.report.End = end
	s.report.State = state
	return s.report, nil
}
