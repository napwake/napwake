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
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/metrics"
)

// Right after the operator writes a phase, its cache can still hold the one
// before, and a reconcile in that time publishes it. A phase published
// that way that was never taken down again kept a paused workload Pausing
// in the gauge, and NapwakeWorkloadStuck fired for it.
func TestReconcile_AStaleCachedPhaseLeavesOnePhaseInTheGauge(t *testing.T) {
	workload := withPauseRequested(lifecycleWorkload("stale-gauge-app", v1alpha1.PhaseRunning))
	workload.Finalizers = []string{finalizerName}
	r := newTestReconciler(t, workload, &stubPauser{pauseDone: true})
	cached := getWorkload(t, r, "stale-gauge-app")

	_, err := r.Reconcile(context.Background(), reconcileFor("stale-gauge-app"))
	require.NoError(t, err)
	require.Equal(t, v1alpha1.PhasePaused, getWorkload(t, r, "stale-gauge-app").Status.Phase)
	var target appsv1.Deployment
	require.NoError(t, r.Get(context.Background(), reconcileFor("stale-gauge-app").NamespacedName, &target))
	target.Spec.Replicas = ptr.To(int32(0))
	require.NoError(t, r.Update(context.Background(), &target))

	live := r.Client
	r.Client = interceptor.NewClient(live.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object,
			opts ...client.GetOption) error {
			if w, ok := obj.(*v1alpha1.ManagedWorkload); ok && key.Name == cached.Name {
				cached.DeepCopyInto(w)
				return nil
			}
			return c.Get(ctx, key, obj, opts...)
		}})
	_, err = r.Reconcile(context.Background(), reconcileFor("stale-gauge-app"))
	require.NoError(t, err)

	phases := reportedPhases(t, "stale-gauge-app")
	assert.Len(t, phases, 1, "one phase from a stale read, not one more: %v", phases)

	r.Client = live
	_, err = r.Reconcile(context.Background(), reconcileFor("stale-gauge-app"))
	require.NoError(t, err)

	assert.Equal(t, map[string]float64{"Paused": 1}, reportedPhases(t, "stale-gauge-app"),
		"once the cache catches up")
}

// recordPhase takes down each phase but the current one by name, so a phase
// the API allows that workloadPhases leaves out would linger in the gauge
// once a workload left it.
func TestWorkloadPhases_AreEveryPhaseTheAPIAllows(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "crd", "bases", "napwake.dev_managedworkloads.yaml"))
	require.NoError(t, err)
	var crd map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &crd))
	versions, _, err := unstructured.NestedSlice(crd, "spec", "versions")
	require.NoError(t, err)
	require.NotEmpty(t, versions)

	for _, v := range versions {
		version, ok := v.(map[string]any)
		require.True(t, ok)
		enum, found, err := unstructured.NestedStringSlice(version,
			"schema", "openAPIV3Schema", "properties", "status", "properties", "phase", "enum")
		require.NoError(t, err)
		require.True(t, found, "status.phase has an enum in version %v", version["name"])

		phases := make([]string, 0, len(workloadPhases))
		for _, p := range workloadPhases {
			phases = append(phases, string(p))
		}
		assert.ElementsMatch(t, enum, phases, "version %v", version["name"])
	}
}

// NapwakeActivityUnreadable and NapwakeResumeIncomplete watch these
// gauges, so each series is 1 only while its condition holds, and gone once
// it clears, leaving nothing stale for an alert to fire on.
func TestRecordConditions(t *testing.T) {
	condition := func(condType string, status metav1.ConditionStatus, reason string) metav1.Condition {
		return metav1.Condition{Type: condType, Status: status, Reason: reason}
	}
	tests := []struct {
		name             string
		phase            v1alpha1.WorkloadPhase
		queries          bool
		conditions       []metav1.Condition
		unreadable       []string
		resumeIncomplete bool
	}{
		{
			name: "every source read", phase: v1alpha1.PhaseRunning, queries: true,
			conditions: []metav1.Condition{
				condition(conditionMetricsAvailable, metav1.ConditionTrue, "MetricsReported"),
				condition(conditionPrometheusAvailable, metav1.ConditionTrue, "QueriesEvaluated"),
			},
		},
		{
			name: "no CPU metrics", phase: v1alpha1.PhaseRunning,
			conditions: []metav1.Condition{condition(conditionMetricsAvailable, metav1.ConditionFalse, reasonNoPodMetrics)},
			unreadable: []string{"cpu"},
		},
		{
			name: "no CPU requests", phase: v1alpha1.PhaseIdle,
			conditions: []metav1.Condition{condition(conditionMetricsAvailable, metav1.ConditionFalse, "NoCPURequests")},
			unreadable: []string{"cpu"},
		},
		{
			name: "new pods' metrics not in yet", phase: v1alpha1.PhaseRunning,
			conditions: []metav1.Condition{condition(conditionMetricsAvailable, metav1.ConditionUnknown, "AwaitingPodMetrics")},
		},
		{
			name: "paused, with CPU unread since before", phase: v1alpha1.PhasePaused,
			conditions: []metav1.Condition{condition(conditionMetricsAvailable, metav1.ConditionFalse, reasonNoPodMetrics)},
		},
		{
			name: "Prometheus failing", phase: v1alpha1.PhaseRunning, queries: true,
			conditions: []metav1.Condition{condition(conditionPrometheusAvailable, metav1.ConditionFalse, "QueryFailed")},
			unreadable: []string{"prometheus"},
		},
		{
			name: "Prometheus failing while paused", phase: v1alpha1.PhasePaused, queries: true,
			conditions: []metav1.Condition{condition(conditionPrometheusAvailable, metav1.ConditionFalse, "QueryFailed")},
			unreadable: []string{"prometheus"},
		},
		{
			name: "Prometheus failing for queries since removed", phase: v1alpha1.PhaseRunning,
			conditions: []metav1.Condition{condition(conditionPrometheusAvailable, metav1.ConditionFalse, "QueryFailed")},
		},
		{
			name: "neither", phase: v1alpha1.PhaseRunning, queries: true,
			conditions: []metav1.Condition{
				condition(conditionMetricsAvailable, metav1.ConditionFalse, "MetricsUnavailable"),
				condition(conditionPrometheusAvailable, metav1.ConditionFalse, "EndpointNotConfigured"),
			},
			unreadable: []string{"cpu", "prometheus"},
		},
		{
			name: "woke with replicas not Ready", phase: v1alpha1.PhaseRunning,
			conditions:       []metav1.Condition{condition(conditionResumeIncomplete, metav1.ConditionTrue, "ReplicasNotReady")},
			resumeIncomplete: true,
		},
		{
			name: "every replica Ready since", phase: v1alpha1.PhaseRunning,
			conditions: []metav1.Condition{condition(conditionResumeIncomplete, metav1.ConditionFalse, ReasonAllReplicasReady)},
		},
		{
			name: "ignored, its activity no longer read", phase: v1alpha1.PhaseRunning, queries: true,
			conditions: []metav1.Condition{
				condition(conditionTargetAvailable, metav1.ConditionFalse, "TargetIgnored"),
				condition(conditionMetricsAvailable, metav1.ConditionFalse, reasonNoPodMetrics),
				condition(conditionPrometheusAvailable, metav1.ConditionFalse, "QueryFailed"),
			},
		},
		{
			name: "protected, its activity no longer read", phase: v1alpha1.PhaseRunning, queries: true,
			conditions: []metav1.Condition{
				condition(conditionProtected, metav1.ConditionTrue, "ProtectedNamespace"),
				condition(conditionMetricsAvailable, metav1.ConditionFalse, reasonNoPodMetrics),
				condition(conditionPrometheusAvailable, metav1.ConditionFalse, "QueryFailed"),
			},
		},
		{
			name: "its target gone", phase: v1alpha1.PhaseRunning, queries: true,
			conditions: []metav1.Condition{
				condition(conditionTargetAvailable, metav1.ConditionFalse, ReasonTargetNotFound),
				condition(conditionMetricsAvailable, metav1.ConditionFalse, reasonNoPodMetrics),
				condition(conditionPrometheusAvailable, metav1.ConditionFalse, "QueryFailed"),
			},
		},
		{
			name: "protected no longer", phase: v1alpha1.PhaseRunning,
			conditions: []metav1.Condition{
				condition(conditionTargetAvailable, metav1.ConditionTrue, "TargetExists"),
				condition(conditionProtected, metav1.ConditionFalse, "NotProtected"),
				condition(conditionMetricsAvailable, metav1.ConditionFalse, reasonNoPodMetrics),
			},
			unreadable: []string{"cpu"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workload := lifecycleWorkload("gauges-app", tt.phase)
			if tt.queries {
				workload.Spec.IdlePolicy = &v1alpha1.IdlePolicySpec{Activity: &v1alpha1.ActivitySpec{
					Prometheus: []v1alpha1.PrometheusActivity{{PromQL: "queue_length"}}}}
			}
			workload.Status.Conditions = tt.conditions
			metrics.ActivityUnreadable.WithLabelValues("default", "gauges-app", "cpu").Set(1)
			metrics.ActivityUnreadable.WithLabelValues("default", "gauges-app", "prometheus").Set(1)
			metrics.ResumeIncomplete.WithLabelValues("default", "gauges-app").Set(1)

			recordConditions(workload)

			unreadable := seriesOf(t, metrics.ActivityUnreadable, "gauges-app", "source")
			assert.ElementsMatch(t, tt.unreadable, slices.Collect(maps.Keys(unreadable)))
			for source, v := range unreadable {
				assert.Equal(t, 1.0, v, source)
			}
			incomplete := seriesOf(t, metrics.ResumeIncomplete, "gauges-app", "")
			if tt.resumeIncomplete {
				assert.Equal(t, map[string]float64{"": 1}, incomplete)
			} else {
				assert.Empty(t, incomplete)
			}
		})
	}
}

// The gauges follow the workload's status as each reconcile reads it.
func TestReconcile_PublishesConditions(t *testing.T) {
	workload := lifecycleWorkload("resumed-gauge-app", v1alpha1.PhaseRunning)
	workload.Finalizers = []string{finalizerName}
	workload.Status.Conditions = []metav1.Condition{{Type: conditionResumeIncomplete, Status: metav1.ConditionTrue,
		Reason: "ReplicasNotReady", LastTransitionTime: metav1.NewTime(fixedTime)}}
	r := newTestReconciler(t, workload, &stubPauser{})

	_, err := r.Reconcile(context.Background(), reconcileFor("resumed-gauge-app"))
	require.NoError(t, err)
	assert.Equal(t, map[string]float64{"": 1},
		seriesOf(t, metrics.ResumeIncomplete, "resumed-gauge-app", ""))

	w := getWorkload(t, r, "resumed-gauge-app")
	meta.SetStatusCondition(&w.Status.Conditions, metav1.Condition{Type: conditionResumeIncomplete,
		Status: metav1.ConditionFalse, Reason: ReasonAllReplicasReady})
	require.NoError(t, r.Status().Update(context.Background(), w))
	_, err = r.Reconcile(context.Background(), reconcileFor("resumed-gauge-app"))
	require.NoError(t, err)
	assert.Empty(t, seriesOf(t, metrics.ResumeIncomplete, "resumed-gauge-app", ""))
}

// A workload another install's annotation now names is that install's to
// manage, so this one drops what it published and kept for it, as for one
// deleted. A Pausing series left behind would keep NapwakeWorkloadStuck
// firing, and a doorman port it claimed would never go to another workload.
func TestReconcile_AWorkloadHandedToAnotherInstallIsForgotten(t *testing.T) {
	workload := lifecycleWorkload("handed-app", v1alpha1.PhaseRunning)
	workload.UID = "handed-uid"
	workload.Finalizers = []string{finalizerName}
	workload.Annotations = map[string]string{v1alpha1.AnnotationOperator: thisInstall}
	r := newTestReconciler(t, workload, &stubPauser{})
	r.OperatorNamespace = thisInstall
	_, err := r.Reconcile(context.Background(), reconcileFor("handed-app"))
	require.NoError(t, err)
	require.Equal(t, map[string]float64{"Running": 1}, reportedPhases(t, "handed-app"))
	metrics.ActivityUnreadable.WithLabelValues("default", "handed-app", "cpu").Set(1)
	metrics.ResumeIncomplete.WithLabelValues("default", "handed-app").Set(1)
	metrics.WrongPauses.WithLabelValues("default", "handed-app").Inc()
	r.forecastEngine(getWorkload(t, r, "handed-app"))
	r.activityMemo.put("handed-uid", v1alpha1.ActivityStatus{}, fixedTime, nil)
	r.quietCPU.see("handed-uid", fixedTime, fixedTime, 0.5, time.Minute)
	r.doormanFailures.next("handed-uid")
	r.doormanPorts.claims = map[int32]portClaim{30002: {owner: "handed-uid", at: fixedTime}}

	handed := getWorkload(t, r, "handed-app")
	handed.Annotations[v1alpha1.AnnotationOperator] = "team-b-system"
	require.NoError(t, r.Update(context.Background(), handed))
	_, err = r.Reconcile(context.Background(), reconcileFor("handed-app"))
	require.NoError(t, err)

	assert.Empty(t, reportedPhases(t, "handed-app"))
	assert.Empty(t, seriesOf(t, metrics.ActivityUnreadable, "handed-app", "source"))
	assert.Empty(t, seriesOf(t, metrics.ResumeIncomplete, "handed-app", ""))
	assert.Empty(t, seriesOf(t, metrics.WrongPauses, "handed-app", ""))
	assert.NotContains(t, r.engines.engines, types.UID("handed-uid"))
	_, remembered := r.activityMemo.get("handed-uid")
	assert.False(t, remembered)
	assert.NotContains(t, r.quietCPU.stretches, types.UID("handed-uid"))
	assert.NotContains(t, r.doormanFailures.failures, types.UID("handed-uid"))
	assert.Empty(t, r.doormanPorts.claims)
}

// NapwakePausedWhileInUse adds up napwake_wrong_pauses_total by the labels
// it groups by, so a wrong pause has to make a series of that name with
// those labels, and the rules, in the chart and in config/prometheus, can
// read no other metric and group by no label the series lacks.
func TestWrongPause_MakesTheSeriesItsAlertReads(t *testing.T) {
	r, now := pausedFor(t, "alerted-app", time.Minute, nil)
	t.Cleanup(func() { metrics.DeleteWorkload("default", "alerted-app") })
	requestCame(t, r, "alerted-app", *now)
	reconcileNamed(t, r, "alerted-app")

	families, err := ctrlmetrics.Registry.Gather()
	require.NoError(t, err)
	var series []map[string]string
	for _, f := range families {
		if f.GetName() != "napwake_wrong_pauses_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["workload"] == "alerted-app" {
				series = append(series, labels)
				assert.Equal(t, 1.0, m.GetCounter().GetValue())
			}
		}
	}
	require.Equal(t, []map[string]string{{"namespace": "default", "workload": "alerted-app"}}, series)

	for _, path := range []string{
		filepath.Join("config", "prometheus", "alerts.yaml"),
		filepath.Join("charts", "napwake", "templates", "prometheusrule.yaml"),
	} {
		raw, err := os.ReadFile(filepath.Join("..", "..", path))
		require.NoError(t, err)
		rule := alertRule.FindStringSubmatch(string(raw))
		require.NotNil(t, rule, "%s has NapwakePausedWhileInUse", path)
		expr := rule[1]
		assert.Equal(t, []string{"napwake_wrong_pauses_total"}, slices.Compact(napwakeMetric.FindAllString(expr, -1)), path)
		groupings := groupedBy.FindAllStringSubmatch(expr, -1)
		require.NotEmpty(t, groupings, path)
		for _, by := range groupings {
			for label := range strings.SplitSeq(by[1], ",") {
				assert.Contains(t, series[0], strings.TrimSpace(label), "%s groups by a label the series lacks", path)
			}
		}
	}
}

var (
	alertRule     = regexp.MustCompile(`(?s)alert: NapwakePausedWhileInUse\s+expr: \|(.*?)\n\s+labels:`)
	napwakeMetric = regexp.MustCompile(`napwake_\w+`)
	groupedBy     = regexp.MustCompile(`by \(([^)]*)\)`)
)

// reportedPhases returns the phases napwake_workload_phase reports for a
// workload, with their values.
func reportedPhases(t *testing.T, name string) map[string]float64 {
	t.Helper()
	return seriesOf(t, metrics.WorkloadPhase, name, "phase")
}

// seriesOf returns the series a collector has for a workload in the test
// reconciler's namespace, default, with their values, by the value of their
// label key, or by "" for a collector without it.
func seriesOf(t *testing.T, c prometheus.Collector, name, key string) map[string]float64 {
	t.Helper()
	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, reg.Register(c))
	families, err := reg.Gather()
	require.NoError(t, err)

	series := map[string]float64{}
	for _, f := range families {
		for _, m := range f.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["namespace"] == "default" && labels["workload"] == name {
				series[labels[key]] = m.GetGauge().GetValue()
			}
		}
	}
	return series
}

// A resume that left replicas not Ready says so while the workload runs.
// Once it pauses, there's nothing left to be Ready, so the condition is
// cleared as the pause begins and NapwakeResumeIncomplete doesn't fire for
// a paused workload, whatever its status says.
func TestPause_ClearsAResumeLeftIncomplete(t *testing.T) {
	r, _, now := pausingReconciler(t, func(w *v1alpha1.ManagedWorkload) {
		w.Status.Conditions = append(w.Status.Conditions, metav1.Condition{Type: conditionResumeIncomplete,
			Status: metav1.ConditionTrue, Reason: "ReplicasNotReady", LastTransitionTime: metav1.NewTime(fixedTime)})
	})
	t.Cleanup(func() { metrics.DeleteWorkload("default", "api") })
	var api appsv1.Deployment
	require.NoError(t, r.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "api"}, &api))
	api.Status.ReadyReplicas = 0
	require.NoError(t, r.Status().Update(context.Background(), &api))

	reconcileAPI(t, r)
	*now = now.Add(doormanListenWait)
	reconcileAPI(t, r)

	w := fetch(t, r, "api")
	require.Equal(t, v1alpha1.PhasePausing, w.Status.Phase)
	assert.False(t, meta.IsStatusConditionTrue(w.Status.Conditions, conditionResumeIncomplete))
	assert.Empty(t, seriesOf(t, metrics.ResumeIncomplete, "api", ""))

	for _, phase := range []v1alpha1.WorkloadPhase{v1alpha1.PhasePausing, v1alpha1.PhasePaused, v1alpha1.PhaseResuming} {
		stale := lifecycleWorkload("stale-app", phase)
		stale.Status.Conditions = []metav1.Condition{{Type: conditionResumeIncomplete, Status: metav1.ConditionTrue,
			Reason: "ReplicasNotReady"}}
		recordConditions(stale)
		assert.Empty(t, seriesOf(t, metrics.ResumeIncomplete, "stale-app", ""), "%s", phase)
	}
}

// A workload Napwake stops reading, here one whose target is labelled
// napwake.dev/ignore, has NapwakeActivityUnreadable's series dropped, though
// its status keeps the condition from when it was read.
func TestReconcile_AnIgnoredWorkloadsActivityIsntUnreadable(t *testing.T) {
	workload := lifecycleWorkload("ignored-app", v1alpha1.PhaseRunning)
	workload.Finalizers = []string{finalizerName}
	workload.Status.Conditions = []metav1.Condition{{Type: conditionMetricsAvailable, Status: metav1.ConditionFalse,
		Reason: reasonNoPodMetrics, LastTransitionTime: metav1.NewTime(fixedTime)}}
	r := newTestReconciler(t, workload, &stubPauser{})
	t.Cleanup(func() { metrics.DeleteWorkload("default", "ignored-app") })
	_, err := r.Reconcile(context.Background(), reconcileFor("ignored-app"))
	require.NoError(t, err)
	require.Equal(t, map[string]float64{"cpu": 1}, seriesOf(t, metrics.ActivityUnreadable, "ignored-app", "source"))

	var target appsv1.Deployment
	require.NoError(t, r.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "ignored-app"}, &target))
	target.Labels = map[string]string{v1alpha1.LabelIgnore: v1alpha1.True}
	require.NoError(t, r.Update(context.Background(), &target))
	for range 2 {
		_, err = r.Reconcile(context.Background(), reconcileFor("ignored-app"))
		require.NoError(t, err)
	}

	assert.Empty(t, seriesOf(t, metrics.ActivityUnreadable, "ignored-app", "source"))
}
