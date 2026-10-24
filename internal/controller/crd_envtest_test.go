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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

// A duration the API server stores but metav1.Duration can't decode breaks
// every typed read of ManagedWorkloads, so the operator's and the doorman's
// caches never sync. The CRD must accept exactly what Go parses.
func TestCRD_DurationsAreThoseGoCanDecode(t *testing.T) {
	cfg := startEnvtest(t)
	c, err := client.New(cfg, client.Options{Scheme: testScheme(t)})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "dev"}}))

	idleAfter, maxWait := []string{"idlePolicy", "idleAfter"}, []string{"wake", "maxWait"}
	tests := []struct {
		paths [][]string
		value string
		valid bool
	}{
		{paths: [][]string{idleAfter, maxWait}, value: "45m", valid: true},
		{paths: [][]string{idleAfter, maxWait}, value: "1m30s", valid: true},
		{paths: [][]string{idleAfter, maxWait}, value: "1.5m", valid: true},
		{paths: [][]string{idleAfter, maxWait}, value: "1d"},
		{paths: [][]string{idleAfter, maxWait}, value: "2w"},
		{paths: [][]string{idleAfter, maxWait}, value: "1 h"},
		{paths: [][]string{idleAfter, maxWait}, value: "1h "},
		{paths: [][]string{idleAfter, maxWait}, value: "0s"},
		{paths: [][]string{idleAfter, maxWait}, value: "-5m"},
		{paths: [][]string{idleAfter, maxWait}, value: "soon"},
		{paths: [][]string{idleAfter}, value: "168h", valid: true},
		{paths: [][]string{idleAfter}, value: "1m", valid: true},
		{paths: [][]string{idleAfter}, value: "59s"},
		{paths: [][]string{idleAfter}, value: "1s"},
		{paths: [][]string{maxWait}, value: "30s", valid: true},
		{paths: [][]string{maxWait}, value: "1h", valid: true},
		{paths: [][]string{maxWait}, value: "61m"},
		{paths: [][]string{maxWait}, value: "10000h"},
	}
	n := 0
	for _, tt := range tests {
		for _, path := range tt.paths {
			t.Run(path[1]+"="+tt.value, func(t *testing.T) {
				n++
				mw := &unstructured.Unstructured{Object: map[string]any{
					"apiVersion": "napwake.dev/v1alpha1", "kind": "ManagedWorkload",
					"metadata": map[string]any{"name": fmt.Sprintf("w%d", n), "namespace": "dev"},
					"spec": map[string]any{
						"target":     map[string]any{"kind": "Deployment", "name": "api"},
						"prediction": map[string]any{"confidence": int64(75)},
					},
				}}
				require.NoError(t, unstructured.SetNestedField(mw.Object, tt.value, append([]string{"spec"}, path...)...))

				err := c.Create(ctx, mw)
				if tt.valid {
					assert.NoError(t, err)
				} else {
					assert.True(t, apierrors.IsInvalid(err), "want the value rejected, got %v", err)
				}
			})
		}
	}

	var list v1alpha1.ManagedWorkloadList
	assert.NoError(t, c.List(ctx, &list, client.InNamespace("dev")), "every stored ManagedWorkload decodes")
}

// Cost rates can't be negative, which would price pausing as a loss, and
// the lists a reconcile works through each time are bounded.
func TestCRD_RatesAndLists(t *testing.T) {
	cfg := startEnvtest(t)
	c, err := client.New(cfg, client.Options{Scheme: testScheme(t)})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "dev"}}))

	listOf := func(n int, item func(i int) any) []any {
		items := make([]any, n)
		for i := range items {
			items[i] = item(i)
		}
		return items
	}
	query := func(int) any { return map[string]any{"promQL": "up"} }
	dependency := func(i int) any { return map[string]any{"kind": "Deployment", "name": fmt.Sprintf("dep-%d", i)} }
	rates := []string{"costTracking", "rates"}
	tests := []struct {
		name  string
		path  []string
		value any
		valid bool
	}{
		{name: "a rate", path: append(rates, "cpuPerHour"), value: "0.031611", valid: true},
		{name: "a rate of zero", path: append(rates, "memoryPerHour"), value: "0", valid: true},
		{name: "a whole-number rate", path: append(rates, "storagePerMonth"), value: int64(2), valid: true},
		{name: "a negative CPU rate", path: append(rates, "cpuPerHour"), value: "-0.03"},
		{name: "a negative memory rate", path: append(rates, "memoryPerHour"), value: "-1m"},
		{name: "a negative storage rate", path: append(rates, "storagePerMonth"), value: int64(-1)},
		{name: "10 queries", path: []string{"idlePolicy", "activity", "prometheus"}, value: listOf(10, query), valid: true},
		{name: "11 queries", path: []string{"idlePolicy", "activity", "prometheus"}, value: listOf(11, query)},
		{name: "20 dependencies", path: []string{"dependsOn"}, value: listOf(20, dependency), valid: true},
		{name: "21 dependencies", path: []string{"dependsOn"}, value: listOf(21, dependency)},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mw := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "napwake.dev/v1alpha1", "kind": "ManagedWorkload",
				"metadata": map[string]any{"name": fmt.Sprintf("w%d", i), "namespace": "dev"},
				"spec":     map[string]any{"target": map[string]any{"kind": "Deployment", "name": "api"}},
			}}
			require.NoError(t, unstructured.SetNestedField(mw.Object, tt.value, append([]string{"spec"}, tt.path...)...))

			err := c.Create(ctx, mw)
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.True(t, apierrors.IsInvalid(err), "want the value rejected, got %v", err)
			}
		})
	}
}

// The CPU threshold is a percentage from 0.1 to 100: a whole number, or a
// decimal written as a string, as Kubernetes APIs write what isn't whole.
// It's stored as written.
func TestCRD_CPUThresholdTakesDecimals(t *testing.T) {
	cfg := startEnvtest(t)
	c, err := client.New(cfg, client.Options{Scheme: testScheme(t)})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "dev"}}))

	tests := []struct {
		value any
		valid bool
	}{
		{value: int64(10), valid: true},
		{value: int64(1), valid: true},
		{value: int64(100), valid: true},
		{value: "0.5", valid: true},
		{value: "0.1", valid: true},
		{value: "12.25", valid: true},
		{value: "10", valid: true},
		{value: "100", valid: true},
		{value: int64(0)},
		{value: int64(101)},
		{value: "0.05"},
		{value: "0"},
		{value: "100.5"},
		{value: "0.125"},
		{value: "-1"},
		{value: "1e1"},
		{value: "ten"},
		{value: 0.5},
	}
	for i, tt := range tests {
		t.Run(fmt.Sprintf("%v", tt.value), func(t *testing.T) {
			mw := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "napwake.dev/v1alpha1", "kind": "ManagedWorkload",
				"metadata": map[string]any{"name": fmt.Sprintf("w%d", i), "namespace": "dev"},
				"spec": map[string]any{"target": map[string]any{"kind": "Deployment", "name": "api"},
					"idlePolicy": map[string]any{"activity": map[string]any{"cpuThreshold": tt.value}}},
			}}

			err := c.Create(ctx, mw)
			if !tt.valid {
				assert.True(t, apierrors.IsInvalid(err), "want %v rejected, got %v", tt.value, err)
				return
			}
			require.NoError(t, err)
			got, _, _ := unstructured.NestedFieldNoCopy(mw.Object, "spec", "idlePolicy", "activity", "cpuThreshold")
			assert.Equal(t, tt.value, got, "stored as written")
		})
	}
}

// The spec the opt-in controller writes is the one the API server stores,
// defaults and all, so reconciling again writes nothing.
func TestOptIn_SecondPassAgainstTheAPIServerWritesNothing(t *testing.T) {
	cfg := startEnvtest(t)
	scheme := testScheme(t)
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "dev",
		Annotations: map[string]string{v1alpha1.AnnotationDependsOn: "deployment/api"}}}))
	d := envtestDeployment("dev", 1)
	d.Labels = map[string]string{v1alpha1.LabelManaged: v1alpha1.True}
	d.Annotations = map[string]string{
		v1alpha1.AnnotationIdleAfter:   "90m",
		v1alpha1.AnnotationWakeMaxWait: "30s",
	}
	require.NoError(t, c.Create(ctx, d))
	r := &OptInReconciler{Client: c, Scheme: scheme, Recorder: events.NewFakeRecorder(100),
		Kind: v1alpha1.TargetKindDeployment, Defaults: DefaultOptInDefaults}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(d)}

	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	var mw v1alpha1.ManagedWorkload
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "dev", Name: "api"}, &mw))
	written := mw.ResourceVersion

	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "dev", Name: "api"}, &mw))
	assert.Equal(t, written, mw.ResourceVersion)
	assert.Empty(t, mw.Spec.DependsOn)
}

// Everything in prediction has a default, so a ManagedWorkload needs no more
// than its target, whether written as YAML or with the Go types.
func TestCRD_TargetIsAllAManagedWorkloadNeeds(t *testing.T) {
	cfg := startEnvtest(t)
	c, err := client.New(cfg, client.Options{Scheme: testScheme(t)})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "dev"}}))

	yaml := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "napwake.dev/v1alpha1", "kind": "ManagedWorkload",
		"metadata": map[string]any{"name": "api", "namespace": "dev"},
		"spec":     map[string]any{"target": map[string]any{"name": "api"}},
	}}
	require.NoError(t, c.Create(ctx, yaml))
	typed := &v1alpha1.ManagedWorkload{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "dev"},
		Spec: v1alpha1.ManagedWorkloadSpec{Target: v1alpha1.WorkloadRef{Kind: v1alpha1.TargetKindDeployment, Name: "worker"}}}
	require.NoError(t, c.Create(ctx, typed))

	for _, name := range []string{"api", "worker"} {
		var mw v1alpha1.ManagedWorkload
		require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "dev", Name: name}, &mw))
		assert.Equal(t, v1alpha1.PredictionBalanced, mw.Spec.Prediction.Mode, "%s takes the default mode", name)
		assert.Zero(t, mw.Spec.Prediction.Confidence, "%s overrides no threshold", name)
	}
}
