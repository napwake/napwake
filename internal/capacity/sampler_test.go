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

package capacity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
	"github.com/okedeji/hybernate/internal/cost"
	"github.com/okedeji/hybernate/internal/metrics"
)

var start = time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)

func node(name, pool string, cpu, memory string, ready bool) *corev1.Node {
	status := corev1.ConditionTrue
	if !ready {
		status = corev1.ConditionFalse
	}
	labels := map[string]string{
		"node.kubernetes.io/instance-type": "m6i.xlarge",
		"topology.kubernetes.io/region":    "us-east-1",
	}
	if pool != "" {
		labels["karpenter.sh/nodepool"] = pool
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(memory),
			},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: status}},
		},
	}
}

func boundPod(name, nodeName, cpu, memory string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop"},
		Spec: corev1.PodSpec{NodeName: nodeName,
			Containers: []corev1.Container{{Name: "app", Resources: requesting(cpu, memory)}}},
		Status: corev1.PodStatus{Phase: phase},
	}
}

type pausedOpt func(*v1alpha1.ManagedWorkload)

// paused is api, which Hybernate paused at pausedAt from 2 replicas of 1 vCPU
// and 4 GiB each, all of them in pool.
func paused(pool string, pausedAt time.Time, opts ...pausedOpt) *v1alpha1.ManagedWorkload {
	w := &v1alpha1.ManagedWorkload{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "shop", UID: "api-uid"},
		Spec:       v1alpha1.ManagedWorkloadSpec{Target: v1alpha1.WorkloadRef{Kind: v1alpha1.TargetKindDeployment, Name: "api"}},
		Status: v1alpha1.ManagedWorkloadStatus{
			Phase: v1alpha1.PhasePaused,
			Pause: &v1alpha1.PauseStatus{
				PreviousReplicas: 2,
				PausedAt:         &metav1.Time{Time: pausedAt},
				Resources: &v1alpha1.ResourceSnapshot{Replicas: 2, CPUMillis: 1000, MemoryBytes: 4 * gib,
					NodePools: []v1alpha1.NodePoolPods{{Name: pool, Pods: 2}}},
			},
		},
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

func testSampler(t *testing.T, opts Options, objs ...client.Object) (*Sampler, *clocktesting.FakeClock, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	clock := clocktesting.NewFakeClock(start)
	opts.Clock = clock
	return NewSampler(c, opts), clock, c
}

func sampleAfter(t *testing.T, s *Sampler, clock *clocktesting.FakeClock, d time.Duration) {
	t.Helper()
	clock.Step(d)
	require.NoError(t, s.sample(context.Background()))
}

// A tight pool: 4 vCPU and 16 GiB allocatable, of which a running pod
// requests 3 vCPU and 8 GiB, leaves 1 vCPU and 8 GiB, so of 2 paused
// replicas of 1 vCPU and 4 GiB, 1 vCPU couldn't come back without a node.
func tightPool(pausedAt time.Time) []client.Object {
	return []client.Object{
		node("node-a", "general", "4", "16Gi", true),
		boundPod("busy", "node-a", "3", "8Gi", corev1.PodRunning),
		paused("general", pausedAt),
	}
}

func TestSampler_CreditsTheShortfallOverTheIntervalBeforeEachSample(t *testing.T) {
	s, clock, _ := testSampler(t, Options{}, tightPool(start.Add(-time.Hour))...)

	sampleAfter(t, s, clock, 0)
	cpu, mem := s.Drain("api-uid")
	assert.Zero(t, cpu, "the first sample has no interval before it")
	assert.Zero(t, mem)

	sampleAfter(t, s, clock, SampleInterval)
	cpu, mem = s.Drain("api-uid")
	assert.InDelta(t, 1*SampleInterval.Hours(), cpu, 1e-9, "1 vCPU short for 5 minutes")
	assert.Zero(t, mem, "the memory fits")

	cpu, _ = s.Drain("api-uid")
	assert.Zero(t, cpu, "drained once")
}

// Time the sampler wasn't sampling isn't credited: a sample covers at most
// the interval before it, and a workload only the part it was paused for.
func TestSampler_CreditsOnlySampledTime(t *testing.T) {
	tests := []struct {
		name     string
		pausedAt time.Time
		gap      time.Duration
		want     time.Duration
	}{
		{name: "an interval", pausedAt: start.Add(-time.Hour), gap: SampleInterval, want: SampleInterval},
		{name: "a gap longer than the interval", pausedAt: start.Add(-time.Hour), gap: time.Hour, want: SampleInterval},
		{name: "paused part way through", pausedAt: start.Add(3 * time.Minute), gap: SampleInterval,
			want: 2 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, clock, _ := testSampler(t, Options{}, tightPool(tt.pausedAt)...)
			sampleAfter(t, s, clock, 0)
			sampleAfter(t, s, clock, tt.gap)

			cpu, _ := s.Drain("api-uid")
			assert.InDelta(t, tt.want.Hours(), cpu, 1e-9)
		})
	}
}

// An interval a sample failed to measure isn't credited, even once
// sampling works again.
func TestSampler_FailedSampleIsntCredited(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	failing := false
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tightPool(start.Add(-time.Hour))...).
		WithInterceptorFuncs(interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch,
			list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*corev1.PodList); ok && failing {
				return errors.New("the cache isn't synced")
			}
			return c.List(ctx, list, opts...)
		}}).Build()
	clock := clocktesting.NewFakeClock(start)
	s := NewSampler(c, Options{Clock: clock})
	ctx := context.Background()

	require.NoError(t, s.sample(ctx))
	clock.Step(SampleInterval)
	failing = true
	require.Error(t, s.sample(ctx))
	failing = false
	clock.Step(SampleInterval)
	require.NoError(t, s.sample(ctx))
	cpu, _ := s.Drain("api-uid")
	assert.Zero(t, cpu, "the sample after a failure starts afresh")

	clock.Step(SampleInterval)
	require.NoError(t, s.sample(ctx))
	cpu, _ = s.Drain("api-uid")
	assert.InDelta(t, SampleInterval.Hours(), cpu, 1e-9)
}

// Only capacity a paused replica could come back to counts as spare, and
// only what this install has paused is credited.
func TestSampler_WhatCounts(t *testing.T) {
	long := start.Add(-time.Hour)
	tests := []struct {
		name    string
		objects []client.Object
		uid     types.UID
		wantCPU float64
	}{
		{name: "plenty of spare capacity", uid: "api-uid", objects: []client.Object{
			node("node-a", "general", "16", "64Gi", true), paused("general", long)}},
		{name: "a NotReady node offers nothing", uid: "api-uid", wantCPU: 2, objects: []client.Object{
			node("node-a", "general", "16", "64Gi", false), paused("general", long)}},
		{name: "a cordoned node offers nothing", uid: "api-uid", wantCPU: 2, objects: []client.Object{
			cordoned(node("node-a", "general", "16", "64Gi", true)), paused("general", long)}},
		{name: "a finished pod requests nothing", uid: "api-uid", objects: []client.Object{
			node("node-a", "general", "4", "16Gi", true),
			boundPod("done", "node-a", "4", "16Gi", corev1.PodSucceeded), paused("general", long)}},
		{name: "another pool's spare capacity isn't this one's", uid: "api-uid", wantCPU: 2, objects: []client.Object{
			node("node-a", "general", "16", "64Gi", true), paused("gpu", long)}},
		{name: "a pause with no pool recorded meets the whole cluster", uid: "api-uid", wantCPU: 1, objects: []client.Object{
			node("node-a", "general", "0.5", "8Gi", true), node("node-b", "gpu", "0.5", "8Gi", true),
			paused("", long, noPools)}},
		{name: "dry-run never paused", uid: "api-uid", objects: []client.Object{
			paused("general", long, func(w *v1alpha1.ManagedWorkload) { w.Spec.DryRun = true })}},
		{name: "not paused", uid: "api-uid", objects: []client.Object{
			paused("general", long, func(w *v1alpha1.ManagedWorkload) { w.Status.Phase = v1alpha1.PhaseResuming })}},
		{name: "another install's", uid: "api-uid", objects: []client.Object{
			paused("general", long, func(w *v1alpha1.ManagedWorkload) {
				w.Annotations = map[string]string{v1alpha1.AnnotationOperator: "team-b-system"}
			})}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, clock, _ := testSampler(t, Options{OperatorNamespace: "hybernate-system"}, tt.objects...)
			sampleAfter(t, s, clock, 0)
			sampleAfter(t, s, clock, time.Hour)

			cpu, _ := s.Drain(tt.uid)
			assert.InDelta(t, tt.wantCPU*SampleInterval.Hours(), cpu, 1e-9)
		})
	}
}

func cordoned(n *corev1.Node) *corev1.Node {
	n.Spec.Unschedulable = true
	return n
}

func noPools(w *v1alpha1.ManagedWorkload) { w.Status.Pause.Resources.NodePools = nil }

func TestSampler_ForgetsWorkloadsThatAreGone(t *testing.T) {
	s, clock, c := testSampler(t, Options{}, tightPool(start.Add(-time.Hour))...)
	sampleAfter(t, s, clock, 0)
	sampleAfter(t, s, clock, SampleInterval)

	require.NoError(t, c.Delete(context.Background(), paused("general", start)))
	sampleAfter(t, s, clock, SampleInterval)

	cpu, _ := s.Drain("api-uid")
	assert.Zero(t, cpu)
}

func TestSampler_PublishesEachPool(t *testing.T) {
	s, clock, c := testSampler(t, Options{}, tightPool(start.Add(-time.Hour))...)
	sampleAfter(t, s, clock, 0)

	assert.InDelta(t, 2, testutil.ToFloat64(metrics.CapacityPausedCPUCores.WithLabelValues("general")), 1e-9)
	assert.InDelta(t, 8*gib, testutil.ToFloat64(metrics.CapacityPausedMemoryBytes.WithLabelValues("general")), 1)
	assert.InDelta(t, 1, testutil.ToFloat64(metrics.CapacitySpareCPUCores.WithLabelValues("general")), 1e-9)
	assert.InDelta(t, 8*gib, testutil.ToFloat64(metrics.CapacitySpareMemoryBytes.WithLabelValues("general")), 1)
	price, ok := cost.ListPriceOf(cost.NodeType{InstanceType: "m6i.xlarge", Region: "us-east-1"})
	require.True(t, ok)
	assert.InDelta(t, price.Rates.CPUPerHour,
		testutil.ToFloat64(metrics.SavingsRealisedDollarsPerHour.WithLabelValues("general")), 1e-9,
		"1 vCPU short, at the pool's nodes' list rate")

	require.NoError(t, c.Delete(context.Background(), node("node-a", "general", "4", "16Gi", true)))
	require.NoError(t, c.Delete(context.Background(), paused("general", start)))
	sampleAfter(t, s, clock, SampleInterval)
	assert.False(t, metrics.CapacitySpareCPUCores.DeleteLabelValues("general"), "a pool that's gone has no series")
}

// The sampler runs on its clock until the manager stops it.
func TestSampler_Start(t *testing.T) {
	s, clock, _ := testSampler(t, Options{}, tightPool(start.Add(-time.Hour))...)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	require.Eventually(t, clock.HasWaiters, 10*time.Second, 10*time.Millisecond, "waiting for the first tick")
	clock.Step(SampleInterval)
	var credited float64
	require.Eventually(t, func() bool {
		cpu, _ := s.Drain("api-uid")
		credited += cpu
		return credited > 0
	}, 10*time.Second, 10*time.Millisecond)
	assert.InDelta(t, SampleInterval.Hours(), credited, 1e-9)

	cancel()
	assert.NoError(t, <-done)
}

// An operator that sees only some namespaces can't see what other pods
// request of its nodes, so it says savings can't be measured, and doesn't
// sample.
func TestSampler_UnwatchedNamespaces(t *testing.T) {
	s, _, _ := testSampler(t, Options{WatchNamespaces: []string{"shop"}}, tightPool(start.Add(-time.Hour))...)

	assert.Equal(t, Unwatched, s.Unmeasurable())
	assert.NoError(t, s.Start(context.Background()), "returns at once")
}
