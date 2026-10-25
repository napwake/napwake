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
	"errors"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/cost"
	"github.com/napwake/napwake/internal/forecast"
	"github.com/napwake/napwake/internal/human"
	"github.com/napwake/napwake/internal/lifecycle"
	opmetrics "github.com/napwake/napwake/internal/metrics"

	ctrl "sigs.k8s.io/controller-runtime"
)

type forecaster interface {
	Observe(actual float64, now time.Time) (float64, error)
	Observed(now time.Time) bool
	Expect(h int, now time.Time) (float64, bool)
	Export() (string, error)
	Configure(settings forecast.Settings)
	GetPhase() forecast.Phase
	DailyConfidence() int
	WeeklyConfidence() int
	GetDataPoints() int
	RegimeChanged() bool
	AnomalyDetected() bool
	Outlook() forecast.Outlook
}

// metricsReader measures a workload two ways. Activity, and the forecast,
// compare the usage of the workload's own containers with their requests,
// so an injected sidecar's background work doesn't keep it awake. Cost
// counts the whole pod's requests, sidecars included, since that's what the
// workload reserves and what pausing frees.
type metricsReader interface {
	WorkloadCPUMillis(ctx context.Context, workload *v1alpha1.ManagedWorkload) (cpuMillis float64, pods int, err error)
	CPURequestPerReplica(ctx context.Context, workload *v1alpha1.ManagedWorkload) (float64, error)
	PodRequestsPerReplica(ctx context.Context, workload *v1alpha1.ManagedWorkload) (cpuMillis, memBytes float64, err error)
	Replicas(ctx context.Context, workload *v1alpha1.ManagedWorkload) (int32, error)
	TotalPVCBytes(ctx context.Context, workload *v1alpha1.ManagedWorkload) (float64, error)
}

// nodePooler reads the node pools a workload's pods run in.
type nodePooler interface {
	NodePools(ctx context.Context, workload *v1alpha1.ManagedWorkload) ([]v1alpha1.NodePoolPods, error)
}

// listPricer prices a workload at the nodes it runs on.
type listPricer interface {
	ListRates(ctx context.Context, workload *v1alpha1.ManagedWorkload) (cost.Rates, v1alpha1.ListPrice, error)
}

// engineRegistry holds each workload's forecast engine between reconciles,
// and the hour of demand it is seeing. The engine in memory is the
// authority while the operator runs; the state it exports to the workload's
// status is where a restarted operator, or a new leader, picks up. The hour
// under way is in memory only. Engines are keyed by UID, so a workload
// deleted and created again under the same name starts afresh.
type engineRegistry struct {
	mu        sync.Mutex
	engines   map[types.UID]forecaster
	hours     map[types.UID]forecast.Hour
	exports   map[types.UID]exportedState
	outlooks  map[types.UID]projectedOutlook
	seeds     map[types.UID]historySeed
	newEngine func() forecaster
	restore   func(state string) (forecaster, error)
}

// exportedState is the state an engine last exported, and the data points
// it had then. Only an observation changes what an engine has learned, and
// each adds a data point, so the state stands until the count moves.
type exportedState struct {
	engine     forecaster
	dataPoints int
	state      string
}

func newEngineRegistry(newEngine func() forecaster) *engineRegistry {
	return &engineRegistry{
		engines:   make(map[types.UID]forecaster),
		hours:     make(map[types.UID]forecast.Hour),
		exports:   make(map[types.UID]exportedState),
		outlooks:  make(map[types.UID]projectedOutlook),
		seeds:     make(map[types.UID]historySeed),
		newEngine: newEngine,
		restore: func(state string) (forecaster, error) {
			e, err := forecast.ImportEngine(state, forecast.Settings{})
			if err != nil {
				return nil, err
			}
			return e, nil
		},
	}
}

// getOrCreate returns the workload's engine with settings applied, restoring
// it from state the first time. State that can't be restored is discarded
// for a new engine, and the error says why.
func (reg *engineRegistry) getOrCreate(uid types.UID, settings forecast.Settings, state string) (forecaster, error) {
	reg.mu.Lock()
	defer reg.mu.Unlock()

	e, ok := reg.engines[uid]
	var restoreErr error
	if !ok {
		e, restoreErr = reg.load(state)
		reg.engines[uid] = e
	}
	e.Configure(settings)
	return e, restoreErr
}

func (reg *engineRegistry) load(state string) (forecaster, error) {
	if state == "" {
		return reg.newEngine(), nil
	}
	e, err := reg.restore(state)
	if err != nil {
		return reg.newEngine(), fmt.Errorf("restoring forecast state: %w", err)
	}
	return e, nil
}

// forget drops a workload's engine, once the workload is gone.
func (reg *engineRegistry) forget(uid types.UID) {
	reg.mu.Lock()
	delete(reg.engines, uid)
	delete(reg.hours, uid)
	delete(reg.exports, uid)
	delete(reg.outlooks, uid)
	delete(reg.seeds, uid)
	reg.mu.Unlock()
}

// projectedOutlook is the outlook an engine last gave, with the data points
// and threshold it had then: projecting it runs the engine on for weeks,
// so it's done again only once either changes.
type projectedOutlook struct {
	engine     forecaster
	dataPoints int
	threshold  int
	outlook    forecast.Outlook
}

// outlook is the workload's engine's outlook, at the threshold its
// settings give it.
func (reg *engineRegistry) outlook(uid types.UID, engine forecaster, threshold int) forecast.Outlook {
	points := engine.GetDataPoints()
	reg.mu.Lock()
	last, ok := reg.outlooks[uid]
	reg.mu.Unlock()
	if ok && last.engine == engine && last.dataPoints == points && last.threshold == threshold {
		return last.outlook
	}
	o := engine.Outlook()
	reg.mu.Lock()
	reg.outlooks[uid] = projectedOutlook{engine: engine, dataPoints: points, threshold: threshold, outlook: o}
	reg.mu.Unlock()
	return o
}

// export is what the workload's engine has learned, encoded for its status.
// Encoding compresses the whole state, so it's done again only once the
// engine has observed another hour.
func (reg *engineRegistry) export(uid types.UID, engine forecaster) (string, error) {
	points := engine.GetDataPoints()
	reg.mu.Lock()
	last, ok := reg.exports[uid]
	reg.mu.Unlock()
	if ok && last.engine == engine && last.dataPoints == points {
		return last.state, nil
	}
	state, err := engine.Export()
	if err != nil {
		return "", err
	}
	reg.mu.Lock()
	reg.exports[uid] = exportedState{engine: engine, dataPoints: points, state: state}
	reg.mu.Unlock()
	return state, nil
}

// see adds a look at a workload's demand, made at now in the hour that
// began at start. When the look is the first since an hour ended, that
// hour is returned, if it was seen whole.
func (reg *engineRegistry) see(uid types.UID, start, now time.Time, demand float64, visible bool) (forecast.Hour, bool) {
	reg.mu.Lock()
	defer reg.mu.Unlock()

	h := reg.hours[uid]
	ended, over := h.Look(start, now, demand, visible)
	reg.hours[uid] = h
	return ended, over && ended.Whole(unobservedGap)
}

func (r *Reconciler) reconcileAutomation(ctx context.Context, workload *v1alpha1.ManagedWorkload, target client.Object) (*ctrl.Result, error) {
	phase := workload.Status.Phase
	if phase != v1alpha1.PhasePaused && phase != v1alpha1.PhaseRunning && phase != v1alpha1.PhaseIdle {
		return nil, nil
	}

	if err := r.checkDependenciesExist(ctx, workload); err != nil {
		return nil, err
	}
	switch {
	case workload.Spec.DryRun:
		r.endBackOff(workload, reasonDryRun)
	case workload.Spec.IdlePolicy == nil:
		r.endBackOff(workload, reasonNoIdlePolicy)
	}
	if phase == v1alpha1.PhasePaused {
		return r.reconcileWake(ctx, workload, target)
	}

	logger := log.FromContext(ctx)
	engine := r.forecastEngine(workload)

	// The forecast learns whether or not automation acts on it, so every
	// awake workload is looked at as often as the activity clock looks,
	// for the forecast to see each hour's busiest minutes.
	var load cpuLoad
	if r.metrics != nil {
		usage, pods, err := r.observedCPU(ctx, workload)
		if err != nil {
			r.clearIdleVeto(workload)
			if workload.Spec.IdlePolicy != nil {
				r.observeActivity(ctx, workload, target, cpuLoad{err: err})
			}
			return r.reportMetricsUnavailable(ctx, workload, target, err)
		}
		load.percent, load.err = r.utilization(ctx, workload, usage, pods)
		if load.err == nil {
			r.setCondition(workload, conditionMetricsAvailable, metav1.ConditionTrue, "MetricsReported", "")
		}
		r.seeDemand(ctx, workload, engine, r.awakeDemand(workload, load.percent), load.err == nil)
	}
	r.updatePredictionStatus(ctx, workload, engine)

	// Under dry-run, Idle is a would-be pause being measured, which a pause
	// request begins whatever the idle policy, and only activity ends.
	measuring := workload.Spec.DryRun && phase == v1alpha1.PhaseIdle
	if workload.Spec.IdlePolicy == nil && !measuring {
		r.clearIdleVeto(workload)
		if phase == v1alpha1.PhaseIdle {
			if err := r.transition(ctx, workload, v1alpha1.PhaseRunning, reasonNoIdlePolicy); err != nil {
				return nil, err
			}
		}
		return &ctrl.Result{RequeueAfter: activityCheckInterval}, nil
	}

	logger.V(1).Info("automation tick", "workload", workload.Name, "namespace", workload.Namespace,
		"engine_phase", engine.GetPhase(), "data_points", engine.GetDataPoints())
	return r.reconcileIdleClock(ctx, workload, target, engine, load)
}

// cpuLoad is what one look at an awake workload's CPU found: its
// utilization, or why it couldn't be worked out.
type cpuLoad struct {
	percent float64
	err     error
}

// utilization is CPU usage as a percentage of what the workload's own
// containers request in the pods it was measured over. Divided by every
// replica's request, the usage of one pod serving while the others are
// Pending or crash-looping would read as a fraction of itself, and an
// in-use workload as idle. The activity threshold, the
// forecast and every decision made on it share this one measure, so a
// forecast that holds an idle workload awake would wake it once paused.
// A target scaled to zero by something else is idle, though its pods may
// report usage for the seconds they take to terminate.
func (r *Reconciler) utilization(ctx context.Context, workload *v1alpha1.ManagedWorkload, usage float64, pods int) (float64, error) {
	if usage == 0 {
		return 0, nil
	}
	replicas, err := r.metrics.Replicas(ctx, workload)
	if err != nil {
		return 0, fmt.Errorf("reading replicas: %w", err)
	}
	if replicas == 0 {
		return 0, nil
	}
	perReplica, err := r.metrics.CPURequestPerReplica(ctx, workload)
	if errors.Is(err, opmetrics.ErrNoCPURequests) {
		return 0, fmt.Errorf("%w: %w", errNoCPURequests, err)
	}
	if err != nil {
		return 0, fmt.Errorf("reading CPU requests: %w", err)
	}
	requested := perReplica * float64(pods)
	if requested <= 0 {
		return 0, errNoCPURequests
	}
	return usage / requested * 100, nil
}

// reconcileWake resumes a paused workload when a wake request or an
// activity annotation asks for it, a Prometheus activity query finds
// activity, or ahead of the demand a confident forecast predicts. Otherwise
// it's looked at again when a query or that forecast could next change its
// mind.
func (r *Reconciler) reconcileWake(ctx context.Context, workload *v1alpha1.ManagedWorkload, target client.Object) (*ctrl.Result, error) {
	recheck := &ctrl.Result{RequeueAfter: r.pausedRecheck(workload)}
	r.clearIdleVeto(workload)
	recordIdleSeconds(workload, r.now())
	engine := r.forecastEngine(workload)
	woken := r.wokenByActivity(workload, target)
	requested := r.takeWakeRequest(workload)
	woken = woken || requested
	var queries pausedQueries
	if !woken {
		queries = r.checkPausedQueries(ctx, workload)
	}
	r.seePausedDemand(ctx, workload, engine, woken, queries)
	r.updatePredictionStatus(ctx, workload, engine)

	if requested {
		return r.handleResume(ctx, workload, &phaseEvent{reason: ReasonWakeRequested, message: wakeRequestedMessage,
			cause: v1alpha1.WokenByWakeRequest})
	}
	if woken {
		why := &phaseEvent{reason: ReasonWokeByActivity, message: "activity annotation is newer than the pause, waking",
			cause: v1alpha1.WokenByAnnotation}
		if wakeSource(workload) == v1alpha1.ActivitySourceRequest {
			why.cause = requestCause(workload)
			why.message = "a request is waiting, waking"
			if why.cause == v1alpha1.WokenByClick {
				why.message = "a person asked to wake it, waking"
			}
		}
		return r.handleResume(ctx, workload, why)
	}
	r.endBackOffIfHeld(workload)
	if queries.wake {
		r.recordQueryWake(workload)
		return r.handleResume(ctx, workload, &phaseEvent{reason: ReasonWokeByActivity, message: queryWakeMessage})
	}

	if workload.Spec.IdlePolicy == nil || !workload.Spec.IdlePolicy.AutoResume {
		return recheck, nil
	}
	threshold := cpuThresholdFor(workload)
	wake, predicted, nextHour := forecast.WakeAhead(engine, r.now(), r.Timezone, threshold)
	if !wake {
		return recheck, nil
	}
	when := "this hour"
	if nextHour {
		when = "next hour"
	}

	return r.handleResume(ctx, workload, &phaseEvent{reason: ReasonAutoResume, message: fmt.Sprintf(
		"forecast expects %.0f%% utilization %s (threshold %s%%), waking ahead of demand", predicted, when,
		v1alpha1.FormatCPUThreshold(threshold))})
}

// requestCause is what a request the doorman stamped is: a person's click,
// for a workload that asks before waking, or any request.
func requestCause(workload *v1alpha1.ManagedWorkload) v1alpha1.WakeCause {
	if workload.Spec.Wake.Asks() {
		return v1alpha1.WokenByClick
	}
	return v1alpha1.WokenByRequest
}

func (r *Reconciler) untilNextHour(now time.Time) time.Duration {
	return forecast.HourStart(now, r.Timezone).Add(time.Hour).Sub(now)
}

// pausedRecheck is when a paused workload is next looked at: when autoResume
// could next decide differently, forecast.AutoResumeLead before the hour and
// on it, and at least every statusFlushInterval, so the forecast learns each
// quiet hour and savings accrue steadily. One with Prometheus activity
// queries is looked at as often as an awake one, so a query wakes it within
// a minute of finding activity.
func (r *Reconciler) pausedRecheck(workload *v1alpha1.ManagedWorkload) time.Duration {
	wait := statusFlushInterval
	if hasPrometheusActivity(workload) {
		wait = activityCheckInterval
	}
	next := r.untilNextHour(r.now())
	for _, d := range []time.Duration{next - forecast.AutoResumeLead, next} {
		if d > 0 && d < wait {
			wait = d
		}
	}
	return wait
}

// seePausedDemand is a look at a paused workload's demand. Behind the
// doorman, any request would have woken it, and a Prometheus activity
// query shows any activity it measures, so a look that finds it still
// paused, and its queries quiet, found no demand, and one that finds a
// request waking it, or a query showing activity, found some. The doorman's
// watch stands while the queries can't be evaluated: a request would still
// have woken it. Without the doorman, a query that can't be evaluated
// leaves the look blind, and with neither, it can't see demand.
//
// With wake.mode Ask, only people wake it, with a click on the doorman's
// asleep page, a wake request or an activity annotation, so behind the
// doorman a look that finds it still paused found nobody needed it. But
// the doorman then sees only people, not the activity the queries measure,
// so its watch doesn't stand while they can't be evaluated: an outage of
// Prometheus mustn't teach quiet hours, any more than one of the doorman.
func (r *Reconciler) seePausedDemand(ctx context.Context, workload *v1alpha1.ManagedWorkload, engine forecaster,
	woken bool, queries pausedQueries) {
	routed := meta.IsStatusConditionTrue(workload.Status.Conditions, conditionWakeOnRequest)
	busy := woken || queries.active
	var demand float64
	if busy {
		demand = forecast.RequestDemand(cpuThresholdFor(workload))
	}
	visible := busy || routed || queries.watched
	if workload.Spec.Wake.Asks() && queries.failed {
		visible = busy
	}
	r.seeDemand(ctx, workload, engine, demand, visible)
}

// awakeDemand is the demand an awake workload's utilization shows, counting
// its warm-up from its last wake.
func (r *Reconciler) awakeDemand(workload *v1alpha1.ManagedWorkload, percent float64) float64 {
	var woke time.Time
	if w := workload.Status.LastActedAt; w != nil {
		woke = w.Time
	}
	return forecast.AwakeDemand(r.now(), woke, r.Timezone, percent, cpuThresholdFor(workload),
		func(since time.Time) bool { return r.usedSince(workload, since) })
}

// usedSince reports whether the activity clock last saw the workload in use
// at or after since. The clock's own restarts, on creation, a wake, a
// rollout or a gap in watching, aren't use.
func (r *Reconciler) usedSince(workload *v1alpha1.ManagedWorkload, since time.Time) bool {
	clock, ok := r.activityMemo.get(workload.UID)
	if !ok {
		if workload.Status.Activity == nil {
			return false
		}
		clock = *workload.Status.Activity
	}
	switch clock.LastActivitySource {
	case v1alpha1.ActivitySourceCPU, v1alpha1.ActivitySourceAnnotation, v1alpha1.ActivitySourcePrometheus:
		return !clock.LastActivityTime.Time.Before(since)
	}
	return false
}

// seeDemand adds a look at the workload's demand to the hour under way, and
// once an hour is over, feeds the forecast the peak seen in it.
func (r *Reconciler) seeDemand(ctx context.Context, workload *v1alpha1.ManagedWorkload, engine forecaster, demand float64, visible bool) {
	now := r.now()
	hour, ended := r.engines.see(workload.UID, forecast.HourStart(now, r.Timezone), now, demand, visible)
	if !ended || engine.Observed(hour.Start) {
		return
	}
	r.observeHour(ctx, workload, engine, hour)
}

// observeHour feeds the forecast an hour that has ended. A rejected
// observation means a bad metric: it's logged, and the hour is skipped
// rather than holding up the workload's automation.
func (r *Reconciler) observeHour(ctx context.Context, workload *v1alpha1.ManagedWorkload, engine forecaster, hour forecast.Hour) {
	prediction, err := engine.Observe(hour.Peak, hour.Start)
	if err != nil {
		log.FromContext(ctx).Error(err, "feeding the forecast",
			"workload", workload.Name, "namespace", workload.Namespace, "cpu_percent", hour.Peak)
		return
	}
	r.emitEvent(workload, false, "Normal", ReasonPredictionFed, actionForecast,
		"the hour from %s peaked at %.0f%% of the CPU requested, forecast %.0f%%, phase %s",
		hour.Start.UTC().Format("15:04 UTC"), hour.Peak, prediction, engine.GetPhase())

	if engine.RegimeChanged() {
		opmetrics.PredictionRegimeChanges.WithLabelValues(workload.Namespace, workload.Name).Inc()
		r.emitEvent(workload, false, "Warning", ReasonRegimeChange, actionForecast,
			"regime change detected, prediction engine demoted to %s", engine.GetPhase())
	}
	if engine.AnomalyDetected() {
		opmetrics.PredictionAnomalies.WithLabelValues(workload.Namespace, workload.Name).Inc()
	}
}

// observedCPU reads total CPU usage to feed the forecast. A target scaled to
// zero has no pods and therefore no pod metrics, but that is a real
// observation of zero demand, not missing data.
func (r *Reconciler) observedCPU(ctx context.Context, workload *v1alpha1.ManagedWorkload) (millis float64, pods int, err error) {
	millis, pods, err = r.metrics.WorkloadCPUMillis(ctx, workload)
	if !errors.Is(err, opmetrics.ErrNoPodMetrics) {
		return millis, pods, err
	}
	if replicas, rerr := r.metrics.Replicas(ctx, workload); rerr == nil && replicas == 0 {
		return 0, 0, nil
	}
	return 0, 0, err
}

const (
	reasonNoPodMetrics = "NoPodMetrics"

	// podMetricsGrace is how long a workload's Ready pods may go without
	// metrics before that's reported as a problem. metrics-server reports
	// a new pod's CPU once it has scraped it twice, which at its slowest
	// default resolution, a minute, takes two, and the kubelet's own
	// collection adds a little more.
	podMetricsGrace = 3 * time.Minute

	metricsRecheck = time.Minute
)

// reportMetricsUnavailable surfaces why CPU can't be read. Both idle
// detection and the forecast depend on it, so without this the workload would
// sit awake indefinitely with nothing to say why.
func (r *Reconciler) reportMetricsUnavailable(ctx context.Context, workload *v1alpha1.ManagedWorkload,
	target client.Object, err error) (*ctrl.Result, error) {
	reason := "MetricsUnavailable"
	msg := fmt.Sprintf("cannot read CPU usage, so idle detection and the forecast are paused: %v", err)
	switch {
	case errors.Is(err, opmetrics.ErrNoPodMetrics):
		if wait, awaiting := r.awaitPodMetrics(workload, target); awaiting {
			log.FromContext(ctx).V(1).Info("waiting for new pods' metrics",
				"workload", workload.Name, "namespace", workload.Namespace, "error", err.Error())
			return &ctrl.Result{RequeueAfter: wait}, nil
		}
		reason = reasonNoPodMetrics
		msg = fmt.Sprintf("the target's Ready pods have had no metrics for over %s, so idle detection and the "+
			"forecast are paused; check that metrics-server is installed and reporting them: kubectl top pods -n %s",
			human.Duration(podMetricsGrace), workload.Namespace)
	case errors.Is(err, errNoCPURequests):
		reason = "NoCPURequests"
		msg = "the target's containers request no CPU, in its running pods or, with none running, in its pod template, " +
			"so CPU activity can't be measured and idle detection is paused; set CPU requests on its containers, " +
			"or a default in the namespace's LimitRange"
	}

	firstFailure := !meta.IsStatusConditionFalse(workload.Status.Conditions, conditionMetricsAvailable)
	r.setCondition(workload, conditionMetricsAvailable, metav1.ConditionFalse, reason, msg)
	if firstFailure {
		r.emitEvent(workload, false, "Warning", reason, actionForecast, "%s", msg)
	}
	log.FromContext(ctx).V(1).Info("CPU usage unavailable",
		"workload", workload.Name, "namespace", workload.Namespace, "reason", reason, "error", err.Error())

	return &ctrl.Result{RequeueAfter: metricsRecheck}, nil
}

// awaitPodMetrics reports whether the target's pods may yet report the
// metrics they don't have, and says so on MetricsAvailable, Unknown, with
// no warning, returning when to look again. A pod has none until it runs,
// and for a scrape or two after, so for the minutes a wake or a rollout
// takes, missing metrics are no fault of metrics-server's. Ready pods that
// go on without them past podMetricsGrace are, and so is a problem already
// reported, until metrics are read again.
func (r *Reconciler) awaitPodMetrics(workload *v1alpha1.ManagedWorkload, target client.Object) (time.Duration, bool) {
	if conditionFalseWith(workload, conditionMetricsAvailable, reasonNoPodMetrics) {
		return 0, false
	}
	if lifecycle.ReadyReplicas(target) == 0 {
		r.restartCondition(workload, conditionMetricsAvailable, metav1.ConditionUnknown, "NoReadyPods",
			"none of the target's pods is Ready yet, so there's no CPU to measure; idle detection and the forecast "+
				"wait for them")
		return metricsRecheck, true
	}
	r.restartCondition(workload, conditionMetricsAvailable, metav1.ConditionUnknown, "AwaitingPodMetrics",
		"the target's pods have no metrics yet, which metrics-server reports a scrape or two after a pod starts; "+
			"idle detection and the forecast wait for them")
	since := meta.FindStatusCondition(workload.Status.Conditions, conditionMetricsAvailable).LastTransitionTime
	left := podMetricsGrace - r.now().Sub(since.Time)
	if left <= 0 {
		return 0, false
	}
	return min(metricsRecheck, left), true
}

// restartCondition sets a condition whose LastTransitionTime says how long
// it has held for its reason, not only its status: one that changes reason
// is set afresh.
func (r *Reconciler) restartCondition(workload *v1alpha1.ManagedWorkload, condType string,
	status metav1.ConditionStatus, reason, message string) {
	if c := meta.FindStatusCondition(workload.Status.Conditions, condType); c != nil && c.Reason != reason {
		meta.RemoveStatusCondition(&workload.Status.Conditions, condType)
	}
	r.setCondition(workload, condType, status, reason, message)
}
