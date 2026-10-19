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

// Package nodescale finds what scales a cluster's nodes, and what keeps a
// node from being scaled down. Hybernate never manages nodes, but freed
// capacity only comes off the bill once a node autoscaler removes it.
package nodescale

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Kind is a node autoscaler.
type Kind string

const (
	Karpenter         Kind = "Karpenter"
	ClusterAutoscaler Kind = "Cluster Autoscaler"
	EKSAutoMode       Kind = "EKS Auto Mode"
	GKEAutopilot      Kind = "GKE Autopilot"
)

// Karpenter's NodePool consolidation policies, and the consolidateAfter
// that turns consolidation off.
const (
	ConsolidateWhenEmpty                = "WhenEmpty"
	ConsolidateWhenEmptyOrUnderutilized = "WhenEmptyOrUnderutilized"
	ConsolidateBalanced                 = "Balanced"
	ConsolidateAfterNever               = "Never"
)

const (
	labelKarpenterNodePool  = "karpenter.sh/nodepool"
	labelEKSComputeType     = "eks.amazonaws.com/compute-type"
	labelGKENodePool        = "cloud.google.com/gke-nodepool"
	labelEKSNodeGroup       = "eks.amazonaws.com/nodegroup"
	labelAKSAgentPool       = "kubernetes.azure.com/agentpool"
	clusterAutoscalerStatus = "cluster-autoscaler-status"
	clusterAutoscalerName   = "cluster-autoscaler"
	gkeAutopilotNodePrefix  = "gk3-"
	eksAutoModeComputeType  = "auto"
)

// readTimeout bounds each read Detect and Read make. They're single lists
// and gets against the API server; one this slow means it isn't answering.
const readTimeout = 20 * time.Second

var nodePoolList = schema.GroupVersionKind{Group: "karpenter.sh", Version: "v1", Kind: "NodePoolList"}

// Scaler is one node autoscaler found, and what showed it.
type Scaler struct {
	Kind Kind `json:"kind"`
	// Provider is the cloud a Cluster Autoscaler runs on, when its nodes
	// say: EKS, GKE or AKS.
	Provider string `json:"provider,omitempty"`
	Evidence string `json:"evidence"`
	// Hint means the evidence suggests the autoscaler rather than shows
	// it's running, such as a Deployment named cluster-autoscaler that has
	// written no status.
	Hint bool `json:"hint,omitempty"`
}

// NodePool is a Karpenter NodePool's consolidation settings.
type NodePool struct {
	Name                string `json:"name"`
	ConsolidationPolicy string `json:"consolidationPolicy"`
	ConsolidateAfter    string `json:"consolidateAfter"`
}

// Autoscaler is what scales a cluster's nodes, as far as Detect could see.
type Autoscaler struct {
	Scalers   []Scaler   `json:"scalers"`
	NodePools []NodePool `json:"nodePools,omitempty"`
	// Unread names what Detect wasn't allowed to read. With nothing found
	// and something unread, which autoscaler runs isn't known.
	Unread []string `json:"unread,omitempty"`
}

// Known says whether Detect could tell what scales the nodes: it found an
// autoscaler, or read everything it looks at and found none.
func (a Autoscaler) Known() bool {
	return len(a.Scalers) > 0 || len(a.Unread) == 0
}

// Has says whether an autoscaler of kind k was found.
func (a Autoscaler) Has(k Kind) bool {
	return slices.ContainsFunc(a.Scalers, func(s Scaler) bool { return s.Kind == k })
}

// NodePool is the Karpenter NodePool named name, and false when there's
// none, or Detect couldn't read NodePools.
func (a Autoscaler) NodePool(name string) (NodePool, bool) {
	i := slices.IndexFunc(a.NodePools, func(p NodePool) bool { return p.Name == name })
	if i < 0 {
		return NodePool{}, false
	}
	return a.NodePools[i], true
}

// Detect finds which node autoscalers run in the cluster, from reads cheap
// enough to make on every command: Karpenter's NodePools, the nodes'
// metadata, Cluster Autoscaler's status ConfigMap and, without that, a
// Deployment named cluster-autoscaler in kube-system. A read the caller
// isn't allowed is noted in Unread rather than failing, so a user with
// access to one namespace still gets an answer, if a less certain one.
func Detect(ctx context.Context, r client.Reader) (Autoscaler, error) {
	var a Autoscaler
	pools, havePools, err := readNodePools(ctx, r)
	switch {
	case apierrors.IsForbidden(err):
		a.Unread = append(a.Unread, "Karpenter NodePools")
	case err != nil:
		return Autoscaler{}, err
	}
	a.NodePools = pools

	nodes, err := listNodeMetadata(ctx, r)
	switch {
	case apierrors.IsForbidden(err):
		a.Unread = append(a.Unread, "nodes")
	case err != nil:
		return Autoscaler{}, err
	}
	auto, karpenter, provider, autopilot := classifyNodes(nodes)

	if auto > 0 {
		a.Scalers = append(a.Scalers, Scaler{Kind: EKSAutoMode,
			Evidence: fmt.Sprintf("%s labelled %s=%s", countNodes(auto), labelEKSComputeType, eksAutoModeComputeType)})
	}
	switch {
	case karpenter > 0:
		a.Scalers = append(a.Scalers, Scaler{Kind: Karpenter,
			Evidence: fmt.Sprintf("%s labelled %s", countNodes(karpenter), labelKarpenterNodePool)})
	case havePools && auto == 0:
		a.Scalers = append(a.Scalers, Scaler{Kind: Karpenter, Evidence: "the nodepools.karpenter.sh CRD"})
	}

	status, err := statusConfigMap(ctx, r)
	switch {
	case apierrors.IsForbidden(err):
		a.Unread = append(a.Unread, "the "+clusterAutoscalerStatus+" ConfigMap in kube-system")
	case err != nil:
		return Autoscaler{}, err
	}
	switch {
	case status && autopilot:
		a.Scalers = append(a.Scalers, Scaler{Kind: GKEAutopilot, Provider: "GKE",
			Evidence: "the " + clusterAutoscalerStatus + " ConfigMap, and nodes named " + gkeAutopilotNodePrefix + "*"})
	case status:
		a.Scalers = append(a.Scalers, Scaler{Kind: ClusterAutoscaler, Provider: provider,
			Evidence: "the " + clusterAutoscalerStatus + " ConfigMap in kube-system"})
	default:
		found, err := clusterAutoscalerDeployment(ctx, r)
		switch {
		case apierrors.IsForbidden(err):
			// The status ConfigMap is the evidence that counts; a
			// Deployment is only looked for once it's missing.
			if !slices.Contains(a.Unread, "the "+clusterAutoscalerStatus+" ConfigMap in kube-system") {
				a.Unread = append(a.Unread, "Deployments in kube-system")
			}
		case err != nil:
			return Autoscaler{}, err
		case found != "":
			a.Scalers = append(a.Scalers, Scaler{Kind: ClusterAutoscaler, Provider: provider, Hint: true,
				Evidence: "a Deployment named " + found + " in kube-system, which has written no status"})
		}
	}
	return a, nil
}

func readNodePools(ctx context.Context, r client.Reader) ([]NodePool, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	var list unstructured.UnstructuredList
	list.SetGroupVersionKind(nodePoolList)
	err := r.List(ctx, &list)
	if meta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("listing Karpenter NodePools: %w", err)
	}
	pools := make([]NodePool, 0, len(list.Items))
	for _, item := range list.Items {
		policy, _, _ := unstructured.NestedString(item.Object, "spec", "disruption", "consolidationPolicy")
		after, _, _ := unstructured.NestedString(item.Object, "spec", "disruption", "consolidateAfter")
		if policy == "" {
			policy = ConsolidateWhenEmptyOrUnderutilized
		}
		pools = append(pools, NodePool{Name: item.GetName(), ConsolidationPolicy: policy, ConsolidateAfter: after})
	}
	slices.SortFunc(pools, func(a, b NodePool) int { return strings.Compare(a.Name, b.Name) })
	return pools, true, nil
}

func listNodeMetadata(ctx context.Context, r client.Reader) ([]metav1.PartialObjectMetadata, error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	var list metav1.PartialObjectMetadataList
	list.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("NodeList"))
	if err := r.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}
	return list.Items, nil
}

// classifyNodes counts the nodes EKS Auto Mode and Karpenter manage, and
// reads which cloud the rest are on. GKE Autopilot labels nothing that sets
// its nodes apart, but it names them gk3-, which Standard clusters don't.
func classifyNodes(nodes []metav1.PartialObjectMetadata) (auto, karpenter int, provider string, autopilot bool) {
	gke, gk3 := 0, 0
	for _, n := range nodes {
		labels := n.Labels
		switch {
		case labels[labelEKSComputeType] == eksAutoModeComputeType:
			auto++
		case labels[labelKarpenterNodePool] != "":
			karpenter++
		}
		switch {
		case labels[labelGKENodePool] != "":
			provider = "GKE"
			gke++
			if strings.HasPrefix(n.Name, gkeAutopilotNodePrefix) {
				gk3++
			}
		case labels[labelAKSAgentPool] != "":
			provider = "AKS"
		case labels[labelEKSNodeGroup] != "":
			provider = "EKS"
		}
	}
	return auto, karpenter, provider, gke > 0 && gk3 == gke
}

func statusConfigMap(ctx context.Context, r client.Reader) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	var cm metav1.PartialObjectMetadata
	cm.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("ConfigMap"))
	err := r.Get(ctx, client.ObjectKey{Namespace: metav1.NamespaceSystem, Name: clusterAutoscalerStatus}, &cm)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("getting ConfigMap kube-system/%s: %w", clusterAutoscalerStatus, err)
	}
	return true, nil
}

// clusterAutoscalerDeployment is the name of a Deployment in kube-system
// that looks like Cluster Autoscaler, as its Helm chart names it
// <release>-<cloud>-cluster-autoscaler, or "" when there's none.
func clusterAutoscalerDeployment(ctx context.Context, r client.Reader) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	var list metav1.PartialObjectMetadataList
	list.SetGroupVersionKind(appsv1.SchemeGroupVersion.WithKind("DeploymentList"))
	if err := r.List(ctx, &list, client.InNamespace(metav1.NamespaceSystem)); err != nil {
		return "", fmt.Errorf("listing Deployments in kube-system: %w", err)
	}
	for _, d := range list.Items {
		if strings.HasSuffix(d.Name, clusterAutoscalerName) {
			return d.Name, nil
		}
	}
	return "", nil
}

func countNodes(n int) string {
	if n == 1 {
		return "1 node"
	}
	return fmt.Sprintf("%d nodes", n)
}
