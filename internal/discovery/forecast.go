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
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/labels"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/doorman"
	"github.com/napwake/napwake/internal/forecast"
	"github.com/napwake/napwake/internal/forecast/sim"
)

// ForecastOptions has a scan replay Napwake's forecast over each
// workload's history.
type ForecastOptions struct {
	// Confidence is the confidence threshold the forecast of a workload
	// Napwake doesn't manage acts at; managed ones use their own.
	Confidence int
	// Location is the operator's --timezone, whose hours and weekdays the
	// forecast learns.
	Location *time.Location
}

// MinForecastHistory is the least history worth replaying the forecast
// over: a day to learn from and a day to replay.
const MinForecastHistory = 2 * 24 * time.Hour

// ForecastReplay is what Napwake's forecast would make of a workload's
// history: whether it would act were the workload opted in now, and how it
// would have done over the last half of the history, had it learned the
// first, waking the workload ahead of demand with autoResume.
type ForecastReplay struct {
	// Threshold is the confidence it acts at.
	Threshold int `json:"threshold"`
	// IfOptedIn is the forecast the operator would start a new
	// ManagedWorkload with, having learned the whole history at once.
	IfOptedIn ForecastOutlook `json:"ifOptedIn"`
	// Live is the operator's own forecast's, for a workload Napwake
	// manages.
	Live *ForecastOutlook `json:"live,omitempty"`
	// Replay is the last half of the history, replayed.
	Replay *ForecastRun `json:"replay,omitempty"`
	// Wakes is what wakes the workload once it's paused, in the replay.
	Wakes ReplayWakes `json:"wakes"`
}

// ReplayWakes is what wakes a paused workload, as it would under Napwake,
// and so what shows its demand while it's paused: a request through the
// doorman, when one of its Services can be routed there, a Prometheus
// activity query, when its ManagedWorkload has one, and people alone, with
// wake.mode Ask, when nothing else may. With none, people wake it by hand,
// and the forecast doesn't learn the hours it's paused.
type ReplayWakes struct {
	Request bool `json:"request"`
	Query   bool `json:"query"`
	People  bool `json:"people"`
	// NoRequest says why a request wouldn't wake it, when one wouldn't.
	NoRequest string `json:"noRequest,omitempty"`
}

// Why a request wouldn't wake a paused workload, in ReplayWakes.NoRequest.
const (
	noRequestAsks      = "the doorman asks before waking it, and only a person's click does"
	noRequestNoService = "no Service routes requests to it"
	noRequestNEGs      = "its Services are behind GKE container-native load balancing (NEGs), which the doorman " +
		"routes only with its forwarder"
	noRequestNodeLocal  = "its Services keep traffic on the caller's node"
	noRequestDoormanOff = "the doorman is turned off for the cluster"
)

// notRouted are the WakeOnRequest reasons with which the operator found it
// can't route a paused workload's requests, and why, for the replay. Other
// reasons pass: an awake workload's NotPaused, and a doorman that's down or
// out of ports for now.
var notRouted = map[string]string{
	"NoServices":              noRequestNoService,
	"DoormanDisabled":         noRequestDoormanOff,
	"UnsupportedLoadBalancer": noRequestNEGs,
	"ServedByOtherPods":       "its Services are served by other workloads' Ready pods",
	"NodeLocalTraffic":        noRequestNodeLocal,
	"UnsupportedIPFamily":     "no doorman pod has an address in its Services' IP family",
	"TooManyPorts":            "it would hold more doorman ports than it may",
}

// optedInAsks is whether the ManagedWorkload opting a workload in would
// have the doorman ask before waking it, by napwake.dev/wake as the opt-in
// controller resolves it: the workload's annotation, then its namespace's,
// then the install's default, where a value that can't be read asks.
func optedInAsks(workload, namespace map[string]string, install Install) bool {
	for _, annotations := range []map[string]string{workload, namespace} {
		v, ok := annotations[v1alpha1.AnnotationWake]
		if !ok {
			continue
		}
		on, ok := v1alpha1.ParseWake(strings.TrimSpace(v))
		return !ok || on == v1alpha1.WakeOnAsk
	}
	return install.Asks
}

// replayWakes is what would wake the workload were it paused: its
// ManagedWorkload mw's, if Napwake manages it, and otherwise what one
// opting it in would, asking before waking it as asks says, under install,
// the Napwake install that would manage it. A doorman that asks wakes it
// only for a person, who can click Wake it only where the doorman routes
// it: elsewhere nothing shows that nobody needed it, as without the doorman.
func replayWakes(mw *v1alpha1.ManagedWorkload, asks bool, podLabels map[string]string,
	services map[string]corev1.Service, install Install) ReplayWakes {
	var w ReplayWakes
	if mw != nil {
		p := mw.Spec.IdlePolicy
		w.Query = p != nil && p.Activity != nil && len(p.Activity.Prometheus) > 0
		asks = mw.Spec.Wake.Asks()
	}
	w.Request, w.NoRequest = doormanRoutes(mw, podLabels, services, install)
	if asks && w.Request {
		w.Request, w.People, w.NoRequest = false, true, noRequestAsks
	}
	return w
}

// doormanRoutes reports whether the doorman would route the paused
// workload's Services, and why not when it wouldn't. An install with its
// doorman off routes nothing. The operator's own verdict on the workload's
// routing stands where it has one, as from its last pause; otherwise it's
// judged from services, its namespace's Services, against its pods'
// labels, and with none read, the doorman is assumed, as Napwake's
// default.
func doormanRoutes(mw *v1alpha1.ManagedWorkload, podLabels map[string]string, services map[string]corev1.Service,
	install Install) (bool, string) {
	if install.DoormanOff {
		return false, noRequestDoormanOff
	}
	if mw != nil {
		if c := meta.FindStatusCondition(mw.Status.Conditions, "WakeOnRequest"); c != nil {
			if c.Status == "True" || c.Reason == "ForwarderNotReady" {
				return true, ""
			}
			if why, ok := notRouted[c.Reason]; ok {
				return false, why
			}
		}
	}
	if services == nil {
		return true, ""
	}
	why := noRequestNoService
	for _, svc := range services {
		switch doormanRoute(svc, podLabels) {
		case routed:
			return true, ""
		case inNEGs:
			if install.NEGForwarder {
				return true, ""
			}
			why = noRequestNEGs
		case nodeLocal:
			if why == noRequestNoService {
				why = noRequestNodeLocal
			}
		}
	}
	return false, why
}

// route is whether the doorman would route a Service to a paused workload.
type route int

const (
	notTheWorkloads route = iota
	routed
	inNEGs
	nodeLocal
)

// doormanRoute is whether the doorman would route svc to the paused
// workload whose pods are labeled podLabels, as the operator decides it: a
// Service with a selector and a cluster IP that selects them on a TCP port
// the doorman serves, and keeps traffic cluster-wide. One in GKE's pod NEGs
// is routed only through the doorman's forwarder, where the install runs it.
func doormanRoute(svc corev1.Service, podLabels map[string]string) route {
	if len(svc.Spec.Selector) == 0 || svc.Spec.ClusterIP == corev1.ClusterIPNone ||
		svc.Spec.Type == corev1.ServiceTypeExternalName ||
		!labels.SelectorFromSet(svc.Spec.Selector).Matches(labels.Set(podLabels)) ||
		len(doorman.RoutablePorts(svc)) == 0 {
		return notTheWorkloads
	}
	switch {
	case doorman.InPodNEGs(svc):
		return inNEGs
	case svc.Spec.InternalTrafficPolicy != nil && *svc.Spec.InternalTrafficPolicy == corev1.ServiceInternalTrafficPolicyLocal:
		return nodeLocal
	}
	return routed
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

// ForecastRun is Napwake managing a workload over part of its history,
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
// threshold its confidence threshold, mw its ManagedWorkload, if Napwake
// manages it, and wakes what would wake it while it's paused.
func replayForecast(r replay, threshold int, mw *v1alpha1.ManagedWorkload, wakes ReplayWakes,
	opts ForecastOptions) *ForecastReplay {
	if r.end.Sub(r.start) < MinForecastHistory || r.step > sim.LookGap || time.Hour%r.step != 0 {
		return nil
	}
	loc := opts.Location
	if loc == nil {
		loc = time.UTC
	}
	rec := r.recording()
	settings := forecast.Settings{Threshold: threshold, Location: loc}
	fr := &ForecastReplay{Threshold: threshold, Wakes: wakes,
		IfOptedIn: outlookOf(learnedEngine(rec.Hours(loc), settings, r.threshold))}
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
			CPUThreshold: r.threshold, IdleAfter: r.idleAfter, AutoResume: true, WakeOnRequest: wakes.Request,
			WakeOnQuery: wakes.Query, WakeOnAsk: wakes.People, Location: loc}})
	if err != nil {
		return fr
	}
	fr.Replay = forecastRun(report)
	return fr
}

func learnedEngine(hours []forecast.Hour, settings forecast.Settings, cpuThreshold float64) *forecast.Engine {
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
// its own when Napwake manages it, otherwise the scan's.
func confidenceFor(mw *v1alpha1.ManagedWorkload, opts ForecastOptions) int {
	if mw != nil {
		return forecast.Threshold(forecast.Mode(mw.Spec.Prediction.Mode), mw.Spec.Prediction.Confidence)
	}
	return forecast.Threshold(forecast.Balanced, opts.Confidence)
}
