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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/cost"
	"github.com/napwake/napwake/internal/metrics"
)

func costWorkload(phase v1alpha1.WorkloadPhase) *v1alpha1.ManagedWorkload {
	return &v1alpha1.ManagedWorkload{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default"},
		Spec: v1alpha1.ManagedWorkloadSpec{
			Target:       v1alpha1.WorkloadRef{Kind: v1alpha1.TargetKindDeployment, Name: "api"},
			Prediction:   v1alpha1.PredictionSpec{Confidence: 85},
			CostTracking: &v1alpha1.CostTrackingSpec{},
		},
		Status: v1alpha1.ManagedWorkloadStatus{Phase: phase},
	}
}

// costClock is a reconciler on a clock the test moves.
func costClock(start time.Time, m *stubMetrics) (*Reconciler, *time.Time) {
	now := start
	r := &Reconciler{clock: func() time.Time { return now }}
	if m != nil {
		r.metrics = m
	}
	return r, &now
}

func costReconciler(now time.Time, m *stubMetrics) *Reconciler {
	r, _ := costClock(now, m)
	return r
}

func accumulatedAt(t time.Time) *v1alpha1.CostStatus {
	return &v1alpha1.CostStatus{LastAccumulatedAt: &metav1.Time{Time: t}}
}

func pausedWith(w *v1alpha1.ManagedWorkload, rs v1alpha1.ResourceSnapshot) {
	w.Status.Pause = &v1alpha1.PauseStatus{PreviousReplicas: rs.Replicas, Resources: &rs}
}

func dollars(t *testing.T, s string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(strings.TrimPrefix(s, "$"), 64)
	require.NoError(t, err, "a dollar figure: %q", s)
	return v
}

// flushFor accumulates a workload's cost at every status flush for d.
func flushFor(r *Reconciler, now *time.Time, w *v1alpha1.ManagedWorkload, d time.Duration) {
	for end := now.Add(d); now.Before(end); {
		*now = now.Add(statusFlushInterval)
		r.accumulateCost(context.Background(), w)
	}
}

func TestAccumulateCost_SkipsWhenNoMetrics(t *testing.T) {
	w := costWorkload(v1alpha1.PhaseRunning)

	costReconciler(fixedTime, nil).accumulateCost(context.Background(), w)

	assert.Nil(t, w.Status.Cost)
}

func TestAccumulateCost_StartsTracking(t *testing.T) {
	w := costWorkload(v1alpha1.PhaseRunning)

	costReconciler(fixedTime, &stubMetrics{cpuPerReplica: 1000}).accumulateCost(context.Background(), w)

	require.NotNil(t, w.Status.Cost)
	assert.True(t, w.Status.Cost.LastAccumulatedAt.Time.Equal(fixedTime))
	assert.Zero(t, w.Status.Cost.Tracked.Duration)
	assert.Equal(t, "$0.00", w.Status.Cost.CostThisMonth)
}

// An awake workload costs what its replicas request, sidecars included,
// whatever they happen to use.
func TestAccumulateCost_AwakeIsPricedOnRequests(t *testing.T) {
	w := costWorkload(v1alpha1.PhaseRunning)
	w.Status.Cost = accumulatedAt(fixedTime.Add(-time.Hour))
	m := &stubMetrics{cpuMillis: 5, cpuPerReplica: 500, memoryPerReplica: 2 * bytesPerGiB, replicas: 3, pvcBytes: 10 * bytesPerGiB}

	costReconciler(fixedTime, m).accumulateCost(context.Background(), w)

	c := w.Status.Cost
	assert.InDelta(t, 1.5, c.AwakeCPUHours.AsApproximateFloat64(), 1e-9, "3 replicas of 500m for an hour")
	assert.InDelta(t, 6, c.AwakeMemoryHours.AsApproximateFloat64(), 1e-9)
	assert.InDelta(t, 10, c.StorageHours.AsApproximateFloat64(), 1e-9)
	assert.Zero(t, c.PausedCPUHours.AsApproximateFloat64())
	assert.Equal(t, time.Hour, c.Tracked.Duration)
	require.NotNil(t, c.Running)
	assert.Equal(t, v1alpha1.ResourceSnapshot{Replicas: 3, CPUMillis: 500, MemoryBytes: 2 * bytesPerGiB,
		StorageBytes: 10 * bytesPerGiB}, *c.Running)
}

func TestAccumulateCost_PausedFreesComputeButNotStorage(t *testing.T) {
	w := costWorkload(v1alpha1.PhasePaused)
	w.Status.Cost = accumulatedAt(fixedTime.Add(-time.Hour))
	pausedWith(w, v1alpha1.ResourceSnapshot{Replicas: 3, CPUMillis: 500, MemoryBytes: 2 * bytesPerGiB,
		StorageBytes: 20 * bytesPerGiB})

	costReconciler(fixedTime, &stubMetrics{}).accumulateCost(context.Background(), w)

	c := w.Status.Cost
	assert.Zero(t, c.AwakeCPUHours.AsApproximateFloat64(), "a paused workload runs nothing")
	assert.InDelta(t, 1.5, c.PausedCPUHours.AsApproximateFloat64(), 1e-9)
	assert.InDelta(t, 6, c.PausedMemoryHours.AsApproximateFloat64(), 1e-9)
	assert.InDelta(t, 20, c.StorageHours.AsApproximateFloat64(), 1e-9, "its claims still cost while it's paused")
	assert.Equal(t, cost.FormatDollars(1.5*0.031+6*0.004), c.FreedThisMonth)
	require.NotNil(t, c.ResourceReduction)
	assert.Equal(t, int64(1500), c.ResourceReduction.CPUMillis)
}

// Totals are kept to a billionth of an hour, so they add up to what the
// workload ran however small it is: rounding each five-minute flush to the
// cent, or to a milli-unit, used to drop them to nothing.
func TestAccumulateCost_FullPrecisionOverADayOfFlushes(t *testing.T) {
	const tolerance = 0.001
	tests := []struct {
		name      string
		phase     v1alpha1.WorkloadPhase
		cpuMillis int64
		memBytes  int64
	}{
		{name: "1 vCPU and 2 GiB paused", phase: v1alpha1.PhasePaused, cpuMillis: 1000, memBytes: 2 * bytesPerGiB},
		{name: "10m and 64Mi paused", phase: v1alpha1.PhasePaused, cpuMillis: 10, memBytes: 64 << 20},
		{name: "10m and 64Mi running", phase: v1alpha1.PhaseRunning, cpuMillis: 10, memBytes: 64 << 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := costWorkload(tt.phase)
			w.Status.Cost = accumulatedAt(fixedTime)
			pausedWith(w, v1alpha1.ResourceSnapshot{Replicas: 1, CPUMillis: tt.cpuMillis, MemoryBytes: tt.memBytes})
			r, now := costClock(fixedTime, &stubMetrics{cpuPerReplica: float64(tt.cpuMillis),
				memoryPerReplica: float64(tt.memBytes), replicas: 1})

			flushFor(r, now, w, 24*time.Hour)

			c := w.Status.Cost
			wantCPU := float64(tt.cpuMillis) / 1000 * 24
			wantMem := float64(tt.memBytes) / bytesPerGiB * 24
			cpu, mem := c.PausedCPUHours, c.PausedMemoryHours
			if tt.phase != v1alpha1.PhasePaused {
				cpu, mem = c.AwakeCPUHours, c.AwakeMemoryHours
			}
			assert.InEpsilon(t, wantCPU, cpu.AsApproximateFloat64(), tolerance)
			assert.InEpsilon(t, wantMem, mem.AsApproximateFloat64(), tolerance)
			assert.Equal(t, 24*time.Hour, c.Tracked.Duration)
			if tt.phase == v1alpha1.PhasePaused {
				want := cost.ComputeHourly(float64(tt.cpuMillis)/1000, float64(tt.memBytes)/bytesPerGiB, cost.DefaultRates) * 24
				assert.Equal(t, cost.FormatDollars(want), c.FreedThisMonth)
			}
		})
	}
}

// Cost and what pausing freed are on the same basis, requests, over the
// same time, so they add up to what the workload would have cost without
// Napwake.
func TestAccumulateCost_CostAndFreedAddUp(t *testing.T) {
	w := costWorkload(v1alpha1.PhaseRunning)
	w.Status.Cost = accumulatedAt(fixedTime)
	snapshot := v1alpha1.ResourceSnapshot{Replicas: 2, CPUMillis: 1000, MemoryBytes: 4 * bytesPerGiB}
	r, now := costClock(fixedTime, &stubMetrics{cpuPerReplica: 1000, memoryPerReplica: 4 * bytesPerGiB, replicas: 2})

	flushFor(r, now, w, 8*time.Hour)
	w.Status.Phase = v1alpha1.PhasePaused
	pausedWith(w, snapshot)
	flushFor(r, now, w, 16*time.Hour)

	c := w.Status.Cost
	hourly := cost.ComputeHourly(2, 8, cost.DefaultRates)
	assert.Equal(t, cost.FormatDollars(8*hourly), c.CostThisMonth)
	assert.Equal(t, cost.FormatDollars(16*hourly), c.FreedThisMonth)
	assert.Equal(t, cost.FormatDollars(24*hourly), c.CostWithoutNapwakeThisMonth)
	assert.InDelta(t, dollars(t, c.CostThisMonth)+dollars(t, c.FreedThisMonth),
		dollars(t, c.CostWithoutNapwakeThisMonth), 0.01)
}

// A workload created late in the month is projected from the time it has
// been tracked, at the rate it has run, not from the day of the month.
func TestAccumulateCost_ProjectsFromTheTimeTracked(t *testing.T) {
	march20 := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	w := costWorkload(v1alpha1.PhaseRunning)
	r, now := costClock(march20, &stubMetrics{cpuPerReplica: 1000, replicas: 1})
	r.accumulateCost(context.Background(), w)

	flushFor(r, now, w, 23*time.Hour)
	assert.Equal(t, "pending", w.Status.Cost.ProjectedMonthlyCost, "less than a day tracked")

	flushFor(r, now, w, time.Hour)
	assert.Equal(t, cost.FormatDollars(0.031*24*31), w.Status.Cost.ProjectedMonthlyCost, "a vCPU for all of March")
	assert.Equal(t, "$0.00", w.Status.Cost.ProjectedMonthlyFreed)
}

// The interval that spans the start of a month counts toward the new
// month from its first instant, in UTC.
func TestAccumulateCost_MonthRolloverKeepsTheNewMonthsShare(t *testing.T) {
	w := costWorkload(v1alpha1.PhasePaused)
	w.Status.Cost = accumulatedAt(time.Date(2026, 2, 28, 23, 50, 0, 0, time.UTC))
	w.Status.Cost.PausedCPUHours = resource.MustParse("500")
	w.Status.Cost.ListRates, w.Status.Cost.ListPrice = recordedRates(), v1alpha1.ListPriceSpot
	pausedWith(w, v1alpha1.ResourceSnapshot{Replicas: 1, CPUMillis: 1000})

	costReconciler(time.Date(2026, 3, 1, 0, 5, 0, 0, time.UTC), &stubMetrics{}).accumulateCost(context.Background(), w)

	c := w.Status.Cost
	assert.InDelta(t, 5.0/60, c.PausedCPUHours.AsApproximateFloat64(), 1e-9, "last month's totals reset, the 5 minutes of March kept")
	assert.Equal(t, 5*time.Minute, c.Tracked.Duration)
	assert.NotNil(t, c.ListRates, "where it ran carries over")
	assert.Equal(t, v1alpha1.ListPriceSpot, c.ListPrice, "and at which prices")
}

// A gap longer than cost.MaxInterval, such as operator downtime, counts as
// cost.MaxInterval, and so does the time tracked, keeping projections true.
func TestAccumulateCost_DowntimeCountsAsTheLimit(t *testing.T) {
	w := costWorkload(v1alpha1.PhasePaused)
	w.Status.Cost = accumulatedAt(fixedTime.Add(-10 * time.Hour))
	pausedWith(w, v1alpha1.ResourceSnapshot{Replicas: 1, CPUMillis: 1000})

	costReconciler(fixedTime, &stubMetrics{}).accumulateCost(context.Background(), w)

	assert.InDelta(t, cost.MaxInterval.Hours(), w.Status.Cost.PausedCPUHours.AsApproximateFloat64(), 1e-9)
	assert.Equal(t, cost.MaxInterval, w.Status.Cost.Tracked.Duration)
	assert.True(t, w.Status.Cost.LastAccumulatedAt.Time.Equal(fixedTime))
}

// Cost is brought up to date only when status is due a flush: between
// flushes it would be dropped unwritten.
func TestAccumulateCost_WaitsForTheFlush(t *testing.T) {
	w := costWorkload(v1alpha1.PhaseRunning)
	w.Status.Cost = accumulatedAt(fixedTime.Add(-time.Minute))

	costReconciler(fixedTime, &stubMetrics{cpuPerReplica: 1000}).accumulateCost(context.Background(), w)

	assert.True(t, w.Status.Cost.LastAccumulatedAt.Time.Equal(fixedTime.Add(-time.Minute)))
	assert.Zero(t, w.Status.Cost.AwakeCPUHours.AsApproximateFloat64())
}

// A wake counts the pause as paused right up to the wake, and what follows
// as awake: the paused gap isn't billed as running.
func TestTransition_SettlesCostInThePhaseItWasSpentIn(t *testing.T) {
	w := costWorkload(v1alpha1.PhasePaused)
	w.Status.Cost = accumulatedAt(fixedTime.Add(-4 * time.Minute))
	pausedWith(w, v1alpha1.ResourceSnapshot{Replicas: 2, CPUMillis: 1000, MemoryBytes: bytesPerGiB})
	r := newTestReconciler(t, w, &stubPauser{})
	now := fixedTime
	r.clock = func() time.Time { return now }
	r.metrics = &stubMetrics{cpuPerReplica: 1000, memoryPerReplica: bytesPerGiB, replicas: 2}
	ctx := context.Background()

	err := r.transition(ctx, w, v1alpha1.PhaseResuming, "ResumeRequested")
	require.NoError(t, err)
	w.Status.Pause = nil
	now = now.Add(time.Minute)
	err = r.transition(ctx, w, v1alpha1.PhaseRunning, "Resumed")
	require.NoError(t, err)
	now = now.Add(statusFlushInterval)
	r.accumulateCost(ctx, w)

	c := getWorkload(t, r, "api").Status.Cost
	require.NotNil(t, c, "settled cost is written with the transition")
	assert.InDelta(t, 2*4.0/60, c.PausedCPUHours.AsApproximateFloat64(), 1e-9, "paused until the wake")
	assert.InDelta(t, 2*1.0/60, c.AwakeCPUHours.AsApproximateFloat64(), 1e-9, "the minute spent resuming")
	assert.InDelta(t, 2*6.0/60, w.Status.Cost.AwakeCPUHours.AsApproximateFloat64(), 1e-9, "and running since")
}

// A transition settles the time before it in the phase it was spent in,
// even when the replicas can't be read then: left unsettled, the minutes
// awake before a pause would be counted as paused, freed rather than
// spent.
func TestTransition_SettlesAtTheLastReadWhenReplicasCantBeRead(t *testing.T) {
	w := costWorkload(v1alpha1.PhaseRunning)
	w.Status.Cost = accumulatedAt(fixedTime.Add(-4 * time.Minute))
	w.Status.Cost.Running = &v1alpha1.ResourceSnapshot{Replicas: 2, CPUMillis: 1000, MemoryBytes: bytesPerGiB}
	w.Status.Cost.PricedAt = &metav1.Time{Time: fixedTime}
	r := newTestReconciler(t, w, &stubPauser{})
	r.metrics = &stubMetrics{err: errors.New("the API server timed out")}

	require.NoError(t, r.transition(context.Background(), w, v1alpha1.PhasePausing, "PauseRequested"))

	c := w.Status.Cost
	assert.True(t, c.LastAccumulatedAt.Time.Equal(fixedTime), "settled up to the transition")
	assert.InDelta(t, 2*4.0/60, c.AwakeCPUHours.AsApproximateFloat64(), 1e-9, "counted awake, at the last read")
	assert.Zero(t, c.PausedCPUHours.AsApproximateFloat64())
}

// A pause resumed from Pausing without its record finds the target at zero
// already. That's what the pause did, not what it frees, so the snapshot
// keeps the replicas last seen running rather than pricing the pause at
// nothing freed.
func TestCaptureResourceSnapshot_PauseResumedAtZero(t *testing.T) {
	tests := []struct {
		name     string
		previous int32
		want     int32
	}{
		{name: "the pause's own record", previous: 4, want: 4},
		{name: "a record taken at zero", previous: 0, want: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workload := costWorkload(v1alpha1.PhasePausing)
			workload.Status.Pause = &v1alpha1.PauseStatus{PreviousReplicas: tt.previous}
			workload.Status.Cost = &v1alpha1.CostStatus{Running: &v1alpha1.ResourceSnapshot{Replicas: 3, CPUMillis: 500}}
			r := newTestReconcilerWithReplicas(t, workload, &stubPauser{}, 0)
			r.metrics = &zeroReplicaMetrics{stubMetrics{cpuPerReplica: 500, memoryPerReplica: 512 << 20}}

			snap := r.captureResourceSnapshot(context.Background(), workload)

			require.NotNil(t, snap)
			assert.Equal(t, tt.want, snap.Replicas)
		})
	}
}

type countingPricer struct {
	stubPricer
	calls int
}

func (p *countingPricer) ListRates(ctx context.Context, w *v1alpha1.ManagedWorkload) (
	cost.Rates, v1alpha1.ListPrice, error) {
	p.calls++
	return p.stubPricer.ListRates(ctx, w)
}

// What a replica requests and its nodes' rates are read from its pods,
// which aren't cached, hourly and after a deploy rather than every flush.
func TestAccumulateCost_ReadsPodsHourlyOrAfterADeploy(t *testing.T) {
	w := costWorkload(v1alpha1.PhaseRunning)
	w.Status.Activity = &v1alpha1.ActivityStatus{TemplateHash: "v1"}
	r, now := costClock(fixedTime, &stubMetrics{cpuPerReplica: 1000})
	pricer := &countingPricer{stubPricer: stubPricer{rates: nodeRates, price: v1alpha1.ListPriceOnDemand}}
	r.prices = pricer

	r.accumulateCost(context.Background(), w)
	flushFor(r, now, w, pricingInterval-statusFlushInterval)
	assert.Equal(t, 1, pricer.calls, "read once in the first hour")

	flushFor(r, now, w, statusFlushInterval)
	assert.Equal(t, 2, pricer.calls, "and again an hour on")

	w.Status.Activity.TemplateHash = "v2"
	flushFor(r, now, w, statusFlushInterval)
	assert.Equal(t, 3, pricer.calls, "and after a deploy")
}

func TestAccumulateCost_CustomRates(t *testing.T) {
	spent := func(rates *v1alpha1.CostRates) float64 {
		w := costWorkload(v1alpha1.PhaseRunning)
		w.Spec.CostTracking.Rates = rates
		w.Status.Cost = accumulatedAt(fixedTime.Add(-time.Hour))
		costReconciler(fixedTime, &stubMetrics{cpuPerReplica: 10000}).accumulateCost(context.Background(), w)
		return dollars(t, w.Status.Cost.CostThisMonth)
	}
	cpuRate := resource.MustParse("0.1")

	assert.InDelta(t, 10*0.031, spent(nil), 0.005, "the default rate")
	assert.InDelta(t, 10*0.1, spent(&v1alpha1.CostRates{CPUPerHour: &cpuRate}), 0.005, "its own rate")
}

type zeroReplicaMetrics struct{ stubMetrics }

func (*zeroReplicaMetrics) Replicas(_ context.Context, _ *v1alpha1.ManagedWorkload) (int32, error) {
	return 0, nil
}

func TestCaptureResourceSnapshot_PricesMemoryOnRequest(t *testing.T) {
	workload := costWorkload(v1alpha1.PhaseRunning)
	r := newTestReconciler(t, workload, &stubPauser{})
	r.metrics = &stubMetrics{
		cpuPerReplica:    500,
		memoryPerReplica: 512 << 20,
		replicas:         3,
	}

	snap := r.captureResourceSnapshot(context.Background(), workload)

	require.NotNil(t, snap)
	assert.Equal(t, int32(3), snap.Replicas)
	assert.Equal(t, int64(500), snap.CPUMillis)
	assert.Equal(t, int64(512<<20), snap.MemoryBytes, "memory per replica must come from the request, like CPU")
}

func TestCaptureResourceSnapshot_ZeroReplicas(t *testing.T) {
	workload := costWorkload(v1alpha1.PhaseRunning)
	r := newTestReconcilerWithReplicas(t, workload, &stubPauser{}, 0)
	r.metrics = &zeroReplicaMetrics{stubMetrics{cpuPerReplica: 500, memoryPerReplica: 512 << 20}}

	snap := r.captureResourceSnapshot(context.Background(), workload)

	require.NotNil(t, snap)
	assert.Equal(t, int32(0), snap.Replicas)
	assert.Equal(t, int64(512<<20), snap.MemoryBytes)
}

type unreadablePodMetrics struct{ stubMetrics }

func (*unreadablePodMetrics) PodRequestsPerReplica(_ context.Context, _ *v1alpha1.ManagedWorkload) (float64, float64, error) {
	return 0, 0, errors.New("the API server timed out")
}

func (*unreadablePodMetrics) TotalPVCBytes(_ context.Context, _ *v1alpha1.ManagedWorkload) (float64, error) {
	return 0, errors.New("the API server timed out")
}

// The snapshot is taken once, as the workload pauses, and the whole pause is
// priced from it. A read that fails then falls back to what cost tracking
// last read of the running workload, rather than pricing the pause at
// nothing freed.
func TestCaptureResourceSnapshot_KeepsTheLastReadWhenPodsCantBeRead(t *testing.T) {
	workload := costWorkload(v1alpha1.PhaseRunning)
	workload.Status.Cost = &v1alpha1.CostStatus{Running: &v1alpha1.ResourceSnapshot{
		Replicas: 2, CPUMillis: 300, MemoryBytes: 256 << 20, StorageBytes: 1 << 30}}
	r := newTestReconciler(t, workload, &stubPauser{})
	r.metrics = &unreadablePodMetrics{stubMetrics{replicas: 3}}

	snap := r.captureResourceSnapshot(context.Background(), workload)

	require.NotNil(t, snap)
	assert.Equal(t, v1alpha1.ResourceSnapshot{Replicas: 3, CPUMillis: 300, MemoryBytes: 256 << 20, StorageBytes: 1 << 30}, *snap)
}

func TestCaptureResourceSnapshot_MissingTargetEmitsNoEvent(t *testing.T) {
	workload := costWorkload(v1alpha1.PhaseRunning)
	r := newTestReconcilerWithTarget(t, workload, &stubPauser{}, false)
	r.metrics = &stubMetrics{cpuPerReplica: 500, memoryPerReplica: 512 << 20}

	r.captureResourceSnapshot(context.Background(), workload)

	recorder, ok := r.Recorder.(*events.FakeRecorder)
	require.True(t, ok)
	assert.Empty(t, recorder.Events, "capturing a snapshot must not report TargetNotFound as a side effect")
}

type stubPricer struct {
	rates cost.Rates
	price v1alpha1.ListPrice
	err   error
}

func (p stubPricer) ListRates(_ context.Context, _ *v1alpha1.ManagedWorkload) (
	cost.Rates, v1alpha1.ListPrice, error) {
	return p.rates, p.price, p.err
}

var nodeRates = cost.Rates{CPUPerHour: 0.05, MemoryPerHour: 0.006}

// recordedRates are list rates Napwake recorded for a workload's nodes.
func recordedRates() *v1alpha1.CostRates {
	c, m := resource.MustParse("0.05"), resource.MustParse("0.006")
	return &v1alpha1.CostRates{CPUPerHour: &c, MemoryPerHour: &m}
}

// What the nodes a workload runs on cost is recorded while it runs, and
// kept when there's nothing new to price it at.
func TestAccumulateCost_RecordsListRates(t *testing.T) {
	tests := []struct {
		name      string
		phase     v1alpha1.WorkloadPhase
		pricer    stubPricer
		before    *v1alpha1.CostRates
		want      *v1alpha1.CostRates
		wantPrice v1alpha1.ListPrice
	}{
		{name: "running on listed nodes", phase: v1alpha1.PhaseRunning,
			pricer: stubPricer{rates: nodeRates, price: v1alpha1.ListPriceOnDemand}, want: recordedRates(),
			wantPrice: v1alpha1.ListPriceOnDemand},
		{name: "running on spot nodes", phase: v1alpha1.PhaseRunning,
			pricer: stubPricer{rates: nodeRates, price: v1alpha1.ListPriceSpot}, before: recordedRates(),
			want: recordedRates(), wantPrice: v1alpha1.ListPriceSpot},
		{name: "moved to nodes not in the table", phase: v1alpha1.PhaseRunning,
			pricer: stubPricer{}, before: recordedRates()},
		{name: "no pod on a node yet", phase: v1alpha1.PhaseRunning,
			pricer: stubPricer{err: metrics.ErrNoScheduledPods}, before: recordedRates(),
			want: recordedRates(), wantPrice: v1alpha1.ListPriceSpot},
		{name: "nodes can't be read", phase: v1alpha1.PhaseIdle,
			pricer: stubPricer{err: errors.New("forbidden")}, before: recordedRates(),
			want: recordedRates(), wantPrice: v1alpha1.ListPriceSpot},
		{name: "paused: priced where it ran", phase: v1alpha1.PhasePaused,
			pricer: stubPricer{rates: cost.Rates{CPUPerHour: 1}, price: v1alpha1.ListPriceOnDemand},
			before: recordedRates(), want: recordedRates(), wantPrice: v1alpha1.ListPriceSpot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := costWorkload(tt.phase)
			w.Status.Cost = &v1alpha1.CostStatus{ListRates: tt.before}
			if tt.before != nil {
				w.Status.Cost.ListPrice = v1alpha1.ListPriceSpot
			}
			r := costReconciler(fixedTime, &stubMetrics{cpuPerReplica: 1000})
			r.prices = tt.pricer

			r.accumulateCost(context.Background(), w)

			assert.Equal(t, tt.wantPrice, w.Status.Cost.ListPrice)
			if tt.want == nil {
				assert.Nil(t, w.Status.Cost.ListRates)
				return
			}
			require.NotNil(t, w.Status.Cost.ListRates)
			assert.InDelta(t, tt.want.CPUPerHour.AsApproximateFloat64(),
				w.Status.Cost.ListRates.CPUPerHour.AsApproximateFloat64(), 1e-6)
			assert.InDelta(t, tt.want.MemoryPerHour.AsApproximateFloat64(),
				w.Status.Cost.ListRates.MemoryPerHour.AsApproximateFloat64(), 1e-6)
		})
	}
}

// What a paused workload frees is priced at the nodes it ran on.
func TestAccumulateCost_PausedFreedAtListRates(t *testing.T) {
	freed := func(rates *v1alpha1.CostRates) string {
		w := costWorkload(v1alpha1.PhasePaused)
		w.Status.Cost = accumulatedAt(fixedTime.Add(-time.Hour))
		w.Status.Cost.ListRates = rates
		pausedWith(w, v1alpha1.ResourceSnapshot{Replicas: 1, CPUMillis: 2000, MemoryBytes: 8 * bytesPerGiB})
		costReconciler(fixedTime, &stubMetrics{}).accumulateCost(context.Background(), w)
		return w.Status.Cost.FreedThisMonth
	}

	assert.Equal(t, cost.FormatDollars(2*0.031+8*0.004), freed(nil), "default rates without list rates")
	assert.Equal(t, cost.FormatDollars(2*0.05+8*0.006), freed(recordedRates()), "the nodes' list rates")
}

// Rates a workload sets win over its nodes' list rates, part by part.
func TestResolveCostRates(t *testing.T) {
	w := costWorkload(v1alpha1.PhaseRunning)
	w.Status.Cost = &v1alpha1.CostStatus{ListRates: recordedRates()}
	cpu := resource.MustParse("0.1")
	w.Spec.CostTracking.Rates = &v1alpha1.CostRates{CPUPerHour: &cpu}

	got := resolveCostRates(w)

	assert.InDelta(t, 0.1, got.CPUPerHour, 1e-9, "its own CPU rate")
	assert.InDelta(t, 0.006, got.MemoryPerHour, 1e-9, "its nodes' memory rate")
	assert.InDelta(t, cost.DefaultRates.StoragePerMonth, got.StoragePerMonth, 1e-9, "the default storage rate")
}

// stubSavings credits saved hours the way the capacity sampler does, until
// they're drained.
type stubSavings struct {
	credited     map[types.UID]cost.Hours
	unmeasurable string
}

func (s *stubSavings) credit(uid types.UID, cpuHours, memoryHours float64) {
	if s.credited == nil {
		s.credited = map[types.UID]cost.Hours{}
	}
	h := s.credited[uid]
	h.CPU += cpuHours
	h.Memory += memoryHours
	s.credited[uid] = h
}

func (s *stubSavings) Drain(uid types.UID) (float64, float64) {
	h := s.credited[uid]
	delete(s.credited, uid)
	return h.CPU, h.Memory
}

func (s *stubSavings) Unmeasurable() string { return s.unmeasurable }

// What's credited as saved is drained into the month's saved hours when
// cost is brought up to date, and priced like the rest; freed is still the
// whole of what the pause released.
func TestAccumulateCost_DrainsSavedHours(t *testing.T) {
	w := costWorkload(v1alpha1.PhasePaused)
	w.UID = "drained-uid"
	w.Status.Cost = accumulatedAt(fixedTime.Add(-time.Hour))
	w.Status.Cost.Tracked.Duration = 24 * time.Hour
	pausedWith(w, v1alpha1.ResourceSnapshot{Replicas: 2, CPUMillis: 1000, MemoryBytes: 4 * bytesPerGiB})
	savings := &stubSavings{}
	savings.credit(w.UID, 0.5, 2)
	savings.credit("another-uid", 9, 9)
	r, now := costClock(fixedTime, &stubMetrics{})
	r.Savings = savings

	r.accumulateCost(context.Background(), w)

	c := w.Status.Cost
	assert.InDelta(t, 0.5, c.SavedCPUHours.AsApproximateFloat64(), 1e-9)
	assert.InDelta(t, 2, c.SavedMemoryHours.AsApproximateFloat64(), 1e-9)
	assert.Equal(t, cost.FormatDollars(0.5*0.031+2*0.004), c.SavedThisMonth)
	assert.Equal(t, cost.FormatDollars(2*0.031+8*0.004), c.FreedThisMonth, "freed is what the pause released")
	assert.NotEqual(t, "pending", c.ProjectedMonthlySavings)
	assert.Empty(t, c.SavingsUnknown)

	flushFor(r, now, w, statusFlushInterval)
	assert.InDelta(t, 0.5, c.SavedCPUHours.AsApproximateFloat64(), 1e-9, "drained once")
	assert.Contains(t, savings.credited, types.UID("another-uid"), "another workload's credit is its own")
}

// Where spare capacity can't be seen, saved reads unknown, with why,
// rather than a figure that would be wrong.
func TestAccumulateCost_SavingsUnknown(t *testing.T) {
	w := costWorkload(v1alpha1.PhasePaused)
	w.Status.Cost = accumulatedAt(fixedTime.Add(-time.Hour))
	pausedWith(w, v1alpha1.ResourceSnapshot{Replicas: 1, CPUMillis: 1000})
	r := costReconciler(fixedTime, &stubMetrics{})
	r.Savings = &stubSavings{unmeasurable: "pods outside watchNamespaces aren't visible"}

	r.accumulateCost(context.Background(), w)

	c := w.Status.Cost
	assert.Equal(t, "pods outside watchNamespaces aren't visible", c.SavingsUnknown)
	assert.Equal(t, "unknown", c.SavedThisMonth)
	assert.Equal(t, "unknown", c.ProjectedMonthlySavings)
	assert.Equal(t, cost.FormatDollars(0.031), c.FreedThisMonth, "freed is still measured")
}

// Saved hours reach the workload's status through the reconciler's
// periodic cost flush, not a write of their own, and only once due.
func TestReconcile_PersistsSavedHoursWithTheCostFlush(t *testing.T) {
	workload := pausedWorkload(v1alpha1.PhasePaused)
	workload.UID = "flushed-uid"
	workload.Status.Pause.Resources = &v1alpha1.ResourceSnapshot{Replicas: 3, CPUMillis: 1000, MemoryBytes: bytesPerGiB}
	workload.Status.Cost = accumulatedAt(fixedTime.Add(-time.Minute))
	r := newTestReconcilerWithReplicas(t, workload, &stubPauser{}, 0)
	now := fixedTime
	r.clock = func() time.Time { return now }
	r.metrics = &stubMetrics{}
	savings := &stubSavings{}
	r.Savings = savings
	ctx := context.Background()

	savings.credit(workload.UID, 0.25, 0.25)
	_, err := r.Reconcile(ctx, reconcileFor("api"))
	require.NoError(t, err)
	assert.True(t, getWorkload(t, r, "api").Status.Cost.SavedCPUHours.IsZero(), "not yet due a flush")
	assert.Contains(t, savings.credited, workload.UID, "so it's still held")

	now = now.Add(statusFlushInterval)
	_, err = r.Reconcile(ctx, reconcileFor("api"))
	require.NoError(t, err)

	c := getWorkload(t, r, "api").Status.Cost
	assert.InDelta(t, 0.25, c.SavedCPUHours.AsApproximateFloat64(), 1e-9)
	assert.InDelta(t, 0.25, c.SavedMemoryHours.AsApproximateFloat64(), 1e-9)
	assert.Equal(t, cost.FormatDollars(0.25*0.031+0.25*0.004), c.SavedThisMonth)
}

type stubPools struct {
	pools []v1alpha1.NodePoolPods
	err   error
}

func (p stubPools) NodePools(_ context.Context, _ *v1alpha1.ManagedWorkload) ([]v1alpha1.NodePoolPods, error) {
	return p.pools, p.err
}

// A pause records the node pools its pods ran in, which what it saves is
// measured against; unreadable, none are recorded, and it's measured
// against the whole cluster.
func TestCaptureResourceSnapshot_RecordsNodePools(t *testing.T) {
	general := []v1alpha1.NodePoolPods{{Name: "general", Pods: 2}, {Name: "spot", Pods: 1}}
	tests := []struct {
		name  string
		pools stubPools
		want  []v1alpha1.NodePoolPods
	}{
		{name: "read", pools: stubPools{pools: general}, want: general},
		{name: "unreadable", pools: stubPools{err: errors.New("the API server timed out")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workload := costWorkload(v1alpha1.PhaseRunning)
			r := newTestReconciler(t, workload, &stubPauser{})
			r.metrics = &stubMetrics{cpuPerReplica: 500, replicas: 3}
			r.pools = tt.pools

			snap := r.captureResourceSnapshot(context.Background(), workload)

			require.NotNil(t, snap)
			assert.Equal(t, tt.want, snap.NodePools)
		})
	}
}
