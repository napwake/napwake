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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/event"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

// graphReconciler is a Reconciler whose dependency graph is fed only by
// relatedHandler, as SetupWithManager feeds it, with a client that counts
// the lists of every ManagedWorkload made, which the graph makes unneeded.
func graphReconciler(t *testing.T, objs ...client.Object) (*Reconciler, *int) {
	t.Helper()
	r := depReconciler(t, &stubPauser{}, objs...)
	r.graph = newWorkloadGraph()
	var lists int
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			var o client.ListOptions
			o.ApplyOptions(opts)
			if _, ok := list.(*v1alpha1.ManagedWorkloadList); ok && o.FieldSelector == nil {
				lists++
			}
			return c.List(ctx, list, opts...)
		},
	})
	return r, &lists
}

func newQueue(t *testing.T) workqueue.TypedRateLimitingInterface[reconcile.Request] {
	t.Helper()
	q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	t.Cleanup(q.ShutDown)
	return q
}

func holdersOf(t *testing.T, r *Reconciler, workload *v1alpha1.ManagedWorkload) []string {
	t.Helper()
	h, err := r.heldByDependencies(context.Background(), workload)
	require.NoError(t, err)
	return h.holders
}

// The graph follows each ManagedWorkload as the watch reports it, so a
// hold is decided from the dependents' phases now without a list.
func TestWorkloadGraph_FollowsTheWatch(t *testing.T) {
	postgres := depWorkload("default", "postgres", v1alpha1.TargetKindStatefulSet, v1alpha1.PhaseRunning)
	api := depWorkload("default", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning, postgresRef())
	r, lists := graphReconciler(t)
	h, q := r.relatedHandler(), newQueue(t)
	ctx := context.Background()
	update := func(old *v1alpha1.ManagedWorkload, phase v1alpha1.WorkloadPhase) *v1alpha1.ManagedWorkload {
		cur := old.DeepCopy()
		cur.Status.Phase = phase
		h.Update(ctx, event.UpdateEvent{ObjectOld: old, ObjectNew: cur}, q)
		return cur
	}

	h.Create(ctx, event.CreateEvent{Object: postgres}, q)
	assert.Empty(t, holdersOf(t, r, postgres))

	h.Create(ctx, event.CreateEvent{Object: api}, q)
	assert.Equal(t, []string{"default/api"}, holdersOf(t, r, postgres), "a new dependent holds it")

	api = update(api, v1alpha1.PhasePaused)
	assert.Empty(t, holdersOf(t, r, postgres), "a dependent that paused lets it go")

	api = update(api, v1alpha1.PhaseResuming)
	assert.Equal(t, []string{"default/api"}, holdersOf(t, r, postgres), "a dependent waking holds it again")

	withoutDeps := api.DeepCopy()
	withoutDeps.Spec.DependsOn = nil
	withoutDeps.Generation++
	h.Update(ctx, event.UpdateEvent{ObjectOld: api, ObjectNew: withoutDeps}, q)
	assert.Empty(t, holdersOf(t, r, postgres), "a dependency dropped from dependsOn is no longer held")

	h.Update(ctx, event.UpdateEvent{ObjectOld: withoutDeps, ObjectNew: api}, q)
	h.Delete(ctx, event.DeleteEvent{Object: api}, q)
	assert.Empty(t, holdersOf(t, r, postgres), "a deleted dependent lets it go")
	assert.Zero(t, *lists, "no ManagedWorkloads are listed for the graph")
}

// A workload re-checking its hold because a dependent changed sees the
// change: the graph is updated before the re-check is queued.
func TestWorkloadGraph_UpdatedBeforeRelatedAreQueued(t *testing.T) {
	postgres := depWorkload("default", "postgres", v1alpha1.TargetKindStatefulSet, v1alpha1.PhaseRunning)
	paused := depWorkload("default", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhasePaused, postgresRef())
	r, _ := graphReconciler(t, postgres, paused)
	h := r.relatedHandler()
	ctx := context.Background()
	h.Create(ctx, event.CreateEvent{Object: postgres}, newQueue(t))
	h.Create(ctx, event.CreateEvent{Object: paused}, newQueue(t))
	woken := paused.DeepCopy()
	woken.Status.Phase = v1alpha1.PhaseResuming

	q := &checkingQueue{TypedRateLimitingInterface: newQueue(t), onAdd: func(req reconcile.Request) {
		if req.Name == postgres.Name {
			assert.Equal(t, []string{"default/api"}, holdersOf(t, r, postgres))
		}
	}}
	h.Update(ctx, event.UpdateEvent{ObjectOld: paused, ObjectNew: woken}, q)

	assert.Positive(t, q.Len(), "postgres re-checks its hold")
}

type checkingQueue struct {
	workqueue.TypedRateLimitingInterface[reconcile.Request]
	onAdd func(reconcile.Request)
}

func (q *checkingQueue) Add(req reconcile.Request) {
	q.onAdd(req)
	q.TypedRateLimitingInterface.Add(req)
}

// Of two ManagedWorkloads for one target, the one the duplicate check lets
// act on it is the one woken, until it's gone.
func TestWorkloadGraph_DuplicateOwnerIsWoken(t *testing.T) {
	older := depWorkload("default", "postgres", v1alpha1.TargetKindStatefulSet, v1alpha1.PhasePaused)
	older.CreationTimestamp = metav1.NewTime(fixedTime.Add(-time.Hour))
	newer := depWorkload("default", "postgres-copy", v1alpha1.TargetKindStatefulSet, v1alpha1.PhasePaused)
	newer.Spec.Target.Name = older.Name
	newer.CreationTimestamp = metav1.NewTime(fixedTime)
	api := depWorkload("default", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseResuming, postgresRef())
	r, _ := graphReconciler(t, older, newer, api)
	h, q := r.relatedHandler(), newQueue(t)
	ctx := context.Background()
	for _, w := range []*v1alpha1.ManagedWorkload{newer, api, older} {
		h.Create(ctx, event.CreateEvent{Object: w}, q)
	}
	stamped := func(name string) bool {
		return fetch(t, r, name).Annotations[v1alpha1.AnnotationLastActivity] != ""
	}

	r.wakeDependencies(ctx, api)
	assert.True(t, stamped(older.Name))
	assert.False(t, stamped("postgres-copy"))

	h.Delete(ctx, event.DeleteEvent{Object: older}, q)
	r.wakeDependencies(ctx, api)
	assert.True(t, stamped("postgres-copy"), "the duplicate takes over once the owner is gone")
}

// A dependency in another namespace is allowed by that namespace's
// annotation, read for as long as a workload depends on one there.
func TestWorkloadGraph_CrossedNamespaces(t *testing.T) {
	shared := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default",
		Annotations: map[string]string{v1alpha1.AnnotationDependentsFrom: "preview-42"}}}
	postgres := depWorkload("default", "postgres", v1alpha1.TargetKindStatefulSet, v1alpha1.PhaseRunning)
	preview := depWorkload("preview-42", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning,
		v1alpha1.DependencyRef{Namespace: "default", Kind: v1alpha1.TargetKindStatefulSet, Name: "postgres"})
	r, _ := graphReconciler(t, shared)
	h, q := r.relatedHandler(), newQueue(t)
	ctx := context.Background()
	h.Create(ctx, event.CreateEvent{Object: postgres}, q)
	h.Create(ctx, event.CreateEvent{Object: preview}, q)

	assert.Equal(t, []string{"default"}, r.graph.crossedNamespaces())
	assert.Equal(t, []string{"preview-42/api"}, holdersOf(t, r, postgres))

	h.Delete(ctx, event.DeleteEvent{Object: preview}, q)
	assert.Empty(t, r.graph.crossedNamespaces())
}

// Run by the manager, the graph is fed by the operator's own watch from
// its start: a dependent created before the operator holds its dependency
// awake, and lets it go once it's paused itself.
func TestDependencies_HeldThroughTheOperatorsWatch(t *testing.T) {
	cfg := startEnvtest(t)
	opScheme := testScheme(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c, err := client.New(cfg, client.Options{Scheme: opScheme})
	require.NoError(t, err)

	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}}))
	for _, name := range []string{"api", "postgres"} {
		d := envtestDeployment("shop", 1)
		d.Name = name
		require.NoError(t, c.Create(ctx, d))
	}
	api := envtestWorkload("shop")
	api.Spec.DependsOn = []v1alpha1.DependencyRef{{Kind: v1alpha1.TargetKindDeployment, Name: "postgres"}}
	require.NoError(t, c.Create(ctx, api))
	postgres := withPauseRequested(envtestWorkload("shop"))
	postgres.Name = "postgres"
	postgres.Spec.Target.Name = postgres.Name
	require.NoError(t, createTakenOn(ctx, c, postgres))

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: opScheme,
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: config.Controller{SkipNameValidation: ptr.To(true)}})
	require.NoError(t, err)
	r := &Reconciler{Client: mgr.GetClient(), Scheme: opScheme, Recorder: events.NewFakeRecorder(1000),
		PodReader: mgr.GetAPIReader()}
	require.NoError(t, r.SetupWithManager(mgr))
	mgrCtx, stop := context.WithCancel(context.Background())
	var running sync.WaitGroup
	running.Go(func() { assert.NoError(t, mgr.Start(mgrCtx)) })
	t.Cleanup(func() {
		stop()
		running.Wait()
	})

	get := func(name string) *v1alpha1.ManagedWorkload {
		var w v1alpha1.ManagedWorkload
		require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "shop", Name: name}, &w))
		return &w
	}
	requestPause := func(name, token string) {
		patch := client.RawPatch(types.MergePatchType,
			[]byte(`{"metadata":{"annotations":{"`+v1alpha1.AnnotationPauseRequested+`":"`+token+`"}}}`))
		require.NoError(t, c.Patch(ctx, get(name), patch))
	}

	require.Eventually(t, func() bool {
		cond := meta.FindStatusCondition(get("postgres").Status.Conditions, conditionPauseRequest)
		return cond != nil && cond.Reason == conditionHeldByDependents
	}, 30*time.Second, 100*time.Millisecond, "postgres is held awake for api")
	assert.Equal(t, v1alpha1.PhaseRunning, get("postgres").Status.Phase)

	// postgres can be held for api before api is taken on, and a request
	// already on api as it's taken on isn't acted on.
	require.Eventually(t, func() bool {
		return get("api").Status.Phase == v1alpha1.PhaseRunning
	}, 30*time.Second, 100*time.Millisecond, "api is taken on")
	requestPause("api", "api")
	require.Eventually(t, func() bool {
		return get("api").Status.Phase == v1alpha1.PhasePaused
	}, 30*time.Second, 100*time.Millisecond, "api pauses")

	requestPause("postgres", "again")
	assert.Eventually(t, func() bool {
		return get("postgres").Status.Phase == v1alpha1.PhasePaused
	}, 30*time.Second, 100*time.Millisecond, "postgres pauses once api has")
}
