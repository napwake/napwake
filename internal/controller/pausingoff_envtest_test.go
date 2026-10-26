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
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/autoscaler"
	"github.com/napwake/napwake/internal/lifecycle"
)

// switchEnv is an API server in which each case gets a namespace with a
// Deployment at 3 replicas, idle by the metrics, and a ManagedWorkload for
// it, taken on. The reconcilers it makes report every write of a
// ManagedWorkload that carries spec.dryRun: none of the workloads sets it,
// so one that does was set by the operator.
type switchEnv struct {
	ctx   context.Context
	cfg   *rest.Config
	admin client.WithWatch

	mu           sync.Mutex
	writes       int
	dryRunWrites []string
}

func startSwitchEnv(t *testing.T) *switchEnv {
	t.Helper()
	cfg := startEnvtest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	admin, err := client.NewWithWatch(cfg, client.Options{Scheme: testScheme(t)})
	require.NoError(t, err)
	return &switchEnv{ctx: ctx, cfg: cfg, admin: admin}
}

// create makes the case's namespace, Deployment and ManagedWorkload, with
// an idleAfter of 5 minutes.
func (e *switchEnv) create(t *testing.T, ns string, setup func(*v1alpha1.ManagedWorkload)) client.ObjectKey {
	t.Helper()
	require.NoError(t, e.admin.Create(e.ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	require.NoError(t, e.admin.Create(e.ctx, envtestDeployment(ns, 3)))
	w := envtestWorkload(ns)
	w.Spec.IdlePolicy = &v1alpha1.IdlePolicySpec{IdleAfter: &metav1.Duration{Duration: 5 * time.Minute}}
	if setup != nil {
		setup(w)
	}
	require.NoError(t, createTakenOn(e.ctx, e.admin, w))
	return client.ObjectKeyFromObject(w)
}

// reconciler is an operator started with pausing off, or on, reading the
// time from clock.
func (e *switchEnv) reconciler(t *testing.T, pausingOff bool, clock func() time.Time) *Reconciler {
	t.Helper()
	c := client.WithFieldOwner(indexedClient{e.watchDryRun(e.admin)}, v1alpha1.FieldManager)
	finder := autoscaler.NewFinder(c)
	return &Reconciler{
		Client: c, Scheme: testScheme(t), Recorder: events.NewFakeRecorder(1000),
		pauser: lifecycle.NewPauser(c, finder), autoscalers: finder,
		metrics:    &stubMetrics{cpuPerReplica: 100, memoryPerReplica: 1 << 30, replicas: 3},
		engines:    newEngineRegistry(func() forecaster { return &stubForecaster{} }),
		clock:      clock,
		PausingOff: pausingOff,
	}
}

// watchDryRun records every write of a ManagedWorkload, and those that
// carry spec.dryRun, as the object in memory or the patch sent.
func (e *switchEnv) watchDryRun(c client.WithWatch) client.WithWatch {
	saw := func(obj client.Object, patch client.Patch) {
		w, ok := obj.(*v1alpha1.ManagedWorkload)
		if !ok {
			return
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		e.writes++
		var data []byte
		if patch != nil {
			data, _ = patch.Data(obj) // a patch that can't be encoded isn't sent either
		}
		if w.Spec.DryRun || bytes.Contains(data, []byte(`"dryRun"`)) {
			e.dryRunWrites = append(e.dryRunWrites, fmt.Sprintf("%s/%s: %s", w.Namespace, w.Name, data))
		}
	}
	return interceptor.NewClient(c, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			saw(obj, nil)
			return c.Update(ctx, obj, opts...)
		},
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch,
			opts ...client.PatchOption) error {
			saw(obj, p)
			return c.Patch(ctx, obj, p, opts...)
		},
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object,
			opts ...client.SubResourceUpdateOption) error {
			saw(obj, nil)
			return c.SubResource(sub).Update(ctx, obj, opts...)
		},
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, p client.Patch,
			opts ...client.SubResourcePatchOption) error {
			saw(obj, p)
			return c.SubResource(sub).Patch(ctx, obj, p, opts...)
		},
	})
}

// written is how many writes of ManagedWorkloads the env's reconcilers have
// made, and those of them that carried spec.dryRun.
func (e *switchEnv) written() (int, []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.writes, slices.Clone(e.dryRunWrites)
}

// watchless is a client that doesn't watch, such as a manager's, for
// watchDryRun to wrap.
type watchless struct{ client.Client }

func (watchless) Watch(context.Context, client.ObjectList, ...client.ListOption) (watch.Interface, error) {
	return nil, errors.New("the manager's client doesn't watch")
}

// createWatched creates w as a workload Napwake has taken on and checked:
// Running, with activity five minutes ago, so a pause request finds its idle
// clock running, as one does once Napwake has checked the workload.
func createWatched(ctx context.Context, c client.Client, w *v1alpha1.ManagedWorkload) error {
	if err := createTakenOn(ctx, c, w); err != nil {
		return err
	}
	now := time.Now()
	w.Status.Activity = &v1alpha1.ActivityStatus{
		LastActivityTime:   metav1.NewTime(now.Add(-5 * time.Minute)),
		LastActivitySource: v1alpha1.ActivitySourceCPU,
		PauseAt:            ptr.To(metav1.NewTime(now.Add(55 * time.Minute))),
	}
	return c.Status().Update(ctx, w)
}

func (e *switchEnv) workload(t *testing.T, key client.ObjectKey) *v1alpha1.ManagedWorkload {
	t.Helper()
	var w v1alpha1.ManagedWorkload
	require.NoError(t, e.admin.Get(e.ctx, key, &w))
	return &w
}

func (e *switchEnv) replicas(t *testing.T, key client.ObjectKey) int32 {
	t.Helper()
	return *(&faultEnv{ctx: e.ctx, admin: e.admin}).deployment(t, key).Spec.Replicas
}

// reconcile has r reconcile the workload once, after the Deployment
// controller's part: the pods asked for are Ready at once.
func (e *switchEnv) reconcile(t *testing.T, r *Reconciler, key client.ObjectKey) {
	t.Helper()
	(&faultEnv{ctx: e.ctx, admin: e.admin}).settle(t, key)
	_, err := r.Reconcile(e.ctx, ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
}

// watch has r check the workload once a minute, as the operator does, for
// the minutes given, stopping early once it's in phase until.
func (e *switchEnv) watch(t *testing.T, r *Reconciler, key client.ObjectKey, clock *time.Time, minutes int,
	until v1alpha1.WorkloadPhase) {
	t.Helper()
	for range minutes {
		e.reconcile(t, r, key)
		if e.workload(t, key).Status.Phase == until {
			return
		}
		*clock = clock.Add(time.Minute)
	}
}

func pausingOffCondition(w *v1alpha1.ManagedWorkload) *metav1.Condition {
	return meta.FindStatusCondition(w.Status.Conditions, conditionPausingOff)
}

// With pausing off for the install, nothing it manages pauses, while a workload
// is measured as in dry-run, every one paused wakes, and the dependency
// graph lets go of dependencies as dry-run does. Turned back on, pausing
// picks up again. spec.dryRun, which the operator writes back with every
// update, is never set.
func TestPausingOff(t *testing.T) {
	env := startSwitchEnv(t)

	t.Run("an idle workload isn't paused, and its would-be pause is counted", func(t *testing.T) {
		key := env.create(t, "idle", nil)
		clock := time.Now()
		r := env.reconciler(t, true, func() time.Time { return clock })

		env.watch(t, r, key, &clock, 10, v1alpha1.PhaseIdle)

		w := env.workload(t, key)
		assert.Equal(t, v1alpha1.PhaseIdle, w.Status.Phase, "idle, as a would-be pause")
		assert.Equal(t, int32(3), env.replicas(t, key), "and not scaled down")
		require.NotNil(t, w.Status.DryRun)
		assert.Equal(t, int32(1), w.Status.DryRun.Pauses, "the would-be pause is counted")
		assert.Nil(t, w.Status.Pause)
		c := pausingOffCondition(w)
		require.NotNil(t, c, "the workload says pausing is off")
		assert.Equal(t, metav1.ConditionTrue, c.Status)
		assert.Equal(t, reasonDisabledForTheInstall, c.Reason)
		assert.Contains(t, c.Message, "pausing is off for this install")
		idle := drainEvents(t, r)
		assert.Contains(t, idle, "Normal "+ReasonIdleDetected+" [dry-run] api: idle for 5 minutes")
		assert.Contains(t, idle, "would have paused")
	})

	t.Run("a paused workload is woken, saying pausing is off", func(t *testing.T) {
		key := env.create(t, "paused", func(w *v1alpha1.ManagedWorkload) { withPauseRequested(w) })
		on := env.reconciler(t, false, time.Now)
		(&faultEnv{ctx: env.ctx, admin: env.admin}).reconcileUntil(t, on, key, v1alpha1.PhasePaused, 30)
		require.Zero(t, env.replicas(t, key))

		off := env.reconciler(t, true, time.Now)
		(&faultEnv{ctx: env.ctx, admin: env.admin}).reconcileUntil(t, off, key, v1alpha1.PhaseRunning, 30)

		assert.Equal(t, int32(3), env.replicas(t, key), "woken to the replicas it had")
		woken := drainEvents(t, off)
		assert.Contains(t, woken, "Normal "+reasonPausingOffWake+" api: pausing is off for this install, "+
			"so Napwake wakes the workload it had paused")
		assert.NotContains(t, woken, reasonDryRunWake)
		require.NotNil(t, pausingOffCondition(env.workload(t, key)))
	})

	t.Run("turned back on, pausing picks up again", func(t *testing.T) {
		key := env.create(t, "back-on", nil)
		clock := time.Now()
		off := env.reconciler(t, true, func() time.Time { return clock })
		env.watch(t, off, key, &clock, 10, v1alpha1.PhaseIdle)
		require.Equal(t, v1alpha1.PhaseIdle, env.workload(t, key).Status.Phase)

		// The switch is a flag, so turning it back on restarts the operator,
		// which forgets the clocks it kept.
		on := env.reconciler(t, false, func() time.Time { return clock })
		env.watch(t, on, key, &clock, 15, v1alpha1.PhasePaused)

		w := env.workload(t, key)
		assert.Equal(t, v1alpha1.PhasePaused, w.Status.Phase)
		assert.Zero(t, env.replicas(t, key))
		assert.Nil(t, pausingOffCondition(w), "it no longer says pausing is off")
		assert.Nil(t, w.Status.DryRun, "nor that it's measured")
	})

	t.Run("a wrong-pause back-off carries through pausing off and back on", func(t *testing.T) {
		key := env.create(t, "backed-off", nil)
		w := env.workload(t, key)
		w.Status.IdleWait = &v1alpha1.IdleWaitStatus{WrongPauses: 2}
		w.Status.Conditions = []metav1.Condition{{Type: conditionPausedWhileInUse, Status: metav1.ConditionTrue,
			Reason: reasonWokenSoonAfterPause, Message: "a request came 1 minute after it paused",
			LastTransitionTime: metav1.Now()}}
		require.NoError(t, env.admin.Status().Update(env.ctx, w))
		clock := time.Now()
		off := env.reconciler(t, true, func() time.Time { return clock })

		env.watch(t, off, key, &clock, 10, v1alpha1.PhaseIdle)

		measured := env.workload(t, key)
		assert.Equal(t, v1alpha1.PhaseIdle, measured.Status.Phase, "a would-be pause waits idleAfter alone")
		require.NotNil(t, measured.Status.IdleWait)
		assert.Equal(t, int32(2), measured.Status.IdleWait.WrongPauses, "pausing off keeps the back-off")
		assert.True(t, meta.IsStatusConditionTrue(measured.Status.Conditions, conditionPausedWhileInUse))

		on := env.reconciler(t, false, func() time.Time { return clock })
		env.watch(t, on, key, &clock, 15, v1alpha1.PhasePaused)
		require.Equal(t, v1alpha1.PhaseRunning, env.workload(t, key).Status.Phase,
			"back on, it waits the back-off's 20 minutes, not idleAfter's 5")
		env.watch(t, on, key, &clock, 10, v1alpha1.PhasePaused)
		assert.Equal(t, v1alpha1.PhasePaused, env.workload(t, key).Status.Phase)
	})

	t.Run("a pause request is answered as dry-run answers it, naming the switch", func(t *testing.T) {
		key := env.create(t, "requested", func(w *v1alpha1.ManagedWorkload) { withPauseRequested(w) })
		r := env.reconciler(t, true, time.Now)

		env.reconcile(t, r, key)

		w := env.workload(t, key)
		assert.Equal(t, v1alpha1.PhaseIdle, w.Status.Phase)
		assert.Equal(t, int32(3), env.replicas(t, key))
		c := meta.FindStatusCondition(w.Status.Conditions, conditionPauseRequest)
		require.NotNil(t, c)
		assert.Equal(t, metav1.ConditionFalse, c.Status)
		assert.Equal(t, reasonPausingOff, c.Reason)
		assert.Contains(t, c.Message, ": pausing is off for this install, so Napwake counted a would-be pause "+
			"and left the workload running")
	})

	t.Run("a dependency is let go, and woken with its dependent, through the operator's graph", func(t *testing.T) {
		const ns, dependency = "deps", "postgres"
		require.NoError(t, env.admin.Create(env.ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
		for _, name := range []string{"api", dependency} {
			d := envtestDeployment(ns, 1)
			d.Name = name
			require.NoError(t, env.admin.Create(env.ctx, d))
		}
		api := withPauseRequested(envtestWorkload(ns))
		api.Spec.DependsOn = []v1alpha1.DependencyRef{{Kind: v1alpha1.TargetKindDeployment, Name: dependency}}
		require.NoError(t, createWatched(env.ctx, env.admin, api))
		postgres := envtestWorkload(ns)
		postgres.Name, postgres.Spec.Target.Name = dependency, dependency
		require.NoError(t, createWatched(env.ctx, env.admin, postgres))

		opScheme := testScheme(t)
		mgr, err := ctrl.NewManager(env.cfg, ctrl.Options{Scheme: opScheme,
			Metrics:    metricsserver.Options{BindAddress: "0"},
			Controller: config.Controller{SkipNameValidation: ptr.To(true)},
			Cache:      cache.Options{DefaultNamespaces: map[string]cache.Config{ns: {}}}})
		require.NoError(t, err)
		writesBefore, _ := env.written()
		r := &Reconciler{Client: env.watchDryRun(watchless{mgr.GetClient()}), Scheme: opScheme,
			Recorder:  events.NewFakeRecorder(1000),
			PodReader: mgr.GetAPIReader(), PausingOff: true,
			metrics: &stubMetrics{cpuPerReplica: 100, memoryPerReplica: 1 << 30, replicas: 1},
			engines: newEngineRegistry(func() forecaster { return &stubForecaster{} })}
		require.NoError(t, r.SetupWithManager(mgr))
		mgrCtx, stop := context.WithCancel(context.Background())
		var running sync.WaitGroup
		running.Go(func() { assert.NoError(t, mgr.Start(mgrCtx)) })
		t.Cleanup(func() {
			stop()
			running.Wait()
		})

		get := func(name string) *v1alpha1.ManagedWorkload {
			return env.workload(t, client.ObjectKey{Namespace: ns, Name: name})
		}
		pauses := func(name string) int32 {
			if d := get(name).Status.DryRun; d != nil {
				return d.Pauses
			}
			return 0
		}
		// The graph the operator's watch keeps has api measured, as the
		// switch has it, whatever its spec says.
		inGraph := func(uid types.UID, phase v1alpha1.WorkloadPhase) bool {
			r.graph.mu.RLock()
			defer r.graph.mu.RUnlock()
			n := r.graph.nodes[uid]
			return n != nil && n.phase == phase && n.dryRun
		}
		patch := func(name, annotation, value string) {
			p := client.RawPatch(types.MergePatchType,
				fmt.Appendf(nil, `{"metadata":{"annotations":{%q:%q}}}`, annotation, value))
			require.NoError(t, env.admin.Patch(env.ctx, get(name), p))
		}

		require.Eventually(t, func() bool {
			return get("api").Status.Phase == v1alpha1.PhaseIdle && pauses("api") == 1 &&
				inGraph(get("api").UID, v1alpha1.PhaseIdle)
		}, 30*time.Second, 100*time.Millisecond, "api's pause request is a would-be pause, in the graph as measured")

		patch(dependency, v1alpha1.AnnotationPauseRequested, "now")
		if !assert.Eventually(t, func() bool {
			return get(dependency).Status.Phase == v1alpha1.PhaseIdle && pauses(dependency) == 1
		}, 30*time.Second, 100*time.Millisecond, "api's would-be pause lets postgres go, as it would were both in dry-run") {
			t.Fatalf("postgres's pause request: %v",
				meta.FindStatusCondition(get(dependency).Status.Conditions, conditionPauseRequest))
		}
		for _, name := range []string{"api", dependency} {
			assert.Equal(t, int32(1), env.replicas(t, client.ObjectKey{Namespace: ns, Name: name}),
				"%s stays up", name)
		}

		patch("api", v1alpha1.AnnotationLastActivity, time.Now().UTC().Format(time.RFC3339))
		require.Eventually(t, func() bool {
			return get("api").Status.Phase == v1alpha1.PhaseRunning && get(dependency).Status.Phase == v1alpha1.PhaseRunning
		}, 30*time.Second, 100*time.Millisecond, "api's would-be pause ends, and postgres's with it")
		assert.NotEmpty(t, get(dependency).Annotations[v1alpha1.AnnotationLastActivity], "api's wake stamped postgres")
		for _, name := range []string{"api", dependency} {
			assert.False(t, get(name).Spec.DryRun)
			assert.Equal(t, int32(1), env.replicas(t, client.ObjectKey{Namespace: ns, Name: name}),
				"%s never paused", name)
		}
		writes, dryRun := env.written()
		assert.Greater(t, writes, writesBefore, "the manager's reconciler wrote ManagedWorkloads")
		assert.Empty(t, dryRun, "none of its writes set spec.dryRun")
	})

	t.Run("spec.dryRun is never written", func(t *testing.T) {
		writes, dryRun := env.written()
		if writes == 0 {
			// Run alone, this case pauses, wakes, measures and answers a
			// request itself.
			key := env.create(t, "never-written", func(w *v1alpha1.ManagedWorkload) { withPauseRequested(w) })
			clock := time.Now()
			on := env.reconciler(t, false, func() time.Time { return clock })
			env.watch(t, on, key, &clock, 30, v1alpha1.PhasePaused)
			off := env.reconciler(t, true, func() time.Time { return clock })
			env.watch(t, off, key, &clock, 30, v1alpha1.PhaseIdle)
			writes, dryRun = env.written()
		}
		require.Positive(t, writes, "the operator wrote ManagedWorkloads")
		assert.Empty(t, dryRun, "none of its writes set spec.dryRun")

		var all v1alpha1.ManagedWorkloadList
		require.NoError(t, env.admin.List(env.ctx, &all))
		require.NotEmpty(t, all.Items)
		for _, w := range all.Items {
			assert.False(t, w.Spec.DryRun, "%s/%s", w.Namespace, w.Name)
		}
	})
}
