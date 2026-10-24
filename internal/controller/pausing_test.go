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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
// clock has run out, its Service, its own Ready pod, and the doorman, with
// a clock the test moves.
func pausingReconciler(t *testing.T, objs ...client.Object) (*Reconciler, *stubPauser, *time.Time) {
	t.Helper()
	workload := depWorkload("default", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning)
	pod, slice := apiOwnPod()
	pauser := &stubPauser{pauseDone: true, resumeDone: true}
	all := append([]client.Object{workload, runningAPI(), defaultNamespace(nil), doormanEndpoints("10.0.0.7"),
		service("api", map[string]string{"app": "api"}, corev1.ServicePort{Name: "http", Port: 80}), pod, slice},
		objs...)
	r := doormanReconciler(t, all...)
	r.pauser = pauser
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
	r, pauser, now := pausingReconciler(t)

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

// A workload the doorman doesn't route pauses as it always has, with nothing
// to wait for.
func TestPause_AWorkloadTheDoormanDoesNotRoutePausesAtOnce(t *testing.T) {
	r, pauser, _ := pausingReconciler(t)
	r.DoormanService = ""

	reconcileAPI(t, r)

	assert.Equal(t, 1, pauser.pauseCalls)
	assert.Equal(t, v1alpha1.PhasePaused, fetch(t, r, "api").Status.Phase)
}

// However the doorman fails to front it, a pause is never held up for long.
func TestPause_WaitsForTheDoormanOnlySoLong(t *testing.T) {
	r, pauser, now := pausingReconciler(t)
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
