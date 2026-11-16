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
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

// standIn is the field manager of an operator that owns the workload and
// sets its replicas, as mattermost-operator does.
const standIn = "stand-in-operator"

// undoneEnv is an API server, with the events.k8s.io recorder the operator
// runs, in which each case gets a namespace with an api Deployment of 1,
// written by the stand-in operator, and a ManagedWorkload whose idle clock,
// with an idleAfter of 5 minutes, ran out at 09:55.
type undoneEnv struct {
	ctx   context.Context
	admin client.WithWatch
	sent  sentEvents
}

func startUndoneEnv(t *testing.T) *undoneEnv {
	t.Helper()
	cfg := startEnvtest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	admin, err := client.NewWithWatch(cfg, client.Options{Scheme: testScheme(t)})
	require.NoError(t, err)
	return &undoneEnv{ctx: ctx, admin: admin, sent: startEventRecorder(t, cfg, testScheme(t))}
}

// undoneCase is one case's workload, and the operator watching it.
type undoneCase struct {
	*undoneEnv
	key client.ObjectKey
	r   *Reconciler
	now time.Time
}

// madeUpMattermost is a controller owner reference to a Mattermost
// installation, as mattermost-operator puts on the Deployment it makes.
// envtest runs no garbage collector, so the owner needn't exist.
func madeUpMattermost() metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: "installation.mattermost.com/v1beta1", Kind: "Mattermost", Name: "chat",
		UID: "6f1d3c2e-8b7a-4c55-9e0f-2a4b6c8d0e1f", Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true)}
}

// create makes the case's namespace, its Deployment, owned by the made-up
// Mattermost when owned, and its ManagedWorkload, and an operator whose
// clock reads 10:00.
func (e *undoneEnv) create(t *testing.T, ns string, owned bool) *undoneCase {
	t.Helper()
	start := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	tc := &undoneCase{undoneEnv: e, now: monotonic(start)}
	require.NoError(t, e.admin.Create(e.ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	api := envtestDeployment(ns, 1)
	if owned {
		api.OwnerReferences = []metav1.OwnerReference{madeUpMattermost()}
	}
	require.NoError(t, e.admin.Create(e.ctx, api, client.FieldOwner(standIn)))

	w := envtestWorkload(ns)
	w.Spec.IdlePolicy = &v1alpha1.IdlePolicySpec{IdleAfter: &metav1.Duration{Duration: 5 * time.Minute}}
	require.NoError(t, createTakenOn(e.ctx, e.admin, w))
	evaluated := metav1.NewTime(start.Add(-30 * time.Second))
	w.Status.Activity = &v1alpha1.ActivityStatus{LastActivityTime: metav1.NewTime(start.Add(-5*time.Minute - 30*time.Second)),
		LastActivitySource: v1alpha1.ActivitySourceCPU, LastEvaluatedTime: &evaluated, TemplateHash: podTemplateHash(api)}
	require.NoError(t, e.admin.Status().Update(e.ctx, w))
	tc.key = client.ObjectKeyFromObject(w)

	c := client.WithFieldOwner(indexedClient{e.admin}, v1alpha1.FieldManager)
	tc.r = withTheOperatorsPauser(&Reconciler{
		Client: c, Scheme: testScheme(t), Recorder: e.sent.recorder, PodReader: indexedClient{e.admin},
		metrics: &stubMetrics{cpuMillis: 10, cpuPerReplica: 1000, replicas: 1},
		engines: newEngineRegistry(func() forecaster { return &stubForecaster{} }),
		clock:   func() time.Time { return tc.now },
	}, c)
	remember(tc.r, w)
	return tc
}

// reconcile has the operator reconcile the workload at its clock, after
// the Deployment controller's part: the pods asked for are Ready, or gone,
// at once.
func (tc *undoneCase) reconcile(t *testing.T) *v1alpha1.ManagedWorkload {
	t.Helper()
	(&faultEnv{ctx: tc.ctx, admin: tc.admin}).settle(t, tc.key)
	_, err := tc.r.Reconcile(tc.ctx, ctrl.Request{NamespacedName: tc.key})
	require.NoError(t, err)
	var w v1alpha1.ManagedWorkload
	require.NoError(t, tc.admin.Get(tc.ctx, tc.key, &w))
	return &w
}

// pause reconciles once a second until the workload is paused, its pods
// gone, failing after a minute.
func (tc *undoneCase) pause(t *testing.T) *v1alpha1.ManagedWorkload {
	t.Helper()
	for range 60 {
		if w := tc.reconcile(t); w.Status.Phase == v1alpha1.PhasePaused {
			return w
		}
		tc.now = tc.now.Add(time.Second)
	}
	t.Fatalf("not paused by %s", tc.now)
	return nil
}

// afterThePause moves the operator's clock to after past the pause's scale
// to zero, as the workload's status records it.
func (tc *undoneCase) afterThePause(t *testing.T, after time.Duration) {
	t.Helper()
	var w v1alpha1.ManagedWorkload
	require.NoError(t, tc.admin.Get(tc.ctx, tc.key, &w))
	require.NotNil(t, w.Status.Pause)
	require.NotNil(t, w.Status.Pause.PausedAt)
	tc.now = monotonic(w.Status.Pause.PausedAt.Add(after))
}

// setBack has the stand-in operator write its Deployment back with its
// replicas, after the pause's scale to zero, as an operator that owns a
// workload does once the scale starts its reconcile.
func (tc *undoneCase) setBack(t *testing.T, after time.Duration) {
	t.Helper()
	tc.afterThePause(t, after)
	d := (&faultEnv{ctx: tc.ctx, admin: tc.admin}).deployment(t, tc.key)
	d.Spec.Replicas = ptr.To(int32(1))
	require.NoError(t, tc.admin.Update(tc.ctx, d, client.FieldOwner(standIn)))
}

// scaleBack sets the replicas through the scale subresource, as kubectl
// scale, an HPA and KEDA do.
func (tc *undoneCase) scaleBack(t *testing.T, after time.Duration) {
	t.Helper()
	tc.afterThePause(t, after)
	d := (&faultEnv{ctx: tc.ctx, admin: tc.admin}).deployment(t, tc.key)
	scale := &autoscalingv1.Scale{}
	require.NoError(t, tc.admin.SubResource("scale").Get(tc.ctx, d, scale))
	scale.Spec.Replicas = 1
	require.NoError(t, tc.admin.SubResource("scale").Update(tc.ctx, d, client.WithSubResourceBody(scale),
		client.FieldOwner(standIn)))
}

// awake reconciles the workload as the operator does once its target's
// replicas are back, and requires it running.
func (tc *undoneCase) awake(t *testing.T) *v1alpha1.ManagedWorkload {
	t.Helper()
	w := tc.reconcile(t)
	require.Equal(t, v1alpha1.PhaseRunning, w.Status.Phase, "woken by the scale-up")
	require.NotNil(t, w.Status.LastScaledUp)
	return w
}

// watchUntil has the operator check the workload once a minute up to and
// including until, failing if it pauses.
func (tc *undoneCase) watchUntil(t *testing.T, until time.Time) {
	t.Helper()
	for next := tc.now.Add(time.Minute); !next.After(until); next = next.Add(time.Minute) {
		tc.now = next
		if w := tc.reconcile(t); w.Status.Phase == v1alpha1.PhasePausing || w.Status.Phase == v1alpha1.PhasePaused {
			t.Fatalf("pausing at %s, before %s", tc.now.UTC().Format("15:04:05"), until.UTC().Format("15:04:05"))
		}
	}
	tc.now = until
}

// pausesAt checks the workload is still awake just before at, then pauses
// at it, on its next check.
func (tc *undoneCase) pausesAt(t *testing.T, at time.Time) {
	t.Helper()
	tc.watchUntil(t, at.Add(-time.Second))
	tc.now = at.Add(requeueLag)
	if w := tc.reconcile(t); w.Status.Phase != v1alpha1.PhasePausing && w.Status.Phase != v1alpha1.PhasePaused {
		t.Fatalf("not pausing at %s: %s, conditions %v", at.UTC().Format("15:04:05"), w.Status.Phase,
			conditionTypes(w))
	}
}

func conditionTypes(w *v1alpha1.ManagedWorkload) []string {
	var out []string
	for _, c := range w.Status.Conditions {
		if c.Status == metav1.ConditionTrue {
			out = append(out, c.Type)
		}
	}
	return out
}

// warned waits for the API server to keep an event of eventType and reason
// on the ManagedWorkload whose note is want, as kubectl describe would show
// it, and fails with what it keeps instead.
func (tc *undoneCase) warned(t *testing.T, eventType, reason, want string) {
	t.Helper()
	notes := func() []string {
		var out []string
		for _, e := range tc.sent.list(t, tc.key.Namespace) {
			if e.Regarding.Kind == "ManagedWorkload" && e.Reason == reason {
				out = append(out, e.Type+" "+e.Note)
			}
		}
		return out
	}
	want = eventType + " api: " + want
	if !assert.Eventually(t, func() bool { return slices.Contains(notes(), want) }, 10*time.Second, 50*time.Millisecond) {
		t.Errorf("kubectl describe would show its %s events as %q, none %q", reason, notes(), want)
	}
}

// undoneMessage is the message of PauseUndone while it's True, or "".
func undoneMessage(w *v1alpha1.ManagedWorkload) string {
	if c := meta.FindStatusCondition(w.Status.Conditions, "PauseUndone"); c != nil && c.Status == metav1.ConditionTrue {
		return c.Message
	}
	return ""
}

// P95: an operator that owns its Deployment and sets its replicas, as
// mattermost-operator does, writes them back within seconds of each pause.
// That was taken for a wake from outside, as kubectl scale is, and the
// workload paused again after each idle hour, restarting its pods, with
// only a Normal ScaledUp event. A pause undone at once is a conflict,
// whoever undoes it: PauseUndone and a warning name who did it, how soon,
// and the owner, and Napwake holds off an hour after the scale-up, then 2,
// doubling with each pause in a row undone, before pausing again. A pause
// that holds an hour ends it.
func TestPauseUndone_ByTheOwnerIsHeldOffDoubling(t *testing.T) {
	env := startUndoneEnv(t)
	tc := env.create(t, "owned", true)
	requirePauserOnItsClock(t, tc.r, tc.key.Namespace)

	paused := tc.pause(t)
	require.NotNil(t, paused.Status.Pause.PausedAt)
	tc.setBack(t, 3*time.Second)
	w := tc.awake(t)
	scaledUp := w.Status.LastScaledUp.At.Time
	assert.Equal(t, standIn, w.Status.LastScaledUp.By)
	assert.Equal(t, int32(1), w.Status.LastScaledUp.Undone)
	first := standIn + " set the replicas back 3 seconds after the pause, undoing it. Mattermost chat controls the " +
		"workload. Napwake pauses it again from 11:00 UTC. To stop it, opt the workload out, or have its replicas " +
		"left to Napwake: " + scaledUpGuide
	assert.Equal(t, first, undoneMessage(w), "PauseUndone, with conditions %v", conditionTypes(w))
	tc.warned(t, "Warning", "PauseUndone", first)
	tc.warned(t, "Normal", ReasonScaledUp, "scaled up to 1 replicas by "+standIn+" outside Napwake; counted as activity")

	t.Run("held off an hour after the scale-up", func(t *testing.T) {
		tc.pausesAt(t, scaledUp.Add(time.Hour))
	})

	t.Run("a second pause in a row undone holds it off 2 hours", func(t *testing.T) {
		tc.pause(t)
		tc.setBack(t, 3*time.Second)
		w := tc.awake(t)
		scaledUp = w.Status.LastScaledUp.At.Time
		assert.Equal(t, int32(2), w.Status.LastScaledUp.Undone)
		second := standIn + " set the replicas back 3 seconds after the pause, undoing it. Mattermost chat controls " +
			"the workload. Napwake pauses it again from 13:00 UTC. To stop it, opt the workload out, or have its " +
			"replicas left to Napwake: " + scaledUpGuide
		assert.Equal(t, second, undoneMessage(w))
		tc.warned(t, "Warning", "PauseUndone", second)
		tc.pausesAt(t, scaledUp.Add(2*time.Hour))
	})

	// An hour, whatever the count: a workload in daily use is woken before a
	// pause holds the longer waits, which would leave the report standing.
	t.Run("a pause that holds an hour clears both", func(t *testing.T) {
		w := tc.pause(t)
		pausedAt := w.Status.Pause.PausedAt.Time
		tc.now = pausedAt.Add(time.Hour - time.Second)
		w = tc.reconcile(t)
		require.Equal(t, v1alpha1.PhasePaused, w.Status.Phase)
		require.True(t, meta.IsStatusConditionTrue(w.Status.Conditions, "PauseUndone"), "not before an hour")
		require.Equal(t, int32(2), w.Status.LastScaledUp.Undone)

		tc.now = pausedAt.Add(time.Hour + requeueLag)
		w = tc.reconcile(t)
		require.Equal(t, v1alpha1.PhasePaused, w.Status.Phase)
		assert.Nil(t, meta.FindStatusCondition(w.Status.Conditions, "PauseUndone"))
		assert.Zero(t, w.Status.LastScaledUp.Undone)
		tc.warned(t, "Normal", "PauseUndoneResolved", "a pause held for an hour, so its replicas are left to Napwake now")
	})
}

// What isn't a pause undone at once: a scale through the scale subresource,
// as kubectl scale, an HPA and KEDA make, which records no writer, and a
// writer that comes more than 5 minutes after the pause, on a target with
// no controller owner. Each wakes the workload, as before, and the next
// pause comes when its idle clock runs out, 5 minutes later. A recorded
// writer within 5 minutes counts, owner or not, and one at any time on a
// target with a controller owner.
func TestPauseUndone_WhatCounts(t *testing.T) {
	env := startUndoneEnv(t)
	for i, tt := range []struct {
		name   string
		owned  bool
		after  time.Duration
		scale  bool
		counts bool
	}{
		{name: "the owner's write, at once", owned: true, after: 3 * time.Second, counts: true},
		{name: "the owner's write, an hour later", owned: true, after: time.Hour, counts: true},
		{name: "a write within 5 minutes, no owner", after: 4*time.Minute + 59*time.Second, counts: true},
		{name: "a write after 5 minutes, no owner", after: 5*time.Minute + time.Second},
		{name: "through the scale subresource, owned", owned: true, after: 3 * time.Second, scale: true},
		{name: "through the scale subresource, no owner", after: 3 * time.Second, scale: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tc := env.create(t, fmt.Sprintf("counts-%d", i), tt.owned)
			tc.pause(t)
			if tt.scale {
				tc.scaleBack(t, tt.after)
			} else {
				tc.setBack(t, tt.after)
			}
			w := tc.awake(t)
			scaledUp := w.Status.LastScaledUp.At.Time

			undone := meta.FindStatusCondition(w.Status.Conditions, "PauseUndone")
			if tt.counts {
				require.NotNil(t, undone, "PauseUndone, with conditions %v", conditionTypes(w))
				assert.Equal(t, int32(1), w.Status.LastScaledUp.Undone)
				tc.pausesAt(t, scaledUp.Add(time.Hour))
				return
			}
			assert.Nil(t, undone)
			assert.Zero(t, w.Status.LastScaledUp.Undone)
			assert.False(t, slices.ContainsFunc(tc.sent.list(t, tc.key.Namespace), func(e eventsv1.Event) bool {
				return e.Reason == "PauseUndone"
			}), "no warning")
			tc.pausesAt(t, scaledUp.Add(5*time.Minute))
		})
	}
}

// ControllerOwned says, before any pause, that the target has a controller
// owner, which undoes each pause if it sets the replicas, and goes once the
// owner reference does.
func TestControllerOwned_SetAndDropped(t *testing.T) {
	env := startUndoneEnv(t)
	tc := env.create(t, "owner", true)

	w := tc.reconcile(t)
	c := meta.FindStatusCondition(w.Status.Conditions, "ControllerOwned")
	require.NotNil(t, c, "ControllerOwned, with conditions %v", conditionTypes(w))
	assert.Equal(t, metav1.ConditionTrue, c.Status)
	assert.Equal(t, "Mattermost chat controls it, so if that sets its replicas, it will undo each pause", c.Message)

	d := (&faultEnv{ctx: tc.ctx, admin: tc.admin}).deployment(t, tc.key)
	d.OwnerReferences = nil
	require.NoError(t, tc.admin.Update(tc.ctx, d))
	tc.now = tc.now.Add(time.Minute)
	w = tc.reconcile(t)
	assert.Nil(t, meta.FindStatusCondition(w.Status.Conditions, "ControllerOwned"), "dropped with the owner reference")

	none := env.create(t, "no-owner", false)
	w = none.reconcile(t)
	assert.Nil(t, meta.FindStatusCondition(w.Status.Conditions, "ControllerOwned"),
		"conditions %s", strings.Join(conditionTypes(w), ", "))
}
