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

package nodescale

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

var (
	withCA        = Autoscaler{Scalers: []Scaler{{Kind: ClusterAutoscaler}}}
	withKarpenter = Autoscaler{Scalers: []Scaler{{Kind: Karpenter}},
		NodePools: []NodePool{
			{Name: "default", ConsolidationPolicy: ConsolidateWhenEmptyOrUnderutilized, ConsolidateAfter: "0s"},
			{Name: "empty-only", ConsolidationPolicy: ConsolidateWhenEmpty, ConsolidateAfter: "1m"},
			{Name: "frozen", ConsolidationPolicy: ConsolidateWhenEmptyOrUnderutilized, ConsolidateAfter: "Never"},
		}}
)

type podOption func(*corev1.Pod)

func pod(namespace, name, nodeName string, opts ...podOption) corev1.Pod {
	p := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: map[string]string{"app": name},
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: name + "-abc", Controller: ptr.To(true)}}},
		Spec:   corev1.PodSpec{NodeName: nodeName},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	for _, o := range opts {
		o(&p)
	}
	return p
}

func annotated(key, value string) podOption {
	return func(p *corev1.Pod) {
		if p.Annotations == nil {
			p.Annotations = map[string]string{}
		}
		p.Annotations[key] = value
	}
}

func ownedBy(kind string) podOption {
	return func(p *corev1.Pod) {
		p.OwnerReferences = []metav1.OwnerReference{{Kind: kind, Name: "owner", Controller: ptr.To(true)}}
	}
}

func bare(p *corev1.Pod) { p.OwnerReferences = nil }

func volume(v corev1.Volume) podOption {
	return func(p *corev1.Pod) { p.Spec.Volumes = append(p.Spec.Volumes, v) }
}

func emptyDir(name string, medium corev1.StorageMedium) corev1.Volume {
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: medium}}}
}

func pdb(namespace, name string, selector *metav1.LabelSelector, allowed int32) policyv1.PodDisruptionBudget {
	return policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       policyv1.PodDisruptionBudgetSpec{Selector: selector},
		Status:     policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: allowed},
	}
}

func appSelector(app string) *metav1.LabelSelector {
	return &metav1.LabelSelector{MatchLabels: map[string]string{"app": app}}
}

func annotatedNode(name string, labels, annotations map[string]string) corev1.Node {
	return corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels, Annotations: annotations}}
}

func TestBlockers(t *testing.T) {
	plain := annotatedNode("n1", nil, nil)
	karpenterNode := func(pool string, annotations map[string]string) corev1.Node {
		return annotatedNode("n1", map[string]string{"karpenter.sh/nodepool": pool}, annotations)
	}
	tests := []struct {
		name   string
		a      Autoscaler
		node   corev1.Node
		pods   []corev1.Pod
		pdbs   []policyv1.PodDisruptionBudget
		reason []Reason
	}{
		{
			name: "nothing in the way", a: withCA, node: plain,
			pods: []corev1.Pod{pod("shop", "api", "n1")},
		},
		{
			name: "safe-to-evict false", a: withCA, node: plain,
			pods: []corev1.Pod{pod("shop", "db", "n1", annotated(annotationSafeToEvict, "false"))},
			reason: []Reason{{Code: CodeSafeToEvict, Pod: "shop/db",
				Detail: "cluster-autoscaler.kubernetes.io/safe-to-evict=false"}},
		},
		{
			name: "a PodDisruptionBudget that allows nothing", a: withCA, node: plain,
			pods: []corev1.Pod{pod("shop", "api", "n1")},
			pdbs: []policyv1.PodDisruptionBudget{pdb("shop", "api", appSelector("api"), 0),
				pdb("shop", "other", appSelector("other"), 0), pdb("shop", "roomy", appSelector("api"), 1)},
			reason: []Reason{{Code: CodePDB, Pod: "shop/api", Detail: "PodDisruptionBudget api allows no disruptions"}},
		},
		{
			name: "an empty selector covers the namespace", a: withCA, node: plain,
			pods:   []corev1.Pod{pod("shop", "api", "n1")},
			pdbs:   []policyv1.PodDisruptionBudget{pdb("shop", "all", &metav1.LabelSelector{}, 0), pdb("shop", "none", nil, 0)},
			reason: []Reason{{Code: CodePDB, Pod: "shop/api", Detail: "PodDisruptionBudget all allows no disruptions"}},
		},
		{
			name: "local storage", a: withCA, node: plain,
			pods: []corev1.Pod{pod("shop", "api", "n1", volume(emptyDir("cache", "")), volume(emptyDir("tmp", "Memory")),
				volume(corev1.Volume{Name: "logs", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/log"}}}))},
			reason: []Reason{{Code: CodeLocalStorage, Pod: "shop/api", Detail: "local storage in cache, logs"}},
		},
		{
			name: "local storage on EKS, whose autoscaler keeps upstream's default",
			a:    Autoscaler{Scalers: []Scaler{{Kind: ClusterAutoscaler, Provider: "EKS"}}}, node: plain,
			pods:   []corev1.Pod{pod("shop", "api", "n1", volume(emptyDir("cache", "")))},
			reason: []Reason{{Code: CodeLocalStorage, Pod: "shop/api", Detail: "local storage in cache"}},
		},
		{
			name: "local storage on GKE, whose autoscaler ignores it from 1.22",
			a:    Autoscaler{Scalers: []Scaler{{Kind: ClusterAutoscaler, Provider: "GKE"}}}, node: plain,
			pods: []corev1.Pod{pod("shop", "api", "n1", volume(emptyDir("cache", "")))},
		},
		{
			name: "local storage on GKE Autopilot",
			a:    Autoscaler{Scalers: []Scaler{{Kind: GKEAutopilot, Provider: "GKE"}}}, node: plain,
			pods: []corev1.Pod{pod("shop", "api", "n1", volume(emptyDir("cache", "")))},
		},
		{
			name: "local storage on AKS, which turns the check off by default",
			a:    Autoscaler{Scalers: []Scaler{{Kind: ClusterAutoscaler, Provider: "AKS"}}}, node: plain,
			pods: []corev1.Pod{pod("shop", "api", "n1", volume(emptyDir("cache", "")))},
		},
		{
			name: "local volumes declared safe to evict", a: withCA, node: plain,
			pods: []corev1.Pod{pod("shop", "api", "n1", volume(emptyDir("cache", "")),
				annotated(annotationSafeToEvictLocalVolumes, "scratch, cache"))},
		},
		{
			name: "a bare pod", a: withCA, node: plain,
			pods:   []corev1.Pod{pod("shop", "debug", "n1", bare)},
			reason: []Reason{{Code: CodeNoController, Pod: "shop/debug", Detail: "no controller would recreate it"}},
		},
		{
			name: "safe-to-evict true lets a bare pod with local storage go", a: withCA, node: plain,
			pods: []corev1.Pod{pod("shop", "debug", "n1", bare, volume(emptyDir("cache", "")),
				annotated(annotationSafeToEvict, "true"))},
		},
		{
			name: "kube-system without a PodDisruptionBudget", a: withCA, node: plain,
			pods: []corev1.Pod{pod("kube-system", "coredns", "n1"), pod("kube-system", "metrics-server", "n1")},
			pdbs: []policyv1.PodDisruptionBudget{pdb("kube-system", "metrics-server", appSelector("metrics-server"), 1)},
			reason: []Reason{{Code: CodeSystemPod, Pod: "kube-system/coredns",
				Detail: "a kube-system pod without a PodDisruptionBudget"}},
		},
		{
			name: "DaemonSet, mirror, finished and other nodes' pods don't count", a: withCA, node: plain,
			pods: []corev1.Pod{
				pod("kube-system", "kube-proxy", "n1", ownedBy("DaemonSet"), annotated(annotationSafeToEvict, "false")),
				pod("kube-system", "etcd", "n1", bare, annotated(annotationMirrorPod, "x")),
				pod("shop", "job", "n1", bare, func(p *corev1.Pod) { p.Status.Phase = corev1.PodSucceeded }),
				pod("shop", "elsewhere", "n2", bare),
			},
		},
		{
			name: "scale-down disabled on the node", a: withCA,
			node: annotatedNode("n1", nil, map[string]string{annotationScaleDownDisabled: "true"}),
			reason: []Reason{{Code: CodeScaleDownDisabled,
				Detail: "cluster-autoscaler.kubernetes.io/scale-down-disabled=true on the node"}},
		},
		{
			name: "do-not-disrupt on a pod", a: withKarpenter, node: karpenterNode("default", nil),
			pods: []corev1.Pod{pod("shop", "db", "n1", annotated(annotationDoNotDisrupt, "true")),
				pod("shop", "debug", "n1", bare, annotated(annotationSafeToEvict, "false"))},
			reason: []Reason{{Code: CodeDoNotDisrupt, Pod: "shop/db", Detail: "karpenter.sh/do-not-disrupt=true"}},
		},
		{
			name: "do-not-disrupt on the node", a: withKarpenter,
			node:   karpenterNode("default", map[string]string{annotationDoNotDisrupt: "true"}),
			reason: []Reason{{Code: CodeNodeDoNotDisrupt, Detail: "karpenter.sh/do-not-disrupt=true on the node"}},
		},
		{
			name: "Karpenter honours PodDisruptionBudgets", a: withKarpenter, node: karpenterNode("default", nil),
			pods:   []corev1.Pod{pod("shop", "api", "n1")},
			pdbs:   []policyv1.PodDisruptionBudget{pdb("shop", "api", appSelector("api"), 0)},
			reason: []Reason{{Code: CodePDB, Pod: "shop/api", Detail: "PodDisruptionBudget api allows no disruptions"}},
		},
		{
			name: "a WhenEmpty NodePool with pods on the node", a: withKarpenter, node: karpenterNode("empty-only", nil),
			pods: []corev1.Pod{pod("shop", "api", "n1"), pod("kube-system", "aws-node", "n1", ownedBy("DaemonSet"))},
			reason: []Reason{{Code: CodeWhenEmpty,
				Detail: "NodePool empty-only consolidates only empty nodes, and this one runs 1 pod"}},
		},
		{
			name: "a WhenEmpty NodePool's node with only DaemonSets is empty", a: withKarpenter,
			node: karpenterNode("empty-only", nil),
			pods: []corev1.Pod{pod("kube-system", "aws-node", "n1", ownedBy("DaemonSet"))},
		},
		{
			name: "consolidateAfter Never", a: withKarpenter, node: karpenterNode("frozen", nil),
			reason: []Reason{{Code: CodeConsolidationOff, Detail: "NodePool frozen has consolidateAfter: Never"}},
		},
		{
			name: "a node outside Karpenter's NodePools", a: withKarpenter, node: plain,
			reason: []Reason{{Code: CodeNotAutoscaled,
				Detail: "not in a Karpenter NodePool, and no Cluster Autoscaler found"}},
		},
		{
			name: "no autoscaler at all", a: Autoscaler{}, node: plain,
			reason: []Reason{{Code: CodeNotAutoscaled, Detail: "no node autoscaler found"}},
		},
		{
			name: "an unknown autoscaler is judged by Cluster Autoscaler's rules", a: Autoscaler{Unread: []string{"nodes"}},
			node: plain, pods: []corev1.Pod{pod("shop", "debug", "n1", bare)},
			reason: []Reason{{Code: CodeNoController, Pod: "shop/debug", Detail: "no controller would recreate it"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Cluster{Nodes: []corev1.Node{tt.node}, Pods: tt.pods, PDBs: tt.pdbs}

			got := c.Blockers(tt.a)

			assert.Equal(t, tt.reason, got[tt.node.Name])
		})
	}
}

func TestRead_NotesWhatItCantList(t *testing.T) {
	c := fakeCluster(t, false, forbid("PodList", "PodDisruptionBudgetList"),
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "b"}}, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "a"}})

	got, err := Read(context.Background(), c)

	require.NoError(t, err)
	require.Len(t, got.Nodes, 2)
	assert.Equal(t, "a", got.Nodes[0].Name)
	assert.Equal(t, []string{"pods in every namespace", "PodDisruptionBudgets in every namespace"}, got.Unread)
}

func TestRead_FailsOnOtherErrors(t *testing.T) {
	c := fakeCluster(t, false, interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList,
		...client.ListOption) error {
		return context.DeadlineExceeded
	}})

	_, err := Read(context.Background(), c)

	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.ErrorContains(t, err, "listing nodes")
}

func TestPodRequests(t *testing.T) {
	requests := func(cpu, memory string) corev1.ResourceRequirements {
		return corev1.ResourceRequirements{Requests: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(memory)}}
	}
	p := &corev1.Pod{Spec: corev1.PodSpec{
		InitContainers: []corev1.Container{
			{Name: "sidecar", Resources: requests("100m", "64Mi"), RestartPolicy: ptr.To(corev1.ContainerRestartPolicyAlways)},
			{Name: "migrate", Resources: requests("2", "128Mi")},
		},
		Containers: []corev1.Container{{Name: "app", Resources: requests("500m", "1Gi")},
			{Name: "proxy", Resources: requests("100m", "128Mi")}},
		Overhead: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m")},
	}}

	got := PodRequests(p)

	assert.Equal(t, int64(2110), got.Cpu().MilliValue(), "the migration with the sidecar beside it, plus overhead")
	assert.Equal(t, int64((1024+128+64)<<20), got.Memory().Value(), "the app, its proxy and the sidecar")
}
