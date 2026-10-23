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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
)

const wakeToken = "2026-03-14T12:00:30.123456789Z"

func wakeRequestCondition(t *testing.T, w *v1alpha1.ManagedWorkload) *metav1.Condition {
	t.Helper()
	c := meta.FindStatusCondition(w.Status.Conditions, conditionWakeRequest)
	require.NotNil(t, c, "the WakeRequest condition says what came of the request")
	return c
}

func requestWake(t *testing.T, r *Reconciler, token string) {
	t.Helper()
	patchWorkload(t, r, func(w *v1alpha1.ManagedWorkload) {
		w.Annotations[v1alpha1.AnnotationWakeRequested] = token
	})
}

// pausedOnRequest is api paused, as a pause request leaves it, with the
// events of its pause drained.
func pausedOnRequest(t *testing.T) *Reconciler {
	t.Helper()
	r := lifecycleReconciler(t, requestedPause(), 3, interceptor.Funcs{})
	reconcileUntilSettled(t, r)
	require.Equal(t, v1alpha1.PhasePaused, getWorkload(t, r, "api").Status.Phase)
	require.Equal(t, int32(0), targetReplicas(t, r))
	drainEvents(t, r)
	return r
}

// A wake request wakes a paused workload as activity does, to every replica
// it had, and is acted on once: paused again later, the workload isn't
// woken by the same request.
func TestWakeRequest_WakesAPausedWorkloadOnce(t *testing.T) {
	r := pausedOnRequest(t)

	requestWake(t, r, wakeToken)
	reconcileUntilSettled(t, r)

	got := getWorkload(t, r, "api")
	assert.Equal(t, v1alpha1.PhaseRunning, got.Status.Phase)
	assert.Equal(t, int32(3), targetReplicas(t, r))
	assert.Equal(t, wakeToken, got.Status.LastWakeRequest)
	assert.Equal(t, v1alpha1.ActivitySourceWoke, got.Status.Activity.LastActivitySource)
	c := wakeRequestCondition(t, got)
	assert.Equal(t, metav1.ConditionTrue, c.Status)
	assert.Equal(t, reasonWoken, c.Reason)
	assert.Equal(t, "woken as requested at 2026-03-14 12:00 UTC", c.Message,
		"what happened and when, as it still reads after the workload has paused again")
	recorded := drainEvents(t, r)
	assert.Equal(t, 1, strings.Count(recorded, "Normal WakeRequested api: wake requested, waking"), recorded)
	assert.Equal(t, 1, strings.Count(recorded, "Normal Resumed api: resumed"), recorded)

	patchWorkload(t, r, func(w *v1alpha1.ManagedWorkload) {
		w.Annotations[v1alpha1.AnnotationPauseRequested] = "a-second-pause"
	})
	reconcileUntilSettled(t, r)

	got = getWorkload(t, r, "api")
	assert.Equal(t, v1alpha1.PhasePaused, got.Status.Phase, "the handled request doesn't wake it again")
	assert.Equal(t, int32(0), targetReplicas(t, r))
	assert.Equal(t, "woken as requested at 2026-03-14 12:00 UTC", wakeRequestCondition(t, got).Message,
		"it answers the last request until the next")
	assert.NotContains(t, drainEvents(t, r), ReasonWakeRequested)

	requestWake(t, r, "a-second-wake")
	reconcileUntilSettled(t, r)

	assert.Equal(t, v1alpha1.PhaseRunning, getWorkload(t, r, "api").Status.Phase, "a new request wakes it")
}

// A wake request for a workload that's awake is answered so, and restarts
// its idle clock, as someone means to use it: it pauses an idleAfter later,
// not when its clock would have run out.
func TestWakeRequest_AlreadyAwake(t *testing.T) {
	workload := requestedPause()
	delete(workload.Annotations, v1alpha1.AnnotationPauseRequested)
	workload.Annotations[v1alpha1.AnnotationWakeRequested] = wakeToken
	workload.Status.Activity.LastActivityTime = metav1.NewTime(fixedTime.Add(-50 * time.Minute))
	r := lifecycleReconciler(t, workload, 3, interceptor.Funcs{})

	reconcileUntilSettled(t, r)

	got := getWorkload(t, r, "api")
	assert.Equal(t, v1alpha1.PhaseRunning, got.Status.Phase)
	assert.Equal(t, int32(3), targetReplicas(t, r))
	assert.Equal(t, wakeToken, got.Status.LastWakeRequest)
	c := wakeRequestCondition(t, got)
	assert.Equal(t, metav1.ConditionTrue, c.Status)
	assert.Equal(t, reasonAlreadyAwake, c.Reason)
	assert.Equal(t, "already awake at 2026-03-14 12:00 UTC", c.Message)
	assert.Equal(t, fixedTime, got.Status.Activity.LastActivityTime.UTC(), "its idle clock restarts")
	assert.Equal(t, fixedTime.Add(time.Hour), got.Status.Activity.PauseAt.UTC())
	recorded := drainEvents(t, r)
	assert.NotContains(t, recorded, ReasonWakeRequested)
	assert.NotContains(t, recorded, "wake requested")
}

// Under dry-run nothing is paused, so a wake request finds the workload
// awake, and ends the would-be pause it's in, as activity would, measuring
// what it would have slept.
func TestWakeRequest_EndsAWouldBePause(t *testing.T) {
	workload := requestedPause()
	workload.Spec.DryRun = true
	r := lifecycleReconciler(t, workload, 3, interceptor.Funcs{})
	now := fixedTime
	r.clock = func() time.Time { return now }
	reconcileUntilSettled(t, r)
	require.Equal(t, v1alpha1.PhaseIdle, getWorkload(t, r, "api").Status.Phase)

	now = fixedTime.Add(30 * time.Minute)
	requestWake(t, r, wakeToken)
	reconcileUntilSettled(t, r)

	got := getWorkload(t, r, "api")
	assert.Equal(t, v1alpha1.PhaseRunning, got.Status.Phase)
	assert.Equal(t, int32(3), targetReplicas(t, r), "dry-run never scales")
	assert.Equal(t, reasonAlreadyAwake, wakeRequestCondition(t, got).Reason)
	assert.Equal(t, 30*time.Minute, got.Status.DryRun.Slept.Duration)
}

// A wake request that finds the workload pausing is answered once the pause
// is done, by waking it; one that finds it waking, once it's up.
func TestWakeRequest_DuringATransition(t *testing.T) {
	tests := []struct {
		phase       v1alpha1.WorkloadPhase
		wantReason  string
		wantMessage string
	}{
		{phase: v1alpha1.PhasePausing, wantReason: reasonWoken,
			wantMessage: "woken as requested at 2026-03-14 12:00 UTC"},
		{phase: v1alpha1.PhaseResuming, wantReason: reasonAlreadyAwake,
			wantMessage: "already awake at 2026-03-14 12:00 UTC"},
	}
	for _, tt := range tests {
		t.Run(string(tt.phase), func(t *testing.T) {
			workload := requestedPause()
			delete(workload.Annotations, v1alpha1.AnnotationPauseRequested)
			workload.Annotations[v1alpha1.AnnotationWakeRequested] = wakeToken
			workload.Status.Phase = tt.phase
			workload.Status.Pause = &v1alpha1.PauseStatus{PreviousReplicas: 3}
			r := lifecycleReconciler(t, workload, 3, interceptor.Funcs{})

			reconcileUntilSettled(t, r)

			got := getWorkload(t, r, "api")
			assert.Equal(t, v1alpha1.PhaseRunning, got.Status.Phase)
			assert.Equal(t, int32(3), targetReplicas(t, r))
			assert.Equal(t, wakeToken, got.Status.LastWakeRequest)
			c := wakeRequestCondition(t, got)
			assert.Equal(t, tt.wantReason, c.Reason)
			assert.Equal(t, tt.wantMessage, c.Message)
		})
	}
}

// A request Hybernate can't act on is answered once, with the reason, so
// the tool that asked can say why, and it isn't tried again on every
// reconcile.
func TestWakeRequest_Refused(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*v1alpha1.ManagedWorkload)
		replicas   int32
		objs       []client.Object
		setup      func(*testing.T, *Reconciler)
		wantReason string
		wantEvent  string
	}{
		{
			name: "protected namespace", replicas: 3, objs: []client.Object{defaultNamespace(protectedLabel)},
			wantReason: ReasonProtected,
			wantEvent:  "Warning Protected api: wake requested, but namespace default is protected",
		},
		{
			name: "scaled to zero outside Hybernate", replicas: 0, wantReason: ReasonScaledToZero,
			wantEvent: "Normal ScaledToZero api: wake requested, but it's scaled to zero outside Hybernate, " +
				"which leaves it off until its replicas are set above zero",
		},
		{
			name: "target missing", replicas: 3, wantReason: ReasonTargetNotFound,
			setup: func(t *testing.T, r *Reconciler) {
				require.NoError(t, r.Delete(context.Background(), targetDeployment("api", "default")))
			},
			wantEvent: "Warning TargetNotFound api: wake requested, but Deployment api not found",
		},
		{
			name: "target ignored", replicas: 3, wantReason: "TargetIgnored",
			setup: func(t *testing.T, r *Reconciler) {
				patchTarget(t, r, func(d *appsv1.Deployment) {
					d.Labels = map[string]string{v1alpha1.LabelIgnore: v1alpha1.True}
				})
			},
			wantEvent: "Warning TargetIgnored api: wake requested, but Deployment api has the hybernate.io/ignore label",
		},
		{
			name: "another ManagedWorkload manages the target", replicas: 3,
			mutate: func(w *v1alpha1.ManagedWorkload) { w.UID = "the-second" },
			objs: []client.Object{func() client.Object {
				owner := lifecycleWorkload("api", v1alpha1.PhaseRunning)
				owner.Name, owner.UID = "api-first", "the-first"
				return owner
			}()},
			wantReason: conditionDuplicateTarget,
			wantEvent:  "Warning DuplicateTarget api: wake requested, but Deployment/api is already managed by api-first",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workload := requestedPause()
			delete(workload.Annotations, v1alpha1.AnnotationPauseRequested)
			workload.Annotations[v1alpha1.AnnotationWakeRequested] = wakeToken
			if tt.mutate != nil {
				tt.mutate(workload)
			}
			r := lifecycleReconciler(t, workload, tt.replicas, interceptor.Funcs{}, tt.objs...)
			if tt.setup != nil {
				tt.setup(t, r)
			}

			reconcileUntilSettled(t, r)

			got := getWorkload(t, r, "api")
			assert.Equal(t, wakeToken, got.Status.LastWakeRequest)
			c := wakeRequestCondition(t, got)
			assert.Equal(t, metav1.ConditionFalse, c.Status)
			assert.Equal(t, tt.wantReason, c.Reason)
			assert.True(t, strings.HasPrefix(c.Message, "not woken at 2026-03-14 12:00 UTC: "), c.Message)
			recorded := drainEvents(t, r)
			assert.Contains(t, recorded, tt.wantEvent)
			assert.Equal(t, 1, strings.Count(recorded, "wake requested, but"), "answered once: %s", recorded)
		})
	}
}

// A wake request for a ManagedWorkload being deleted is answered before it
// goes: Hybernate hands the workload back with the replicas it had, rather
// than wake it to manage it on.
func TestWakeRequest_RefusedWhileBeingDeleted(t *testing.T) {
	workload := pausedWorkload(v1alpha1.PhasePaused)
	workload.Finalizers = []string{finalizerName}
	workload.DeletionTimestamp = &metav1.Time{Time: fixedTime.Add(-time.Second)}
	workload.Annotations = map[string]string{v1alpha1.AnnotationWakeRequested: wakeToken}
	var answered *metav1.Condition
	var token string
	funcs := interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c client.Client, sub string,
		obj client.Object, opts ...client.SubResourceUpdateOption) error {
		if w, ok := obj.(*v1alpha1.ManagedWorkload); ok && sub == statusSubresource {
			if found := meta.FindStatusCondition(w.Status.Conditions, conditionWakeRequest); found != nil {
				answered, token = found.DeepCopy(), w.Status.LastWakeRequest
			}
		}
		return c.SubResource(sub).Update(ctx, obj, opts...)
	}}
	r := lifecycleReconciler(t, workload, 0, funcs)

	reconcileUntilSettled(t, r)

	require.NotNil(t, answered, "answered while the ManagedWorkload is still there to read")
	assert.Equal(t, wakeToken, token)
	assert.Equal(t, metav1.ConditionFalse, answered.Status)
	assert.Equal(t, reasonBeingDeleted, answered.Reason)
	assert.Equal(t, "not woken at 2026-03-14 12:00 UTC: its ManagedWorkload is being deleted, so Hybernate hands "+
		"the workload back, with the replicas it had, as it lets go", answered.Message)
	assert.Equal(t, int32(3), targetReplicas(t, r), "handed back")
	assert.Equal(t, 1, strings.Count(drainEvents(t, r), "wake requested, but"))
}

// Against an API server, a wake request is recorded in status, so a
// workload paused since isn't woken by it again, and a new request wakes it.
func TestWakeRequest_AgainstAnAPIServer(t *testing.T) {
	cfg := startEnvtest(t)
	opScheme := testScheme(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c, err := client.New(cfg, client.Options{Scheme: opScheme})
	require.NoError(t, err)

	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "preview"}}))
	require.NoError(t, c.Create(ctx, envtestDeployment("preview", 3)))
	mw := envtestWorkload("preview")
	mw.Annotations = map[string]string{v1alpha1.AnnotationWakeRequested: wakeToken}
	mw.Spec.IdlePolicy = &v1alpha1.IdlePolicySpec{IdleAfter: &metav1.Duration{Duration: time.Hour}}
	require.NoError(t, c.Create(ctx, mw))

	key := client.ObjectKeyFromObject(mw)
	get := func() *v1alpha1.ManagedWorkload {
		var w v1alpha1.ManagedWorkload
		require.NoError(t, c.Get(ctx, key, &w))
		return &w
	}
	replicas := func() int32 {
		var d appsv1.Deployment
		require.NoError(t, c.Get(ctx, key, &d))
		return *d.Spec.Replicas
	}
	annotate := func(name, value string) {
		patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, name, value)
		require.NoError(t, c.Patch(ctx, get(), client.RawPatch(types.MergePatchType, []byte(patch))))
	}
	podsAt := func(n int32) {
		var d appsv1.Deployment
		require.NoError(t, c.Get(ctx, key, &d))
		d.Status.ObservedGeneration = d.Generation
		d.Status.Replicas, d.Status.ReadyReplicas = n, n
		require.NoError(t, c.Status().Update(ctx, &d))
	}

	startOperator(t, cfg, opScheme)
	require.Eventually(t, func() bool {
		w := get()
		answer := meta.FindStatusCondition(w.Status.Conditions, conditionWakeRequest)
		return w.Status.LastWakeRequest == wakeToken && answer != nil && answer.Reason == reasonAlreadyAwake
	}, 30*time.Second, 100*time.Millisecond, "a running workload is answered as already awake")

	annotate(v1alpha1.AnnotationPauseRequested, "a-pause")
	require.Eventually(t, func() bool { return get().Status.Phase == v1alpha1.PhasePaused && replicas() == 0 },
		30*time.Second, 100*time.Millisecond, "a pause request pauses it, with no pods to wait for")
	annotate("poke", "after-the-pause")
	assert.Never(t, func() bool { return replicas() != 0 }, 3*time.Second, 100*time.Millisecond,
		"the handled request doesn't wake it")

	annotate(v1alpha1.AnnotationWakeRequested, "a-new-wake")
	require.Eventually(t, func() bool { return get().Status.Phase == v1alpha1.PhaseResuming && replicas() == 3 },
		30*time.Second, 100*time.Millisecond, "a new request wakes it to the replicas it had")
	podsAt(3)
	require.Eventually(t, func() bool {
		w := get()
		answer := meta.FindStatusCondition(w.Status.Conditions, conditionWakeRequest)
		return w.Status.Phase == v1alpha1.PhaseRunning && w.Status.LastWakeRequest == "a-new-wake" &&
			answer != nil && answer.Reason == reasonWoken
	}, 30*time.Second, 100*time.Millisecond, "Running once its pods are Ready, the request answered")
}
