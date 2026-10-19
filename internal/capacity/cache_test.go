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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func requesting(cpu, memory string) corev1.ResourceRequirements {
	return corev1.ResourceRequirements{Requests: corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(cpu),
		corev1.ResourceMemory: resource.MustParse(memory),
	}}
}

func sidecar(cpu, memory string) corev1.Container {
	return corev1.Container{Resources: requesting(cpu, memory),
		RestartPolicy: ptr.To(corev1.ContainerRestartPolicyAlways)}
}

// A pod requests of its node what the scheduler reserves for it.
func TestPodRequests(t *testing.T) {
	tests := []struct {
		name string
		spec corev1.PodSpec
		want Resources
	}{
		{name: "its containers", spec: corev1.PodSpec{Containers: []corev1.Container{
			{Resources: requesting("500m", "1Gi")}, {Resources: requesting("100m", "128Mi")}}},
			want: Resources{CPU: 0.6, Memory: 1.125 * gib}},
		{name: "and its sidecars", spec: corev1.PodSpec{
			Containers:     []corev1.Container{{Resources: requesting("500m", "1Gi")}},
			InitContainers: []corev1.Container{sidecar("100m", "64Mi")}},
			want: Resources{CPU: 0.6, Memory: (1024 + 64) << 20}},
		{name: "an init container larger than what runs after it", spec: corev1.PodSpec{
			Containers:     []corev1.Container{{Resources: requesting("500m", "1Gi")}},
			InitContainers: []corev1.Container{sidecar("100m", "64Mi"), {Resources: requesting("2", "512Mi")}}},
			want: Resources{CPU: 2.1, Memory: (1024 + 64) << 20}},
		{name: "plus the runtime's overhead", spec: corev1.PodSpec{
			Containers: []corev1.Container{{Resources: requesting("500m", "1Gi")}},
			Overhead:   requesting("250m", "160Mi").Requests},
			want: Resources{CPU: 0.75, Memory: (1024 + 160) << 20}},
		{name: "requests set for the pod as a whole", spec: corev1.PodSpec{
			Containers: []corev1.Container{{Resources: requesting("500m", "1Gi")}},
			Resources:  ptr.To(requesting("2", "4Gi"))},
			want: Resources{CPU: 2, Memory: 4 * gib}},
		{name: "nothing requested", spec: corev1.PodSpec{Containers: []corev1.Container{{}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := podRequests(&tt.spec)
			assert.InDelta(t, tt.want.CPU, got.CPU, 1e-9)
			assert.InDelta(t, tt.want.Memory, got.Memory, 1)
		})
	}
}

// What's cached of a pod still requests what the pod does, and holds
// nothing else.
func TestTrimPod(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "shop", ResourceVersion: "7",
			Labels: map[string]string{"app": "api"}, Annotations: map[string]string{"a": "b"}},
		Spec: corev1.PodSpec{
			NodeName: "node-a",
			Containers: []corev1.Container{
				{Name: "app", Image: "api:v1", Resources: requesting("500m", "1Gi"),
					Env: []corev1.EnvVar{{Name: "A", Value: "b"}}},
			},
			InitContainers: []corev1.Container{sidecar("100m", "64Mi")},
			Overhead:       requesting("10m", "16Mi").Requests,
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.1"},
	}

	out, err := trimPod(pod)
	require.NoError(t, err)
	trimmed, ok := out.(*corev1.Pod)
	require.True(t, ok)

	assert.Equal(t, podRequests(&pod.Spec), podRequests(&trimmed.Spec))
	assert.Equal(t, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "shop", ResourceVersion: "7"},
		Spec: corev1.PodSpec{NodeName: "node-a", Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewMilliQuantity(610, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity((1024+64+16)<<20, resource.BinarySI),
			}}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}, trimmed)
}

func TestTrimNode(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-a", ResourceVersion: "3",
			Labels:      map[string]string{"karpenter.sh/nodepool": "general"},
			Annotations: map[string]string{"a": "b"}},
		Spec: corev1.NodeSpec{Unschedulable: true, PodCIDR: "10.0.0.0/24"},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("16Gi"),
				corev1.ResourcePods: resource.MustParse("110"),
			},
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse},
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue, Message: "kubelet is posting ready status"},
			},
			Images: []corev1.ContainerImage{{Names: []string{"api:v1"}, SizeBytes: 1 << 30}},
		},
	}

	out, err := trimNode(node)
	require.NoError(t, err)

	assert.Equal(t, &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-a", ResourceVersion: "3",
			Labels: map[string]string{"karpenter.sh/nodepool": "general"}},
		Spec: corev1.NodeSpec{Unschedulable: true},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("16Gi"),
			},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}, out)
}
