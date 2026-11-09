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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	typedeventsv1 "k8s.io/client-go/kubernetes/typed/events/v1"
	"k8s.io/client-go/rest"
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
//
// The events are sent by the events.k8s.io recorder the operator runs, and
// read back from the API server, as kubectl describe reads them. That
// recorder folds an event into an earlier one with the same type, reason,
// action, regarding object and related object, keeping the earlier note: on
// kind (P85), the announcement became a second count of the stale one,
// which a fake recorder never shows.
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

	sent := startEventRecorder(t, cfg, scheme)
	c := client.WithFieldOwner(indexedClient{admin}, v1alpha1.FieldManager)
	r := &Reconciler{Client: c, Scheme: scheme, Recorder: sent.recorder, PodReader: admin,
		metrics: &stubMetrics{cpuPerReplica: 100, memoryPerReplica: 1 << 30, replicas: 2},
		engines: newEngineRegistry(func() forecaster { return &stubForecaster{} }), clock: time.Now}
	// api's DependenciesLearned events as the API server keeps them, with
	// how many times each was counted.
	learned := func() []string {
		var out []string
		for _, e := range sent.list(t, ns) {
			if e.Regarding.Name != apiMW.Name || e.Reason != ReasonDependenciesLearned {
				continue
			}
			count := int32(1)
			if e.Series != nil {
				count = e.Series.Count
			}
			out = append(out, fmt.Sprintf("x%d %s", count, e.Note))
		}
		return out
	}
	const isnt = "x1 api: depends on orders/postgres (PGRST_DB_URI), which isn't managed by Napwake, so it isn't " +
		"woken with api: opt it in to have them paused and woken together"
	const nowManaged = "x1 api: depends on orders/postgres (PGRST_DB_URI), now managed by Napwake, so " + wokenAndHeld

	require.NoError(t, r.learnDependencies(ctx, apiMW, api))
	// Written before postgres is taken on, as on kind, where the two were a
	// second or more apart.
	if !assert.Eventually(t, func() bool { return len(learned()) == 1 }, 10*time.Second, 50*time.Millisecond) {
		t.Fatalf("api's first DependenciesLearned never reached the API server: %q", learned())
	}
	require.Equal(t, []string{isnt}, learned())

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

	if !assert.Eventually(t, func() bool { return slices.Contains(learned(), nowManaged) }, 10*time.Second,
		50*time.Millisecond) {
		t.Fatalf("kubectl describe would show api's DependenciesLearned as %q, without the announcement", learned())
	}
	assert.ElementsMatch(t, []string{isnt, nowManaged}, learned(), "said once, on api, as postgres is taken on, "+
		"beside what api said before")
	announced := slices.IndexFunc(sent.list(t, ns), func(e eventsv1.Event) bool {
		return strings.Contains(e.Note, "now managed by Napwake")
	})
	require.GreaterOrEqual(t, announced, 0)
	related := sent.list(t, ns)[announced].Related
	require.NotNil(t, related, "the announcement names the dependency it's about")
	assert.Equal(t, "ManagedWorkload", related.Kind)
	assert.Equal(t, ns+"/postgres", related.Namespace+"/"+related.Name)
}

// sentEvents is the events.k8s.io recorder the operator runs, sending to an
// envtest API server, and a client that reads what it sent back.
type sentEvents struct {
	recorder events.EventRecorder
	events   typedeventsv1.EventsV1Interface
}

// startEventRecorder starts the recorder controller-runtime's manager
// builds for GetEventRecorder, with its merging of repeated events, until
// the test ends.
func startEventRecorder(t *testing.T, cfg *rest.Config, scheme *runtime.Scheme) sentEvents {
	t.Helper()
	clientset, err := kubernetes.NewForConfig(cfg)
	require.NoError(t, err)
	broadcaster := events.NewBroadcaster(&events.EventSinkImpl{Interface: clientset.EventsV1()})
	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, broadcaster.StartRecordingToSinkWithContext(ctx))
	t.Cleanup(func() {
		cancel()
		broadcaster.Shutdown()
	})
	return sentEvents{recorder: broadcaster.NewRecorder(scheme, "napwake"), events: clientset.EventsV1()}
}

// list is every event in the namespace, as the API server has it.
func (s sentEvents) list(t *testing.T, namespace string) []eventsv1.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	list, err := s.events.Events(namespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	return list.Items
}
