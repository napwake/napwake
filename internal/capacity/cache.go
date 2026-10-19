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

package capacity

import (
	"math"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"sigs.k8s.io/controller-runtime/pkg/cache"
)

// PodCache is how the operator caches pods: only those bound to a node and
// not finished, as what they request of it and nothing else. A cluster's
// pods are the largest thing an operator could cache, and spare capacity
// is all they're cached for; every other read of a pod goes to the API
// server.
func PodCache() cache.ByObject {
	return cache.ByObject{
		Field: fields.AndSelectors(
			fields.OneTermNotEqualSelector("spec.nodeName", ""),
			fields.OneTermNotEqualSelector("status.phase", string(corev1.PodSucceeded)),
			fields.OneTermNotEqualSelector("status.phase", string(corev1.PodFailed)),
		),
		Transform: trimPod,
	}
}

// NodeCache is how the operator caches nodes: their labels, which price
// them and name their pool, and what's needed to tell the capacity they
// offer pods. Their image lists, often most of their size, are dropped.
func NodeCache() cache.ByObject {
	return cache.ByObject{Transform: trimNode}
}

// trimPod keeps of a pod its node, its phase, and what it requests as the
// scheduler counts it, collapsed into one container: one map of requests a
// pod rather than one a container.
func trimPod(in any) (any, error) {
	pod, ok := in.(*corev1.Pod)
	if !ok {
		return in, nil
	}
	requests := podRequests(&pod.Spec)
	trimmed := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pod.Name,
			Namespace:       pod.Namespace,
			ResourceVersion: pod.ResourceVersion,
		},
		Spec:   corev1.PodSpec{NodeName: pod.Spec.NodeName},
		Status: corev1.PodStatus{Phase: pod.Status.Phase},
	}
	if requests.CPU > 0 || requests.Memory > 0 {
		trimmed.Spec.Containers = []corev1.Container{{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewMilliQuantity(int64(math.Round(requests.CPU*1000)), resource.DecimalSI),
			corev1.ResourceMemory: *resource.NewQuantity(int64(math.Round(requests.Memory)), resource.BinarySI),
		}}}}
	}
	return trimmed, nil
}

func trimNode(in any) (any, error) {
	node, ok := in.(*corev1.Node)
	if !ok {
		return in, nil
	}
	trimmed := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:            node.Name,
			ResourceVersion: node.ResourceVersion,
			Labels:          node.Labels,
		},
		Spec: corev1.NodeSpec{Unschedulable: node.Spec.Unschedulable},
		Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{
			corev1.ResourceCPU:    node.Status.Allocatable[corev1.ResourceCPU],
			corev1.ResourceMemory: node.Status.Allocatable[corev1.ResourceMemory],
		}},
	}
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			trimmed.Status.Conditions = []corev1.NodeCondition{{Type: c.Type, Status: c.Status}}
		}
	}
	return trimmed, nil
}

// podRequests is what a pod requests of its node as the scheduler counts
// it: its containers and the sidecars that run beside them, or the
// largest init container's request with the sidecars started before it if
// that's more, plus the runtime's overhead. Requests set for the pod as a
// whole stand in for its containers'.
func podRequests(spec *corev1.PodSpec) Resources {
	var containers, sidecars, initPeak Resources
	for _, c := range spec.Containers {
		containers = containers.add(requestsOf(c.Resources.Requests))
	}
	for _, c := range spec.InitContainers {
		r := requestsOf(c.Resources.Requests)
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			sidecars = sidecars.add(r)
			continue
		}
		initPeak = Resources{CPU: max(initPeak.CPU, r.CPU+sidecars.CPU), Memory: max(initPeak.Memory, r.Memory+sidecars.Memory)}
	}
	running := containers.add(sidecars)
	total := Resources{CPU: max(running.CPU, initPeak.CPU), Memory: max(running.Memory, initPeak.Memory)}
	if spec.Resources != nil {
		if cpu, ok := spec.Resources.Requests[corev1.ResourceCPU]; ok {
			total.CPU = cpu.AsApproximateFloat64()
		}
		if mem, ok := spec.Resources.Requests[corev1.ResourceMemory]; ok {
			total.Memory = mem.AsApproximateFloat64()
		}
	}
	return total.add(requestsOf(spec.Overhead))
}

func requestsOf(list corev1.ResourceList) Resources {
	return Resources{CPU: list.Cpu().AsApproximateFloat64(), Memory: list.Memory().AsApproximateFloat64()}
}
