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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/nodescale"
)

func testNode(name, pool, instanceType, cpu, memory string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{
			"karpenter.sh/nodepool": pool, "node.kubernetes.io/instance-type": instanceType}},
		Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(memory)}},
	}
}

func testPod(namespace, name, node, cpu, memory string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: map[string]string{"app": name},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: name,
				UID: "uid", Controller: ptr.To(true)}}},
		Spec: corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Name: "app",
			Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(memory)}}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func testNodePool(name, policy, after string) *unstructured.Unstructured {
	p := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"disruption": map[string]any{
		"consolidationPolicy": policy, "consolidateAfter": after}}}}
	p.SetAPIVersion("karpenter.sh/v1")
	p.SetKind("NodePool")
	p.SetName(name)
	return p
}

func pausedWorkload(namespace, name string, cpuMillis, memoryBytes int64) *v1alpha1.ManagedWorkload {
	w := statusWorkloadObj(namespace, name, v1alpha1.TargetKindDeployment, v1alpha1.PhasePaused, 0)
	w.Status.Pause = &v1alpha1.PauseStatus{PreviousReplicas: 2}
	w.Status.Cost = &v1alpha1.CostStatus{ResourceReduction: &v1alpha1.ResourceReduction{
		CPUMillis: cpuMillis, MemoryBytes: memoryBytes, Replicas: 2}}
	return w
}

func karpenterCluster() []client.Object {
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "db"},
		Spec: policyv1.PodDisruptionBudgetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}}},
	}
	daemon := testPod("kube-system", "aws-node", "batch-1", "50m", "64Mi")
	daemon.OwnerReferences[0].Kind = "DaemonSet"
	return []client.Object{
		testNodePool("default", "WhenEmptyOrUnderutilized", "1m"),
		testNodePool("batch", "WhenEmpty", "1m"),
		testNode("busy-1", "default", "m5.xlarge", "3920m", "14Gi"),
		testNode("quiet-1", "default", "m5.large", "1930m", "7Gi"),
		testNode("batch-1", "batch", "c5.large", "1930m", "3Gi"),
		testPod("shop", "api", "busy-1", "3", "8Gi"),
		testPod("shop", "db", "quiet-1", "250m", "1Gi"),
		testPod("shop", "report", "batch-1", "100m", "256Mi"),
		daemon,
		pdb,
		pausedWorkload("preview-7", "web", 1500, 3<<30),
		pausedWorkload("preview-8", "web", 500, 1<<30),
		statusWorkloadObj("preview-9", "busy", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning, 0),
	}
}

func runNodes(t *testing.T, c client.Client) string {
	t.Helper()
	result, err := clusterNodes(context.Background(), c)
	require.NoError(t, err)
	result.Cluster = "staging (EKS us-east-1)"
	var out bytes.Buffer
	require.NoError(t, writeNodesTable(&out, result))
	return out.String()
}

func TestNodes_Table(t *testing.T) {
	out := runNodes(t, newClusterClient(t, interceptor.Funcs{}, karpenterCluster()...))

	//nolint:lll // the expected screen is as wide as the command prints it
	assert.Equal(t, `staging (EKS us-east-1): 3 nodes
Node autoscaler: Karpenter (NodePool batch consolidates only empty nodes after 1m; NodePool default consolidates when underutilized after 1m)
Napwake's 2 paused workloads would request 2.0 vCPU and 4.0 GiB of memory if awake.

  NODE      POOL      INSTANCE TYPE   CPU REQUESTED     MEMORY REQUESTED   UTILISATION   BLOCKED BY
  batch-1   batch     c5.large        0.1 of 1.9 vCPU   0.3 of 3.0 GiB     10%           NodePool consolidates only empty nodes
  quiet-1   default   m5.large        0.2 of 1.9 vCPU   1.0 of 7.0 GiB     14%           PDB allows no disruption (shop/db)
  busy-1    default   m5.xlarge       3.0 of 3.9 vCPU   8.0 of 14.0 GiB    77%           -

Next steps:
  1. A NodePool consolidates only empty nodes, which a node running anything never is. Set spec.disruption.consolidationPolicy: WhenEmptyOrUnderutilized.
     For example: NodePool batch consolidates only empty nodes, and this one runs 1 pod
  2. A PodDisruptionBudget allows no disruptions, so its pods can't be moved. Raise maxUnavailable, lower minAvailable, or run more replicas than it requires.
     For example: shop/db: PodDisruptionBudget db allows no disruptions
  See https://docs.napwake.dev/guides/cluster-autoscaler/
  To have pausing empty whole nodes, put non-production workloads on a node pool of their own: https://docs.napwake.dev/guides/node-pools/

`, out)
}

// A pause records the node pools its pods ran in, so what it would request
// awake is shown against the pools it would need room in, shared by how
// many pods ran in each.
func TestNodes_SplitsPausedByPool(t *testing.T) {
	split := pausedWorkload("shop", "api", 3000, 3<<30)
	split.Status.Pause.Resources = &v1alpha1.ResourceSnapshot{NodePools: []v1alpha1.NodePoolPods{
		{Name: "batch", Pods: 2}, {Name: "web", Pods: 1}}}
	webOnly := pausedWorkload("shop", "cart", 1000, 1<<30)
	webOnly.Status.Pause.Resources = &v1alpha1.ResourceSnapshot{NodePools: []v1alpha1.NodePoolPods{
		{Name: "web", Pods: 2}}}
	c := newClusterClient(t, interceptor.Funcs{}, split, webOnly)

	paused, note, err := pausedWorkloadRequests(context.Background(), c)
	require.NoError(t, err)
	require.Empty(t, note)

	assert.Equal(t, []poolRequests{
		{Pool: "batch", CPUMillis: 2000, MemoryBytes: 2 << 30},
		{Pool: "web", CPUMillis: 2000, MemoryBytes: 2 << 30},
	}, paused.ByPool)
	var out bytes.Buffer
	require.NoError(t, writeNodesTable(&out, nodesResult{Cluster: "staging", Paused: paused}))
	assert.Contains(t, out.String(), "  in pool batch: 2.0 vCPU and 2.0 GiB\n  in pool web: 2.0 vCPU and 2.0 GiB\n")
}

func TestNodes_JSONHasEveryReason(t *testing.T) {
	out, err := runCLI(t, newClusterClient(t, interceptor.Funcs{}, karpenterCluster()...), "nodes", "-o", "json")
	require.NoError(t, err)

	var got nodesResult
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, "hanging", got.Context)
	require.Len(t, got.Nodes, 3)
	assert.Equal(t, []nodescale.Reason{{Code: nodescale.CodePDB, Pod: "shop/db",
		Detail: "PodDisruptionBudget db allows no disruptions"}}, got.Nodes[1].BlockedBy)
	assert.Equal(t, []nodescale.Reason{}, got.Nodes[2].BlockedBy)
	assert.Equal(t, &pausedRequests{Workloads: 2, CPUMillis: 2000, MemoryBytes: 4 << 30,
		ByPool: []poolRequests{{Pool: unrecordedPool, CPUMillis: 2000, MemoryBytes: 4 << 30}}}, got.Paused)
	assert.Equal(t, []string{"Karpenter"}, scalerKinds(got.Autoscaler))
}

// forbidReads refuses every get and list of the kinds named, such as
// "PodList" or "ConfigMap", as RBAC would.
func forbidReads(kinds ...string) interceptor.Funcs {
	denied := func(obj runtime.Object) error {
		kind := obj.GetObjectKind().GroupVersionKind().Kind
		if kind == "" {
			gvk, err := apiutil.GVKForObject(obj, scheme)
			if err != nil {
				return err
			}
			kind = gvk.Kind
		}
		if slices.Contains(kinds, kind) {
			return apierrors.NewForbidden(schema.GroupResource{Resource: kind}, "", errors.New("denied"))
		}
		return nil
	}
	return interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object,
			opts ...client.GetOption) error {
			if err := denied(obj); err != nil {
				return err
			}
			return c.Get(ctx, key, obj, opts...)
		},
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if err := denied(list); err != nil {
				return err
			}
			return c.List(ctx, list, opts...)
		},
	}
}

func scalerKinds(a nodescale.Autoscaler) []string {
	out := make([]string, 0, len(a.Scalers))
	for _, s := range a.Scalers {
		out = append(out, string(s.Kind))
	}
	return out
}

// A user who can read only some of the cluster gets what they can, and
// notes for the rest, never "no autoscaler".
func TestNodes_RespectsAccess(t *testing.T) {
	c := newClusterClient(t, forbidReads("PodList", "PodDisruptionBudgetList", "ManagedWorkloadList", "NodePoolList"),
		karpenterCluster()...)

	out := runNodes(t, c)

	assert.Contains(t, out, "Node autoscaler: Karpenter\n")
	assert.Contains(t, out, "your access doesn't allow listing pods in every namespace")
	assert.Contains(t, out, "your access doesn't allow listing PodDisruptionBudgets in every namespace")
	assert.Contains(t, out, "what paused workloads would request isn't shown")
	assert.Contains(t, out, "0.0 of 3.9 vCPU")
}

func TestNodes_NothingReadable(t *testing.T) {
	c := newClusterClient(t, forbidReads("NodeList", "NodePoolList", "ConfigMap", "DeploymentList"),
		karpenterCluster()...)

	out := runNodes(t, c)

	assert.Contains(t, out, "staging (EKS us-east-1): 0 nodes\nNode autoscaler: unknown, as your access doesn't "+
		"allow reading Karpenter NodePools, nodes or the cluster-autoscaler-status ConfigMap in kube-system")
	assert.Contains(t, out, "ask an admin for list on nodes")
	assert.NotContains(t, out, "NODE ")
}

func TestNodes_NoAutoscaler(t *testing.T) {
	objs := []client.Object{testNode("n1", "", "m5.large", "2", "8Gi"), testPod("shop", "api", "n1", "1", "1Gi")}
	objs[0].SetLabels(map[string]string{"node.kubernetes.io/instance-type": "m5.large"})

	out := runNodes(t, newStatusClient(t, interceptor.Funcs{}, objs...))

	assert.Contains(t, out, "Node autoscaler: none found — freed capacity won't lower the bill until nodes scale "+
		"down; see https://docs.napwake.dev/guides/cluster-autoscaler/")
	assert.Contains(t, out, "not autoscaled")
	assert.Contains(t, out, "1. No node autoscaler scales these nodes")
}

// A node whose node group is at its minimum, as Cluster Autoscaler's status
// ConfigMap reports it, stays whatever runs on it, so nodes says why rather
// than giving steps that would save nothing. The test cluster's last node,
// in default-pool at its minimum of one, was given two.
func TestNodes_ANodeAtItsGroupsMinimum(t *testing.T) {
	status, err := os.ReadFile(filepath.Join("..", "..", "internal", "nodescale", "testdata", "ca-1.32-gke.yaml"))
	require.NoError(t, err)
	node := testNode("gke-prelaunch-default-pool-3f9c1a2b-x7k2", "", "e2-standard-4", "3920m", "13Gi")
	node.Labels = map[string]string{"cloud.google.com/gke-nodepool": "default-pool",
		"node.kubernetes.io/instance-type": "e2-standard-4"}
	redis := testPod("argocd", "argocd-redis", node.Name, "100m", "128Mi")
	redis.Spec.Volumes = []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
	objs := []client.Object{node, redis, testPod("kube-system", "kube-dns", node.Name, "260m", "110Mi"),
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "cluster-autoscaler-status"},
			Data: map[string]string{"status": string(status)}}}

	out := runNodes(t, newStatusClient(t, interceptor.Funcs{}, objs...))

	assert.Contains(t, out, "node group at its minimum")
	assert.NotContains(t, out, "Next steps:")
	assert.NotContains(t, out, "safe-to-evict-local-volumes")
	assert.Contains(t, out, "These nodes stay at their node group's minimum, which Cluster Autoscaler doesn't go "+
		"below, whatever runs on them:\n"+
		"  gke-prelaunch-default-pool-3f9c1a2b-x7k2: its node group, gke-prelaunch-default-pool-3f9c1a2b-grp, is at "+
		"its minimum of 1 node\n")
}

func TestNodeAutoscalerLine(t *testing.T) {
	tests := []struct {
		name string
		a    nodescale.Autoscaler
		want string
	}{
		{
			name: "Karpenter, its pools alike",
			a: nodescale.Autoscaler{Scalers: []nodescale.Scaler{{Kind: nodescale.Karpenter}},
				NodePools: []nodescale.NodePool{
					{Name: "a", ConsolidationPolicy: nodescale.ConsolidateWhenEmptyOrUnderutilized, ConsolidateAfter: "0s"},
					{Name: "b", ConsolidationPolicy: nodescale.ConsolidateWhenEmptyOrUnderutilized, ConsolidateAfter: "0s"},
				}},
			want: "Node autoscaler: Karpenter (consolidates when underutilized)",
		},
		{
			name: "EKS Auto Mode that never consolidates",
			a: nodescale.Autoscaler{Scalers: []nodescale.Scaler{{Kind: nodescale.EKSAutoMode}},
				NodePools: []nodescale.NodePool{{Name: "general-purpose", ConsolidateAfter: "Never"}}},
			want: "Node autoscaler: EKS Auto Mode (never consolidates)",
		},
		{
			name: "Balanced",
			a: nodescale.Autoscaler{Scalers: []nodescale.Scaler{{Kind: nodescale.Karpenter}},
				NodePools: []nodescale.NodePool{{Name: "a", ConsolidationPolicy: nodescale.ConsolidateBalanced}}},
			want: "Node autoscaler: Karpenter (consolidates underutilized nodes when that's worth the disruption)",
		},
		{
			name: "Cluster Autoscaler on GKE",
			a:    nodescale.Autoscaler{Scalers: []nodescale.Scaler{{Kind: nodescale.ClusterAutoscaler, Provider: "GKE"}}},
			want: "Node autoscaler: Cluster Autoscaler on GKE",
		},
		{
			name: "a Cluster Autoscaler Deployment",
			a: nodescale.Autoscaler{Scalers: []nodescale.Scaler{{Kind: nodescale.ClusterAutoscaler, Hint: true,
				Evidence: "a Deployment named cluster-autoscaler in kube-system, which has written no status"}}},
			want: "Node autoscaler: Cluster Autoscaler (from a Deployment named cluster-autoscaler in kube-system, " +
				"which has written no status; it may not be running)",
		},
		{
			name: "GKE Autopilot",
			a:    nodescale.Autoscaler{Scalers: []nodescale.Scaler{{Kind: nodescale.GKEAutopilot}}},
			want: "Node autoscaler: GKE Autopilot (scales nodes itself)",
		},
		{
			name: "two at once",
			a: nodescale.Autoscaler{Scalers: []nodescale.Scaler{{Kind: nodescale.Karpenter},
				{Kind: nodescale.ClusterAutoscaler}}},
			want: "Node autoscaler: Karpenter and Cluster Autoscaler",
		},
		{
			name: "unknown",
			a:    nodescale.Autoscaler{Unread: []string{"nodes"}},
			want: "Node autoscaler: unknown, as your access doesn't allow reading nodes; freed capacity comes off " +
				"the bill only as nodes scale down",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, nodeAutoscalerLine(tt.a))
		})
	}
}
