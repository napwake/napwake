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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/forecast"
)

// theDependency is postgres, the dependency in dependentWakeEnv.
const theDependency = "postgres"

// gate holds whatever passes through it while it's shut, so a test can put
// two reconciles in the order it needs.
type gate struct {
	ctx     context.Context
	mu      sync.Mutex
	shut    chan struct{}
	waiting int
	passed  int
}

func (g *gate) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.shut == nil {
		g.shut = make(chan struct{})
	}
}

func (g *gate) open() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.shut != nil {
		close(g.shut)
		g.shut = nil
	}
}

func (g *gate) pass() {
	g.mu.Lock()
	shut := g.shut
	if shut != nil {
		g.waiting++
	}
	g.mu.Unlock()
	if shut != nil {
		select {
		case <-shut:
		case <-g.ctx.Done():
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if shut != nil {
		g.waiting--
	}
	g.passed++
}

// counts are how many wait at the gate, and how many have passed it.
func (g *gate) counts() (waiting, passed int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.waiting, g.passed
}

// gatedCPU reads each workload's CPU through its gate: what an awake
// workload's check does after, its idle check and learning its
// dependencies, waits while the gate is shut.
type gatedCPU struct {
	*stubMetrics
	gates map[string]*gate
}

func (m *gatedCPU) WorkloadCPUMillis(ctx context.Context, w *v1alpha1.ManagedWorkload) (float64, int, error) {
	if g, ok := m.gates[w.Name]; ok {
		g.pass()
	}
	return m.stubMetrics.WorkloadCPUMillis(ctx, w)
}

// gatedForecaster holds a paused workload's reconcile before it can begin a
// wake: it reads the forecast before it decides.
type gatedForecaster struct {
	*stubForecaster
	gate *gate
}

func (f *gatedForecaster) GetPhase() forecast.Phase {
	f.gate.pass()
	return f.stubForecaster.GetPhase()
}

// dependentWakeEnv is the orders namespace of the test cluster on 26 Oct,
// run by the manager: api, a Deployment of 2 whose PGRST_DB_URI names the
// postgres Service, and postgres, a StatefulSet of 1 behind it. api learned
// postgres from its environment long ago, and its idle wait has backed off
// to 65 minutes. postgres's idleAfter is an hour. Both clocks run out 3
// seconds after it starts. A goroutine stands in for the kubelet and the
// workloads' controllers.
type dependentWakeEnv struct {
	ctx context.Context
	c   client.WithWatch
	ns  string
	r   *Reconciler
	mgr ctrl.Manager

	// postgresCPU holds postgres's idle check, apiCPU api's check, and
	// apiWake api's wake.
	postgresCPU, apiCPU, apiWake *gate

	mu sync.Mutex
	// phases are each workload's phase as the API server reports it, and
	// requested whether a request waits for api, as the doorman stamps one.
	phases    map[string]v1alpha1.WorkloadPhase
	requested bool
	// history lists the phase changes, and violations those in which
	// postgres began to pause, or paused, with api awake, or asleep with a
	// request waiting for it.
	history, violations []string
	// postgresReplicas is the fewest replicas postgres was asked for.
	postgresReplicas int32
}

// dependentWake is how a dependentWakeEnv differs from the test cluster:
// how soon api's pods stop once it's scaled down, whether the doorman
// fronts the workloads, whether api has yet to learn its dependency, as
// when it's taken on, and whether api's clock has an hour left rather than
// running out with postgres's.
type dependentWake struct {
	apiStops  time.Duration
	doorman   bool
	unlearned bool
	apiBusy   bool
}

func startDependentWakeEnv(t *testing.T, cfg *rest.Config, ns string, setup dependentWake) *dependentWakeEnv {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	scheme := testScheme(t)
	c, err := client.NewWithWatch(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	e := &dependentWakeEnv{ctx: ctx, c: c, ns: ns, postgresCPU: &gate{ctx: ctx}, apiCPU: &gate{ctx: ctx},
		apiWake: &gate{ctx: ctx}, phases: map[string]v1alpha1.WorkloadPhase{}, postgresReplicas: 1}

	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	doormanNS := ns + "-doorman"
	if setup.doorman {
		require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: doormanNS}}))
		require.NoError(t, c.Create(ctx, &discoveryv1.EndpointSlice{
			ObjectMeta: metav1.ObjectMeta{Name: "napwake-doorman-abc", Namespace: doormanNS,
				Labels: map[string]string{discoveryv1.LabelServiceName: "napwake-doorman"}},
			AddressType: discoveryv1.AddressTypeIPv4,
			Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.7"},
				Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(true)}}},
		}))
	}
	pgLabels := map[string]string{"app": "postgres"}
	require.NoError(t, c.Create(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "postgres", Namespace: ns},
		Spec: corev1.ServiceSpec{Selector: pgLabels, Ports: []corev1.ServicePort{{Name: "postgres", Port: 5432}}}}))
	require.NoError(t, c.Create(ctx, &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "postgres", Namespace: ns},
		Spec: appsv1.StatefulSetSpec{Replicas: ptr.To(int32(1)), ServiceName: "postgres",
			Selector: &metav1.LabelSelector{MatchLabels: pgLabels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: pgLabels},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "postgres", Image: "postgres:17"}}}}}}))
	api := envtestDeployment(ns, 2)
	api.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{
		{Name: "PGRST_DB_URI", Value: "postgres://authenticator@postgres:5432/orders"}}
	require.NoError(t, c.Create(ctx, api))
	e.runWorkloadControllers(t, map[string]time.Duration{"api": setup.apiStops, theDependency: 500 * time.Millisecond})

	// Both were last checked 20 seconds ago, so neither clock restarts as
	// unwatched.
	now := time.Now()
	evaluated := metav1.NewTime(now.Add(-20 * time.Second))
	clock := func(wait time.Duration) v1alpha1.ActivityStatus {
		return v1alpha1.ActivityStatus{LastActivityTime: metav1.NewTime(now.Add(-wait + 3*time.Second)),
			LastActivitySource: v1alpha1.ActivitySourceAnnotation, LastEvaluatedTime: &evaluated}
	}
	apiMW := envtestWorkload(ns)
	apiMW.Spec.IdlePolicy = &v1alpha1.IdlePolicySpec{IdleAfter: &metav1.Duration{Duration: 5 * time.Minute}}
	require.NoError(t, createTakenOn(ctx, c, apiMW))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(api), api))
	apiMW.Status.Activity = ptr.To(clock(65 * time.Minute))
	if setup.apiBusy {
		apiMW.Status.Activity = ptr.To(clock(5 * time.Minute))
	}
	apiMW.Status.IdleWait = &v1alpha1.IdleWaitStatus{WrongPauses: 4}
	if !setup.unlearned {
		apiMW.Status.LearnedDependencies = &v1alpha1.LearnedDependencies{
			From: learnedFrom(podTemplateHash(api), ignoredDependencies(apiMW, api)),
			At:   metav1.NewTime(now.Add(-30 * time.Minute)),
			Dependencies: []v1alpha1.LearnedDependency{{Namespace: ns, Kind: v1alpha1.TargetKindStatefulSet,
				Name: "postgres", Source: v1alpha1.LearnedFromEnvironment, Via: "PGRST_DB_URI",
				Address: "postgres://postgres:5432"}}}
	}
	require.NoError(t, c.Status().Update(ctx, apiMW))
	postgres := &v1alpha1.ManagedWorkload{ObjectMeta: metav1.ObjectMeta{Name: "postgres", Namespace: ns},
		Spec: v1alpha1.ManagedWorkloadSpec{
			Target:     v1alpha1.WorkloadRef{Kind: v1alpha1.TargetKindStatefulSet, Name: "postgres"},
			Prediction: v1alpha1.PredictionSpec{Confidence: 85},
			IdlePolicy: &v1alpha1.IdlePolicySpec{IdleAfter: &metav1.Duration{Duration: time.Hour}}}}
	require.NoError(t, createTakenOn(ctx, c, postgres))
	postgres.Status.Activity = ptr.To(clock(time.Hour))
	require.NoError(t, c.Status().Update(ctx, postgres))
	e.watchPhases(t)

	caching := cache.Options{DefaultNamespaces: map[string]cache.Config{ns: {}}}
	if setup.doorman {
		caching.DefaultNamespaces[doormanNS] = cache.Config{}
		caching.ByObject = map[client.Object]cache.ByObject{&discoveryv1.EndpointSlice{}: EndpointSliceCache(nil, doormanNS)}
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme,
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: config.Controller{SkipNameValidation: ptr.To(true)},
		Cache:      caching})
	require.NoError(t, err)
	engines := newEngineRegistry(func() forecaster { return &stubForecaster{} })
	engines.engines[apiMW.UID] = &gatedForecaster{stubForecaster: &stubForecaster{}, gate: e.apiWake}
	e.r = &Reconciler{Client: client.WithFieldOwner(mgr.GetClient(), v1alpha1.FieldManager), Scheme: scheme,
		Recorder: events.NewFakeRecorder(10000), PodReader: mgr.GetAPIReader(), MaxConcurrentReconciles: 8,
		metrics: &gatedCPU{stubMetrics: &stubMetrics{cpuPerReplica: 100, memoryPerReplica: 1 << 30, replicas: 2},
			gates: map[string]*gate{theDependency: e.postgresCPU, "api": e.apiCPU}},
		engines: engines}
	if setup.doorman {
		e.r.DoormanService, e.r.DoormanNamespace = "napwake-doorman", doormanNS
	}
	for _, w := range []*v1alpha1.ManagedWorkload{apiMW, postgres} {
		e.r.activityMemo.put(w.UID, *w.Status.Activity, evaluated.Time, nil)
	}
	require.NoError(t, e.r.SetupWithManager(mgr))
	e.mgr = mgr
	return e
}

// start runs the operator until the test ends. Every gate opens first, so
// nothing it holds outlasts the test.
func (e *dependentWakeEnv) start(t *testing.T) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	var running sync.WaitGroup
	running.Go(func() { assert.NoError(t, e.mgr.Start(ctx)) })
	t.Cleanup(func() {
		e.postgresCPU.open()
		e.apiCPU.open()
		e.apiWake.open()
		stop()
		running.Wait()
	})
}

// runWorkloadControllers stands in for the kubelet and the Deployment and
// StatefulSet controllers: a scale-down's pods are gone after the stop time
// given for its workload, and a scale-up's pods are Ready after half a
// second.
func (e *dependentWakeEnv) runWorkloadControllers(t *testing.T, stops map[string]time.Duration) {
	t.Helper()
	ctx, stop := context.WithCancel(e.ctx)
	changed := map[string]time.Time{}
	asked := map[string]int32{}
	var running sync.WaitGroup
	running.Go(func() {
		for ctx.Err() == nil {
			for name, stopAfter := range stops {
				e.settle(ctx, name, stopAfter, changed, asked)
			}
			select {
			case <-ctx.Done():
			case <-time.After(50 * time.Millisecond):
			}
		}
	})
	t.Cleanup(func() {
		stop()
		running.Wait()
	})
}

func (e *dependentWakeEnv) settle(ctx context.Context, name string, stopAfter time.Duration,
	changed map[string]time.Time, asked map[string]int32) {
	var obj client.Object = &appsv1.Deployment{}
	if name == theDependency {
		obj = &appsv1.StatefulSet{}
	}
	if e.c.Get(ctx, client.ObjectKey{Namespace: e.ns, Name: name}, obj) != nil {
		return
	}
	var spec, replicas, ready *int32
	var observed *int64
	switch o := obj.(type) {
	case *appsv1.Deployment:
		spec, replicas, ready, observed = o.Spec.Replicas, &o.Status.Replicas, &o.Status.ReadyReplicas,
			&o.Status.ObservedGeneration
	case *appsv1.StatefulSet:
		spec, replicas, ready, observed = o.Spec.Replicas, &o.Status.Replicas, &o.Status.ReadyReplicas,
			&o.Status.ObservedGeneration
	}
	if prev, seen := asked[name]; !seen || prev != *spec {
		asked[name], changed[name] = *spec, time.Now()
	}
	if name == theDependency {
		e.mu.Lock()
		e.postgresReplicas = min(e.postgresReplicas, *spec)
		e.mu.Unlock()
	}
	wantReplicas, wantReady := *replicas, *ready
	switch since := time.Since(changed[name]); {
	case *spec < *replicas && since >= stopAfter:
		wantReplicas, wantReady = *spec, min(*ready, *spec)
	case *spec > *replicas:
		wantReplicas, wantReady = *spec, 0
	case *spec == *replicas && *ready < *spec && since >= 500*time.Millisecond:
		wantReady = *spec
	}
	if wantReplicas == *replicas && wantReady == *ready && *observed == obj.GetGeneration() {
		return
	}
	*replicas, *ready, *observed = wantReplicas, wantReady, obj.GetGeneration()
	_ = e.c.Status().Update(ctx, obj) // a write that loses to a newer one is made again on the next pass
}

// watchPhases records each workload's phase changes as the API server
// reports them, in order.
func (e *dependentWakeEnv) watchPhases(t *testing.T) {
	t.Helper()
	w, err := e.c.Watch(e.ctx, &v1alpha1.ManagedWorkloadList{}, client.InNamespace(e.ns))
	require.NoError(t, err)
	var watching sync.WaitGroup
	watching.Go(func() {
		for ev := range w.ResultChan() {
			if mw, ok := ev.Object.(*v1alpha1.ManagedWorkload); ok {
				e.saw(mw)
			}
		}
	})
	t.Cleanup(func() {
		w.Stop()
		watching.Wait()
	})
}

func (e *dependentWakeEnv) saw(mw *v1alpha1.ManagedWorkload) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.phases[mw.Name] == mw.Status.Phase {
		return
	}
	e.phases[mw.Name] = mw.Status.Phase
	line := fmt.Sprintf("%s %s (api %s, a request waiting %t)", mw.Name, mw.Status.Phase, e.phases["api"], e.requested)
	e.history = append(e.history, line)
	if mw.Name != theDependency || (e.phases["api"] == v1alpha1.PhasePaused && !e.requested) {
		return
	}
	switch mw.Status.Phase {
	case v1alpha1.PhaseIdle, v1alpha1.PhasePausing, v1alpha1.PhasePaused:
		e.violations = append(e.violations, line)
	}
}

func (e *dependentWakeEnv) phase(name string) v1alpha1.WorkloadPhase {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.phases[name]
}

// state says what the test has seen so far, for a failed wait.
func (e *dependentWakeEnv) state() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	waiting, passed := e.postgresCPU.counts()
	return fmt.Sprintf("phases %v; postgres's CPU reads waiting %d, passed %d; history:\n%s", e.phases, waiting, passed,
		strings.Join(e.history, "\n"))
}

func (e *dependentWakeEnv) workload(t *testing.T, name string) *v1alpha1.ManagedWorkload {
	t.Helper()
	var w v1alpha1.ManagedWorkload
	require.NoError(t, e.c.Get(e.ctx, client.ObjectKey{Namespace: e.ns, Name: name}, &w))
	return &w
}

func (e *dependentWakeEnv) apiReplicas(t *testing.T) int32 {
	t.Helper()
	var d appsv1.Deployment
	require.NoError(t, e.c.Get(e.ctx, client.ObjectKey{Namespace: e.ns, Name: "api"}, &d))
	return *d.Spec.Replicas
}

func (e *dependentWakeEnv) waitFor(t *testing.T, what string, done func() bool) {
	t.Helper()
	if !assert.Eventually(t, done, 30*time.Second, 5*time.Millisecond, what) {
		t.Fatalf("%s: %s", what, e.state())
	}
}

// requestForAPI stamps a request on api as the doorman does for one it
// holds: under Napwake's field manager, with the address it came from,
// which is no pod's.
func (e *dependentWakeEnv) requestForAPI(t *testing.T) time.Time {
	t.Helper()
	e.mu.Lock()
	e.requested = true
	e.mu.Unlock()
	at := time.Now()
	p := client.RawPatch(types.MergePatchType, fmt.Appendf(nil, `{"metadata":{"annotations":{%q:%q,%q:%q}}}`,
		v1alpha1.AnnotationLastRequest, at.UTC().Format(time.RFC3339Nano),
		v1alpha1.AnnotationLastRequestFrom, "10.99.0.1"))
	require.NoError(t, client.WithFieldOwner(e.c, v1alpha1.FieldManager).Patch(e.ctx, e.workload(t, "api"), p))
	return at
}

// assertNeverLetGo checks that postgres never began to pause, or paused,
// while api was awake or a request waited for it, that it was never scaled
// down, and that both end Running.
func (e *dependentWakeEnv) assertNeverLetGo(t *testing.T) {
	t.Helper()
	e.waitFor(t, "both end Running", func() bool {
		return e.phase("api") == v1alpha1.PhaseRunning && e.phase(theDependency) == v1alpha1.PhaseRunning
	})
	e.mu.Lock()
	defer e.mu.Unlock()
	assert.Empty(t, e.violations, "postgres let go as api wakes; history:\n%s", strings.Join(e.history, "\n"))
	assert.Equal(t, int32(1), e.postgresReplicas, "postgres is never scaled down")
}

// On the test cluster on 26 Oct, api's pause reached Paused: its pods
// stopped within a second of its scale-down, before the next request. The
// cluster-wide transition counters read the same for that as for postgres
// pausing while api was turned back. A dependent asleep with a request
// waiting for it is waking, so it holds its dependency as an awake one
// does, whether the operator's graph has the request yet or only the API
// server. A dependent turned back before it's paused holds its dependency
// throughout, and its wake restarts the dependency's clock. And a
// dependency whose dependent learns it only once it has begun to pause has
// its pause called off before it's scaled down.
func TestDependencies_HeldWhileItsDependentWakes(t *testing.T) {
	cfg := startEnvtest(t)

	t.Run("asleep with a request waiting, it holds its dependency's idle check", func(t *testing.T) {
		e := startDependentWakeEnv(t, cfg, "waiting", dependentWake{apiStops: 300 * time.Millisecond})
		// postgres's first idle check waits until api is paused and a
		// request waits for it, and api's wake until postgres has decided.
		e.postgresCPU.close()
		e.start(t)
		e.waitFor(t, "api pauses, its pods stopped, with postgres's idle check waiting", func() bool {
			waiting, _ := e.postgresCPU.counts()
			return e.phase("api") == v1alpha1.PhasePaused && waiting > 0
		})

		e.apiWake.close()
		e.requestForAPI(t)
		e.waitFor(t, "the operator's cache has the request", func() bool {
			var w v1alpha1.ManagedWorkload
			return e.mgr.GetClient().Get(e.ctx, client.ObjectKey{Namespace: e.ns, Name: "api"}, &w) == nil &&
				w.Annotations[v1alpha1.AnnotationLastRequest] != ""
		})
		_, passed := e.postgresCPU.counts()
		e.postgresCPU.open()
		// postgres's next check reads its CPU only once this one is over,
		// unless this one began to pause it.
		p := client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"annotations":{"test.napwake.dev/tick":"1"}}}`))
		require.NoError(t, e.c.Patch(e.ctx, e.workload(t, theDependency), p))
		e.waitFor(t, "postgres's idle check decides", func() bool {
			_, now := e.postgresCPU.counts()
			return now >= passed+2 || e.phase(theDependency) != v1alpha1.PhaseRunning
		})
		assert.Equal(t, v1alpha1.PhasePaused, e.phase("api"), "api hadn't woken yet")
		e.apiWake.open()

		e.assertNeverLetGo(t)
	})

	t.Run("turned back before it's paused, it holds its dependency, and wakes its clock", func(t *testing.T) {
		e := startDependentWakeEnv(t, cfg, "turned-back", dependentWake{apiStops: 3 * time.Second})
		e.start(t)
		e.waitFor(t, "api is scaled down, its pods stopping", func() bool {
			return e.phase("api") == v1alpha1.PhasePausing && e.apiReplicas(t) == 0
		})
		requested := e.requestForAPI(t)

		e.assertNeverLetGo(t)
		e.mu.Lock()
		assert.NotContains(t, e.history, "api Paused (api Paused, a request waiting true)",
			"api is turned back before it's paused")
		e.mu.Unlock()
		postgres := e.workload(t, theDependency)
		assert.NotEmpty(t, postgres.Annotations[v1alpha1.AnnotationLastActivity], "api's wake stamps postgres")
		assert.False(t, postgres.Status.Activity.LastActivityTime.Time.Before(requested.Truncate(time.Second)),
			"which restarts postgres's clock: %s", postgres.Status.Activity.LastActivityTime)
	})

	t.Run("learned while it pauses, it calls the pause off before it's scaled down", func(t *testing.T) {
		e := startDependentWakeEnv(t, cfg, "learned", dependentWake{apiStops: time.Second, doorman: true,
			unlearned: true, apiBusy: true})
		// api is taken on as the operator starts, and its first check, which
		// learns postgres, waits until postgres has begun to pause: nothing
		// held it when its clock ran out.
		e.apiCPU.close()
		e.start(t)
		e.waitFor(t, "postgres begins to pause, the doorman fronting it", func() bool {
			return e.phase(theDependency) == v1alpha1.PhasePausing
		})
		require.Nil(t, e.workload(t, "api").Status.LearnedDependencies, "api hasn't learned postgres yet")
		e.apiCPU.open()
		e.waitFor(t, "api learns postgres", func() bool { return learnedNames(e.workload(t, "api")) != nil })

		e.waitFor(t, "postgres's pause ends, called off or at zero", func() bool {
			e.mu.Lock()
			defer e.mu.Unlock()
			return e.postgresReplicas == 0 || e.phases[theDependency] == v1alpha1.PhaseResuming ||
				slices.Contains(e.history, "postgres Resuming (api Running, a request waiting false)")
		})
		e.waitFor(t, "both end Running", func() bool {
			return e.phase("api") == v1alpha1.PhaseRunning && e.phase(theDependency) == v1alpha1.PhaseRunning
		})
		e.mu.Lock()
		assert.Equal(t, int32(1), e.postgresReplicas, "postgres is never scaled down; history:\n%s",
			strings.Join(e.history, "\n"))
		assert.NotContains(t, e.history, "postgres Paused (api Running, a request waiting false)")
		assert.NotContains(t, e.history, "api Idle (api Idle, a request waiting false)", "api stays awake")
		e.mu.Unlock()
		assert.Contains(t, drainEvents(t, e.r), "Normal "+ReasonPauseCancelled+" postgres: its dependent learned/api "+
			"is awake as it paused; the pause is cancelled")
	})
}
