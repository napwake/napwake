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
	"os"
	"path/filepath"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
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

	phases := reportedPhases(t, "default", "stale-gauge-app")
	assert.Len(t, phases, 1, "one phase from a stale read, not one more: %v", phases)

	r.Client = live
	_, err = r.Reconcile(context.Background(), reconcileFor("stale-gauge-app"))
	require.NoError(t, err)

	assert.Equal(t, map[string]float64{"Paused": 1}, reportedPhases(t, "default", "stale-gauge-app"),
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

// reportedPhases returns the phases napwake_workload_phase reports for a
// workload, with their values.
func reportedPhases(t *testing.T, namespace, name string) map[string]float64 {
	t.Helper()
	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, reg.Register(metrics.WorkloadPhase))
	families, err := reg.Gather()
	require.NoError(t, err)

	phases := map[string]float64{}
	for _, f := range families {
		for _, m := range f.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["namespace"] == namespace && labels["workload"] == name {
				phases[labels["phase"]] = m.GetGauge().GetValue()
			}
		}
	}
	return phases
}
