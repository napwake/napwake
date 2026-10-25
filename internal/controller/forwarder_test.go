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
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/forwarder"
)

const forwarderDoorman = "napwake-doorman-pods.napwake-system.svc"

func forwarderReconciler(t *testing.T, objs ...client.Object) *Reconciler {
	t.Helper()
	r := doormanReconciler(t, objs...)
	r.OperatorNamespace = thisInstall
	r.NEGForwarder = &ForwarderOptions{
		Image:    "ghcr.io/napwake/napwake:test",
		Replicas: 2,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m")},
			Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
		},
		Doorman: forwarderDoorman,
	}
	r.doormanPorts.pick = lowestFreePort
	return r
}

// negService is a Service a GKE Ingress uses through container-native load
// balancing, as GKE annotates it once its NEGs exist.
func negService(name string, ports ...corev1.ServicePort) *corev1.Service {
	if len(ports) == 0 {
		ports = []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromInt32(8080)}}
	}
	svc := service(name, map[string]string{"app": "api"}, ports...)
	svc.Annotations = map[string]string{
		annotationGKENEG:              `{"ingress":true}`,
		"cloud.google.com/neg-status": `{"network_endpoint_groups":{"80":"k8s1-neg"},"zones":["europe-west1-b"]}`,
	}
	return svc
}

func pausedAPI() *v1alpha1.ManagedWorkload {
	w := lifecycleWorkload("api", v1alpha1.PhasePaused)
	w.UID = "forwarded-api-uid"
	return w
}

func forwarderOf(t *testing.T, r *Reconciler) *appsv1.Deployment {
	t.Helper()
	var d appsv1.Deployment
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "napwake-forwarder-api"}, &d))
	return &d
}

func noForwarder(t *testing.T, r *Reconciler) bool {
	t.Helper()
	var list appsv1.DeploymentList
	require.NoError(t, r.List(context.Background(), &list, client.InNamespace("default"),
		client.MatchingLabels{labelAppName: forwarderAppName}))
	return len(list.Items) == 0
}

// forwarderPodFor is a pod of the forwarder as it runs now, on node-1 at ip,
// Ready or not.
func forwarderPodFor(t *testing.T, r *Reconciler, name, ip string, ready bool) *corev1.Pod {
	t.Helper()
	d := forwarderOf(t, r)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID(name + "-uid"),
			Labels: d.Spec.Template.Labels},
		Spec: *d.Spec.Template.Spec.DeepCopy(),
		Status: corev1.PodStatus{
			PodIP:  ip,
			PodIPs: []corev1.PodIP{{IP: ip}},
		},
	}
	pod.Spec.NodeName = "node-1"
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: status}}
	require.NoError(t, r.Create(context.Background(), pod))
	return pod
}

func routeOf(t *testing.T, w *v1alpha1.ManagedWorkload, service string) v1alpha1.DoormanRoute {
	t.Helper()
	i := slices.IndexFunc(w.Status.Doorman, func(rt v1alpha1.DoormanRoute) bool { return rt.Service == service })
	require.GreaterOrEqual(t, i, 0, "no route for %s", service)
	return w.Status.Doorman[i]
}

func eventsOf(r *Reconciler) []string {
	recorder, ok := r.Recorder.(*events.FakeRecorder)
	if !ok {
		return nil
	}
	var out []string
	for len(recorder.Events) > 0 {
		out = append(out, <-recorder.Events)
	}
	return out
}

func TestForwarder_RunsBesideAPausedWorkloadBehindNEGs(t *testing.T) {
	w := pausedAPI()
	r := forwarderReconciler(t, w, doormanEndpoints("10.0.0.7"), negService("api"))
	ctx := context.Background()

	recheck, err := r.reconcileDoorman(ctx, w, appTarget())
	require.NoError(t, err)
	assert.Equal(t, forwarderRecheckInterval, recheck, "looked at again while the forwarder starts")

	route := routeOf(t, w, "api")
	assert.True(t, route.Forwarded, "the doorman needs to know the route arrives through a forwarder")
	d := forwarderOf(t, r)
	assert.True(t, metav1.IsControlledBy(d, w), "owned by the workload, so it's collected with it")
	assert.Equal(t, v1alpha1.True, d.Labels[v1alpha1.LabelIgnore], "a namespace opted in with its label mustn't manage it")
	assert.Equal(t, int32(2), *d.Spec.Replicas)
	pod := d.Spec.Template
	assert.Equal(t, thisInstall, pod.Labels[v1alpha1.LabelOperator], "the doorman's NetworkPolicy admits it by this")
	assert.Equal(t, "false", pod.Annotations["sidecar.istio.io/inject"])
	assert.Equal(t, "disabled", pod.Annotations["linkerd.io/inject"])
	assert.False(t, *pod.Spec.AutomountServiceAccountToken, "it needs no API access")
	assert.True(t, *pod.Spec.SecurityContext.RunAsNonRoot)
	assert.Empty(t, pod.Spec.SecurityContext.Sysctls, "port 8080 needs no unprivileged ports")
	c := pod.Spec.Containers[0]
	assert.Equal(t, "ghcr.io/napwake/napwake:test", c.Image)
	assert.Contains(t, c.Args, "--forwarder")
	assert.Contains(t, c.Args, "--forwarder-doorman="+forwarderDoorman)
	assert.Contains(t, c.Args, "--forward="+forwarder.FormatRoutes(map[int32]int32{8080: route.DoormanPort}),
		"it listens on the target port, which GKE's firewall rules admit the load balancer to")
	assert.Equal(t, []corev1.Capability{"ALL"}, c.SecurityContext.Capabilities.Drop)
	assert.True(t, *c.SecurityContext.ReadOnlyRootFilesystem)
	assert.False(t, *c.SecurityContext.AllowPrivilegeEscalation)

	assert.Empty(t, doormanSlices(t, r), "no Service points at a forwarder with no Ready pod")
	cond := wakeCondition(t, w)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, reasonForwarderNotReady, cond.Reason)
	assert.Contains(t, cond.Message, "napwake-forwarder-api")
	assert.True(t, slices.ContainsFunc(eventsOf(r), func(e string) bool { return strings.Contains(e, ReasonForwarderStarted) }))

	forwarderPodFor(t, r, "fwd-a", "10.244.2.5", true)
	forwarderPodFor(t, r, "fwd-b", "10.244.3.6", false)
	require.NoError(t, doormanErr(ctx, r, w, appTarget()))

	got := doormanSlices(t, r)
	require.Len(t, got, 1)
	slice := got[0]
	assert.Equal(t, "api", slice.Labels[discoveryv1.LabelServiceName])
	assert.Equal(t, v1alpha1.True, slice.Labels[labelForwarded])
	require.Len(t, slice.Endpoints, 1, "only the Ready pod")
	ep := slice.Endpoints[0]
	assert.Equal(t, []string{"10.244.2.5"}, ep.Addresses)
	assert.Equal(t, "node-1", *ep.NodeName, "GKE's NEG controller needs the node")
	assert.Equal(t, &corev1.ObjectReference{Kind: "Pod", Namespace: "default", Name: "fwd-a", UID: "fwd-a-uid"}, ep.TargetRef,
		"GKE's NEG controller needs the pod, in the Service's namespace")
	require.Len(t, slice.Ports, 1)
	assert.Equal(t, "http", *slice.Ports[0].Name)
	assert.Equal(t, int32(8080), *slice.Ports[0].Port)
	assert.JSONEq(t, `{"http":`+itoa(route.DoormanPort)+`}`, slice.Annotations[annotationDoormanPorts])
	cond = wakeCondition(t, w)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, "DoormanRouted", cond.Reason)
}

func itoa(n int32) string {
	b, _ := json.Marshal(n) // an int always marshals
	return string(b)
}

// With the forwarder off, as by default, a Service behind NEGs is left
// unrouted, since pointing it at the doorman's pods could stop GKE syncing
// its load balancer.
func TestForwarder_OffLeavesNEGServicesUnrouted(t *testing.T) {
	w := pausedAPI()
	r := doormanReconciler(t, w, doormanEndpoints("10.0.0.7"), negService("api"))

	require.NoError(t, doormanErr(context.Background(), r, w, appTarget()))

	assert.Empty(t, doormanSlices(t, r))
	assert.Equal(t, "UnsupportedLoadBalancer", wakeCondition(t, w).Reason)
	assert.True(t, noForwarder(t, r))
}

// A workload with a plain Service besides its NEG-backed one wakes for the
// plain one at once; the condition says the other waits for the forwarder.
func TestForwarder_LeavesOtherServicesRoutedToTheDoorman(t *testing.T) {
	w := pausedAPI()
	r := forwarderReconciler(t, w, doormanEndpoints("10.0.0.7"), negService("api-gclb"),
		service("api", map[string]string{"app": "api"}, corev1.ServicePort{Name: "http", Port: 80}))

	require.NoError(t, doormanErr(context.Background(), r, w, appTarget()))

	got := doormanSlices(t, r)
	require.Len(t, got, 1)
	assert.Equal(t, "api", got[0].Labels[discoveryv1.LabelServiceName])
	assert.Equal(t, []string{"10.0.0.7"}, got[0].Endpoints[0].Addresses, "straight to the doorman")
	assert.False(t, routeOf(t, w, "api").Forwarded)
	assert.True(t, routeOf(t, w, "api-gclb").Forwarded)
	cond := wakeCondition(t, w)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Contains(t, cond.Message, "not routed until the forwarder is Ready")
	assert.Contains(t, cond.Message, "api-gclb")
}

func TestForwarder_ListensOnTheTargetPort(t *testing.T) {
	tests := []struct {
		name       string
		port       corev1.ServicePort
		containers []corev1.Container
		wantListen int32
		wantSysctl bool
	}{
		{
			name:       "a numbered target port",
			port:       corev1.ServicePort{Name: "http", Port: 80, TargetPort: intstr.FromInt32(8080)},
			wantListen: 8080,
		},
		{
			name:       "no target port, so the Service's port",
			port:       corev1.ServicePort{Name: "http", Port: 8000},
			wantListen: 8000,
		},
		{
			name: "a named target port",
			port: corev1.ServicePort{Name: "http", Port: 80, TargetPort: intstr.FromString("web")},
			containers: []corev1.Container{{Name: "app", Ports: []corev1.ContainerPort{
				{Name: "metrics", ContainerPort: 9090}, {Name: "web", ContainerPort: 3000},
			}}},
			wantListen: 3000,
		},
		{
			name:       "a privileged port",
			port:       corev1.ServicePort{Name: "http", Port: 80, TargetPort: intstr.FromInt32(80)},
			wantListen: 80,
			wantSysctl: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := pausedAPI()
			r := forwarderReconciler(t, w, doormanEndpoints("10.0.0.7"), negService("api", tt.port))
			target := appTarget()
			target.Spec.Template.Spec.Containers = tt.containers

			require.NoError(t, doormanErr(context.Background(), r, w, target))

			d := forwarderOf(t, r)
			want := forwarder.FormatRoutes(map[int32]int32{tt.wantListen: routeOf(t, w, "api").DoormanPort})
			assert.Contains(t, d.Spec.Template.Spec.Containers[0].Args, "--forward="+want)
			hasSysctl := slices.ContainsFunc(d.Spec.Template.Spec.SecurityContext.Sysctls,
				func(s corev1.Sysctl) bool { return s.Name == sysctlUnprivilegedPorts && s.Value == "0" })
			assert.Equal(t, tt.wantSysctl, hasSysctl, "a non-root forwarder can only listen below 1024 with it")
		})
	}
}

func TestForwarder_ProbesAPortTheWorkloadDoesntServe(t *testing.T) {
	w := pausedAPI()
	r := forwarderReconciler(t, w, doormanEndpoints("10.0.0.7"),
		negService("api", corev1.ServicePort{Name: "http", Port: 80, TargetPort: intstr.FromInt32(8081)}))

	require.NoError(t, doormanErr(context.Background(), r, w, appTarget()))

	c := forwarderOf(t, r).Spec.Template.Spec.Containers[0]
	assert.Contains(t, c.Args, "--health-probe-bind-address=:8082")
	assert.Equal(t, int32(8082), c.Ports[0].ContainerPort)
}

func TestForwarder_SaysWhyItIsntReady(t *testing.T) {
	tests := []struct {
		name  string
		upset func(t *testing.T, r *Reconciler)
		want  string
	}{
		{
			name: "its image can't be pulled",
			upset: func(t *testing.T, r *Reconciler) {
				pod := forwarderPodFor(t, r, "fwd-a", "10.244.2.5", false)
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: forwarderContainer, State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "pull access denied"},
				}}}
				require.NoError(t, r.Status().Update(context.Background(), pod))
			},
			want: "pod fwd-a is ImagePullBackOff: pull access denied",
		},
		{
			name: "its pods can't be created",
			upset: func(t *testing.T, r *Reconciler) {
				d := forwarderOf(t, r)
				d.Status.Conditions = []appsv1.DeploymentCondition{{Type: appsv1.DeploymentReplicaFailure,
					Status: corev1.ConditionTrue, Message: `pods "napwake-forwarder-api-x" is forbidden: exceeded quota`}}
				require.NoError(t, r.Status().Update(context.Background(), d))
			},
			want: "can't create its pods: pods \"napwake-forwarder-api-x\" is forbidden: exceeded quota",
		},
		{
			name: "it can't reach the doorman",
			upset: func(t *testing.T, r *Reconciler) {
				pod := forwarderPodFor(t, r, "fwd-a", "10.244.2.5", false)
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: forwarderContainer, State: corev1.ContainerState{
					Running: &corev1.ContainerStateRunning{},
				}}}
				require.NoError(t, r.Status().Update(context.Background(), pod))
			},
			want: "pod fwd-a can't reach the doorman yet",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := pausedAPI()
			r := forwarderReconciler(t, w, doormanEndpoints("10.0.0.7"), negService("api"))
			require.NoError(t, doormanErr(context.Background(), r, w, appTarget()))
			tt.upset(t, r)

			require.NoError(t, doormanErr(context.Background(), r, w, appTarget()))

			cond := wakeCondition(t, w)
			assert.Equal(t, reasonForwarderNotReady, cond.Reason)
			assert.Contains(t, cond.Message, tt.want)
		})
	}
}

// After a change of ports, a pod still running the old spec would listen
// on, or pass connections to, the wrong ports, so the Service waits for one
// that runs the new.
func TestForwarder_LeavesOutPodsOfAnOldSpec(t *testing.T) {
	w := pausedAPI()
	r := forwarderReconciler(t, w, doormanEndpoints("10.0.0.7"), negService("api"))
	ctx := context.Background()
	require.NoError(t, doormanErr(ctx, r, w, appTarget()))
	old := forwarderPodFor(t, r, "fwd-old", "10.244.2.5", true)
	old.Spec.Containers[0].Args = []string{"--forwarder", "--forward=8080=29999"}
	require.NoError(t, r.Update(ctx, old))

	require.NoError(t, doormanErr(ctx, r, w, appTarget()))

	assert.Empty(t, doormanSlices(t, r))
	assert.Equal(t, reasonForwarderNotReady, wakeCondition(t, w).Reason)
}

// The forwarder is written once, not on every reconcile, and again when
// what it should run changes.
func TestForwarder_WritesItsSpecOnlyWhenItChanges(t *testing.T) {
	w := pausedAPI()
	r := forwarderReconciler(t, w, doormanEndpoints("10.0.0.7"), negService("api"))
	ctx := context.Background()
	require.NoError(t, doormanErr(ctx, r, w, appTarget()))
	first := forwarderOf(t, r).ResourceVersion

	require.NoError(t, doormanErr(ctx, r, w, appTarget()))
	assert.Equal(t, first, forwarderOf(t, r).ResourceVersion, "nothing changed")

	r.NEGForwarder.Replicas = 3
	require.NoError(t, doormanErr(ctx, r, w, appTarget()))
	d := forwarderOf(t, r)
	assert.NotEqual(t, first, d.ResourceVersion)
	assert.Equal(t, int32(3), *d.Spec.Replicas)
}

// Until a forwarder pod is Ready, no slice holds the forwarded routes'
// ports. A reconcile that reads the workload from before its routes were
// written, as from a cache that hasn't caught up, keeps them, rather than
// rolling the forwarder out to new ones.
func TestForwarder_KeepsItsPortsThroughAStaleRead(t *testing.T) {
	w := pausedAPI()
	r := forwarderReconciler(t, w, doormanEndpoints("10.0.0.7"), negService("api"))
	next := int32(21000)
	r.doormanPorts.pick = func(used func(int32) bool) (int32, error) {
		for used(next) {
			next++
		}
		next++
		return next - 1, nil
	}
	ctx := context.Background()
	require.NoError(t, doormanErr(ctx, r, w, appTarget()))
	first := routeOf(t, w, "api").DoormanPort
	written := forwarderOf(t, r).ResourceVersion

	stale := pausedAPI()
	require.NoError(t, doormanErr(ctx, r, stale, appTarget()))

	assert.Equal(t, first, routeOf(t, stale, "api").DoormanPort)
	assert.Equal(t, written, forwarderOf(t, r).ResourceVersion, "rolled out to a new port")
}

func TestForwarder_LeavesADeploymentThatIsntItsOwn(t *testing.T) {
	w := pausedAPI()
	theirs := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "napwake-forwarder-api", Namespace: "default"},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr.To(int32(5))},
	}
	r := forwarderReconciler(t, w, doormanEndpoints("10.0.0.7"), negService("api"), theirs,
		service("api-plain", map[string]string{"app": "api"}, corev1.ServicePort{Name: "http", Port: 80}))

	err := doormanErr(context.Background(), r, w, appTarget())

	require.ErrorContains(t, err, "isn't this workload's forwarder")
	assert.Equal(t, int32(5), *forwarderOf(t, r).Spec.Replicas, "never touched")
	got := doormanSlices(t, r)
	require.Len(t, got, 1, "the workload's other Services are routed anyway")
	assert.Equal(t, "api-plain", got[0].Labels[discoveryv1.LabelServiceName])
}

// Once the workload is awake, its forwarder stays until its routes have
// drained, since load balancers that haven't caught up still send it
// connections, and then goes.
func TestForwarder_RetiresOnceTheWorkloadsRoutesHaveDrained(t *testing.T) {
	w := pausedAPI()
	r := forwarderReconciler(t, w, doormanEndpoints("10.0.0.7"), negService("api"))
	ctx := context.Background()
	require.NoError(t, doormanErr(ctx, r, w, appTarget()))
	forwarderPodFor(t, r, "fwd-a", "10.244.2.5", true)
	require.NoError(t, doormanErr(ctx, r, w, appTarget()))
	require.Len(t, doormanSlices(t, r), 1)
	eventsOf(r)

	w.Status.Phase = v1alpha1.PhaseRunning
	recheck, err := r.reconcileDoorman(ctx, w, appTarget())

	require.NoError(t, err)
	assert.Empty(t, doormanSlices(t, r), "the Service goes straight to the workload's pods")
	assert.False(t, noForwarder(t, r), "kept while its routes drain")
	assert.Equal(t, portDrain, recheck, "looked at again when they've drained")

	r.clock = func() time.Time { return fixedTime.Add(portDrain) }
	recheck, err = r.reconcileDoorman(ctx, w, appTarget())

	require.NoError(t, err)
	assert.True(t, noForwarder(t, r))
	assert.Zero(t, recheck)
	assert.True(t, slices.ContainsFunc(eventsOf(r), func(e string) bool { return strings.Contains(e, ReasonForwarderStopped) }))
}

// While a woken workload resumes, its own Ready pods take its Service back
// before it's Running. Load balancers still send the forwarder connections
// until they catch up, so it stays until the route has drained.
func TestForwarder_OutlastsTheRouteWhileTheWorkloadResumes(t *testing.T) {
	w := pausedAPI()
	r := forwarderReconciler(t, w, doormanEndpoints("10.0.0.7"), negService("api"))
	ctx := context.Background()
	require.NoError(t, doormanErr(ctx, r, w, appTarget()))
	forwarderPodFor(t, r, "fwd-a", "10.244.2.5", true)
	require.NoError(t, doormanErr(ctx, r, w, appTarget()))
	require.Len(t, doormanSlices(t, r), 1)
	require.NoError(t, r.Create(ctx, &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "api-abcde", Namespace: "default", Labels: map[string]string{
			discoveryv1.LabelServiceName: "api",
			discoveryv1.LabelManagedBy:   "endpointslice-controller.k8s.io",
		}},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{"10.244.1.9"},
			Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(true)},
			TargetRef:  &corev1.ObjectReference{Kind: "Pod", Namespace: "default", Name: "api-0"},
		}},
	}))
	require.NoError(t, r.Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api-0", Namespace: "default"},
		Status:     corev1.PodStatus{PodIP: "10.244.1.9", PodIPs: []corev1.PodIP{{IP: "10.244.1.9"}}},
	}))

	w.Status.Phase = v1alpha1.PhaseResuming
	recheck, err := r.reconcileDoorman(ctx, w, appTarget())

	require.NoError(t, err)
	assert.Empty(t, doormanSlices(t, r), "the Service goes to the workload's own Ready pod")
	assert.False(t, noForwarder(t, r), "removed before its route drained")
	assert.Positive(t, recheck)
	assert.LessOrEqual(t, recheck, portDrain, "looked at again by the time it has drained")

	r.clock = func() time.Time { return fixedTime.Add(portDrain) }
	_, err = r.reconcileDoorman(ctx, w, appTarget())

	require.NoError(t, err)
	assert.True(t, noForwarder(t, r))
}

// A doorman port a forwarded slice still sends traffic to through the
// forwarder must never go to another workload, even when the status that
// holds it was lost; the slice's own ports are the forwarder's, so it
// records the doorman's.
func TestForwarder_SlicesKeepTheirDoormanPortsClaimed(t *testing.T) {
	w := pausedAPI()
	r := forwarderReconciler(t, w, doormanEndpoints("10.0.0.7"), negService("api"))
	ctx := context.Background()
	require.NoError(t, doormanErr(ctx, r, w, appTarget()))
	forwarderPodFor(t, r, "fwd-a", "10.244.2.5", true)
	require.NoError(t, doormanErr(ctx, r, w, appTarget()))
	port := routeOf(t, w, "api").DoormanPort
	slice := doormanSlices(t, r)[0]

	var fresh portAllocator
	fresh.merge(portsInUse{slices: []discoveryv1.EndpointSlice{slice}}, fixedTime)

	assert.Contains(t, fresh.claims, port, "claimed for the workload from its slice")
	assert.NotContains(t, fresh.claims, int32(8080), "the forwarder's port is no doorman port")
	assert.Equal(t, map[string]int32{"http": port}, doormanPortsOf(&slice))
}

// The doorman's pods moving never moves a Service that points at a
// forwarder: its pods aren't the doorman's.
func TestDoormanSliceSync_LeavesForwardedSlices(t *testing.T) {
	w := pausedAPI()
	r := forwarderReconciler(t, w, doormanEndpoints("10.0.0.7"), negService("api"))
	ctx := context.Background()
	require.NoError(t, doormanErr(ctx, r, w, appTarget()))
	forwarderPodFor(t, r, "fwd-a", "10.244.2.5", true)
	require.NoError(t, doormanErr(ctx, r, w, appTarget()))
	sync := &doormanSliceSync{reader: r.Client, writer: r.Client, namespace: r.DoormanNamespace, service: r.DoormanService}

	replaceDoormanPods(t, r, "10.0.0.8")
	_, err := sync.Reconcile(ctx, reconcile.Request{})

	require.NoError(t, err)
	assert.Equal(t, []string{"10.244.2.5"}, doormanSlices(t, r)[0].Endpoints[0].Addresses)
}

func TestForwarderName(t *testing.T) {
	assert.Equal(t, "napwake-forwarder-api", forwarderName("api"))
	long := forwarderName(strings.Repeat("a", 60))
	assert.LessOrEqual(t, len(long), 63)
	assert.NotEqual(t, long, forwarderName(strings.Repeat("a", 61)), "names that share a prefix stay apart")
}
