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
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

// benchWorkloads is a cluster of n ManagedWorkloads, ten to a namespace: a
// postgres the other nine depend on, declared and learned, each with the
// status a workload carries after a few weeks.
func benchWorkloads(n int) []v1alpha1.ManagedWorkload {
	state := strings.Repeat("x", 4096)
	out := make([]v1alpha1.ManagedWorkload, 0, n)
	for i := range n {
		ns := fmt.Sprintf("team-%d", i/10)
		name, kind := fmt.Sprintf("api-%d", i%10), v1alpha1.TargetKindDeployment
		var deps []v1alpha1.DependencyRef
		var learned *v1alpha1.LearnedDependencies
		if i%10 == 0 {
			name, kind = postgresRef().Name, v1alpha1.TargetKindStatefulSet
		} else {
			deps = []v1alpha1.DependencyRef{postgresRef()}
			learned = &v1alpha1.LearnedDependencies{From: "environment", Dependencies: []v1alpha1.LearnedDependency{
				{Namespace: ns, Kind: v1alpha1.TargetKindStatefulSet, Name: postgresRef().Name,
					Source: v1alpha1.LearnedFromEnvironment, Via: "env DATABASE_URL"},
				{Namespace: ns, Kind: v1alpha1.TargetKindDeployment, Name: "api-1",
					Source: v1alpha1.LearnedFromEnvironment, Via: "env API_URL"},
			}}
		}
		w := depWorkload(ns, name, kind, v1alpha1.PhaseRunning, deps...)
		w.Annotations = map[string]string{v1alpha1.AnnotationLastActivity: "2026-03-14T10:00:00Z"}
		w.Status.LearnedDependencies = learned
		w.Status.Prediction = &v1alpha1.PredictionStatus{DailyPhase: "Active", WeeklyPhase: "Learning", State: state}
		w.Status.Cost = &v1alpha1.CostStatus{AwakeCPUHours: resource.MustParse("12.5"),
			AwakeMemoryHours: resource.MustParse("3Gi"), StorageHours: resource.MustParse("10Gi"),
			PausedCPUHours: resource.MustParse("40"), PausedMemoryHours: resource.MustParse("9Gi")}
		for _, c := range []string{conditionTargetAvailable, conditionMetricsAvailable, conditionWakeOnRequest,
			conditionHeldByDependents, conditionDependencyCycle, conditionDependencyNotFound, conditionDuplicateTarget} {
			w.Status.Conditions = append(w.Status.Conditions, metav1.Condition{Type: c, Status: metav1.ConditionFalse,
				Reason: "Benchmark", Message: "a message of some length"})
		}
		out = append(out, *w)
	}
	return out
}

// BenchmarkLoadDependencyGraph is the dependency check of one reconcile,
// whether one namespace's postgres may pause, in a cluster of n
// ManagedWorkloads, read from the graph the watch keeps.
func BenchmarkLoadDependencyGraph(b *testing.B) {
	for _, n := range []int{1000, 5000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			workloads := benchWorkloads(n)
			r := &Reconciler{
				Client:   fakeClientBuilder(testScheme(b)).Build(),
				Recorder: events.NewFakeRecorder(1),
				graph:    workloadGraphOf(workloads),
			}
			postgres := workloads[0].DeepCopy()
			ctx := context.Background()
			b.ReportAllocs()
			for b.Loop() {
				h, err := r.heldByDependencies(ctx, postgres)
				require.NoError(b, err)
				require.Len(b, h.holders, 9)
			}
		})
	}
}

// BenchmarkWorkloadGraphUpdate is what the watch spends keeping the graph
// up to date: one ManagedWorkload's update, in a cluster of n.
func BenchmarkWorkloadGraphUpdate(b *testing.B) {
	for _, n := range []int{1000, 5000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			workloads := benchWorkloads(n)
			g := workloadGraphOf(workloads)
			api := &workloads[1]
			b.ReportAllocs()
			for b.Loop() {
				g.set(api)
			}
		})
	}
}
