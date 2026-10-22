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

package main

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
	"github.com/okedeji/hybernate/internal/discovery"
	"github.com/okedeji/hybernate/internal/forecast"
)

// historyLearned is status.prediction.history's reason when the forecast
// learned the workload's CPU history from Prometheus.
const historyLearned = "Learned"

// busyAhead is how far ahead forecast NAME shows the hours the operator's
// forecast expects to be busy: a day, which covers every hour it holds a
// pause off for or wakes the workload ahead of by the next time anyone
// looks.
const busyAhead = 24

// operatorForecast is what the operator's own forecast of a workload it
// manages is doing: its ManagedWorkload's status.prediction, and what the
// forecast's state saved there says it has learned and expects.
type operatorForecast struct {
	ManagedWorkload string `json:"managedWorkload"`
	// Pending says why the operator hasn't recorded a forecast yet, and
	// what to check; the rest is empty while it is.
	Pending string `json:"pending,omitempty"`
	Acting  bool   `json:"acting"`
	// Reason and Message say why it is or isn't acting, and what it waits
	// for, as the operator wrote them.
	Reason           string     `json:"reason,omitempty"`
	Message          string     `json:"message,omitempty"`
	DailyPhase       string     `json:"dailyPhase,omitempty"`
	WeeklyPhase      string     `json:"weeklyPhase,omitempty"`
	DailyConfidence  int        `json:"dailyConfidence"`
	WeeklyConfidence int        `json:"weeklyConfidence"`
	Threshold        int        `json:"threshold"`
	StartsActing     *time.Time `json:"startsActing,omitempty"`
	TimeZone         string     `json:"timeZone,omitempty"`
	AutoResume       bool       `json:"autoResume"`
	// History is what it learned from the workload's CPU history in
	// Prometheus when Hybernate began managing it.
	History      *v1alpha1.PredictionHistory `json:"history,omitempty"`
	ManagedSince time.Time                   `json:"managedSince,omitzero"`
	// Learned is read from the forecast's saved state, and Unread says why
	// it couldn't be, when it couldn't.
	Learned *forecastLearned `json:"learned,omitempty"`
	Unread  string           `json:"unread,omitempty"`
}

// forecastLearned is what the operator's forecast has learned, and the
// hours it expects to be busy, read from its saved state.
type forecastLearned struct {
	// Hours are the hours of demand it has learned, FromHistory of them from
	// Prometheus when Hybernate began managing the workload.
	Hours       int `json:"hours"`
	FromHistory int `json:"fromHistory"`
	// HoursOfDay is how many of the 24 hours of the day it has seen since it
	// started or last relearned, and HoursOfWeek how many of the 168 hours
	// of the week it has ever seen.
	HoursOfDay  int `json:"hoursOfDay"`
	HoursOfWeek int `json:"hoursOfWeek"`
	// Busy are the spans of the next day it expects demand at or above the
	// workload's CPU threshold in: it holds off a pause before each, and with
	// autoResume wakes the workload ahead of it. None until it's acting.
	Busy []busySpan `json:"busy"`
}

// busySpan is consecutive hours the forecast expects to be busy, with Peak
// the most demand it expects in them, as a percentage of the CPU requested.
type busySpan struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	Peak int       `json:"peak"`
}

// forecastNamed shows the forecast of the one workload name names: for one
// Hybernate manages, what its own forecast is doing, and for any, the
// replay of its history when there's enough to replay. A workload that
// exists is never refused: what can't be shown says why, and what to do.
func forecastNamed(ctx context.Context, out io.Writer, name string, run forecastRun, opts forecastOptions) error {
	kind, bare, err := workloadArg(name)
	if err != nil {
		return err
	}
	c, at, err := opts.kube.client(ctx)
	if err != nil {
		return err
	}
	managed := managedNamed(ctx, c, opts.namespaces, at.namespace, kind, bare)
	zone := run.zone
	if zone == "" && len(managed) == 1 && managed[0].Status.Prediction != nil {
		zone = managed[0].Status.Prediction.TimeZone
	}
	if zone == "" {
		zone = operatorTimeZone(ctx, opts.kube)
	}
	loc, err := time.LoadLocation(cmp.Or(zone, "UTC"))
	if err != nil {
		return fmt.Errorf("the operator's time zone, %s: %w", zone, err)
	}

	run.historyOptional = true
	result, scan, err := replayCluster(ctx, run, loc, opts)
	if err != nil {
		return forecastFailed(ctx, err, opts.timeout)
	}
	picked, err := pickNamed(scan, managed, kind, bare, name)
	if err != nil {
		return err
	}
	if len(picked) == 0 {
		return notFound(ctx, c, opts, kind, bare, name)
	}
	now := opts.now()
	w := picked[0]
	if w.mw == nil && w.Managed {
		w.mw = managedIn(ctx, c, w.Namespace, w.Kind, w.Name)
	}
	if w.mw != nil {
		w.Operator = operatorForecastOf(w.mw, now)
	}
	w.NoReplay = whyNoReplay(w, scan, result, now, loc)
	if w.Operator == nil && w.NoReplay != "" && scan.historyUnread != nil {
		return forecastFailed(ctx, scan.historyUnread, opts.timeout)
	}
	result.Workloads = []forecastWorkload{w.forecastWorkload}
	return writeOutput(out, opts.output, result, func() error {
		return writeForecastNamed(out, result, loc, now)
	})
}

// named is a workload NAME names: as the scan read it, if it did, and its
// ManagedWorkload, if Hybernate manages it.
type named struct {
	forecastWorkload
	scanned *discovery.Workload
	mw      *v1alpha1.ManagedWorkload
}

// managedNamed is the ManagedWorkloads of the workloads named kind/name in
// the namespaces asked for, or the one named name. A cluster without
// Hybernate, or ManagedWorkloads the user can't read, has none.
func managedNamed(ctx context.Context, c client.Client, ns namespaceFlags,
	home, kind, name string) []v1alpha1.ManagedWorkload {
	all, _, err := listManaged(ctx, c, ns, home)
	if err != nil {
		return nil
	}
	byTarget := slices.DeleteFunc(slices.Clone(all), func(w v1alpha1.ManagedWorkload) bool {
		return !targets(w.Spec.Target.Kind, w.Spec.Target.Name, kind, name)
	})
	if len(byTarget) > 0 || kind != "" {
		return byTarget
	}
	return slices.DeleteFunc(all, func(w v1alpha1.ManagedWorkload) bool { return w.Name != name })
}

// managedIn is the ManagedWorkload of the workload kind/name in namespace,
// read there: one the scan found managed, which a user who can't list
// ManagedWorkloads in every namespace may not have seen in the context's.
func managedIn(ctx context.Context, c client.Client, namespace string, kind v1alpha1.TargetKind,
	name string) *v1alpha1.ManagedWorkload {
	var list v1alpha1.ManagedWorkloadList
	if err := c.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil
	}
	for i := range list.Items {
		if t := list.Items[i].Spec.Target; t.Kind == kind && t.Name == name {
			return &list.Items[i]
		}
	}
	return nil
}

// targets reports whether the workload kind/name is the one named, by a
// kind that may be empty for either.
func targets(kind v1alpha1.TargetKind, name, wantKind, wantName string) bool {
	return name == wantName && (wantKind == "" || strings.EqualFold(string(kind), wantKind))
}

// pickNamed is the workloads that answer to NAME, or whose ManagedWorkload
// does: as the scan read them, with their ManagedWorkload, if any, and one
// the scan didn't read, as one whose workload is gone, by its
// ManagedWorkload alone. More than one is ambiguous.
func pickNamed(scan scanResult, managed []v1alpha1.ManagedWorkload, kind, bare, name string) ([]named, error) {
	var scanned []discovery.Workload
	if scan.ClusterReport != nil {
		scanned = append(slices.Clone(scan.Workloads), scan.Platform...)
	}
	var picked []named
	add := func(namespace string, kind v1alpha1.TargetKind, name string) int {
		if at := slices.IndexFunc(picked, func(p named) bool {
			return p.Namespace == namespace && p.Kind == kind && p.Name == name
		}); at >= 0 {
			return at
		}
		w := named{forecastWorkload: forecastWorkload{Namespace: namespace, Kind: kind, Name: name}}
		if at := slices.IndexFunc(scanned, func(s discovery.Workload) bool {
			return s.Namespace == namespace && s.Kind == kind && s.Name == name
		}); at >= 0 {
			w.scanned = &scanned[at]
			w.Managed, w.Forecast = w.scanned.Managed, w.scanned.Forecast
		}
		picked = append(picked, w)
		return len(picked) - 1
	}
	for _, s := range scanned {
		if targets(s.Kind, s.Name, kind, bare) {
			add(s.Namespace, s.Kind, s.Name)
		}
	}
	for i := range managed {
		mw := &managed[i]
		at := add(mw.Namespace, mw.Spec.Target.Kind, mw.Spec.Target.Name)
		picked[at].mw, picked[at].Managed = mw, true
	}
	if len(picked) <= 1 {
		return picked, nil
	}
	names := make([]string, 0, len(picked))
	for _, w := range picked {
		names = append(names, w.Namespace+"/"+strings.ToLower(string(w.Kind))+"/"+w.Name)
	}
	return nil, fmt.Errorf("%s is %s; name one with -n or kind/name", name, strings.Join(names, " and "))
}

// notFound says why no workload answers to NAME. In namespaces named with
// -n, a workload that exists but is labelled hybernate.io/ignore is named
// as such, since the scan leaves it out.
func notFound(ctx context.Context, c client.Client, opts forecastOptions, kind, bare, name string) error {
	namespaces := opts.namespaces.list()
	if len(namespaces) == 0 {
		return fmt.Errorf("no Deployment or StatefulSet named %s in the namespaces read, which leave out %s; "+
			"check the name, or name its namespace with -n", bare, strings.Join(opts.exclude, ", "))
	}
	for _, ns := range namespaces {
		for _, obj := range []client.Object{&appsv1.Deployment{}, &appsv1.StatefulSet{}} {
			if kind != "" && !strings.EqualFold(kindOf(obj), kind) {
				continue
			}
			if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: bare}, obj); err != nil {
				if !apierrors.IsNotFound(err) && !apierrors.IsForbidden(err) {
					return fmt.Errorf("getting %s %s/%s: %w", kindOf(obj), ns, bare, err)
				}
				continue
			}
			if obj.GetLabels()[v1alpha1.LabelIgnore] == v1alpha1.True {
				return fmt.Errorf("%s/%s/%s is labelled %s=true, which keeps Hybernate and its forecast away "+
					"from it; remove the label to see its forecast", ns, kindOf(obj), bare, v1alpha1.LabelIgnore)
			}
		}
	}
	return fmt.Errorf("no Deployment or StatefulSet named %s in %s; check the name and the namespace", name,
		strings.Join(namespaces, ", "))
}

// operatorForecastOf is what the operator's forecast of mw is doing, as its
// status says, with what its saved state says it has learned and expects
// from now on.
func operatorForecastOf(mw *v1alpha1.ManagedWorkload, now time.Time) *operatorForecast {
	f := &operatorForecast{ManagedWorkload: mw.Name, ManagedSince: mw.CreationTimestamp.UTC(),
		AutoResume: mw.Spec.IdlePolicy != nil && mw.Spec.IdlePolicy.AutoResume}
	p := mw.Status.Prediction
	if p == nil {
		f.Pending = fmt.Sprintf("Hybernate hasn't recorded a forecast for it yet. It does on its first look at the "+
			"workload while it's running, within a minute of managing it; if it doesn't, kubectl describe "+
			"managedworkload %s -n %s says what's in the way", mw.Name, mw.Namespace)
		return f
	}
	f.Acting, f.Reason, f.Message = p.Acting, p.Reason, p.Message
	f.DailyPhase, f.WeeklyPhase = p.DailyPhase, p.WeeklyPhase
	f.DailyConfidence, f.WeeklyConfidence = p.DailyConfidence, p.WeeklyConfidence
	f.Threshold = cmp.Or(p.Threshold,
		forecast.Threshold(forecast.Mode(mw.Spec.Prediction.Mode), mw.Spec.Prediction.Confidence))
	f.TimeZone, f.History = p.TimeZone, p.History
	if p.StartsActing != nil {
		at := p.StartsActing.UTC()
		f.StartsActing = &at
	}
	f.Learned, f.Unread = learnedFrom(p, f.Threshold, cpuThresholdOf(mw), now)
	return f
}

// cpuThresholdOf is the CPU use, as a percentage of requests, the operator
// counts the workload as in use above, and expects it busy at.
func cpuThresholdOf(mw *v1alpha1.ManagedWorkload) int {
	if p := mw.Spec.IdlePolicy; p != nil && p.Activity != nil && p.Activity.CPUThreshold > 0 {
		return p.Activity.CPUThreshold
	}
	return defaultCPUThreshold
}

// learnedFrom reads the forecast's saved state as the operator restores
// it, for what it has learned and the hours of the next day it expects
// busy, or says why it can't.
func learnedFrom(p *v1alpha1.PredictionStatus, threshold, cpuThreshold int, now time.Time) (*forecastLearned, string) {
	if p.State == "" {
		return nil, "the forecast hasn't saved what it has learned yet"
	}
	loc, err := time.LoadLocation(cmp.Or(p.TimeZone, "UTC"))
	if err != nil {
		return nil, fmt.Sprintf("its time zone, %s, isn't one this machine knows: %v", p.TimeZone, err)
	}
	engine, err := forecast.ImportEngine(p.State, forecast.Settings{Threshold: threshold, Location: loc})
	if err != nil {
		return nil, fmt.Sprintf("this plugin can't read the forecast's saved state, which may be from another "+
			"version of Hybernate; the plugin of the operator's version can (%v)", err)
	}
	o := engine.Outlook()
	l := &forecastLearned{Hours: engine.GetDataPoints(), HoursOfDay: o.HoursOfDaySeen,
		HoursOfWeek: o.HoursOfWeekSeen, Busy: []busySpan{}}
	if h := p.History; h != nil && h.Reason == historyLearned {
		l.FromHistory = min(h.Hours, l.Hours)
	}
	start := forecast.HourStart(now, loc)
	for h := range busyAhead {
		demand := engine.Predict(h, now)
		if demand < float64(cpuThreshold) {
			continue
		}
		from := start.Add(time.Duration(h) * time.Hour)
		if n := len(l.Busy); n > 0 && l.Busy[n-1].To.Equal(from) {
			l.Busy[n-1].To = from.Add(time.Hour)
			l.Busy[n-1].Peak = max(l.Busy[n-1].Peak, int(demand+0.5))
			continue
		}
		l.Busy = append(l.Busy, busySpan{From: from.UTC(), To: from.Add(time.Hour).UTC(), Peak: int(demand + 0.5)})
	}
	return l, ""
}

// whyNoReplay says why a workload's history wasn't replayed, and what would
// let it be, or "" when it was.
func whyNoReplay(w named, scan scanResult, result forecastResult, now time.Time, loc *time.Location) string {
	s := w.scanned
	switch {
	case w.Forecast != nil:
		return ""
	case scan.historyUnread != nil:
		return scan.historyUnread.Error()
	case s == nil:
		return fmt.Sprintf("no %s named %s was read in %s", strings.ToLower(string(w.Kind)), w.Name, w.Namespace)
	case s.ScaledByHand:
		return "it's scaled to zero outside Hybernate, which the replay leaves out"
	case s.PodCPURequestMillis == 0 || s.Unmeasured == "no CPU requests":
		return "it sets no CPU requests, and the forecast measures use as a share of them; set " +
			"resources.requests.cpu on its containers"
	case s.History == nil:
		return fmt.Sprintf("no CPU history of its pods was read from %s: Prometheus needs cAdvisor's "+
			"container_cpu_usage_seconds_total for them, and the notes say if the history ran out of time",
			cmp.Or(result.Prometheus, "Prometheus"))
	}
	least := discovery.MinForecastHistory
	after := now.Add(least - time.Duration(s.History.Hours*float64(time.Hour)))
	return fmt.Sprintf("Prometheus has %s of its CPU history, and the replay needs at least %s, a day to learn "+
		"from and a day to replay; run it again after %s", spanOf(s.History.Hours), spanOf(least.Hours()),
		after.In(loc).Format("Mon 2 Jan 15:04 MST"))
}

func writeForecastNamed(out io.Writer, result forecastResult, loc *time.Location, now time.Time) error {
	p := &printer{w: out}
	w := result.Workloads[0]
	p.line("%s: the forecast of %s/%s, times in %s", result.Cluster, w.Namespace, kindName(w), result.Timezone)
	p.line("")
	if o := w.Operator; o != nil {
		writeOperatorForecast(p, o, loc, now)
	}
	switch {
	case w.Forecast != nil:
		writeReplayDetail(p, w, result, loc)
	case w.NoReplay != "":
		p.line("No replay of its history: %s.", w.NoReplay)
		p.line("")
	}
	writeForecastNotes(p, result.Notes)
	return p.err
}

func kindName(w forecastWorkload) string {
	if w.Kind == v1alpha1.TargetKindStatefulSet {
		return "statefulset/" + w.Name
	}
	return w.Name
}

func writeOperatorForecast(p *printer, o *operatorForecast, loc *time.Location, now time.Time) {
	if o.Pending != "" {
		p.line("Hybernate's forecast: none yet. %s.", o.Pending)
		p.line("")
		return
	}
	p.line("Hybernate's forecast, from ManagedWorkload %s: %s", o.ManagedWorkload,
		forecastCell(statusWorkload{Forecast: o.Reason}))
	if o.Message != "" {
		p.line("  %s", oneLine(o.Message))
	}
	tw := tabwriter.NewWriter(p, 0, 0, 2, ' ', 0)
	row := func(label, value string) { _, _ = fmt.Fprintf(tw, "  %s\t%s\n", label, value) }
	row("Phase:", fmt.Sprintf("daily %s, weekly %s", o.DailyPhase, o.WeeklyPhase))
	if l := o.Learned; l != nil {
		row("Hours seen:", fmt.Sprintf("%d of the 24 hours of the day, %d of the 168 hours of the week",
			l.HoursOfDay, l.HoursOfWeek))
		row("Learned:", learnedCell(o, l, now))
	}
	if h := o.History; h != nil && h.Reason != historyLearned && h.Message != "" {
		row("History:", oneLine(h.Message))
	}
	row("Confidence:", fmt.Sprintf("%d%% right over the last day, %d%% over the last week; it acts from %d%%",
		o.DailyConfidence, o.WeeklyConfidence, o.Threshold))
	row("Acts from:", actsFrom(o, loc))
	if l := o.Learned; l != nil {
		row("Busy hours:", busyCell(o, l, loc))
	}
	row("autoResume:", autoResumeCell(o))
	if o.Unread != "" {
		row("Its state:", o.Unread)
	}
	_ = tw.Flush() // writes through p, which keeps the first error
	p.line("")
}

// learnedCell says how many hours of demand the forecast has learned, and
// how many of them since Hybernate began managing the workload rather than
// from its history.
func learnedCell(o *operatorForecast, l *forecastLearned, now time.Time) string {
	live := l.Hours - l.FromHistory
	managed := ""
	if !o.ManagedSince.IsZero() {
		managed = fmt.Sprintf(" in the %s Hybernate has managed it", spanOf(now.Sub(o.ManagedSince).Hours()))
	}
	switch {
	case l.Hours == 0:
		return "nothing yet" + managed
	case l.FromHistory == 0:
		return plural(l.Hours, "hour", "hours") + ", all live" + managed
	case live == 0:
		return fmt.Sprintf("%s, all from its CPU history in Prometheus; none live%s",
			plural(l.Hours, "hour", "hours"), managed)
	}
	return fmt.Sprintf("%s: %d from its CPU history in Prometheus, and %d live%s", plural(l.Hours, "hour", "hours"),
		l.FromHistory, live, managed)
}

func actsFrom(o *operatorForecast, loc *time.Location) string {
	switch {
	case o.Acting:
		return "now"
	case o.StartsActing != nil:
		return "around " + o.StartsActing.In(loc).Format("Mon 2 Jan 15:04") +
			", if the coming days repeat the last week"
	case o.Learned != nil && o.Learned.HoursOfDay < forecast.DailySeason:
		return "not known until it has seen every hour of the day"
	}
	return "not expected within 4 weeks, if the coming days repeat the last week"
}

func busyCell(o *operatorForecast, l *forecastLearned, loc *time.Location) string {
	switch {
	case !o.Acting:
		return "none yet: it forecasts the hours it acts on once it's acting"
	case len(l.Busy) == 0:
		return fmt.Sprintf("none in the next %d hours", busyAhead)
	}
	spans := make([]string, 0, len(l.Busy))
	for _, b := range l.Busy {
		spans = append(spans, fmt.Sprintf("%s, up to %d%% of its CPU requests", spanText(b, loc), b.Peak))
	}
	return strings.Join(spans, "; ")
}

// spanText is when a busy span is, in loc: "Thu 22 Oct 09:00–17:00", or
// with both days when it runs past midnight.
func spanText(b busySpan, loc *time.Location) string {
	from, to := b.From.In(loc), b.To.In(loc)
	if last := to.Add(-time.Minute); last.YearDay() == from.YearDay() && last.Year() == from.Year() {
		return from.Format("Mon 2 Jan 15:04") + "–" + to.Format("15:04")
	}
	return from.Format("Mon 2 Jan 15:04") + " – " + to.Format("Mon 2 Jan 15:04")
}

func autoResumeCell(o *operatorForecast) string {
	if o.AutoResume {
		return "on: it holds off pauses before the hours it expects busy, and wakes the workload 15 minutes " +
			"ahead of them"
	}
	return fmt.Sprintf("off: it holds off pauses before the hours it expects busy, but doesn't wake the workload "+
		"for them; %s: \"true\" turns it on", v1alpha1.AnnotationAutoResume)
}

// writeReplayDetail is the replay of a workload's history: what the
// forecast would do opted in today, then the last half of the history
// replayed day by day.
func writeReplayDetail(p *printer, w forecastWorkload, result forecastResult, loc *time.Location) {
	f := w.Forecast
	p.line("Replayed over its last %s of CPU history, from %s:", spanOf(result.HistoryHours), result.Prometheus)
	p.line("  Opted in today: %s, at a confidence of %d%%.", ifOptedIn(f, loc), f.Threshold)
	r := f.Replay
	if r == nil {
		p.line("")
		return
	}
	p.line("")
	p.line("From %s to %s, with autoResume, from a forecast that learned only the history before; while it's "+
		"paused, %s:", r.From.In(loc).Format("Mon 2 Jan"), r.To.In(loc).Format("Mon 2 Jan"), wakesSentence(f.Wakes))
	tw := tabwriter.NewWriter(p, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "DAY\tFIRST USE\tFOUND IT\tWOKEN AT\tFORECAST\tWOKEN FOR NOTHING")
	for _, d := range r.Days {
		first, found, woke := "-", "-", "-"
		if d.FirstUse != nil {
			first, found = d.FirstUse.In(loc).Format("15:04"), d.Found
		}
		if d.WokeAt != nil {
			woke = d.WokeAt.In(loc).Format("15:04")
		}
		acting := forecastLearning
		if d.Acting {
			acting = forecastActing
		}
		wasted := "-"
		if d.WastedWakes > 0 {
			wasted = fmt.Sprint(d.WastedWakes)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", d.Date.Format("Mon 2 Jan"), first, found, woke, acting, wasted)
	}
	_ = tw.Flush() // writes through p, which keeps the first error
	p.line("")
	if r.UsedDays > 0 {
		p.line("Ready on %d of %d mornings%s; %s.", r.Ready, r.UsedDays, leadClause(r), wastedSentence(r))
	} else {
		p.line("Nobody used it; %s.", wastedSentence(r))
	}
	p.line("")
}

// ifOptedIn is when the forecast would act were the workload opted in
// today, having learned all its history, whether or not Hybernate already
// manages it.
func ifOptedIn(f *discovery.ForecastReplay, loc *time.Location) string {
	return forecastNow(forecastWorkload{Forecast: &discovery.ForecastReplay{IfOptedIn: f.IfOptedIn}}, loc)
}
