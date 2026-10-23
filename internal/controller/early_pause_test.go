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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
	"github.com/okedeji/hybernate/internal/doorman"
	"github.com/okedeji/hybernate/internal/forecast"
)

// quietForecast is a confident forecast that expects no demand in any hour.
func quietForecast() *stubForecaster {
	return &stubForecaster{phase: forecast.DailyActive, predictValue: 2}
}

// apiService selects default/api's pods, which are labelled apiPods.
const apiService = "api"

var apiPods = map[string]string{"app": "api"}

// earlyTarget is default/api.
func earlyTarget() *appsv1.Deployment {
	target := clockTarget("app:v1", nil)
	target.Spec.Template.Labels = apiPods
	return target
}

// ownPodSlice is the api Service's own slice while the workload is awake:
// one Ready pod, the target's own.
func ownPodSlice() (*discoveryv1.EndpointSlice, *corev1.Pod) {
	const own = "api-5d8f9-x7k2p"
	slice := servingSlice()
	slice.Name = apiService + "-abcde"
	slice.Labels[discoveryv1.LabelServiceName] = apiService
	slice.Endpoints[0].TargetRef.Name = own
	pod := servingPod()
	pod.Name = own
	pod.Labels = map[string]string{"app": "api", appsv1.DefaultDeploymentUniqueLabelKey: "5d8f9"}
	return slice, pod
}

// earlyPause is a Running workload with pauseEarly on, idle for idle of
// its hour's idleAfter, as setup leaves it, whose one Service the doorman
// would route, and a reconciler for it whose forecast is engine.
func earlyPause(t *testing.T, idle time.Duration, engine *stubForecaster, setup func(*v1alpha1.ManagedWorkload),
	objs ...client.Object) (*Reconciler, *v1alpha1.ManagedWorkload, *appsv1.Deployment, *stubPauser) {
	t.Helper()
	target := earlyTarget()
	workload := clockWorkload(fixedTime.Add(-idle), target)
	workload.Spec.IdlePolicy.PauseEarly = true
	if setup != nil {
		setup(workload)
	}
	pauser := &stubPauser{pauseDone: true}
	metrics := idleCPU
	r := newAutomationReconciler(t, workload, engine, automationOpts{metrics: &metrics, pauser: pauser})
	routeThroughDoorman(r)
	slice, pod := ownPodSlice()
	ctx := context.Background()
	for _, obj := range append([]client.Object{doormanEndpoints("10.0.0.7"),
		service(apiService, apiPods, corev1.ServicePort{Name: "http", Port: 80}), slice, pod}, objs...) {
		require.NoError(t, r.Create(ctx, obj))
	}
	return r, workload, target, pauser
}

// Idle for 20 minutes, with a confident forecast of quiet hours ahead and a
// doorman to wake it on a request, the workload is paused without waiting
// out its idleAfter, and the event and status say the forecast brought
// the pause forward, until when it expects quiet.
func TestEarlyPause_PausesBeforeIdleAfterRunsOut(t *testing.T) {
	r, workload, target, pauser := earlyPause(t, 20*time.Minute, quietForecast(), nil)

	_, err := r.reconcileAutomation(context.Background(), workload, target)
	require.NoError(t, err)

	assert.Equal(t, 1, pauser.pauseCalls)
	assert.Equal(t, v1alpha1.PhasePaused, workload.Status.Phase)
	require.NotNil(t, workload.Status.Activity.ForecastQuietUntil)
	assert.Equal(t, fixedTime.Add(2*time.Hour), workload.Status.Activity.ForecastQuietUntil.UTC())
	early := recorded(r.Recorder.(*events.FakeRecorder), ReasonPausedEarly)
	require.Len(t, early, 1)
	assert.Contains(t, early[0], "api: idle for 20 minutes, since its CPU use was last above the activity threshold, "+
		"and the forecast expects the hours from 12:00 to 14:00 UTC to be quiet; pausing before idleAfter (1 hour) "+
		"runs out")
	assert.Empty(t, recorded(r.Recorder.(*events.FakeRecorder), ReasonIdleDetected))
}

// A Service whose every port hybernate.io/doorman-ignore-ports leaves out,
// such as one only a scraper uses, is meant to fail while the workload is
// paused, so it doesn't hold an early pause back.
func TestEarlyPause_IgnoredPortsDontHoldItBack(t *testing.T) {
	r, workload, target, pauser := earlyPause(t, 30*time.Minute, quietForecast(), nil)
	metrics := service("api-metrics", apiPods, corev1.ServicePort{Name: "metrics", Port: 9100})
	metrics.Annotations = map[string]string{doorman.AnnotationIgnorePorts: "metrics"}
	require.NoError(t, r.Create(context.Background(), metrics))

	_, err := r.reconcileAutomation(context.Background(), workload, target)
	require.NoError(t, err)

	assert.Equal(t, 1, pauser.pauseCalls)
}

// requestWouldWake says why a request wouldn't wake the workload, for the
// log to say why the forecast didn't bring its pause forward.
func TestRequestWouldWake_SaysWhyNot(t *testing.T) {
	headless := service("api-headless", apiPods, corev1.ServicePort{Name: "grpc", Port: 9090})
	headless.Spec.ClusterIP = corev1.ClusterIPNone
	udp := service("api-udp", apiPods, corev1.ServicePort{Name: "syslog", Port: 514, Protocol: corev1.ProtocolUDP})
	tests := []struct {
		name  string
		objs  []client.Object
		setup func(*Reconciler)
		want  string
	}{
		{name: "it would", want: ""},
		{name: "Services the doorman can't route", objs: []client.Object{headless, udp},
			want: "Services the doorman can't route select its pods: api-headless (headless), api-udp (UDP port 514)"},
		{name: "no doorman", setup: func(r *Reconciler) { r.DoormanService = "" }, want: "the doorman is off"},
		{name: "too many ports", setup: func(r *Reconciler) { r.DoormanPortsPerWorkload = 1 },
			objs: []client.Object{service("api-admin", apiPods, corev1.ServicePort{Name: "admin", Port: 8081})},
			want: "the workload would hold 2 doorman ports, more than its limit of 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, workload, target, _ := earlyPause(t, 30*time.Minute, quietForecast(), nil, tt.objs...)
			if tt.setup != nil {
				tt.setup(r)
			}
			wakes, why, err := r.requestWouldWake(context.Background(), workload, target)
			require.NoError(t, err)
			assert.Equal(t, tt.want == "", wakes)
			assert.Equal(t, tt.want, why)
		})
	}
}

// Each thing an early pause rests on, missing, leaves the pause to the
// clock: the setting, 20 minutes without activity, a forecast acting and
// sure of every hour until the clock would pause it, and a request that
// would wake it.
func TestEarlyPause_OnlyWhenSure(t *testing.T) {
	busyNext := &stubForecaster{phase: forecast.DailyActive, predictByHour: map[int]float64{0: 2, 1: 30}, predictValue: 2}
	tests := []struct {
		name     string
		idle     time.Duration
		engine   *stubForecaster
		workload func(*v1alpha1.ManagedWorkload)
		setup    func(*Reconciler)
	}{
		{name: "pauseEarly off", idle: 30 * time.Minute, engine: quietForecast(),
			workload: func(w *v1alpha1.ManagedWorkload) { w.Spec.IdlePolicy.PauseEarly = false }},
		{name: "idle under 20 minutes", idle: 19 * time.Minute, engine: quietForecast()},
		{name: "forecast not acting", idle: 30 * time.Minute,
			engine: &stubForecaster{phase: forecast.DailySuggesting, predictValue: 2}},
		{name: "demand forecast for the next hour", idle: 30 * time.Minute, engine: busyNext},
		{name: "demand forecast at the threshold", idle: 30 * time.Minute,
			engine: &stubForecaster{phase: forecast.DailyActive, predictValue: 10}},
		{name: "no doorman", idle: 30 * time.Minute, engine: quietForecast(),
			setup: func(r *Reconciler) { r.DoormanService = "" }},
		{name: "its last pause found its Services couldn't be routed", idle: 30 * time.Minute, engine: quietForecast(),
			workload: func(w *v1alpha1.ManagedWorkload) {
				meta.SetStatusCondition(&w.Status.Conditions, metav1.Condition{Type: conditionWakeOnRequest,
					Status: metav1.ConditionFalse, Reason: "NodeLocalTraffic", Message: foundAtLastPause + "..."})
			}},
		{name: "a Service the doorman can't route", idle: 30 * time.Minute, engine: quietForecast(),
			setup: func(r *Reconciler) {
				local := service("api-local", apiPods, corev1.ServicePort{Name: "http", Port: 80})
				local.Spec.InternalTrafficPolicy = ptr.To(corev1.ServiceInternalTrafficPolicyLocal)
				require.NoError(t, r.Create(context.Background(), local))
			}},
		{name: "another workload's pods serve its Service", idle: 30 * time.Minute, engine: quietForecast(),
			setup: func(r *Reconciler) {
				other := servingSlice()
				other.Name = "api-other"
				other.Labels[discoveryv1.LabelServiceName] = apiService
				require.NoError(t, r.Create(context.Background(), other))
				require.NoError(t, r.Create(context.Background(), servingPod()))
			}},
		{name: "a headless Service selects it too", idle: 30 * time.Minute, engine: quietForecast(),
			setup: func(r *Reconciler) {
				headless := service("api-headless", apiPods, corev1.ServicePort{Name: "grpc", Port: 9090})
				headless.Spec.ClusterIP = corev1.ClusterIPNone
				require.NoError(t, r.Create(context.Background(), headless))
			}},
		{name: "a UDP Service selects it too", idle: 30 * time.Minute, engine: quietForecast(),
			setup: func(r *Reconciler) {
				require.NoError(t, r.Create(context.Background(), service("api-udp", apiPods,
					corev1.ServicePort{Name: "syslog", Port: 514, Protocol: corev1.ProtocolUDP})))
			}},
		{name: "its Service has a UDP port beside its TCP one", idle: 30 * time.Minute, engine: quietForecast(),
			setup: func(r *Reconciler) {
				require.NoError(t, r.Create(context.Background(), service("api-dns", apiPods,
					corev1.ServicePort{Name: "dns-tcp", Port: 53},
					corev1.ServicePort{Name: "dns", Port: 53, Protocol: corev1.ProtocolUDP})))
			}},
		{name: "its namespace's doorman ports are all held", idle: 30 * time.Minute, engine: quietForecast(),
			setup: func(r *Reconciler) {
				r.DoormanPortsPerNamespace = 1
				web := lifecycleWorkload("web", v1alpha1.PhasePaused)
				web.UID = "uid-web"
				web.Status.Doorman = []v1alpha1.DoormanRoute{{Service: "web", PortName: "http", DoormanPort: 20001}}
				require.NoError(t, r.Create(context.Background(), web))
			}},
		{name: "active-until holds it", idle: 30 * time.Minute, engine: quietForecast(),
			workload: func(w *v1alpha1.ManagedWorkload) {
				w.Annotations = map[string]string{v1alpha1.AnnotationActiveUntil: fixedTime.Add(time.Hour).Format(time.RFC3339)}
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, workload, target, pauser := earlyPause(t, tt.idle, tt.engine, tt.workload)
			if tt.setup != nil {
				tt.setup(r)
			}

			_, err := r.reconcileAutomation(context.Background(), workload, target)
			require.NoError(t, err)

			assert.Zero(t, pauser.pauseCalls)
			assert.Equal(t, v1alpha1.PhaseRunning, workload.Status.Phase)
			assert.Nil(t, workload.Status.Activity.ForecastQuietUntil)
			assert.Empty(t, recorded(r.Recorder.(*events.FakeRecorder), ReasonPausedEarly))
		})
	}
}

// The forecast is sure of every hour the workload would otherwise be awake
// for: with idleAfter 3h, that's up to the hour the clock would pause it
// in, two hours on.
func TestEarlyPause_CoversEveryHourItWouldBeAwake(t *testing.T) {
	engine := &stubForecaster{phase: forecast.DailyActive, predictValue: 2, predictByHour: map[int]float64{3: 30}}
	threeHours := func(w *v1alpha1.ManagedWorkload) {
		w.Spec.IdlePolicy.IdleAfter = &metav1.Duration{Duration: 3 * time.Hour}
	}
	r, workload, target, pauser := earlyPause(t, 30*time.Minute, engine, threeHours)

	_, err := r.reconcileAutomation(context.Background(), workload, target)
	require.NoError(t, err)
	require.Equal(t, 1, pauser.pauseCalls, "quiet until 15:00, the hour it would pause at 14:30 in included")
	assert.Equal(t, "15:00", workload.Status.Activity.ForecastQuietUntil.UTC().Format("15:04"))

	engine.predictByHour = map[int]float64{2: 30}
	r, workload, target, pauser = earlyPause(t, 30*time.Minute, engine, threeHours)
	_, err = r.reconcileAutomation(context.Background(), workload, target)
	require.NoError(t, err)
	assert.Zero(t, pauser.pauseCalls, "demand forecast from 14:00, before the clock would pause it")
}

// Awake dependents hold an early pause as they hold any.
func TestEarlyPause_HeldByAwakeDependents(t *testing.T) {
	web := depWorkload("default", "web", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning,
		v1alpha1.DependencyRef{Kind: v1alpha1.TargetKindDeployment, Name: "api"})
	r, workload, target, pauser := earlyPause(t, 30*time.Minute, quietForecast(), nil, web)

	_, err := r.reconcileAutomation(context.Background(), workload, target)
	require.NoError(t, err)

	assert.Zero(t, pauser.pauseCalls)
	assert.Equal(t, v1alpha1.PhaseRunning, workload.Status.Phase)
	assert.True(t, meta.IsStatusConditionTrue(workload.Status.Conditions, conditionHeldByDependents))
}

// After a GitOps tool undid the last pause, an early pause waits out the
// hour Hybernate gives it, as the clock's pause does.
func TestEarlyPause_WaitsAfterAGitOpsConflict(t *testing.T) {
	r, workload, target, pauser := earlyPause(t, 30*time.Minute, quietForecast(), func(w *v1alpha1.ManagedWorkload) {
		w.Status.LastScaledUp = &v1alpha1.ScaledUp{At: metav1.NewTime(fixedTime.Add(-10 * time.Minute)),
			By: "argocd-controller", GitOps: "Argo CD", Replicas: 1}
		meta.SetStatusCondition(&w.Status.Conditions, metav1.Condition{Type: conditionGitOpsConflict,
			Status: metav1.ConditionTrue, Reason: "PauseUndone"})
	})

	result, err := r.reconcileAutomation(context.Background(), workload, target)
	require.NoError(t, err)

	assert.Zero(t, pauser.pauseCalls)
	assert.Equal(t, v1alpha1.PhaseIdle, workload.Status.Phase)
	assert.Equal(t, 50*time.Minute, result.RequeueAfter, "until an hour after the conflict")
}

// In dry-run, an early pause is a would-be pause, measured like any other,
// and the next activity ends it.
func TestEarlyPause_DryRunMeasuresIt(t *testing.T) {
	r, workload, target, pauser := earlyPause(t, 30*time.Minute, quietForecast(),
		func(w *v1alpha1.ManagedWorkload) { w.Spec.DryRun = true })

	_, err := r.reconcileAutomation(context.Background(), workload, target)
	require.NoError(t, err)

	assert.Zero(t, pauser.pauseCalls)
	assert.Equal(t, v1alpha1.PhaseIdle, workload.Status.Phase)
	require.NotNil(t, workload.Status.DryRun)
	assert.Equal(t, int32(1), workload.Status.DryRun.Pauses)
	assert.NotNil(t, workload.Status.Activity.ForecastQuietUntil)
	early := recorded(r.Recorder.(*events.FakeRecorder), ReasonPausedEarly)
	require.Len(t, early, 1)
	assert.Contains(t, early[0], "[dry-run] api: idle for 30 minutes")
	assert.Contains(t, early[0], "would have paused before idleAfter (1 hour) runs out")

	r.clock = func() time.Time { return fixedTime.Add(40 * time.Minute) }
	r.metrics = &stubMetrics{cpuMillis: 500, cpuPerReplica: 1000, replicas: 1}
	_, err = r.reconcileAutomation(context.Background(), workload, target)
	require.NoError(t, err)

	assert.Equal(t, v1alpha1.PhaseRunning, workload.Status.Phase)
	assert.Equal(t, 40*time.Minute, workload.Status.DryRun.Slept.Duration)
	assert.Nil(t, workload.Status.Activity.ForecastQuietUntil, "the would-be pause is over")
}

// A pause the clock decides says nothing of the forecast, even after one
// the forecast brought forward.
func TestEarlyPause_TheClocksPauseIsNotEarly(t *testing.T) {
	r, workload, target, _ := earlyPause(t, 61*time.Minute, quietForecast(), func(w *v1alpha1.ManagedWorkload) {
		w.Status.Activity.ForecastQuietUntil = &metav1.Time{Time: fixedTime.Add(-20 * time.Hour)}
	})

	_, err := r.reconcileAutomation(context.Background(), workload, target)
	require.NoError(t, err)

	assert.Equal(t, v1alpha1.PhasePaused, workload.Status.Phase)
	assert.Nil(t, workload.Status.Activity.ForecastQuietUntil)
	assert.Len(t, recorded(r.Recorder.(*events.FakeRecorder), ReasonIdleDetected), 1)
}
