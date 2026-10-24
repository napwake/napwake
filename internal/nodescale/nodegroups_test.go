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

package nodescale

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// sample is a status Cluster Autoscaler wrote in its status ConfigMap:
//
//   - ca-1.32-upstream.yaml is the YAML it writes since 1.30, the sample its
//     own tests write (cluster-autoscaler/clusterstate/utils/status_test.yaml
//     at cluster-autoscaler-1.32.0);
//   - ca-1.32-gke.yaml is the same, as GKE's writes it for the pre-launch
//     test cluster: an instance group per node pool and zone, the default
//     pool at its minimum of one node;
//   - ca-1.29-text.txt is the text it wrote before 1.30, laid out as its
//     clusterstate package wrote it, for an EKS managed node group and an
//     AKS scale set.
func sample(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return string(data)
}

func TestParseNodeGroups(t *testing.T) {
	tests := []struct {
		sample string
		want   []NodeGroup
	}{
		{sample: "ca-1.32-upstream.yaml", want: []NodeGroup{{Name: "sample-node-group", MinSize: 2, MaxSize: 12, Target: 8}}},
		{sample: "ca-1.32-gke.yaml", want: []NodeGroup{
			{Name: "https://www.googleapis.com/compute/v1/projects/staging-412907/zones/europe-west1-b/" +
				"instanceGroups/gke-prelaunch-default-pool-3f9c1a2b-grp", MinSize: 1, MaxSize: 3, Target: 1},
			{Name: "https://www.googleapis.com/compute/v1/projects/staging-412907/zones/europe-west1-b/" +
				"instanceGroups/gke-prelaunch-workloads-7d2e94c1-grp", MinSize: 0, MaxSize: 4, Target: 0},
		}},
		{sample: "ca-1.29-text.txt", want: []NodeGroup{
			{Name: "eks-system-6ac6b9e2-1f3d-4c8b-9a7e-2d5f8b3c1e04", MinSize: 2, MaxSize: 4, Target: 2},
			{Name: "aks-workloads-18537295-vmss", MinSize: 1, MaxSize: 6, Target: 3},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.sample, func(t *testing.T) {
			assert.Equal(t, tt.want, parseNodeGroups(sample(t, tt.sample)))
		})
	}
	assert.Empty(t, parseNodeGroups("Cluster-autoscaler status at 2024-01-15:\nInitializing"), "none yet")
	assert.Empty(t, parseNodeGroups(":::not yaml"))
}

func gkeNode(name, pool string) corev1.Node {
	return annotatedNode(name, map[string]string{"cloud.google.com/gke-nodepool": pool}, nil)
}

// A node whose node group is at its minimum stays however little it's
// used, so what's on it isn't what keeps it: Cluster Autoscaler can't shrink
// the group below its minimum. One whose group can shrink is judged as
// before.
func TestBlockers_ANodeGroupAtItsMinimum(t *testing.T) {
	cache := pod("argocd", "redis", "", volume(emptyDir("data", "")))
	tests := []struct {
		name   string
		sample string
		node   corev1.Node
		reason []Reason
	}{
		{name: "a GKE node pool at its minimum", sample: "ca-1.32-gke.yaml",
			node: gkeNode("gke-prelaunch-default-pool-3f9c1a2b-x7k2", "default-pool"),
			reason: []Reason{{Code: CodeAtMinimum,
				Detail: "its node group, gke-prelaunch-default-pool-3f9c1a2b-grp, is at its minimum of 1 node"}}},
		{name: "an EKS managed node group at its minimum", sample: "ca-1.29-text.txt",
			node: annotatedNode("ip-10-0-1-23.ec2.internal", map[string]string{"eks.amazonaws.com/nodegroup": "system"}, nil),
			reason: []Reason{{Code: CodeAtMinimum,
				Detail: "its node group, eks-system-6ac6b9e2-1f3d-4c8b-9a7e-2d5f8b3c1e04, is at its minimum of 2 nodes"}}},
		{name: "an AKS scale set above its minimum", sample: "ca-1.29-text.txt",
			node: annotatedNode("aks-workloads-18537295-vmss000002",
				map[string]string{"kubernetes.azure.com/agentpool": "workloads"}, nil),
			reason: []Reason{{Code: CodeLocalStorage, Pod: "argocd/redis", Detail: "local storage in data"}}},
		{name: "a node of no group the status names", sample: "ca-1.32-gke.yaml",
			node:   gkeNode("gke-prelaunch-other-pool-11aa22bb-q1w2", "other-pool"),
			reason: []Reason{{Code: CodeLocalStorage, Pod: "argocd/redis", Detail: "local storage in data"}}},
		{name: "a node group of another pool's prefix", sample: "ca-1.29-text.txt",
			node:   annotatedNode("ip-10-0-2-9.ec2.internal", map[string]string{"eks.amazonaws.com/nodegroup": "sys"}, nil),
			reason: []Reason{{Code: CodeLocalStorage, Pod: "argocd/redis", Detail: "local storage in data"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := cache
			p.Spec.NodeName = tt.node.Name
			c := &Cluster{Nodes: []corev1.Node{tt.node}, Pods: []corev1.Pod{p},
				NodeGroups: parseNodeGroups(sample(t, tt.sample))}

			assert.Equal(t, tt.reason, c.Blockers(withCA)[tt.node.Name])
		})
	}
}

// Read takes the node groups from Cluster Autoscaler's status ConfigMap,
// and notes when it isn't allowed to.
func TestRead_TheNodeGroups(t *testing.T) {
	status := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "cluster-autoscaler-status"},
		Data: map[string]string{"status": sample(t, "ca-1.32-gke.yaml")}}

	got, err := Read(context.Background(), fakeCluster(t, false, forbid(), status))
	require.NoError(t, err)
	assert.Len(t, got.NodeGroups, 2)

	got, err = Read(context.Background(), fakeCluster(t, false, forbid()))
	require.NoError(t, err)
	assert.Empty(t, got.NodeGroups, "without Cluster Autoscaler, there are none")
	assert.Empty(t, got.Unread)

	got, err = Read(context.Background(), fakeCluster(t, false, forbid("ConfigMap"), status))
	require.NoError(t, err)
	assert.Empty(t, got.NodeGroups)
	assert.Equal(t, []string{"Cluster Autoscaler's status ConfigMap in kube-system"}, got.Unread)
}
