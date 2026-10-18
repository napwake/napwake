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
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
	"github.com/okedeji/hybernate/internal/autoscaler"
	"github.com/okedeji/hybernate/internal/lifecycle"
)

var errInjected = errors.New("injected failure")

var scaledObjectGVK = schema.GroupVersionKind{Group: "keda.sh", Version: "v1alpha1", Kind: "ScaledObject"}

// faultClient fails the n-th write it sees: before it reaches the API
// server, as a crash before the write would, or, with after, once it has
// landed, as a write whose response was lost would.
type faultClient struct {
	n, seen int
	after   bool
}

func (f *faultClient) hit(write func() error) error {
	f.seen++
	if f.seen != f.n {
		return write()
	}
	if f.after {
		_ = write() // the write lands; only its response is lost
	}
	return errInjected
}

func (f *faultClient) wrap(c client.WithWatch) client.WithWatch {
	return interceptor.NewClient(c, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			return f.hit(func() error { return c.Create(ctx, obj, opts...) })
		},
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			return f.hit(func() error { return c.Update(ctx, obj, opts...) })
		},
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch,
			opts ...client.PatchOption) error {
			return f.hit(func() error { return c.Patch(ctx, obj, p, opts...) })
		},
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			return f.hit(func() error { return c.Delete(ctx, obj, opts...) })
		},
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object,
			opts ...client.SubResourceUpdateOption) error {
			return f.hit(func() error { return c.SubResource(sub).Update(ctx, obj, opts...) })
		},
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, p client.Patch,
			opts ...client.SubResourcePatchOption) error {
			return f.hit(func() error { return c.SubResource(sub).Patch(ctx, obj, p, opts...) })
		},
	})
}

// faultEnv is an API server with KEDA's CRD, in which each test case gets a
// namespace with a KEDA-scaled Deployment at 3 replicas, whose ScaledObject
// has a paused-replicas of 7 of its own, and a ManagedWorkload asking for it
// to be paused.
type faultEnv struct {
	ctx   context.Context
	admin client.WithWatch
}

func startFaultEnv(t *testing.T) *faultEnv {
	t.Helper()
	cfg := startEnvtest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	admin, err := client.NewWithWatch(cfg, client.Options{Scheme: testScheme(t)})
	require.NoError(t, err)
	require.NoError(t, admin.Create(ctx, kedaCRD()))
	require.Eventually(t, func() bool {
		var l unstructured.UnstructuredList
		l.SetGroupVersionKind(scaledObjectGVK.GroupVersion().WithKind("ScaledObjectList"))
		return admin.List(ctx, &l) == nil
	}, 30*time.Second, 100*time.Millisecond, "KEDA's CRD is served")
	return &faultEnv{ctx: ctx, admin: admin}
}

func (e *faultEnv) create(t *testing.T, ns string) client.ObjectKey {
	t.Helper()
	require.NoError(t, e.admin.Create(e.ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	so := envtestScaledObject(ns, "api")
	so.SetAnnotations(map[string]string{autoscaler.PausedReplicasAnnotation: "7"})
	require.NoError(t, e.admin.Create(e.ctx, so))
	require.NoError(t, e.admin.Create(e.ctx, envtestDeployment(ns, 3)))
	require.NoError(t, e.admin.Create(e.ctx, withPauseRequested(envtestWorkload(ns))))
	return client.ObjectKey{Namespace: ns, Name: "api"}
}

func (e *faultEnv) reconciler(t *testing.T, c client.Client) *Reconciler {
	t.Helper()
	c = client.WithFieldOwner(indexedClient{c}, v1alpha1.FieldManager)
	finder := autoscaler.NewFinder(c)
	return &Reconciler{
		Client: c, Scheme: testScheme(t), Recorder: events.NewFakeRecorder(1000),
		pauser: lifecycle.NewPauser(c, finder), autoscalers: finder,
		metrics: &stubMetrics{cpuPerReplica: 100, memoryPerReplica: 1 << 30, replicas: 3},
		engines: newEngineRegistry(func() forecaster { return &stubForecaster{} }),
		clock:   time.Now,
	}
}

// settle stands in for the Deployment controller: the pods asked for are
// Ready at once.
func (e *faultEnv) settle(t *testing.T, key client.ObjectKey) {
	t.Helper()
	d := e.deployment(t, key)
	if d.Status.ObservedGeneration == d.Generation && d.Status.ReadyReplicas == *d.Spec.Replicas {
		return
	}
	d.Status.ObservedGeneration = d.Generation
	d.Status.Replicas, d.Status.ReadyReplicas = *d.Spec.Replicas, *d.Spec.Replicas
	require.NoError(t, e.admin.Status().Update(e.ctx, d))
}

func (e *faultEnv) deployment(t *testing.T, key client.ObjectKey) *appsv1.Deployment {
	t.Helper()
	var d appsv1.Deployment
	require.NoError(t, e.admin.Get(e.ctx, key, &d))
	return &d
}

// phase is the workload's phase, or "gone" once it's deleted.
func (e *faultEnv) phase(key client.ObjectKey) v1alpha1.WorkloadPhase {
	var w v1alpha1.ManagedWorkload
	if err := e.admin.Get(e.ctx, key, &w); err != nil {
		return "gone"
	}
	return w.Status.Phase
}

func (e *faultEnv) workload(t *testing.T, key client.ObjectKey) *v1alpha1.ManagedWorkload {
	t.Helper()
	var w v1alpha1.ManagedWorkload
	require.NoError(t, e.admin.Get(e.ctx, key, &w))
	return &w
}

func (e *faultEnv) pausedReplicasAnnotation(t *testing.T, key client.ObjectKey) (string, bool) {
	t.Helper()
	so := &unstructured.Unstructured{}
	so.SetGroupVersionKind(scaledObjectGVK)
	require.NoError(t, e.admin.Get(e.ctx, key, so))
	v, ok := so.GetAnnotations()[autoscaler.PausedReplicasAnnotation]
	return v, ok
}

// reconcileUntil reconciles, letting the pods settle before each, until
// the workload reaches want, failing after tries.
func (e *faultEnv) reconcileUntil(t *testing.T, r *Reconciler, key client.ObjectKey, want v1alpha1.WorkloadPhase,
	tries int) {
	t.Helper()
	for range tries {
		e.settle(t, key)
		_, _ = r.Reconcile(e.ctx, ctrl.Request{NamespacedName: key}) // failures are injected; retrying is the point
		if e.phase(key) == want {
			return
		}
	}
	t.Fatalf("never reached %s: phase %s", want, e.phase(key))
}

// Every write of a pause and of a wake is failed in turn, before and after
// it lands. Whichever fails, retrying converges: the pause records the 3
// replicas and holds KEDA at zero, and the wake restores the 3 and puts
// back the ScaledObject's own paused-replicas.
func TestPauseAndWake_ConvergeAfterAnyFailedWrite(t *testing.T) {
	env := startFaultEnv(t)

	run := func(t *testing.T, ns string, faults *faultClient) {
		key := env.create(t, ns)
		r := env.reconciler(t, faults.wrap(env.admin))

		env.reconcileUntil(t, r, key, v1alpha1.PhasePaused, 30)
		paused := env.workload(t, key)
		require.NotNil(t, paused.Status.Pause)
		require.Equal(t, int32(3), paused.Status.Pause.PreviousReplicas, "replicas recorded")
		require.Equal(t, int32(0), *env.deployment(t, key).Spec.Replicas)
		v, ok := env.pausedReplicasAnnotation(t, key)
		require.True(t, ok)
		require.Equal(t, "0", v, "KEDA held at zero")

		paused.Annotations[v1alpha1.AnnotationLastActivity] = time.Now().UTC().Add(time.Second).Format(time.RFC3339)
		require.NoError(t, env.admin.Update(env.ctx, paused))
		env.reconcileUntil(t, r, key, v1alpha1.PhaseRunning, 30)
		require.Equal(t, int32(3), *env.deployment(t, key).Spec.Replicas, "replicas restored")
		v, ok = env.pausedReplicasAnnotation(t, key)
		require.True(t, ok)
		require.Equal(t, "7", v, "the ScaledObject's own paused-replicas is put back")
		require.Nil(t, env.workload(t, key).Status.Pause)
	}

	baseline := &faultClient{n: -1}
	run(t, "baseline", baseline)
	writes := baseline.seen
	require.Positive(t, writes)
	for n := 1; n <= writes; n++ {
		for _, after := range []bool{false, true} {
			t.Run(fmt.Sprintf("write %d failed, landed %t", n, after), func(t *testing.T) {
				run(t, fmt.Sprintf("w%d-%t", n, after), &faultClient{n: n, after: after})
			})
		}
	}
}

// After a clean pause, each way Hybernate hands a workload back (its
// ManagedWorkload deleted, the ignore label, its namespace protected, and a
// scale-up outside Hybernate) has each of its writes failed in turn, before
// and after it lands. Each converges on the target running, KEDA's own
// paused-replicas put back, and no pause record left. Hybernate's own
// scale-up, retried, is never taken for one from outside.
func TestHandBack_ConvergesAfterAnyFailedWrite(t *testing.T) {
	env := startFaultEnv(t)

	type handBack struct {
		name     string
		do       func(t *testing.T, r *Reconciler, key client.ObjectKey)
		want     v1alpha1.WorkloadPhase
		replicas int32
		outside  bool
	}
	handBacks := []handBack{
		{name: "delete", want: "gone", replicas: 3, do: func(t *testing.T, _ *Reconciler, key client.ObjectKey) {
			require.NoError(t, env.admin.Delete(env.ctx, env.workload(t, key)))
		}},
		{name: "ignore", want: v1alpha1.PhaseRunning, replicas: 3,
			do: func(t *testing.T, _ *Reconciler, key client.ObjectKey) {
				d := env.deployment(t, key)
				d.Labels = map[string]string{v1alpha1.LabelIgnore: v1alpha1.True}
				require.NoError(t, env.admin.Update(env.ctx, d))
			}},
		{name: "protect", want: v1alpha1.PhaseRunning, replicas: 3,
			do: func(_ *testing.T, r *Reconciler, key client.ObjectKey) {
				r.ProtectedNamespaces = []string{key.Namespace}
			}},
		{name: "scaleup", want: v1alpha1.PhaseRunning, replicas: 5, outside: true,
			do: func(t *testing.T, _ *Reconciler, key client.ObjectKey) {
				d := env.deployment(t, key)
				d.Spec.Replicas = ptr.To(int32(5))
				require.NoError(t, env.admin.Update(env.ctx, d))
			}},
	}

	run := func(t *testing.T, ns string, hb handBack, faults *faultClient) {
		key := env.create(t, ns)
		r := env.reconciler(t, faults.wrap(env.admin))
		armed := faults.n
		faults.n = -1
		env.reconcileUntil(t, r, key, v1alpha1.PhasePaused, 10)
		faults.n, faults.seen = armed, 0

		hb.do(t, r, key)
		env.reconcileUntil(t, r, key, hb.want, 30)

		require.Equal(t, hb.replicas, *env.deployment(t, key).Spec.Replicas)
		v, _ := env.pausedReplicasAnnotation(t, key)
		require.Equal(t, "7", v, "the ScaledObject's own paused-replicas is put back")
		if hb.want != "gone" {
			w := env.workload(t, key)
			require.Nil(t, w.Status.Pause)
			require.Equal(t, hb.outside, w.Status.LastScaledUp != nil, "scaled up outside Hybernate")
		}
	}

	for _, hb := range handBacks {
		baseline := &faultClient{n: -1}
		run(t, hb.name+"-baseline", hb, baseline)
		writes := baseline.seen
		require.Positive(t, writes)
		for n := 1; n <= writes; n++ {
			for _, after := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s: write %d failed, landed %t", hb.name, n, after), func(t *testing.T) {
					run(t, fmt.Sprintf("%s-w%d-%t", hb.name, n, after), hb, &faultClient{n: n, after: after})
				})
			}
		}
	}
}
