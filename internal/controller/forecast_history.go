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

package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/discovery"
	"github.com/napwake/napwake/internal/forecast"
	"github.com/napwake/napwake/internal/metrics"
)

// What came of reading a workload's history, in
// status.prediction.history.reason.
const (
	HistoryLearned         = "Learned"
	HistoryNoPrometheusURL = "NoPrometheusURL"
	HistoryNone            = "NoHistory"
	HistoryNoCPURequests   = "NoCPURequests"
	HistoryQueryFailed     = "QueryFailed"
)

const (
	ReasonForecastLearnedHistory     = "ForecastLearnedHistory"
	ReasonForecastLearningLive       = "ForecastLearningLive"
	ReasonForecastHistoryUnavailable = "ForecastHistoryUnavailable"
)

// historyWindow is the most history a forecast learns from: four weeks,
// time for the cautious setting to act on a weekday pattern and for every
// setting to learn its weekends.
const historyWindow = 4 * 7 * 24 * time.Hour

// historyQueryTimeout bounds the one query a workload's history takes. It
// answers with a point an hour, and Prometheus works it out from a month
// of one workload's five-minute rates in seconds, even from remote
// storage, so one that takes this long isn't answering.
const historyQueryTimeout = 30 * time.Second

// historyLateFor is how long after Napwake took a workload on without a
// Prometheus URL a URL set since still has its forecast learn the history
// from before: the week before its forecast acts live, through which it
// keeps every hour it has learned, for forecast.Rebuild to learn them
// again on top. A forecast that has learned live for longer has learned
// what the history would have taught it.
const historyLateFor = 7 * 24 * time.Hour

// historyAttempts is how many times a read that failed is made before the
// forecast learns live only, historyRetry apart.
const (
	historyAttempts = 3
	historyRetry    = 2 * time.Minute
)

// historyReader reads a workload's hourly CPU peaks from Prometheus.
type historyReader interface {
	HourlyPeaks(ctx context.Context, w discovery.WorkloadCPU, start, end time.Time) (map[int64]float64, error)
}

// newHistoryReader is the Prometheus at url that new workloads' history is
// read from, with selector's matchers added to the query.
func newHistoryReader(url string, selector discovery.Selector) (*discovery.Prometheus, error) {
	httpClient, err := discovery.NewHTTPClient(discovery.HTTPOptions{})
	if err != nil {
		return nil, fmt.Errorf("creating the Prometheus history client: %w", err)
	}
	p, err := discovery.NewPrometheusURL(url, httpClient, nil)
	if err != nil {
		return nil, fmt.Errorf("reading --prometheus-url for history: %w", err)
	}
	p.Selector = selector
	return p, nil
}

// historySeed is what reading a workload's history came to: a forecast that
// learned it, the history from before a workload was taken on for the
// forecast learning live to be rebuilt on, or why there's none.
type historySeed struct {
	engine  forecaster
	history v1alpha1.PredictionHistory
	// beneath is the history of a workload taken on at takenOn without a
	// Prometheus URL, to be learned beneath what its forecast has learned
	// live since, with its activity threshold, cpuThreshold, when the
	// reconcile takes it up: rebuilt then, the forecast has every hour
	// learned live up to that reconcile.
	beneath      []forecast.Hour
	takenOn      time.Time
	cpuThreshold float64
	// adopted says the workload's reconcile has taken the engine up.
	adopted bool
}

// offerSeed holds a workload's learned history for its reconcile to take
// up.
func (reg *engineRegistry) offerSeed(uid types.UID, seed historySeed) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.seeds[uid] = seed
}

// seeded reports whether the workload's history has been read.
func (reg *engineRegistry) seeded(uid types.UID) bool {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	_, ok := reg.seeds[uid]
	return ok
}

// adoptSeed takes up the workload's learned history. The first time, the
// engine that learned it replaces the one learning live: it learned the
// same hours, from Prometheus, up to the hour it was read in. It reports
// whether there was a seed, and whether this was the first time.
func (reg *engineRegistry) adoptSeed(uid types.UID, settings forecast.Settings) (seed historySeed, first, ok bool) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	seed, ok = reg.seeds[uid]
	if !ok {
		return historySeed{}, false, false
	}
	first = !seed.adopted
	if first && seed.beneath != nil {
		seed = reg.rebuild(uid, seed, settings)
	}
	if first && seed.engine != nil {
		seed.engine.Configure(settings)
		reg.engines[uid] = seed.engine
	}
	seed.adopted = true
	reg.seeds[uid] = seed
	return seed, first, true
}

// rebuild learns the seed's history beneath what the workload's forecast
// has learned live, by forecast.Rebuild, for adoptSeed, whose lock it's
// called with. A forecast that can't be rebuilt, as one that has learned
// live for a week, goes on as it is, and the seed says why.
func (reg *engineRegistry) rebuild(uid types.UID, seed historySeed, settings forecast.Settings) historySeed {
	live, ok := reg.engines[uid]
	if !ok {
		live = reg.newEngine()
	}
	rebuilt, learned, err := rebuildBeneath(seed, live, settings)
	if err != nil {
		return historySeed{history: v1alpha1.PredictionHistory{Reason: HistoryNoPrometheusURL,
			Message: fmt.Sprintf("%s. Its history couldn't be learned beneath what it has learned live since: %v",
				noPrometheusURLMessage, err)}, adopted: seed.adopted}
	}
	seed.engine = rebuilt
	seed.history.Hours = learned
	seed.history.Message = fmt.Sprintf("the forecast learned from %s of the workload's CPU history in Prometheus, "+
		"from before Napwake took it on, beneath the %d hours it had learned live since", historySpan(learned),
		live.GetDataPoints())
	return seed
}

func rebuildBeneath(seed historySeed, live forecaster, settings forecast.Settings) (*forecast.Engine, int, error) {
	state, err := live.Export()
	if err != nil {
		return nil, 0, err
	}
	engine, err := forecast.ImportEngine(state, settings)
	if err != nil {
		return nil, 0, err
	}
	return forecast.Rebuild(seed.beneath, engine, seed.takenOn, seed.cpuThreshold)
}

// needsHistory reports whether the workload's history is yet to be read:
// nothing has come of reading it, or it was taken on without a Prometheus
// URL, and with one now, as url says, it's within historyLateFor of that at
// now (lateForHistory).
func needsHistory(workload *v1alpha1.ManagedWorkload, url bool, now time.Time) bool {
	if workload.DeletionTimestamp != nil {
		return false
	}
	if p := workload.Status.Prediction; p == nil || p.History == nil {
		return true
	}
	return url && lateForHistory(workload, now)
}

// lateForHistory reports whether the workload was taken on without a
// Prometheus URL less than historyLateFor before now. Its history is read
// up to the hour Napwake took it on in, its ManagedWorkload's creation:
// from then on, Prometheus holds the hours Napwake paused it in, which read
// as quiet.
func lateForHistory(workload *v1alpha1.ManagedWorkload, now time.Time) bool {
	p := workload.Status.Prediction
	return p != nil && p.History != nil && p.History.Reason == HistoryNoPrometheusURL &&
		!workload.CreationTimestamp.IsZero() && now.Sub(workload.CreationTimestamp.Time) < historyLateFor
}

// noPrometheusURLMessage says why a workload's forecast learned live from
// the start. It's what was so when Napwake took the workload on, so it's in
// the past tense, and stays true once the operator is given a URL.
const noPrometheusURLMessage = "when Napwake took it on, the operator had no --prometheus-url, so the forecast " +
	"learned from live CPU from the start, rather than from the workload's last four weeks"

// tooLateMessage is noPrometheusURLMessage once the operator has a URL,
// set too late for the workload's forecast to learn its history.
const tooLateMessage = noPrometheusURLMessage + ". The --prometheus-url set since came more than a week after, " +
	"too late to learn them beneath what it had learned live"

// takeUpHistory records in status what came of reading the workload's
// history, and the first time, has its forecast take up what it learned.
// Without a Prometheus to read, the forecast learns live from the start.
func (r *Reconciler) takeUpHistory(workload *v1alpha1.ManagedWorkload, engine forecaster,
	settings forecast.Settings) forecaster {
	if !needsHistory(workload, r.history != nil, r.now()) {
		sayNoURLInThePast(workload, r.history != nil)
		return engine
	}
	if r.history == nil {
		setHistory(workload, v1alpha1.PredictionHistory{Reason: HistoryNoPrometheusURL, Message: noPrometheusURLMessage})
		return engine
	}
	seed, first, ok := r.engines.adoptSeed(workload.UID, settings)
	if !ok {
		return engine
	}
	setHistory(workload, seed.history)
	if seed.engine != nil {
		engine = seed.engine
	}
	if !first {
		return engine
	}
	switch seed.history.Reason {
	case HistoryLearned:
		now := "is learning"
		if engine.GetPhase() >= forecast.DailyActive {
			now = "is acting already"
		}
		r.emitEvent(workload, false, "Normal", ReasonForecastLearnedHistory, actionForecast, "%s, and %s",
			seed.history.Message, now)
	case HistoryNone:
		r.emitEvent(workload, false, "Normal", ReasonForecastLearningLive, actionForecast, "%s", seed.history.Message)
	default:
		r.emitEvent(workload, false, "Warning", ReasonForecastHistoryUnavailable, actionForecast, "%s",
			seed.history.Message)
	}
	return engine
}

// sayNoURLInThePast rewrites a no-Prometheus-URL reason an earlier operator
// wrote in the present tense, which turned false once a URL was set, and
// with a URL now, as url says, that it came too late to learn the history.
// A reason that says the history couldn't be learned beneath the live
// forecast stays as it is.
func sayNoURLInThePast(workload *v1alpha1.ManagedWorkload, url bool) {
	p := workload.Status.Prediction
	if p == nil || p.History == nil || p.History.Reason != HistoryNoPrometheusURL ||
		strings.HasPrefix(p.History.Message, noPrometheusURLMessage+". Its history couldn't") {
		return
	}
	p.History.Message = noPrometheusURLMessage
	if url {
		p.History.Message = tooLateMessage
	}
}

func setHistory(workload *v1alpha1.ManagedWorkload, history v1alpha1.PredictionHistory) {
	if workload.Status.Prediction == nil {
		workload.Status.Prediction = &v1alpha1.PredictionStatus{}
	}
	workload.Status.Prediction.History = &history
}

// historyLearner reads a new ManagedWorkload's CPU history from Prometheus,
// up to historyWindow of it, and has a forecast learn it in one go, as it
// would have learned it live, for the workload's reconcile to take up. It
// runs as a controller of its own, a workload at a time, so a slow
// Prometheus never holds up a reconcile, and a cluster opting in hundreds
// of workloads at once asks Prometheus one query at a time.
type historyLearner struct {
	client   client.Client
	history  historyReader
	metrics  metricsReader
	engines  *engineRegistry
	timezone *time.Location
	clock    func() time.Time

	mu       sync.Mutex
	failures map[types.UID]failedReads
}

// failedReads are a workload's failed attempts at reading its history, and
// when the last was made.
type failedReads struct {
	count int
	last  time.Time
}

// errReadingTarget is a failure to read the workload's target or what it
// requests from the API server, which is retried until it succeeds rather
// than counted against Prometheus.
var errReadingTarget = errors.New("reading the target")

func (l *historyLearner) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var workload v1alpha1.ManagedWorkload
	if err := l.client.Get(ctx, req.NamespacedName, &workload); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !needsHistory(&workload, true, l.clock()) || l.engines.seeded(workload.UID) {
		return ctrl.Result{}, nil
	}
	// A new workload is updated several times in its first minute, and
	// each update reconciles it at once, whatever RequeueAfter asked for.
	if wait := l.untilRetry(workload.UID); wait > 0 {
		return ctrl.Result{RequeueAfter: wait}, nil
	}
	seed, ready, err := l.learn(ctx, &workload)
	if errors.Is(err, errReadingTarget) {
		return ctrl.Result{}, err
	}
	if err != nil {
		if l.failed(workload.UID) < historyAttempts {
			log.FromContext(ctx).Info("Could not read CPU history from Prometheus, trying again",
				"workload", workload.Name, "namespace", workload.Namespace, "error", err.Error())
			return ctrl.Result{RequeueAfter: historyRetry}, nil
		}
		seed, ready = historySeed{history: v1alpha1.PredictionHistory{Reason: HistoryQueryFailed,
			Message: fmt.Sprintf("can't read the workload's CPU history from Prometheus, so the forecast learns "+
				"from now: %v", err)}}, true
	}
	if ready {
		l.forget(workload.UID)
		l.engines.offerSeed(workload.UID, seed)
	}
	return ctrl.Result{}, nil
}

// failed records a failed attempt at reading the workload's history, and
// returns how many there have been.
func (l *historyLearner) failed(uid types.UID) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failures == nil {
		l.failures = map[types.UID]failedReads{}
	}
	f := l.failures[uid]
	f.count++
	f.last = l.clock()
	l.failures[uid] = f
	return f.count
}

// untilRetry is how long until the workload's history may be read again:
// historyRetry after the last failed attempt, or zero.
func (l *historyLearner) untilRetry(uid types.UID) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, ok := l.failures[uid]
	if !ok {
		return 0
	}
	return max(0, f.last.Add(historyRetry).Sub(l.clock()))
}

func (l *historyLearner) forget(uid types.UID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, uid)
}

// learn reads the workload's history and learns it. It isn't ready while
// the workload's target doesn't exist, which a change to the workload
// tries again.
func (l *historyLearner) learn(ctx context.Context, workload *v1alpha1.ManagedWorkload) (historySeed, bool, error) {
	target, err := l.target(ctx, workload)
	if apierrors.IsNotFound(err) {
		return historySeed{}, false, nil
	}
	if err != nil {
		return historySeed{}, false, fmt.Errorf("%w: %w", errReadingTarget, err)
	}
	requested, err := l.metrics.CPURequestPerReplica(ctx, workload)
	switch {
	case errors.Is(err, metrics.ErrNoCPURequests), err == nil && requested <= 0:
		return historySeed{history: v1alpha1.PredictionHistory{Reason: HistoryNoCPURequests,
			Message: "the target's containers request no CPU, so its CPU history can't be measured against " +
				"them, and the forecast learns from now"}}, true, nil
	case err != nil:
		return historySeed{}, false, fmt.Errorf("%w's CPU requests: %w", errReadingTarget, err)
	}
	containers := make([]string, 0, len(podTemplateOf(target).Spec.Containers))
	for _, c := range metrics.WorkloadContainers(podTemplateOf(target).Spec) {
		containers = append(containers, c.Name)
	}

	now := l.clock()
	end := forecast.HourStart(now, l.timezone)
	start := forecast.HourStart(now.Add(-historyWindow), l.timezone)
	late := lateForHistory(workload, now)
	if late {
		takenOn := workload.CreationTimestamp.Time
		end = forecast.HourStart(takenOn, l.timezone)
		start = forecast.HourStart(takenOn.Add(-historyWindow), l.timezone)
	}
	queryCtx, cancel := context.WithTimeout(ctx, historyQueryTimeout)
	defer cancel()
	peaks, err := l.history.HourlyPeaks(queryCtx, discovery.WorkloadCPU{Namespace: workload.Namespace,
		Kind: workload.Spec.Target.Kind, Name: workload.Spec.Target.Name, Containers: containers}, start, end)
	if err != nil {
		return historySeed{}, false, err
	}
	hours := historyHours(peaks, requested, end)
	switch {
	case len(hours) == 0 && late:
		return historySeed{history: v1alpha1.PredictionHistory{Reason: HistoryNone,
			Message: "Prometheus has no CPU history for the workload's pods from before Napwake took it on, so the " +
				"forecast goes on learning live"}}, true, nil
	case len(hours) == 0:
		return historySeed{history: v1alpha1.PredictionHistory{Reason: HistoryNone,
			Message: "Prometheus has no CPU history for the workload's pods, so the forecast learns from now"}}, true, nil
	case late:
		from, to := metav1.NewTime(hours[0].Start.UTC()), metav1.NewTime(end.UTC())
		return historySeed{beneath: hours, takenOn: workload.CreationTimestamp.Time,
			cpuThreshold: cpuThresholdFor(workload),
			history:      v1alpha1.PredictionHistory{Reason: HistoryLearned, From: &from, To: &to}}, true, nil
	}

	engine := forecast.NewEngine(forecast.DefaultParams(),
		forecast.Settings{Threshold: confidenceFor(workload), Location: l.timezone})
	learned, err := engine.Learn(hours, cpuThresholdFor(workload))
	if err != nil {
		return historySeed{}, false, err
	}
	from, to := metav1.NewTime(hours[0].Start.UTC()), metav1.NewTime(end.UTC())
	return historySeed{engine: engine, history: v1alpha1.PredictionHistory{Reason: HistoryLearned, Hours: learned,
		From: &from, To: &to, Message: fmt.Sprintf("the forecast learned from %s of the workload's CPU history in "+
			"Prometheus", historySpan(learned))}}, true, nil
}

func (l *historyLearner) target(ctx context.Context, workload *v1alpha1.ManagedWorkload) (client.Object, error) {
	key := client.ObjectKey{Namespace: workload.Namespace, Name: workload.Spec.Target.Name}
	var obj client.Object = &appsv1.Deployment{}
	if workload.Spec.Target.Kind == v1alpha1.TargetKindStatefulSet {
		obj = &appsv1.StatefulSet{}
	}
	if err := l.client.Get(ctx, key, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// historyHours is the workload's history hour by hour, as utilization of
// requestedMillis per pod, from the first hour it ran in up to end. An hour
// none of its pods ran in, scaled to zero, is quiet, as live: history can't
// tell an hour paused without the doorman, whose demand live learning
// skips, from one nobody used.
func historyHours(peaks map[int64]float64, requestedMillis float64, end time.Time) []forecast.Hour {
	if len(peaks) == 0 {
		return nil
	}
	starts := make([]int64, 0, len(peaks))
	for at := range peaks {
		starts = append(starts, at)
	}
	first := slices.Min(starts)
	var hours []forecast.Hour
	for t := time.Unix(first, 0); t.Before(end); t = t.Add(time.Hour) {
		hours = append(hours, forecast.Hour{Start: t, Peak: peaks[t.Unix()] * 1000 / requestedMillis * 100})
	}
	return hours
}

// historySpan is a number of hours of history in weeks, or in days under
// two weeks.
func historySpan(hours int) string {
	days := hours / 24
	switch {
	case days >= 14:
		return fmt.Sprintf("%d weeks", days/7)
	case days == 1:
		return "1 day"
	case days > 1:
		return fmt.Sprintf("%d days", days)
	case hours == 1:
		return "1 hour"
	default:
		return fmt.Sprintf("%d hours", hours)
	}
}
