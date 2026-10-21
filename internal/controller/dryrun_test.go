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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
	"github.com/okedeji/hybernate/internal/cost"
	"github.com/okedeji/hybernate/internal/forecast"
)

const gibibyte = 1 << 30

// Two replicas of 1 vCPU and 1 GiB: $0.07 an hour at the default rates.
var twoReplicas = stubMetrics{cpuPerReplica: 1000, memoryPerReplica: gibibyte, replicas: 2}

func measuring(since time.Time) *v1alpha1.DryRunStatus {
	return &v1alpha1.DryRunStatus{Since: metav1.NewTime(since), EstimatedFreed: "$0.00"}
}

func runDryRunClock(t *testing.T, workload *v1alpha1.ManagedWorkload, metrics stubMetrics) []string {
	t.Helper()
	target := clockTarget("app:v1", nil)
	workload.Status.Activity.TemplateHash = podTemplateHash(target)
	pauser := &stubPauser{pauseDone: true}
	r := newAutomationReconciler(t, workload, &stubForecaster{phase: forecast.Observing},
		automationOpts{metrics: &metrics, pauser: pauser})
	_, err := r.reconcileAutomation(context.Background(), workload, target)
	require.NoError(t, err)
	require.Zero(t, pauser.pauseCalls, "dry-run never pauses")
	return recorded(r.Recorder.(*events.FakeRecorder), ReasonActivityResumed)
}

func TestDryRun_CountsAWouldBePause(t *testing.T) {
	workload := clockWorkload(fixedTime.Add(-61*time.Minute), clockTarget("app:v1", nil))
	workload.Spec.DryRun = true
	workload.Status.DryRun = measuring(fixedTime.Add(-24 * time.Hour))
	metrics := twoReplicas
	metrics.cpuMillis = 5

	runDryRunClock(t, workload, metrics)

	assert.Equal(t, v1alpha1.PhaseIdle, workload.Status.Phase)
	require.NotNil(t, workload.Status.DryRun)
	assert.Equal(t, int32(1), workload.Status.DryRun.Pauses)
	require.NotNil(t, workload.Status.DryRun.Resources, "what the pause would free is recorded as it begins")
	assert.Equal(t, int32(2), workload.Status.DryRun.Resources.Replicas)
}

func TestDryRun_ActivityEndsAWouldBePause(t *testing.T) {
	workload := clockWorkload(fixedTime, clockTarget("app:v1", nil))
	workload.Spec.DryRun = true
	workload.Status.Phase = v1alpha1.PhaseIdle
	idleSince := metav1.NewTime(fixedTime.Add(-3 * time.Hour))
	workload.Status.LastTransitionTime = &idleSince
	workload.Status.DryRun = &v1alpha1.DryRunStatus{
		Since:            metav1.NewTime(time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)),
		Pauses:           2,
		Slept:            metav1.Duration{Duration: time.Hour},
		FreedCPUHours:    resource.MustParse("2"),
		FreedMemoryHours: resource.MustParse("2"),
		EstimatedFreed:   "$0.07",
		Resources:        &v1alpha1.ResourceSnapshot{Replicas: 2, CPUMillis: 1000, MemoryBytes: gibibyte},
	}
	metrics := twoReplicas
	metrics.cpuMillis = 1500

	resumed := runDryRunClock(t, workload, metrics)

	assert.Equal(t, v1alpha1.PhaseRunning, workload.Status.Phase)
	d := workload.Status.DryRun
	assert.Equal(t, int32(2), d.Pauses)
	assert.Equal(t, 4*time.Hour, d.Slept.Duration, "asleep from going Idle until the activity that would have woken it")
	assert.Equal(t, "$0.28", d.EstimatedFreed, "3h of 2 vCPU and 2 GiB added to $0.07")
	assert.Nil(t, d.Resources)
	require.Len(t, resumed, 1)
	assert.Contains(t, resumed[0], "[dry-run]")
	assert.Contains(t, resumed[0], "would have slept 3h, freeing $0.21")
	assert.Contains(t, resumed[0], "since Mar 10: would have paused 2 times, slept 4h, freeing $0.28")
}

// The estimate counts the would-be pause under way, so a workload idle for
// days shows what it would have freed before its next activity ends the
// pause. The hours, the record, wait for the pause to end.
func TestDryRun_EstimateCountsThePauseUnderWay(t *testing.T) {
	workload := clockWorkload(fixedTime.Add(-4*time.Hour), clockTarget("app:v1", nil))
	workload.Spec.DryRun = true
	workload.Status.Phase = v1alpha1.PhaseIdle
	idleSince := metav1.NewTime(fixedTime.Add(-3 * time.Hour))
	workload.Status.LastTransitionTime = &idleSince
	workload.Status.DryRun = &v1alpha1.DryRunStatus{
		Since:            metav1.NewTime(time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)),
		Pauses:           2,
		Slept:            metav1.Duration{Duration: time.Hour},
		FreedCPUHours:    resource.MustParse("2"),
		FreedMemoryHours: resource.MustParse("2"),
		EstimatedFreed:   "$0.07",
		Resources:        &v1alpha1.ResourceSnapshot{Replicas: 2, CPUMillis: 1000, MemoryBytes: gibibyte},
	}
	r := &Reconciler{clock: func() time.Time { return fixedTime }}

	r.trackDryRun(workload)

	d := workload.Status.DryRun
	assert.Equal(t, "$0.28", d.EstimatedFreed, "3h under way of 2 vCPU and 2 GiB added to $0.07")
	assert.Equal(t, time.Hour, d.Slept.Duration, "the pause under way is added to the record when it ends")
	assert.Equal(t, "2", d.FreedCPUHours.String())
	require.NotNil(t, d.Resources)

	workload.Status.Phase = v1alpha1.PhaseRunning
	d.Resources = nil
	r.trackDryRun(workload)

	assert.Equal(t, "$0.07", d.EstimatedFreed, "a running workload has no pause under way")
}

// kubectl get shows the estimate as status has it, so it grows while the
// workload stays idle. It moves on every check, so, as cost does, it's
// written with the flush rather than on every check.
func TestReconcile_DryRunEstimateGrowsWhileIdle(t *testing.T) {
	workload := lifecycleWorkload("idle-app", v1alpha1.PhaseIdle)
	workload.Spec.DryRun = true
	workload.Spec.IdlePolicy = &v1alpha1.IdlePolicySpec{IdleAfter: &metav1.Duration{Duration: time.Hour}}
	idleSince := metav1.NewTime(fixedTime.Add(-time.Hour))
	workload.Status.LastTransitionTime = &idleSince
	evaluated := metav1.NewTime(fixedTime)
	workload.Status.Activity = &v1alpha1.ActivityStatus{LastActivityTime: metav1.NewTime(fixedTime.Add(-2 * time.Hour)),
		LastActivitySource: v1alpha1.ActivitySourceCPU, PauseAt: &idleSince, LastEvaluatedTime: &evaluated,
		TemplateHash: podTemplateHash(targetDeployment("idle-app", "default"))}
	workload.Status.DryRun = &v1alpha1.DryRunStatus{Since: metav1.NewTime(fixedTime.Add(-24 * time.Hour)),
		Pauses: 1, EstimatedFreed: "$0.00",
		Resources: &v1alpha1.ResourceSnapshot{Replicas: 2, CPUMillis: 1000, MemoryBytes: gibibyte}}
	r := newTestReconciler(t, workload, &stubPauser{})
	r.metrics = &stubMetrics{cpuMillis: 5, cpuPerReplica: 1000, memoryPerReplica: gibibyte, replicas: 2}
	now := fixedTime
	r.clock = func() time.Time { return now }
	written := func() *v1alpha1.ManagedWorkload {
		_, err := r.Reconcile(context.Background(), reconcileFor("idle-app"))
		require.NoError(t, err)
		return getWorkload(t, r, "idle-app")
	}

	first := written()
	require.Equal(t, v1alpha1.PhaseIdle, first.Status.Phase)
	assert.Equal(t, "$0.07", first.Status.DryRun.EstimatedFreed, "an hour of 2 vCPU and 2 GiB so far")

	now = now.Add(time.Minute)
	assert.Equal(t, first.ResourceVersion, written().ResourceVersion, "a moving estimate alone isn't written")

	now = now.Add(statusFlushInterval)
	flushed := written()
	assert.Equal(t, "$0.08", flushed.Status.DryRun.EstimatedFreed, "1h6m so far")
	assert.Zero(t, flushed.Status.DryRun.Slept.Duration, "the record waits for the pause to end")
}

// A workload already Idle when measuring started, such as across an operator
// upgrade, didn't have its would-be pause counted as it began, so its end
// isn't counted either.
func TestDryRun_APauseThatBeganBeforeMeasuringIsNotCounted(t *testing.T) {
	workload := clockWorkload(fixedTime, clockTarget("app:v1", nil))
	workload.Spec.DryRun = true
	workload.Status.Phase = v1alpha1.PhaseIdle
	idleSince := metav1.NewTime(fixedTime.Add(-3 * time.Hour))
	workload.Status.LastTransitionTime = &idleSince
	workload.Status.DryRun = measuring(fixedTime.Add(-time.Minute))
	metrics := twoReplicas
	metrics.cpuMillis = 1500

	resumed := runDryRunClock(t, workload, metrics)

	assert.Equal(t, v1alpha1.PhaseRunning, workload.Status.Phase)
	assert.Zero(t, workload.Status.DryRun.Slept.Duration)
	require.Len(t, resumed, 1)
	assert.NotContains(t, resumed[0], "would have slept")
}

// Would-be pauses add up to what they would have freed however short they
// are and however small the workload: each one used to be rounded to the
// cent as it was added, so a small workload never seemed worth pausing.
func TestDryRun_ShortPausesOfSmallWorkloadsAddUp(t *testing.T) {
	r := &Reconciler{clock: func() time.Time { return fixedTime }}
	workload := automationWorkload(v1alpha1.PhaseIdle)
	workload.Spec.DryRun = true
	workload.Status.DryRun = measuring(fixedTime.Add(-24 * time.Hour))
	idleSince := metav1.NewTime(fixedTime)
	workload.Status.LastTransitionTime = &idleSince
	workload.Status.Activity = &v1alpha1.ActivityStatus{LastActivityTime: metav1.NewTime(fixedTime.Add(3 * time.Minute))}

	for range 480 {
		workload.Status.DryRun.Pauses++
		workload.Status.DryRun.Resources = &v1alpha1.ResourceSnapshot{Replicas: 1, CPUMillis: 10, MemoryBytes: 64 << 20}
		_, _, measured := r.endWouldBePause(workload)
		require.True(t, measured)
	}

	d := workload.Status.DryRun
	assert.Equal(t, 24*time.Hour, d.Slept.Duration)
	assert.InEpsilon(t, 0.01*24, d.FreedCPUHours.AsApproximateFloat64(), 0.001)
	assert.InEpsilon(t, 0.0625*24, d.FreedMemoryHours.AsApproximateFloat64(), 0.001)
	assert.Equal(t, cost.FormatDollars(cost.ComputeHourly(0.01, 0.0625, cost.DefaultRates)*24), d.EstimatedFreed)
}

// A dry-run workload made Idle by a pause request before its activity was
// ever measured, such as without metrics-server, has no activity status.
// Its target then scaled to zero ends the would-be pause without one.
func TestDryRun_WouldBePauseEndsWithoutMeasuredActivity(t *testing.T) {
	w := withPauseRequested(lifecycleWorkload("api", v1alpha1.PhaseRunning))
	w.Spec.DryRun = true
	w.Finalizers = []string{finalizerName}
	r := newTestReconcilerWithReplicas(t, w, &stubPauser{}, 2)
	r.metrics = &stubMetrics{err: errors.New("metrics-server unavailable")}
	for range 2 {
		_, err := r.Reconcile(context.Background(), reconcileFor("api"))
		require.NoError(t, err)
	}
	idle := getWorkload(t, r, "api")
	require.Equal(t, v1alpha1.PhaseIdle, idle.Status.Phase)
	require.Nil(t, idle.Status.Activity, "activity was never measured")

	var d appsv1.Deployment
	require.NoError(t, r.Get(context.Background(), reconcileFor("api").NamespacedName, &d))
	d.Spec.Replicas = ptr.To(int32(0))
	require.NoError(t, r.Update(context.Background(), &d))

	require.NotPanics(t, func() {
		_, err := r.Reconcile(context.Background(), reconcileFor("api"))
		require.NoError(t, err)
	})
	assert.Equal(t, v1alpha1.PhaseRunning, getWorkload(t, r, "api").Status.Phase)
}

// A would-be pause ends, and is counted, when Hybernate stops measuring it:
// the target scaled to zero, labelled to be ignored, or in a namespace
// protected since. It lasted until then, however long ago the last activity
// was.
func TestDryRun_WouldBePauseEndsWhenHybernateLetsGo(t *testing.T) {
	running := func() *appsv1.Deployment { return targetDeploymentWithReplicas("api", "default", 2) }
	ignored := running()
	ignored.Labels = map[string]string{v1alpha1.LabelIgnore: v1alpha1.True}
	tests := []struct {
		name   string
		ns     *corev1.Namespace
		target *appsv1.Deployment
	}{
		{name: "scaled to zero", ns: defaultNamespace(nil), target: targetDeploymentWithReplicas("api", "default", 0)},
		{name: "ignored", ns: defaultNamespace(nil), target: ignored},
		{name: "protected", ns: defaultNamespace(protectedLabel), target: running()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workload := depWorkload("default", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseIdle)
			workload.Finalizers = []string{finalizerName}
			workload.Spec.DryRun = true
			idleSince := metav1.NewTime(fixedTime.Add(-3 * time.Hour))
			workload.Status.LastTransitionTime = &idleSince
			workload.Status.Activity.LastActivityTime = metav1.NewTime(fixedTime.Add(-4 * time.Hour))
			workload.Status.DryRun = measuring(fixedTime.Add(-24 * time.Hour))
			workload.Status.DryRun.Pauses = 1
			workload.Status.DryRun.Resources = &v1alpha1.ResourceSnapshot{Replicas: 2, CPUMillis: 1000,
				MemoryBytes: gibibyte}
			r := depReconciler(t, &stubPauser{}, workload, tt.ns, tt.target)

			_, err := r.Reconcile(context.Background(), reconcileFor("api"))
			require.NoError(t, err)

			got := fetch(t, r, "api")
			assert.Equal(t, v1alpha1.PhaseRunning, got.Status.Phase)
			require.NotNil(t, got.Status.DryRun)
			assert.Equal(t, 3*time.Hour, got.Status.DryRun.Slept.Duration)
			assert.Equal(t, "$0.21", got.Status.DryRun.EstimatedFreed, "3h of 2 vCPU and 2 GiB")
			assert.Nil(t, got.Status.DryRun.Resources)
		})
	}
}

func TestTrackDryRun(t *testing.T) {
	r := &Reconciler{clock: func() time.Time { return fixedTime }}
	workload := automationWorkload(v1alpha1.PhaseRunning)

	workload.Spec.DryRun = true
	r.trackDryRun(workload)
	require.NotNil(t, workload.Status.DryRun, "measuring starts with dry-run")
	assert.True(t, workload.Status.DryRun.Since.Time.Equal(fixedTime))

	workload.Status.DryRun.Pauses = 3
	r.trackDryRun(workload)
	assert.Equal(t, int32(3), workload.Status.DryRun.Pauses, "and carries on while it lasts")

	workload.Spec.DryRun = false
	r.trackDryRun(workload)
	assert.Nil(t, workload.Status.DryRun, "and is dropped when dry-run ends")
}

func TestRoundedDuration(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{10 * time.Second, "under a minute"},
		{30 * time.Second, "under a minute"},
		{90 * time.Second, "2m"},
		{45 * time.Minute, "45m"},
		{3*time.Hour + 12*time.Minute, "3h12m"},
		{3*time.Hour + 10*time.Minute, "3h10m"},
		{96 * time.Hour, "96h"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, roundedDuration(tt.in), tt.in.String())
	}
}
