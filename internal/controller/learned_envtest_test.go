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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

// The break-it table on rc.11: api learned postgres a second before
// postgres was labelled, so api's only DependenciesLearned said postgres
// isn't managed and isn't woken with it, and kubectl describe kept saying
// so while postgres was held and woken with api. Once Napwake takes the
// dependency on, api says so, once.
func TestLearnedDependency_ManagedLaterIsAnnounced(t *testing.T) {
	cfg := startEnvtest(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	scheme := testScheme(t)
	admin, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	const ns = "orders"
	require.NoError(t, admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	pgLabels := map[string]string{"app": "postgres"}
	require.NoError(t, admin.Create(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "postgres", Namespace: ns},
		Spec: corev1.ServiceSpec{Selector: pgLabels, Ports: []corev1.ServicePort{{Name: "postgres", Port: 5432}}}}))
	require.NoError(t, admin.Create(ctx, &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "postgres", Namespace: ns},
		Spec: appsv1.StatefulSetSpec{Replicas: ptr.To(int32(1)), ServiceName: "postgres",
			Selector: &metav1.LabelSelector{MatchLabels: pgLabels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: pgLabels},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "postgres", Image: "postgres:17"}}}}}}))
	api := envtestDeployment(ns, 2)
	api.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{
		{Name: "PGRST_DB_URI", Value: "postgres://authenticator@postgres:5432/orders"}}
	require.NoError(t, admin.Create(ctx, api))
	apiMW := envtestWorkload(ns)
	require.NoError(t, createTakenOn(ctx, admin, apiMW))

	recorder := events.NewFakeRecorder(100)
	c := client.WithFieldOwner(indexedClient{admin}, v1alpha1.FieldManager)
	r := &Reconciler{Client: c, Scheme: scheme, Recorder: recorder, PodReader: admin,
		metrics: &stubMetrics{cpuPerReplica: 100, memoryPerReplica: 1 << 30, replicas: 2},
		engines: newEngineRegistry(func() forecaster { return &stubForecaster{} }), clock: time.Now}
	// api's DependenciesLearned events, by the target each event's note
	// starts with.
	learned := func() []string {
		var out []string
		for len(recorder.Events) > 0 {
			if e := <-recorder.Events; strings.HasPrefix(e, "Normal "+ReasonDependenciesLearned+" api: ") {
				out = append(out, e)
			}
		}
		return out
	}

	require.NoError(t, r.learnDependencies(ctx, apiMW, api))
	before := learned()
	require.Len(t, before, 1)
	require.Contains(t, before[0], "api: depends on orders/postgres (PGRST_DB_URI), which isn't managed by Napwake")

	postgres := &v1alpha1.ManagedWorkload{ObjectMeta: metav1.ObjectMeta{Name: "postgres", Namespace: ns},
		Spec: v1alpha1.ManagedWorkloadSpec{
			Target:     v1alpha1.WorkloadRef{Kind: v1alpha1.TargetKindStatefulSet, Name: "postgres"},
			Prediction: v1alpha1.PredictionSpec{Confidence: 85}}}
	require.NoError(t, admin.Create(ctx, postgres))
	key := client.ObjectKeyFromObject(postgres)
	for range 3 {
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		require.NoError(t, err)
	}
	require.NoError(t, admin.Get(ctx, key, postgres))
	require.Equal(t, v1alpha1.PhaseRunning, postgres.Status.Phase, "Napwake took postgres on")

	after := learned()
	assert.Equal(t, []string{"Normal DependenciesLearned api: depends on orders/postgres (PGRST_DB_URI), " +
		"now managed by Napwake, so " + wokenAndHeld}, after, "said once, on api, as postgres is taken on")
}
