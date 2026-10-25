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
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

// apiPodHash is the pod-template-hash of the api Deployment's pods.
const apiPodHash = "5d8f7c9b4"

// runningAPI is the api Deployment, running one replica, whose pods the api
// Service selects.
func runningAPI() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "api:v1"}}},
			},
		},
		Status: appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1},
	}
}

// apiOwnPod is the api Deployment's own pod, and the api Service's slice
// listing it Ready, as the EndpointSlice controller writes it while the
// workload runs.
func apiOwnPod() (*corev1.Pod, *discoveryv1.EndpointSlice) {
	name := "api-" + apiPodHash + "-x7k2p"
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default",
			Labels: map[string]string{"app": "api", appsv1.DefaultDeploymentUniqueLabelKey: apiPodHash}},
		Status: corev1.PodStatus{PodIP: "10.244.0.5", PodIPs: []corev1.PodIP{{IP: "10.244.0.5"}}},
	}
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name: "api-abcde", Namespace: "default",
			Labels: map[string]string{
				discoveryv1.LabelServiceName: "api",
				discoveryv1.LabelManagedBy:   "endpointslice-controller.k8s.io",
			},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{"10.244.0.5"},
			Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(true)},
			TargetRef:  &corev1.ObjectReference{Kind: "Pod", Namespace: "default", Name: name},
		}},
		Ports: []discoveryv1.EndpointPort{{Name: ptr.To("http"), Port: ptr.To(int32(8080))}},
	}
	return pod, slice
}

// pausingReconciler is a reconciler for the running api workload, whose
// clock has run out, as setup leaves it, its Service, its own Ready pod, and
// the doorman, with a clock the test moves.
func pausingReconciler(t *testing.T, setup func(*v1alpha1.ManagedWorkload), objs ...client.Object) (
	*Reconciler, *stubPauser, *time.Time) {
	t.Helper()
	workload := depWorkload("default", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning)
	if setup != nil {
		setup(workload)
	}
	pod, slice := apiOwnPod()
	pauser := &stubPauser{pauseDone: true, resumeDone: true}
	all := append([]client.Object{workload, runningAPI(), defaultNamespace(nil), doormanEndpoints("10.0.0.7"),
		service("api", map[string]string{"app": "api"}, corev1.ServicePort{Name: "http", Port: 80}), pod, slice},
		objs...)
	r := doormanReconciler(t, all...)
	r.pauser = pauser
	r.Recorder = events.NewFakeRecorder(100)
	now := monotonic(fixedTime)
	r.clock = func() time.Time { return now }
	return r, pauser, &now
}

func reconcileAPI(t *testing.T, r *Reconciler) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), reconcileFor("api"))
	require.NoError(t, err)
}

// From the moment its pause starts, the doorman fronts a workload's
// Services beside its pods, so a request that comes while they stop is held
// and served rather than refused. The routes go into status first, for the
// doorman to listen on; the slice follows once it does; and the pods stop
// once proxies send to the slice too.
func TestPause_TheDoormanFrontsTheWorkloadBeforeItsPodsStop(t *testing.T) {
	r, pauser, now := pausingReconciler(t, nil)

	reconcileAPI(t, r)
	w := fetch(t, r, "api")
	require.Equal(t, v1alpha1.PhasePausing, w.Status.Phase)
	require.NotEmpty(t, w.Status.Doorman, "its routes are written as it starts pausing, for the doorman to listen on")
	assert.Empty(t, doormanSlices(t, r), "nothing is sent to the doorman before it listens")
	assert.Zero(t, pauser.pauseCalls)

	*now = now.Add(doormanListenWait)
	reconcileAPI(t, r)
	slices := doormanSlices(t, r)
	require.Len(t, slices, 1, "the doorman fronts the Service beside the workload's own pods, which still serve")
	assert.Zero(t, pauser.pauseCalls, "the pods stay until proxies send to the doorman too")
	assert.NotNil(t, fetch(t, r, "api").Status.Pause.RoutedAt, "when it began fronting it outlives a restart")

	*now = now.Add(doormanTakeover)
	reconcileAPI(t, r)
	assert.Equal(t, 1, pauser.pauseCalls)
	w = fetch(t, r, "api")
	assert.Equal(t, v1alpha1.PhasePaused, w.Status.Phase)
	after := doormanSlices(t, r)
	require.Len(t, after, 1)
	assert.Equal(t, slices[0].UID, after[0].UID, "the same slice fronts it from Pausing into Paused")
}

// The listen wait is time for every doorman replica to read the routes,
// not proof each did: a replica slow to, as on a busy node, would refuse
// what its slice is sent while the pods still serve. So the slice goes in
// only once every Ready replica answers on the routes' ports, and the pods
// stop only once proxies send to the slice too.
func TestPause_TheDoormanFrontsTheWorkloadOnlyOnceEachReplicaAnswers(t *testing.T) {
	r, pauser, now := pausingReconciler(t, nil)
	answering := false
	var dialled []string
	r.DialDoorman = func(_ context.Context, addr string) error {
		dialled = append(dialled, addr)
		if !answering {
			return errors.New("connection refused")
		}
		return nil
	}

	reconcileAPI(t, r)
	require.Equal(t, v1alpha1.PhasePausing, fetch(t, r, "api").Status.Phase)
	*now = now.Add(doormanListenWait)
	reconcileAPI(t, r)
	assert.Empty(t, doormanSlices(t, r), "a replica that doesn't answer yet is sent nothing")
	assert.Nil(t, fetch(t, r, "api").Status.Pause.RoutedAt)
	assert.Zero(t, pauser.pauseCalls, "and the pods keep serving")
	require.NotEmpty(t, dialled)
	route := fetch(t, r, "api").Status.Doorman[0]
	assert.Contains(t, dialled, fmt.Sprintf("10.0.0.7:%d", route.DoormanPort), "it dials the replica on the route's port")

	answering = true
	*now = now.Add(staleCacheRetry)
	reconcileAPI(t, r)
	require.Len(t, doormanSlices(t, r), 1, "once it answers, the doorman fronts the Service")
	assert.Zero(t, pauser.pauseCalls)
	*now = now.Add(doormanTakeover)
	reconcileAPI(t, r)
	assert.Equal(t, 1, pauser.pauseCalls, "and the pods stop once proxies send to it too")
}

// A doorman that never answers, as one a NetworkPolicy keeps the operator
// from, holds a pause no longer than doormanFrontLimit: it goes on as it
// did before the doorman fronted pauses.
func TestPause_ADoormanThatNeverAnswersHoldsThePauseOnlySoLong(t *testing.T) {
	r, pauser, now := pausingReconciler(t, nil)
	r.DialDoorman = func(context.Context, string) error { return errors.New("connection timed out") }

	reconcileAPI(t, r)
	for range 3 {
		*now = now.Add(doormanListenWait)
		reconcileAPI(t, r)
	}
	assert.Zero(t, pauser.pauseCalls)
	*now = now.Add(doormanFrontLimit)
	reconcileAPI(t, r)
	assert.Equal(t, 1, pauser.pauseCalls, "after doormanFrontLimit the pause goes on")
}

// A pause records when it began in the write that moves the workload to
// Pausing, before anything is scaled or routed, and keeps it to the end:
// the doorman's Wake it button is good for that pause alone, from its
// first moment.
func TestPause_RecordsWhenItBegan(t *testing.T) {
	r, _, now := pausingReconciler(t, nil)
	began := metav1.NewTime(*now)

	reconcileAPI(t, r)
	pause := fetch(t, r, "api").Status.Pause
	require.NotNil(t, pause)
	require.NotNil(t, pause.StartedAt, "named as the pause begins")
	assert.True(t, began.Equal(pause.StartedAt))
	assert.Nil(t, pause.RoutedAt)
	assert.Nil(t, pause.PausedAt)

	*now = now.Add(doormanListenWait)
	reconcileAPI(t, r)
	*now = now.Add(doormanTakeover)
	reconcileAPI(t, r)
	w := fetch(t, r, "api")
	require.Equal(t, v1alpha1.PhasePaused, w.Status.Phase)
	assert.True(t, began.Equal(w.Status.Pause.StartedAt), "the same pause once Paused")
}

// A workload the doorman doesn't route pauses as it always has, with nothing
// to wait for.
func TestPause_AWorkloadTheDoormanDoesNotRoutePausesAtOnce(t *testing.T) {
	r, pauser, _ := pausingReconciler(t, nil)
	r.DoormanService = ""

	reconcileAPI(t, r)

	assert.Equal(t, 1, pauser.pauseCalls)
	assert.Equal(t, v1alpha1.PhasePaused, fetch(t, r, "api").Status.Phase)
}

// However the doorman fails to front it, a pause is never held up for long.
func TestPause_WaitsForTheDoormanOnlySoLong(t *testing.T) {
	r, pauser, now := pausingReconciler(t, nil)
	reconcileAPI(t, r)
	require.Equal(t, v1alpha1.PhasePausing, fetch(t, r, "api").Status.Phase)

	require.NoError(t, r.Delete(context.Background(), doormanEndpoints("10.0.0.7")))
	*now = now.Add(doormanListenWait)
	reconcileAPI(t, r)

	assert.Equal(t, 1, pauser.pauseCalls, "with no doorman pod Ready, it pauses as it would without the doorman")
}

// automate does what a reconcile does for an awake or paused workload, and
// for one pausing, goes on with its pause, as Reconcile does once the
// doorman's routing is up to date.
func automate(ctx context.Context, r *Reconciler, workload *v1alpha1.ManagedWorkload,
	target client.Object) (*ctrl.Result, error) {
	if workload.Status.Phase == v1alpha1.PhasePausing {
		r.routeDoorman(ctx, workload, target)
		return r.resumeTransition(ctx, workload, target)
	}
	return r.reconcileAutomation(ctx, workload, target)
}

// annotate sets an annotation on the ManagedWorkload named, as the doorman,
// kubectl napwake wake, or another tool does.
func annotate(t *testing.T, r *Reconciler, name, key, value string) {
	t.Helper()
	w := fetch(t, r, name)
	if w.Annotations == nil {
		w.Annotations = map[string]string{}
	}
	w.Annotations[key] = value
	require.NoError(t, r.Update(context.Background(), w))
}

// stampRequest stamps the api workload with a request at now, as the
// doorman does for one it holds or passes on while the workload pauses.
func stampRequest(t *testing.T, r *Reconciler, now time.Time) {
	t.Helper()
	annotate(t, r, "api", v1alpha1.AnnotationLastRequest, now.UTC().Format(time.RFC3339))
	annotate(t, r, "api", v1alpha1.AnnotationLastRequestFrom, "10.244.3.8")
}

// A request the doorman sees while a workload pauses means it's in use: the
// pause is called off, and the workload is Running again, its pods never
// stopped. The doorman stops fronting it.
func TestPause_ARequestAsItPausesCancelsIt(t *testing.T) {
	r, pauser, now := pausingReconciler(t, nil)
	reconcileAPI(t, r)
	*now = now.Add(doormanListenWait)
	reconcileAPI(t, r)
	require.Equal(t, v1alpha1.PhasePausing, fetch(t, r, "api").Status.Phase)
	require.NotEmpty(t, doormanSlices(t, r))

	stampRequest(t, r, *now)
	reconcileAPI(t, r)

	w := fetch(t, r, "api")
	assert.Equal(t, v1alpha1.PhaseRunning, w.Status.Phase)
	assert.Zero(t, pauser.pauseCalls, "its pods never stopped")
	assert.Equal(t, v1alpha1.ActivitySourceRequest, w.Status.Activity.LastActivitySource)
	reconcileAPI(t, r)
	assert.Empty(t, doormanSlices(t, r), "once Running, its own pods serve it alone")
	cancelled := recorded(r.Recorder.(*events.FakeRecorder), ReasonPauseCancelled)
	require.Len(t, cancelled, 1)
	assert.Contains(t, cancelled[0], "Normal PauseCancelled")
	assert.Contains(t, cancelled[0], "a request came as it paused; the pause is cancelled")
}

// A request that comes once the pods have begun to stop has the workload
// scaled back up, and its pause cancelled, without waiting for the pods to
// be gone first.
func TestPause_ARequestAfterTheScaleDownScalesItBackUp(t *testing.T) {
	r, pauser, now := pausingReconciler(t, nil)
	pauser.pauseDone = false
	reconcileAPI(t, r)
	*now = now.Add(doormanListenWait)
	reconcileAPI(t, r)
	*now = now.Add(doormanTakeover)
	reconcileAPI(t, r)
	require.Equal(t, 1, pauser.pauseCalls)
	require.Equal(t, v1alpha1.PhasePausing, fetch(t, r, "api").Status.Phase, "its pods are still stopping")
	scaledDown := runningAPI()
	require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(scaledDown), scaledDown))
	scaledDown.Spec.Replicas = ptr.To(int32(0))
	require.NoError(t, r.Update(context.Background(), scaledDown))

	stampRequest(t, r, *now)
	reconcileAPI(t, r)

	assert.Equal(t, 1, pauser.pauseCalls, "it isn't scaled down further")
	assert.Equal(t, 1, pauser.resumeCalls)
	assert.Equal(t, v1alpha1.PhaseRunning, fetch(t, r, "api").Status.Phase)
	assert.Len(t, recorded(r.Recorder.(*events.FakeRecorder), ReasonPauseCancelled), 1)
}

// Whatever would wake a paused workload calls off a pause under way: a
// person asking for a workload that asks first, a wake request, and an
// activity annotation. Each says what came.
func TestPause_WhatWouldWakeItCancelsThePause(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*v1alpha1.ManagedWorkload)
		come  func(t *testing.T, r *Reconciler, now time.Time)
		want  string
	}{
		{name: "a person's click on a workload that asks", want: "a person asked to wake it as it paused",
			setup: func(w *v1alpha1.ManagedWorkload) { w.Spec.Wake = &v1alpha1.WakeSpec{Mode: v1alpha1.WakeOnAsk} },
			come:  stampRequest},
		{name: "a wake request", want: "a wake request came as it paused",
			come: func(t *testing.T, r *Reconciler, _ time.Time) {
				annotate(t, r, "api", v1alpha1.AnnotationWakeRequested, "wake-1")
			}},
		{name: "an activity annotation", want: "an activity annotation came as it paused",
			come: func(t *testing.T, r *Reconciler, now time.Time) {
				annotate(t, r, "api", v1alpha1.AnnotationLastActivity, now.UTC().Format(time.RFC3339))
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, pauser, now := pausingReconciler(t, tt.setup)
			reconcileAPI(t, r)
			require.Equal(t, v1alpha1.PhasePausing, fetch(t, r, "api").Status.Phase)

			tt.come(t, r, *now)
			reconcileAPI(t, r)

			assert.Equal(t, v1alpha1.PhaseRunning, fetch(t, r, "api").Status.Phase)
			assert.Zero(t, pauser.pauseCalls)
			cancelled := recorded(r.Recorder.(*events.FakeRecorder), ReasonPauseCancelled)
			require.Len(t, cancelled, 1)
			assert.Contains(t, cancelled[0], tt.want)
		})
	}
}

// A wake request that calls a pause off is answered as having woken it.
func TestPause_AWakeRequestThatCancelsAPauseIsAnswered(t *testing.T) {
	r, _, _ := pausingReconciler(t, nil)
	reconcileAPI(t, r)

	annotate(t, r, "api", v1alpha1.AnnotationWakeRequested, "wake-1")
	reconcileAPI(t, r)

	w := fetch(t, r, "api")
	assert.Equal(t, "wake-1", w.Status.LastWakeRequest)
	c := meta.FindStatusCondition(w.Status.Conditions, conditionWakeRequest)
	require.NotNil(t, c)
	assert.Equal(t, metav1.ConditionTrue, c.Status)
	assert.Equal(t, reasonWoken, c.Reason)
}

// A pause someone asked for, with kubectl napwake pause, that a request
// calls off says so in its answer, so the command doesn't wait for a pause
// that won't come.
func TestPause_ARequestedPauseCancelledSaysSo(t *testing.T) {
	r, _, now := pausingReconciler(t, func(w *v1alpha1.ManagedWorkload) {
		w.Status.Activity.LastActivityTime = metav1.NewTime(fixedTime.Add(-time.Minute))
		w.Annotations = map[string]string{v1alpha1.AnnotationPauseRequested: "pause-1"}
	})
	reconcileAPI(t, r)
	w := fetch(t, r, "api")
	require.Equal(t, v1alpha1.PhasePausing, w.Status.Phase)
	require.True(t, w.Status.Pause.Requested)

	stampRequest(t, r, *now)
	reconcileAPI(t, r)

	w = fetch(t, r, "api")
	assert.Equal(t, v1alpha1.PhaseRunning, w.Status.Phase)
	c := meta.FindStatusCondition(w.Status.Conditions, conditionPauseRequest)
	require.NotNil(t, c)
	assert.Equal(t, metav1.ConditionFalse, c.Status)
	assert.Equal(t, ReasonPauseCancelled, c.Reason)
	assert.Equal(t, "not paused at 2026-03-14 12:00 UTC: a request came as it paused", c.Message)
}

// A dependency whose dependent wakes as it pauses was woken for its
// dependent, which the cancelled pause names.
func TestPause_ADependentWakingCancelsItsDependencysPause(t *testing.T) {
	web := depWorkload("default", "web", v1alpha1.TargetKindDeployment, v1alpha1.PhasePaused,
		v1alpha1.DependencyRef{Kind: v1alpha1.TargetKindDeployment, Name: "api"})
	r, _, now := pausingReconciler(t, nil, web, targetDeploymentWithReplicas("web", "default", 0))
	reconcileAPI(t, r)
	require.Equal(t, v1alpha1.PhasePausing, fetch(t, r, "api").Status.Phase)

	waking := fetch(t, r, "web")
	waking.Status.Phase = v1alpha1.PhaseResuming
	require.NoError(t, r.Status().Update(context.Background(), waking))
	annotate(t, r, "api", v1alpha1.AnnotationLastActivity, now.UTC().Format(time.RFC3339))
	reconcileAPI(t, r)

	assert.Equal(t, v1alpha1.PhaseRunning, fetch(t, r, "api").Status.Phase)
	cancelled := recorded(r.Recorder.(*events.FakeRecorder), ReasonPauseCancelled)
	require.Len(t, cancelled, 1)
	assert.Contains(t, cancelled[0], "its dependent default/web woke as it paused")
}

// A cancelled pause didn't hold, so it doesn't show a GitOps tool now
// leaves the replicas to Napwake.
func TestPause_ACancelledPauseKeepsAGitOpsConflict(t *testing.T) {
	r, _, now := pausingReconciler(t, func(w *v1alpha1.ManagedWorkload) {
		w.Status.LastScaledUp = &v1alpha1.ScaledUp{At: metav1.NewTime(fixedTime.Add(-2 * time.Hour)),
			By: "argocd-controller", GitOps: "Argo CD", Replicas: 1}
		meta.SetStatusCondition(&w.Status.Conditions, metav1.Condition{Type: conditionGitOpsConflict,
			Status: metav1.ConditionTrue, Reason: "PauseUndone"})
	})
	reconcileAPI(t, r)
	require.Equal(t, v1alpha1.PhasePausing, fetch(t, r, "api").Status.Phase, "the hour after the conflict is over")

	stampRequest(t, r, *now)
	reconcileAPI(t, r)

	w := fetch(t, r, "api")
	assert.Equal(t, v1alpha1.PhaseRunning, w.Status.Phase)
	assert.True(t, meta.IsStatusConditionTrue(w.Status.Conditions, conditionGitOpsConflict))
}

// Dry-run never pauses, so there's no pause to call off: activity ends the
// would-be pause as it always has.
func TestPause_DryRunHasNoPauseToCancel(t *testing.T) {
	r, pauser, now := pausingReconciler(t, func(w *v1alpha1.ManagedWorkload) { w.Spec.DryRun = true })
	reconcileAPI(t, r)
	require.Equal(t, v1alpha1.PhaseIdle, fetch(t, r, "api").Status.Phase)
	assert.Empty(t, fetch(t, r, "api").Status.Doorman, "nothing is routed")

	*now = now.Add(time.Minute)
	annotate(t, r, "api", v1alpha1.AnnotationLastActivity, now.UTC().Format(time.RFC3339))
	reconcileAPI(t, r)

	assert.Equal(t, v1alpha1.PhaseRunning, fetch(t, r, "api").Status.Phase)
	assert.Zero(t, pauser.pauseCalls)
	assert.Empty(t, recorded(r.Recorder.(*events.FakeRecorder), ReasonPauseCancelled))
}

// A reconcile that comes late, past doormanFrontLimit, still has the
// doorman's slice stand beside the pods for doormanTakeover before they
// stop, since proxies haven't seen the slice it has only just made.
func TestPause_ALateReconcileStillWaitsForProxiesToSeeTheSlice(t *testing.T) {
	r, pauser, now := pausingReconciler(t, nil)
	reconcileAPI(t, r)
	require.Equal(t, v1alpha1.PhasePausing, fetch(t, r, "api").Status.Phase)

	*now = now.Add(doormanFrontLimit + time.Second)
	reconcileAPI(t, r)
	require.Len(t, doormanSlices(t, r), 1, "the doorman fronts it")
	assert.Zero(t, pauser.pauseCalls, "the pods stay until proxies send to the slice too")
	assert.NotNil(t, fetch(t, r, "api").Status.Pause.RoutedAt)

	*now = now.Add(doormanTakeover)
	reconcileAPI(t, r)
	assert.Equal(t, 1, pauser.pauseCalls)
}

// The doorman listens on routes it reads from status, so a slice may send
// to them only doormanListenWait after they're stored there. When the
// write that stores them fails, they're written again, and the wait is
// timed from that.
func TestPause_TheDoormanListensOnlyOnRoutesStored(t *testing.T) {
	r, pauser, now := pausingReconciler(t, nil)
	writes := 0
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object,
			opts ...client.SubResourceUpdateOption) error {
			if w, ok := obj.(*v1alpha1.ManagedWorkload); ok && len(w.Status.Doorman) > 0 {
				if writes++; writes == 1 {
					return apierrors.NewServiceUnavailable("etcd is slow")
				}
			}
			return c.SubResource(sub).Update(ctx, obj, opts...)
		}})

	_, _ = r.Reconcile(context.Background(), reconcileFor("api"))
	w := fetch(t, r, "api")
	require.Equal(t, v1alpha1.PhasePausing, w.Status.Phase)
	require.Nil(t, w.Status.Pause.RoutesWrittenAt, "the routes' write failed")
	assert.Empty(t, doormanSlices(t, r))

	*now = now.Add(doormanListenWait)
	reconcileAPI(t, r)
	require.NotEmpty(t, fetch(t, r, "api").Status.Doorman, "the routes are written again")
	assert.Empty(t, doormanSlices(t, r), "the doorman hasn't read them yet")

	*now = now.Add(doormanListenWait)
	reconcileAPI(t, r)
	assert.Len(t, doormanSlices(t, r), 1)
	assert.Zero(t, pauser.pauseCalls)

	*now = now.Add(doormanTakeover)
	reconcileAPI(t, r)
	assert.Equal(t, 1, pauser.pauseCalls)
}

// A conflict on the write that stores the routes, as a reconcile reading a
// cache that hasn't seen the move to Pausing gets, is a stale read to
// retry soon, not a routing failure: nothing warns, WakeOnRequest isn't
// set False, and routing isn't backed off.
func TestPause_AConflictStoringTheRoutesIsRetriedQuietly(t *testing.T) {
	r, pauser, now := pausingReconciler(t, nil)
	conflicts := 0
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object,
			opts ...client.SubResourceUpdateOption) error {
			if w, ok := obj.(*v1alpha1.ManagedWorkload); ok && len(w.Status.Doorman) > 0 && conflicts == 0 {
				conflicts++
				return apierrors.NewConflict(v1alpha1.GroupVersion.WithResource("managedworkloads").GroupResource(),
					w.Name, errors.New("the object has been modified"))
			}
			return c.SubResource(sub).Update(ctx, obj, opts...)
		}})

	reconcileAPI(t, r)
	w := fetch(t, r, "api")
	require.Equal(t, v1alpha1.PhasePausing, w.Status.Phase)
	require.Equal(t, 1, conflicts)
	assert.False(t, conditionFalseWith(w, conditionWakeOnRequest, reasonRoutingFailed))
	assert.Empty(t, recorded(r.Recorder.(*events.FakeRecorder), reasonRoutingFailed))
	assert.NotContains(t, r.doormanFailures.failures, w.UID, "routing isn't backed off")

	*now = now.Add(staleCacheRetry)
	reconcileAPI(t, r)
	require.NotNil(t, fetch(t, r, "api").Status.Pause.RoutesWrittenAt, "stored on the retry")
	assert.Empty(t, doormanSlices(t, r))
	assert.Zero(t, pauser.pauseCalls)
}

// The takeover is timed from the reconcile that put the doorman's slice in
// place, not from the clock passing the listen wait: a reconcile whose
// routing ran a moment before the wait ended put no slice in, so the pause
// waits for the next to, and only then the takeover.
func TestPause_TheTakeoverIsTimedFromTheSliceGoingIn(t *testing.T) {
	r, pauser, now := pausingReconciler(t, nil)
	reconcileAPI(t, r)
	require.Equal(t, v1alpha1.PhasePausing, fetch(t, r, "api").Status.Phase)

	*now = now.Add(doormanListenWait)
	wait := r.untilFronted(fetch(t, r, "api"))
	assert.Positive(t, wait)
	assert.Less(t, wait, doormanTakeover, "the next reconcile puts the slice in")
	assert.Nil(t, fetch(t, r, "api").Status.Pause.RoutedAt, "nothing stands in front of it yet")
	assert.Empty(t, doormanSlices(t, r))

	*now = now.Add(wait)
	reconcileAPI(t, r)
	require.Len(t, doormanSlices(t, r), 1)
	require.NotNil(t, fetch(t, r, "api").Status.Pause.RoutedAt)
	assert.Zero(t, pauser.pauseCalls, "proxies haven't seen the slice yet")

	*now = now.Add(doormanTakeover)
	reconcileAPI(t, r)
	assert.Equal(t, 1, pauser.pauseCalls)
}
