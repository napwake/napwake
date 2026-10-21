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

package controller

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
	"github.com/okedeji/hybernate/internal/discovery"
	"github.com/okedeji/hybernate/internal/forecast"
	"github.com/okedeji/hybernate/internal/metrics"
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
// learned it, or why there's none.
type historySeed struct {
	engine  forecaster
	history v1alpha1.PredictionHistory
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
	if first && seed.engine != nil {
		seed.engine.Configure(settings)
		reg.engines[uid] = seed.engine
	}
	seed.adopted = true
	reg.seeds[uid] = seed
	return seed, first, true
}

// needsHistory reports whether nothing has yet come of reading a
// workload's history.
func needsHistory(workload *v1alpha1.ManagedWorkload) bool {
	return workload.DeletionTimestamp == nil &&
		(workload.Status.Prediction == nil || workload.Status.Prediction.History == nil)
}

// takeUpHistory records in status what came of reading the workload's
// history, and the first time, has its forecast take up what it learned.
// Without a Prometheus to read, the forecast learns live from the start.
func (r *Reconciler) takeUpHistory(workload *v1alpha1.ManagedWorkload, engine forecaster,
	settings forecast.Settings) forecaster {
	if !needsHistory(workload) {
		return engine
	}
	if r.history == nil {
		setHistory(workload, v1alpha1.PredictionHistory{Reason: HistoryNoPrometheusURL,
			Message: "the operator has no --prometheus-url, so the forecast learns from live CPU from the start, " +
				"rather than from the workload's last four weeks"})
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
	failures map[types.UID]int
}

func (l *historyLearner) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var workload v1alpha1.ManagedWorkload
	if err := l.client.Get(ctx, req.NamespacedName, &workload); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !needsHistory(&workload) || l.engines.seeded(workload.UID) {
		return ctrl.Result{}, nil
	}
	seed, ready, err := l.learn(ctx, &workload)
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

func (l *historyLearner) failed(uid types.UID) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failures == nil {
		l.failures = map[types.UID]int{}
	}
	l.failures[uid]++
	return l.failures[uid]
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
		return historySeed{}, false, fmt.Errorf("reading the target: %w", err)
	}
	requested, err := l.metrics.CPURequestPerReplica(ctx, workload)
	if err != nil || requested <= 0 {
		return historySeed{history: v1alpha1.PredictionHistory{Reason: HistoryNoCPURequests,
			Message: "the target's containers request no CPU, so its CPU history can't be measured against " +
				"them, and the forecast learns from now"}}, true, nil
	}
	containers := make([]string, 0, len(podTemplateOf(target).Spec.Containers))
	for _, c := range metrics.WorkloadContainers(podTemplateOf(target).Spec) {
		containers = append(containers, c.Name)
	}

	now := l.clock()
	end := forecast.HourStart(now, l.timezone)
	start := forecast.HourStart(now.Add(-historyWindow), l.timezone)
	queryCtx, cancel := context.WithTimeout(ctx, historyQueryTimeout)
	defer cancel()
	peaks, err := l.history.HourlyPeaks(queryCtx, discovery.WorkloadCPU{Namespace: workload.Namespace,
		Kind: workload.Spec.Target.Kind, Name: workload.Spec.Target.Name, Containers: containers}, start, end)
	if err != nil {
		return historySeed{}, false, err
	}
	hours := historyHours(peaks, requested, end)
	if len(hours) == 0 {
		return historySeed{history: v1alpha1.PredictionHistory{Reason: HistoryNone,
			Message: "Prometheus has no CPU history for the workload's pods, so the forecast learns from now"}}, true, nil
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
