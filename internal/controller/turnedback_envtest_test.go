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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/autoscaler"
	"github.com/napwake/napwake/internal/forecast"
	"github.com/napwake/napwake/internal/lifecycle"
)

// turnedBackEnv is an API server with the doorman's Service in front of
// each case's api, as on the test cluster: a Deployment of 1 whose only
// activity source is its CPU, at 7% of the 10% threshold, and whose idle
// clock has just run out.
type turnedBackEnv struct {
	ctx   context.Context
	admin client.WithWatch
}

func startTurnedBackEnv(t *testing.T) *turnedBackEnv {
	t.Helper()
	cfg := startEnvtest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	admin, err := client.NewWithWatch(cfg, client.Options{Scheme: testScheme(t)})
	require.NoError(t, err)
	doorman := doormanEndpoints("10.0.0.7")
	require.NoError(t, admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: doorman.Namespace}}))
	require.NoError(t, admin.Create(ctx, doorman))
	return &turnedBackEnv{ctx: ctx, admin: admin}
}

// turnedBackCase is one case's workload, and the operator watching it.
type turnedBackCase struct {
	*turnedBackEnv
	key      client.ObjectKey
	r        *Reconciler
	now      time.Time
	forecast *stubForecaster
	dialled  []string
}

// create makes the case's namespace, its api Deployment, Service and
// ManagedWorkload, last active at 08:59:30 with an idleAfter of an hour,
// and an operator whose clock reads 10:00:30, a look after its clock ran
// out.
func (e *turnedBackEnv) create(t *testing.T, ns string, setup func(*v1alpha1.ManagedWorkload)) *turnedBackCase {
	t.Helper()
	hour := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	tc := &turnedBackCase{turnedBackEnv: e, now: monotonic(hour.Add(30 * time.Second)), forecast: &stubForecaster{}}
	require.NoError(t, e.admin.Create(e.ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	api := envtestDeployment(ns, 1)
	require.NoError(t, e.admin.Create(e.ctx, api))
	svc := service("api", map[string]string{"app": "api"}, corev1.ServicePort{Name: "http", Port: 80})
	svc.Namespace = ns
	require.NoError(t, e.admin.Create(e.ctx, svc))

	w := envtestWorkload(ns)
	w.Spec.IdlePolicy = &v1alpha1.IdlePolicySpec{IdleAfter: &metav1.Duration{Duration: time.Hour}}
	if setup != nil {
		setup(w)
	}
	require.NoError(t, createTakenOn(e.ctx, e.admin, w))
	evaluated := metav1.NewTime(hour.Add(-30 * time.Second))
	w.Status.Activity = &v1alpha1.ActivityStatus{LastActivityTime: metav1.NewTime(hour.Add(-time.Hour - 30*time.Second)),
		LastActivitySource: v1alpha1.ActivitySourceCPU, LastEvaluatedTime: &evaluated, TemplateHash: podTemplateHash(api)}
	require.NoError(t, e.admin.Status().Update(e.ctx, w))
	tc.key = client.ObjectKeyFromObject(w)

	c := client.WithFieldOwner(indexedClient{e.admin}, v1alpha1.FieldManager)
	finder := autoscaler.NewFinder(c)
	tc.r = &Reconciler{
		Client: c, Scheme: testScheme(t), Recorder: events.NewFakeRecorder(1000), PodReader: indexedClient{e.admin},
		pauser: lifecycle.NewPauser(c, finder), autoscalers: finder,
		metrics: &stubMetrics{cpuMillis: 70, cpuPerReplica: 1000, replicas: 1},
		engines: newEngineRegistry(func() forecaster { return tc.forecast }),
		clock:   func() time.Time { return tc.now },
		DialDoorman: func(_ context.Context, addr string) error {
			tc.dialled = append(tc.dialled, addr)
			return nil
		},
	}
	routeThroughDoorman(tc.r)
	remember(tc.r, w)
	return tc
}

// reconcile has the operator reconcile the workload at its clock, after
// the Deployment controller's part: the pods asked for are Ready at once.
func (tc *turnedBackCase) reconcile(t *testing.T) *v1alpha1.ManagedWorkload {
	t.Helper()
	(&faultEnv{ctx: tc.ctx, admin: tc.admin}).settle(t, tc.key)
	_, err := tc.r.Reconcile(tc.ctx, ctrl.Request{NamespacedName: tc.key})
	require.NoError(t, err)
	var w v1alpha1.ManagedWorkload
	require.NoError(t, tc.admin.Get(tc.ctx, tc.key, &w))
	return &w
}

// pauseUntilFronted reconciles the workload into Pausing, then on until the
// doorman fronts it, which the operator's check of the doorman's replica
// comes before. Its pods still serve.
func (tc *turnedBackCase) pauseUntilFronted(t *testing.T) {
	t.Helper()
	require.Equal(t, v1alpha1.PhasePausing, tc.reconcile(t).Status.Phase, "its clock ran out at 09:59:30")
	tc.now = tc.now.Add(doormanListenWait)
	w := tc.reconcile(t)
	require.Equal(t, v1alpha1.PhasePausing, w.Status.Phase)
	require.NotNil(t, w.Status.Pause.RoutedAt, "the doorman fronts it")
	require.Nil(t, w.Status.Pause.PausedAt, "and its pods still serve")
	require.NotEmpty(t, tc.dialled, "once the operator has checked the doorman listens")
}

// stamp has the doorman stamp a request on the workload, as it does for one
// passed on or held as the workload pauses.
func (tc *turnedBackCase) stamp(t *testing.T) {
	t.Helper()
	var w v1alpha1.ManagedWorkload
	require.NoError(t, tc.admin.Get(tc.ctx, tc.key, &w))
	original := w.DeepCopy()
	metav1.SetMetaDataAnnotation(&w.ObjectMeta, v1alpha1.AnnotationLastRequest, tc.now.UTC().Format(time.RFC3339))
	metav1.SetMetaDataAnnotation(&w.ObjectMeta, v1alpha1.AnnotationLastRequestFrom, "10.244.3.8")
	require.NoError(t, tc.admin.Patch(tc.ctx, &w, client.MergeFrom(original)))
}

// watchUntil has the operator check the workload once a minute, at half
// past each, as it does, up to and including until.
func (tc *turnedBackCase) watchUntil(t *testing.T, until time.Time) {
	t.Helper()
	next := monotonic(tc.now.Round(0).Truncate(time.Minute).Add(time.Minute + 30*time.Second))
	for tc.now = next; !tc.now.After(until); tc.now = tc.now.Add(time.Minute) {
		tc.reconcile(t)
	}
}

// fed is the peak the forecast was fed for the hour from start, failing if
// it wasn't fed one.
func (tc *turnedBackCase) fed(t *testing.T, start time.Time) float64 {
	t.Helper()
	i := slices.IndexFunc(tc.forecast.observedHours, start.Equal)
	require.GreaterOrEqual(t, i, 0, "the hour from %s was fed to the forecast: fed %v", start.Format("15:04"),
		tc.forecast.observedHours)
	return tc.forecast.observed[i]
}

// setDryRun switches dry-run on for the workload, as kubectl napwake
// dry-run does.
func (tc *turnedBackCase) setDryRun(t *testing.T) {
	t.Helper()
	var w v1alpha1.ManagedWorkload
	require.NoError(t, tc.admin.Get(tc.ctx, tc.key, &w))
	w.Spec.DryRun = true
	require.NoError(t, tc.admin.Update(tc.ctx, &w))
}

// The soak's orders/api, P71: its only activity source is its CPU, under
// its threshold through each load day, so it was paused about every 65
// minutes, and each pause was turned back within seconds by a request
// through the doorman. No look at its demand is made while it pauses, and
// the wake's warm-up hid the request after, so the forecast learned those
// hours from its CPU, and wouldn't hold its pauses there. A request that
// turns a pause back is the demand one that wakes it from zero is, in the
// hour it comes in, and in no other. The operator's own check that the
// doorman listens is no request. A click, for a workload that asks first,
// and dry-run, learn as they did.
func TestTurnedBackPause_TeachesTheForecastItsHourIsBusy(t *testing.T) {
	env := startTurnedBackEnv(t)
	hour := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	busy := forecast.RequestDemand(10)
	tests := []struct {
		name  string
		setup func(*v1alpha1.ManagedWorkload)
		// come is what comes as the workload pauses, once the doorman
		// fronts it, a second before the operator next looks.
		come func(tc *turnedBackCase, t *testing.T)
		want v1alpha1.WorkloadPhase
		busy bool
	}{
		{name: "a request through the doorman", come: (*turnedBackCase).stamp,
			want: v1alpha1.PhaseRunning, busy: true},
		{name: "nothing but the operator's check that the doorman listens",
			come: func(tc *turnedBackCase, _ *testing.T) { tc.now = tc.now.Add(doormanTakeover) },
			want: v1alpha1.PhasePaused},
		{name: "a person's click, for a workload that asks",
			setup: func(w *v1alpha1.ManagedWorkload) { w.Spec.Wake = &v1alpha1.WakeSpec{Mode: v1alpha1.WakeOnAsk} },
			come:  (*turnedBackCase).stamp, want: v1alpha1.PhaseRunning},
		{name: "a request, as dry-run is switched on",
			come: func(tc *turnedBackCase, t *testing.T) {
				tc.stamp(t)
				tc.setDryRun(t)
			},
			// Woken at once, and measuring its next would-be pause by 12:00.
			want: v1alpha1.PhaseIdle},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc := env.create(t, fmt.Sprintf("case-%d", i), tt.setup)
			tc.pauseUntilFronted(t)
			tt.come(tc, t)
			tc.now = tc.now.Add(time.Second)
			tc.reconcile(t)
			tc.watchUntil(t, hour.Add(2*time.Hour+30*time.Second))

			var w v1alpha1.ManagedWorkload
			require.NoError(t, tc.admin.Get(tc.ctx, tc.key, &w))
			assert.Equal(t, tt.want, w.Status.Phase)
			if tt.busy {
				assert.GreaterOrEqual(t, tc.fed(t, hour), busy, "the hour the request came in is busy")
			} else {
				assert.Less(t, tc.fed(t, hour), 10.0, "the hour is its CPU, 7%")
			}
			assert.Less(t, tc.fed(t, hour.Add(time.Hour)), 10.0, "the hour after, with no request, isn't busy")
		})
	}
}
