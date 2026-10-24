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
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/doorman"
	"github.com/napwake/napwake/internal/forwarder"
)

// A Service behind GKE container-native load balancing can't be pointed at
// the doorman's pods: GKE's NEG controller only accepts endpoints whose pods
// are in the Service's namespace. With ForwarderOptions set, the operator
// runs a forwarder Deployment beside each paused workload that has such a
// Service, and points the Service at the forwarder's pods instead, which
// pass every connection to the doorman.
//
// Each forwarder pod listens on the target ports the workload's own pods
// serve, since those are the ports GKE's firewall rules let Google's load
// balancers reach. Two workloads in one namespace can serve the same port,
// so one forwarder serves one workload.
//
// No kubebuilder marker grants creating and deleting Deployments: only an
// install that turns forwarders on needs it, and the Helm chart grants it
// then, in a role of its own.

const (
	ReasonForwarderStarted = "ForwarderStarted"
	ReasonForwarderStopped = "ForwarderStopped"

	reasonForwarderNotReady = "ForwarderNotReady"

	// labelForwarded marks a doorman EndpointSlice that points a Service at
	// its workload's forwarder rather than at the doorman.
	labelForwarded = "napwake.dev/forwarded"

	// annotationDoormanPorts, on a forwarded slice, maps each Service port's
	// name to its doorman port, as JSON. The slice's own ports are the
	// forwarder's, which aren't the doorman's, and a doorman port a slice
	// still sends traffic to must never go to another workload.
	annotationDoormanPorts = "napwake.dev/doorman-ports"

	// annotationForwarderSpec, on a forwarder Deployment, is a hash of what
	// the operator last wrote, so it's written again only when that
	// changes, not on every reconcile for the API server's defaults.
	annotationForwarderSpec = "napwake.dev/forwarder-spec"

	// annotationForwarderRoutes, on a forwarder Deployment, is the routes it
	// passes connections on for, as JSON (see forwarderDeployment).
	annotationForwarderRoutes = "napwake.dev/doorman-routes"

	labelAppName       = "app.kubernetes.io/name"
	forwarderAppName   = "napwake-forwarder"
	forwarderPrefix    = forwarderAppName + "-"
	forwarderContainer = "forwarder"

	// forwarderProbePort is the first port tried for the forwarder's health
	// probes; the next free one is used when the workload serves it.
	forwarderProbePort = 8081

	// forwarderGraceSeconds covers the 15 seconds a stopping forwarder goes
	// on accepting connections and the 20 it gives them to finish, with
	// room to exit.
	forwarderGraceSeconds = 40

	// sysctlUnprivilegedPorts lets the forwarder, which never runs as root,
	// listen on a port below 1024, as a workload serving port 80 needs.
	// Pod Security's restricted level allows it.
	sysctlUnprivilegedPorts = "net.ipv4.ip_unprivileged_port_start"

	envMemoryLimit = "MEMORY_LIMIT"

	// istioInject, as a label and an annotation, keeps Istio's sidecar out
	// of a pod.
	istioInject = "sidecar.istio.io/inject"

	// forwarderRecheckInterval is how soon a workload whose forwarder has
	// no Ready pod is reconciled again, besides whenever the forwarder's
	// status changes.
	forwarderRecheckInterval = 30 * time.Second
)

// ForwarderOptions configure the forwarders that route Services behind GKE
// container-native load balancing to the doorman.
type ForwarderOptions struct {
	// Image is the operator's own image, which runs a forwarder with
	// --forwarder.
	Image string

	// Replicas is how many pods each forwarder runs.
	Replicas int32

	// Resources are each forwarder pod's requests and limits.
	Resources corev1.ResourceRequirements

	// Doorman is the DNS name that resolves to the doorman's Ready pods: a
	// headless Service's.
	Doorman string
}

// forwarding is how a workload's Services behind GKE container-native load
// balancing reach the doorman.
type forwarding struct {
	// endpoints are the forwarder's Ready pods, by address family.
	endpoints map[discoveryv1.AddressType][]discoveryv1.Endpoint

	// ports are the port a forwarded Service port's slice gives: the target
	// port the workload's pods serve it on, where the forwarder listens.
	ports map[routeKey]int32

	// unready says why no forwarder pod is Ready, while none is.
	unready string

	// recheck is how soon to look again, to retire a forwarder that's no
	// longer needed once its routes have drained.
	recheck time.Duration
}

// reconcileForwarder runs a forwarder for the workload while any of its
// routed Services is behind GKE container-native load balancing, marking
// those Services' routes forwarded, and retires it otherwise, once the
// routes it served have drained, which recordDrains must have recorded by
// then. A forwarder that can't be run only keeps those Services from being
// routed: its error is returned for the workload's other Services to be
// routed anyway.
func (r *Reconciler) reconcileForwarder(ctx context.Context, workload *v1alpha1.ManagedWorkload, target client.Object,
	services []doormanService, routes []v1alpha1.DoormanRoute) (forwarding, error) {
	fwd := forwarding{ports: map[routeKey]int32{}}
	forwarded := map[string]corev1.Service{}
	for _, s := range services {
		if s.forwarded {
			forwarded[s.svc.Name] = s.svc
		}
	}
	if len(forwarded) == 0 {
		fwd.recheck = r.forwarderRecheck(workload)
		return fwd, r.retireForwarder(ctx, workload)
	}

	spec := podTemplateOf(target).Spec
	listen := map[int32]int32{}
	var forwardedRoutes []v1alpha1.DoormanRoute
	for i := range routes {
		svc, ok := forwarded[routes[i].Service]
		if !ok {
			continue
		}
		routes[i].Forwarded = true
		forwardedRoutes = append(forwardedRoutes, routes[i])
		port, ok := targetPort(svc, routes[i].PortName, spec)
		if !ok {
			continue
		}
		fwd.ports[routeKey{service: routes[i].Service, portName: routes[i].PortName}] = port
		if _, taken := listen[port]; !taken {
			listen[port] = routes[i].DoormanPort
		}
	}
	if len(listen) == 0 {
		fwd.unready = "none of their target ports is a port the workload's pods serve"
		fwd.recheck = r.forwarderRecheck(workload)
		return fwd, r.retireForwarder(ctx, workload)
	}

	want, err := r.forwarderDeployment(workload, listen, forwardedRoutes)
	if err != nil {
		return fwd, err
	}
	deploy, err := r.applyForwarder(ctx, workload, want)
	if err != nil {
		fwd.unready = "it can't be run: " + err.Error()
		return fwd, err
	}
	fwd.endpoints, fwd.unready, err = r.forwarderEndpoints(ctx, deploy, want)
	return fwd, err
}

// targetPort is the port the workload's pods serve the Service's named port
// on: its number, or for a named target port, the container port of that
// name in the workload's pod template, as the EndpointSlice controller
// resolves it.
func targetPort(svc corev1.Service, portName string, spec corev1.PodSpec) (int32, bool) {
	i := slices.IndexFunc(svc.Spec.Ports, func(p corev1.ServicePort) bool { return p.Name == portName })
	if i < 0 {
		return 0, false
	}
	port := svc.Spec.Ports[i]
	if port.TargetPort.Type == intstr.String && port.TargetPort.StrVal != "" {
		for _, c := range spec.Containers {
			for _, p := range c.Ports {
				if p.Name == port.TargetPort.StrVal && (p.Protocol == "" || p.Protocol == corev1.ProtocolTCP) {
					return p.ContainerPort, true
				}
			}
		}
		return 0, false
	}
	if port.TargetPort.IntVal > 0 {
		return port.TargetPort.IntVal, true
	}
	return port.Port, true
}

// forwarderName is the workload's forwarder Deployment's name, cut down
// with a hash for a long workload name, as WorkloadLabel cuts it.
func forwarderName(workload string) string {
	name := forwarderPrefix + workload
	if len(name) <= 63 {
		return name
	}
	sum := sha256.Sum256([]byte(workload))
	return strings.TrimRight(name[:52], "-.") + "-" + hex.EncodeToString(sum[:5])
}

// forwarderSelector picks the workload's forwarder's pods.
func forwarderSelector(workload *v1alpha1.ManagedWorkload) map[string]string {
	return map[string]string{
		labelAppName:                 forwarderAppName,
		doorman.LabelManagedWorkload: doorman.WorkloadLabel(workload.Name),
	}
}

// forwarderDeployment is the forwarder the workload's listen ports need,
// each passed to its doorman port, for routes.
//
// It records routes in napwake.dev/doorman-routes: until a forwarder pod
// is Ready no slice holds their ports, and a reconcile reading status from
// before they were written would otherwise give them new ones, rolling the
// forwarder out again.
//
// Its pods run with no API access, as the namespace's default
// ServiceAccount without its token, so a namespace's own image pull secrets
// apply. They keep Istio's and Linkerd's sidecars out: the forwarder passes
// TCP on untouched, and a sidecar would cost far more than it does. They
// carry napwake.dev/operator, which the chart's doorman NetworkPolicy
// admits them by, and the Deployment carries napwake.dev/ignore, so a
// namespace opted in with its label doesn't have Napwake manage it.
func (r *Reconciler) forwarderDeployment(workload *v1alpha1.ManagedWorkload, listen map[int32]int32,
	routes []v1alpha1.DoormanRoute) (*appsv1.Deployment, error) {
	opts := r.NEGForwarder
	selector := forwarderSelector(workload)
	podLabels := maps.Clone(selector)
	if r.OperatorNamespace != "" {
		podLabels[v1alpha1.LabelOperator] = r.OperatorNamespace
	}
	labels := maps.Clone(podLabels)
	labels[v1alpha1.LabelIgnore] = v1alpha1.True
	noSidecars := map[string]string{istioInject: "false", "linkerd.io/inject": "disabled"}
	podLabels[istioInject] = noSidecars[istioInject]

	ports := slices.Sorted(maps.Keys(listen))
	probe := int32(forwarderProbePort)
	for slices.Contains(ports, probe) {
		probe++
	}
	containerPorts := make([]corev1.ContainerPort, 0, 1+len(ports))
	containerPorts = append(containerPorts, corev1.ContainerPort{Name: "probes", ContainerPort: probe, Protocol: corev1.ProtocolTCP})
	for _, p := range ports {
		containerPorts = append(containerPorts, corev1.ContainerPort{ContainerPort: p, Protocol: corev1.ProtocolTCP})
	}
	security := &corev1.PodSecurityContext{
		RunAsNonRoot:   ptr.To(true),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	if ports[0] < 1024 {
		security.Sysctls = []corev1.Sysctl{{Name: sysctlUnprivilegedPorts, Value: "0"}}
	}
	var env []corev1.EnvVar
	if _, ok := opts.Resources.Limits[corev1.ResourceMemory]; ok {
		env = []corev1.EnvVar{{Name: envMemoryLimit, ValueFrom: &corev1.EnvVarSource{
			ResourceFieldRef: &corev1.ResourceFieldSelector{Resource: "limits.memory"},
		}}}
	}
	probeOn := func(path string) corev1.ProbeHandler {
		return corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: path, Port: intstr.FromString("probes")}}
	}

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      forwarderName(workload.Name),
			Namespace: workload.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(opts.Replicas),
			Selector: &metav1.LabelSelector{MatchLabels: selector},
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxSurge:       ptr.To(intstr.FromInt32(1)),
					MaxUnavailable: ptr.To(intstr.FromInt32(0)),
				},
			},
			RevisionHistoryLimit: ptr.To(int32(0)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels, Annotations: noSidecars},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken:  ptr.To(false),
					EnableServiceLinks:            ptr.To(false),
					TerminationGracePeriodSeconds: ptr.To(int64(forwarderGraceSeconds)),
					SecurityContext:               security,
					TopologySpreadConstraints: []corev1.TopologySpreadConstraint{{
						MaxSkew:           1,
						TopologyKey:       corev1.LabelHostname,
						WhenUnsatisfiable: corev1.ScheduleAnyway,
						LabelSelector:     &metav1.LabelSelector{MatchLabels: selector},
					}},
					Containers: []corev1.Container{{
						Name:            forwarderContainer,
						Image:           opts.Image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Command:         []string{"/manager"},
						Args: []string{
							"--forwarder",
							"--forward=" + forwarder.FormatRoutes(listen),
							"--forwarder-doorman=" + opts.Doorman,
							"--health-probe-bind-address=:" + strconv.Itoa(int(probe)),
						},
						Ports:     containerPorts,
						Env:       env,
						Resources: opts.Resources,
						ReadinessProbe: &corev1.Probe{
							ProbeHandler:     probeOn("/readyz"),
							PeriodSeconds:    2,
							FailureThreshold: 2,
						},
						LivenessProbe: &corev1.Probe{ProbeHandler: probeOn("/healthz"), PeriodSeconds: 20},
						SecurityContext: &corev1.SecurityContext{
							ReadOnlyRootFilesystem:   ptr.To(true),
							AllowPrivilegeEscalation: ptr.To(false),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
					}},
				},
			},
		},
	}
	recorded, err := json.Marshal(routes)
	if err != nil {
		return nil, fmt.Errorf("recording the forwarder's routes: %w", err)
	}
	written, err := json.Marshal(struct {
		Labels map[string]string
		Spec   appsv1.DeploymentSpec
		Routes string
	}{deploy.Labels, deploy.Spec, string(recorded)})
	if err != nil {
		return nil, fmt.Errorf("hashing the forwarder's spec: %w", err)
	}
	sum := sha256.Sum256(written)
	deploy.Annotations = map[string]string{
		annotationForwarderSpec:   hex.EncodeToString(sum[:8]),
		annotationForwarderRoutes: string(recorded),
	}
	if err := controllerutil.SetControllerReference(workload, deploy, r.Scheme); err != nil {
		return nil, fmt.Errorf("owning the forwarder: %w", err)
	}
	return deploy, nil
}

// applyForwarder creates the workload's forwarder, or brings it up to date
// with want. A Deployment of the forwarder's name that isn't this
// workload's is never touched.
func (r *Reconciler) applyForwarder(ctx context.Context, workload *v1alpha1.ManagedWorkload,
	want *appsv1.Deployment) (*appsv1.Deployment, error) {
	var have appsv1.Deployment
	err := r.Get(ctx, client.ObjectKeyFromObject(want), &have)
	if apierrors.IsNotFound(err) {
		err = r.Create(ctx, want)
		if err == nil {
			r.emitEvent(workload, false, "Normal", ReasonForwarderStarted, actionRouteDoorman,
				"started Deployment %s, which GKE's load balancer reaches the doorman through while the workload is paused",
				want.Name)
			return want, nil
		}
		if !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("creating forwarder deployment %s: %w", want.Name, err)
		}
		// Created moments ago, and not in the cache yet.
		err = r.apiReader().Get(ctx, client.ObjectKeyFromObject(want), &have)
	}
	if err != nil {
		return nil, fmt.Errorf("reading forwarder deployment %s: %w", want.Name, err)
	}
	if !metav1.IsControlledBy(&have, workload) {
		return nil, fmt.Errorf("deployment %s exists and isn't this workload's forwarder", want.Name)
	}
	if have.Annotations[annotationForwarderSpec] == want.Annotations[annotationForwarderSpec] {
		return &have, nil
	}
	patch := client.MergeFrom(have.DeepCopy())
	for k, v := range want.Labels {
		metav1.SetMetaDataLabel(&have.ObjectMeta, k, v)
	}
	for k, v := range want.Annotations {
		metav1.SetMetaDataAnnotation(&have.ObjectMeta, k, v)
	}
	have.Spec.Replicas = want.Spec.Replicas
	have.Spec.Strategy = want.Spec.Strategy
	have.Spec.RevisionHistoryLimit = want.Spec.RevisionHistoryLimit
	have.Spec.Template = want.Spec.Template
	if err := r.Patch(ctx, &have, patch); err != nil {
		return nil, fmt.Errorf("updating forwarder deployment %s: %w", want.Name, err)
	}
	return &have, nil
}

// forwarderEndpoints are an endpoint for each Ready pod of the forwarder
// that runs want's spec, by address family, or why there are none. A pod
// left from before a change of ports would listen on, or pass connections
// to, the wrong ones, so it isn't one. Each endpoint names its pod and its
// node, which GKE's NEG controller requires. The pods are read from the API
// server, since the operator's cache keeps too little of a pod.
func (r *Reconciler) forwarderEndpoints(ctx context.Context, deploy, want *appsv1.Deployment) (
	map[discoveryv1.AddressType][]discoveryv1.Endpoint, string, error) {
	ctx, cancel := context.WithTimeout(ctx, endpointsReadTimeout)
	defer cancel()
	var pods corev1.PodList
	if err := r.apiReader().List(ctx, &pods, client.InNamespace(deploy.Namespace),
		client.MatchingLabels(deploy.Spec.Selector.MatchLabels)); err != nil {
		return nil, "", fmt.Errorf("listing forwarder pods: %w", err)
	}
	args := want.Spec.Template.Spec.Containers[0].Args
	out := map[discoveryv1.AddressType][]discoveryv1.Endpoint{}
	var why string
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.DeletionTimestamp != nil || len(pod.Spec.Containers) == 0 || !slices.Equal(pod.Spec.Containers[0].Args, args) {
			continue
		}
		if !podReady(pod) {
			why = cmp.Or(why, podNotReady(pod))
			continue
		}
		zone := r.nodeZone(ctx, pod.Spec.NodeName)
		for _, ip := range pod.Status.PodIPs {
			addr, err := netip.ParseAddr(ip.IP)
			if err != nil {
				continue
			}
			family := discoveryv1.AddressTypeIPv4
			if !addr.Unmap().Is4() {
				family = discoveryv1.AddressTypeIPv6
			}
			out[family] = append(out[family], discoveryv1.Endpoint{
				Addresses:  []string{ip.IP},
				Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(true)},
				NodeName:   ptr.To(pod.Spec.NodeName),
				Zone:       zone,
				TargetRef:  &corev1.ObjectReference{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID},
			})
		}
	}
	for _, eps := range out {
		slices.SortFunc(eps, func(a, b discoveryv1.Endpoint) int { return cmp.Compare(a.Addresses[0], b.Addresses[0]) })
	}
	if len(out) > 0 {
		return out, "", nil
	}
	return nil, forwarderNotReady(deploy, why), nil
}

func podReady(pod *corev1.Pod) bool {
	return slices.ContainsFunc(pod.Status.Conditions, func(c corev1.PodCondition) bool {
		return c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue
	})
}

// podNotReady says why a forwarder pod isn't Ready, as far as its status
// tells.
func podNotReady(pod *corev1.Pod) string {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
			return fmt.Sprintf("pod %s can't be scheduled: %s", pod.Name, c.Message)
		}
	}
	for _, s := range pod.Status.ContainerStatuses {
		if w := s.State.Waiting; w != nil && w.Reason != "" && w.Reason != "ContainerCreating" {
			return strings.TrimSuffix(fmt.Sprintf("pod %s is %s: %s", pod.Name, w.Reason, w.Message), ": ")
		}
		if s.State.Running != nil {
			return fmt.Sprintf("pod %s can't reach the doorman yet", pod.Name)
		}
	}
	return ""
}

// forwarderNotReady says why the forwarder has no Ready pod.
func forwarderNotReady(deploy *appsv1.Deployment, why string) string {
	for _, c := range deploy.Status.Conditions {
		if c.Type == appsv1.DeploymentReplicaFailure && c.Status == corev1.ConditionTrue {
			return fmt.Sprintf("Deployment %s can't create its pods: %s", deploy.Name, c.Message)
		}
	}
	if why == "" {
		why = "its pods are starting"
	}
	return fmt.Sprintf("Deployment %s has no Ready pod: %s", deploy.Name, why)
}

// nodeZone is the node's zone, from the cached node, or nil if it isn't
// known.
func (r *Reconciler) nodeZone(ctx context.Context, name string) *string {
	var node corev1.Node
	if err := r.Get(ctx, types.NamespacedName{Name: name}, &node); err != nil {
		return nil
	}
	if zone, ok := node.Labels[corev1.LabelTopologyZone]; ok {
		return &zone
	}
	return nil
}

// retireForwarder deletes the workload's forwarder once its routes have
// drained: until then, load balancers that haven't caught up may still send
// it connections, which it passes to the doorman's draining routes.
func (r *Reconciler) retireForwarder(ctx context.Context, workload *v1alpha1.ManagedWorkload) error {
	if r.NEGForwarder == nil || r.drainLeft(workload) > 0 {
		return nil
	}
	var deploy appsv1.Deployment
	key := types.NamespacedName{Namespace: workload.Namespace, Name: forwarderName(workload.Name)}
	if err := r.Get(ctx, key, &deploy); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("reading forwarder deployment %s: %w", key.Name, err)
	}
	if !metav1.IsControlledBy(&deploy, workload) {
		return nil
	}
	if err := r.Delete(ctx, &deploy); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("deleting forwarder deployment %s: %w", key.Name, err)
	}
	r.emitEvent(workload, false, "Normal", ReasonForwarderStopped, actionRouteDoorman,
		"removed Deployment %s, the forwarder GKE's load balancer reached the doorman through", key.Name)
	return nil
}

// forwarderRoutes are the routes the workload's forwarder was last written
// for, from the cache.
func (r *Reconciler) forwarderRoutes(ctx context.Context, workload *v1alpha1.ManagedWorkload) []v1alpha1.DoormanRoute {
	if r.NEGForwarder == nil {
		return nil
	}
	var deploy appsv1.Deployment
	key := types.NamespacedName{Namespace: workload.Namespace, Name: forwarderName(workload.Name)}
	if r.Get(ctx, key, &deploy) != nil || !metav1.IsControlledBy(&deploy, workload) {
		return nil
	}
	var routes []v1alpha1.DoormanRoute
	_ = json.Unmarshal([]byte(deploy.Annotations[annotationForwarderRoutes]), &routes) // without it, it records none
	return routes
}

// forwarderRecheck is how soon a workload no longer routed should be
// reconciled again, to retire its forwarder once its routes have drained.
func (r *Reconciler) forwarderRecheck(workload *v1alpha1.ManagedWorkload) time.Duration {
	if r.NEGForwarder == nil {
		return 0
	}
	return r.drainLeft(workload)
}

// slicePorts are a doorman slice's ports for routes: each route's doorman
// port, or, for a forwarded Service, the port the forwarder listens on for
// it, leaving out a route it doesn't listen for.
func slicePorts(routes []v1alpha1.DoormanRoute, forwarded map[routeKey]int32) []discoveryv1.EndpointPort {
	out := make([]discoveryv1.EndpointPort, 0, len(routes))
	for _, route := range routes {
		port := route.DoormanPort
		if forwarded != nil {
			var ok bool
			if port, ok = forwarded[routeKey{service: route.Service, portName: route.PortName}]; !ok {
				continue
			}
		}
		out = append(out, discoveryv1.EndpointPort{
			Name:     ptr.To(route.PortName),
			Port:     ptr.To(port),
			Protocol: ptr.To(corev1.ProtocolTCP),
		})
	}
	return out
}

// doormanPortsOf is the doorman port a doorman slice holds for each Service
// port, by the port's name.
func doormanPortsOf(slice *discoveryv1.EndpointSlice) map[string]int32 {
	if slice.Labels[labelForwarded] == v1alpha1.True {
		var ports map[string]int32
		_ = json.Unmarshal([]byte(slice.Annotations[annotationDoormanPorts]), &ports) // without it, it holds none
		return ports
	}
	ports := make(map[string]int32, len(slice.Ports))
	for _, p := range slice.Ports {
		if p.Port != nil {
			ports[ptr.Deref(p.Name, "")] = *p.Port
		}
	}
	return ports
}

// doormanPortsAnnotation records routes' doorman ports on a forwarded slice
// (see annotationDoormanPorts).
func doormanPortsAnnotation(routes []v1alpha1.DoormanRoute) (string, error) {
	ports := make(map[string]int32, len(routes))
	for _, route := range routes {
		ports[route.PortName] = route.DoormanPort
	}
	out, err := json.Marshal(ports)
	if err != nil {
		return "", fmt.Errorf("recording doorman ports: %w", err)
	}
	return string(out), nil
}
