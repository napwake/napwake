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
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Code is why a node can't be removed.
type Code string

const (
	// CodeSafeToEvict is a pod annotated
	// cluster-autoscaler.kubernetes.io/safe-to-evict: "false".
	CodeSafeToEvict Code = "SafeToEvictFalse"
	// CodeDoNotDisrupt is a pod annotated karpenter.sh/do-not-disrupt.
	CodeDoNotDisrupt Code = "DoNotDisrupt"
	// CodeNodeDoNotDisrupt is the node annotated karpenter.sh/do-not-disrupt.
	CodeNodeDoNotDisrupt Code = "NodeDoNotDisrupt"
	// CodePDB is a pod whose PodDisruptionBudget allows no disruptions.
	CodePDB Code = "PodDisruptionBudget"
	// CodeLocalStorage is a pod with an emptyDir or hostPath volume not
	// listed in cluster-autoscaler.kubernetes.io/safe-to-evict-local-volumes.
	CodeLocalStorage Code = "LocalStorage"
	// CodeNoController is a pod no controller would recreate elsewhere.
	CodeNoController Code = "NoController"
	// CodeSystemPod is a kube-system pod without a PodDisruptionBudget.
	CodeSystemPod Code = "KubeSystemWithoutPDB"
	// CodeScaleDownDisabled is the node annotated
	// cluster-autoscaler.kubernetes.io/scale-down-disabled: "true".
	CodeScaleDownDisabled Code = "ScaleDownDisabled"
	// CodeWhenEmpty is a node of a NodePool that only consolidates empty
	// nodes, with pods on it.
	CodeWhenEmpty Code = "ConsolidateWhenEmpty"
	// CodeConsolidationOff is a node of a NodePool whose consolidateAfter
	// is Never.
	CodeConsolidationOff Code = "ConsolidateAfterNever"
	// CodeNotAutoscaled is a node no autoscaler found scales.
	CodeNotAutoscaled Code = "NotAutoscaled"
)

const (
	annotationSafeToEvict             = "cluster-autoscaler.kubernetes.io/safe-to-evict"
	annotationSafeToEvictLocalVolumes = "cluster-autoscaler.kubernetes.io/safe-to-evict-local-volumes"
	annotationScaleDownDisabled       = "cluster-autoscaler.kubernetes.io/scale-down-disabled"
	annotationDoNotDisrupt            = "karpenter.sh/do-not-disrupt"
	annotationMirrorPod               = "kubernetes.io/config.mirror"

	enabled = "true"
)

// Reason is one thing keeping a node from being removed. Pod is the
// namespace/name of the pod that does, empty for one about the node.
type Reason struct {
	Code   Code   `json:"code"`
	Pod    string `json:"pod,omitempty"`
	Detail string `json:"detail"`
}

// Cluster is what Blockers judges nodes from, read once.
type Cluster struct {
	Nodes []corev1.Node
	Pods  []corev1.Pod
	PDBs  []policyv1.PodDisruptionBudget
	// Unread names what the caller wasn't allowed to read. Without pods
	// or PodDisruptionBudgets, the reasons that come from them are missing.
	Unread []string
}

// Read lists the cluster's nodes, pods and PodDisruptionBudgets. A list
// the caller isn't allowed is noted in Unread rather than failing.
func Read(ctx context.Context, r client.Reader) (*Cluster, error) {
	c := &Cluster{}
	reads := []struct {
		what string
		list client.ObjectList
	}{
		{"nodes", &corev1.NodeList{}},
		{"pods in every namespace", &corev1.PodList{}},
		{"PodDisruptionBudgets in every namespace", &policyv1.PodDisruptionBudgetList{}},
	}
	for _, read := range reads {
		err := listWithin(ctx, r, read.list)
		if apierrors.IsForbidden(err) {
			c.Unread = append(c.Unread, read.what)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", read.what, err)
		}
		switch list := read.list.(type) {
		case *corev1.NodeList:
			c.Nodes = list.Items
		case *corev1.PodList:
			c.Pods = list.Items
		case *policyv1.PodDisruptionBudgetList:
			c.PDBs = list.Items
		}
	}
	slices.SortFunc(c.Nodes, func(a, b corev1.Node) int { return strings.Compare(a.Name, b.Name) })
	return c, nil
}

func listWithin(ctx context.Context, r client.Reader, list client.ObjectList) error {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	return r.List(ctx, list)
}

// PodsOn are the pods on node that hold its capacity: neither finished nor
// being deleted.
func (c *Cluster) PodsOn(node string) []corev1.Pod {
	var out []corev1.Pod
	for _, p := range c.Pods {
		if p.Spec.NodeName == node && running(&p) {
			out = append(out, p)
		}
	}
	return out
}

// Blockers says, for each node, why the autoscaler that scales it won't
// remove it, however little of it is used: Cluster Autoscaler's rules for a
// node it scales, Karpenter's for one a NodePool owns. A node no autoscaler
// found scales gets CodeNotAutoscaled. While what scales the nodes isn't
// known, a node Karpenter doesn't own is judged by Cluster Autoscaler's
// rules, the stricter of the two. Nodes with nothing in the way have no
// entry.
func (c *Cluster) Blockers(a Autoscaler) map[string][]Reason {
	pdbs := pdbsByNamespace(c.PDBs)
	podsByNode := map[string][]*corev1.Pod{}
	for i := range c.Pods {
		p := &c.Pods[i]
		if p.Spec.NodeName != "" && running(p) && !daemonOrMirror(p) {
			podsByNode[p.Spec.NodeName] = append(podsByNode[p.Spec.NodeName], p)
		}
	}
	caScales := a.Has(ClusterAutoscaler) || a.Has(GKEAutopilot) || !a.Known()

	out := map[string][]Reason{}
	for i := range c.Nodes {
		node := &c.Nodes[i]
		pods := podsByNode[node.Name]
		var reasons []Reason
		switch pool := node.Labels[labelKarpenterNodePool]; {
		case pool != "":
			reasons = karpenterBlockers(node, pool, pods, pdbs, a)
		case caScales:
			reasons = clusterAutoscalerBlockers(node, pods, pdbs)
		default:
			reasons = []Reason{{Code: CodeNotAutoscaled, Detail: notAutoscaled(a)}}
		}
		if len(reasons) > 0 {
			out[node.Name] = reasons
		}
	}
	return out
}

func notAutoscaled(a Autoscaler) string {
	if len(a.Scalers) == 0 {
		return "no node autoscaler found"
	}
	return "not in a Karpenter NodePool, and no Cluster Autoscaler found"
}

// clusterAutoscalerBlockers are the reasons Cluster Autoscaler gives for not
// removing a node, from its FAQ's "What types of pods can prevent CA from
// removing a node?". DaemonSet and mirror pods never block it. A pod
// annotated safe-to-evict "true" is moved whatever its storage, owner or
// namespace, though its PodDisruptionBudget still holds.
func clusterAutoscalerBlockers(node *corev1.Node, pods []*corev1.Pod,
	pdbs map[string][]policyv1.PodDisruptionBudget) []Reason {
	var out []Reason
	if node.Annotations[annotationScaleDownDisabled] == enabled {
		out = append(out, Reason{Code: CodeScaleDownDisabled, Detail: annotationScaleDownDisabled + "=true on the node"})
	}
	for _, p := range pods {
		ref := p.Namespace + "/" + p.Name
		covering := coveringPDBs(p, pdbs[p.Namespace])
		out = append(out, pdbReasons(ref, covering)...)
		switch p.Annotations[annotationSafeToEvict] {
		case "false":
			out = append(out, Reason{Code: CodeSafeToEvict, Pod: ref, Detail: annotationSafeToEvict + "=false"})
			continue
		case enabled:
			continue
		}
		if volumes := unsafeLocalVolumes(p); len(volumes) > 0 {
			out = append(out, Reason{Code: CodeLocalStorage, Pod: ref,
				Detail: "local storage in " + strings.Join(volumes, ", ")})
		}
		if metav1.GetControllerOf(p) == nil {
			out = append(out, Reason{Code: CodeNoController, Pod: ref, Detail: "no controller would recreate it"})
		}
		if p.Namespace == metav1.NamespaceSystem && len(covering) == 0 {
			out = append(out, Reason{Code: CodeSystemPod, Pod: ref,
				Detail: "a kube-system pod without a PodDisruptionBudget"})
		}
	}
	return out
}

// karpenterBlockers are the reasons Karpenter gives for not consolidating a
// node, from its disruption docs: do-not-disrupt on the node or a pod, a
// PodDisruptionBudget that allows nothing, or a NodePool that doesn't
// consolidate a node like it.
func karpenterBlockers(node *corev1.Node, pool string, pods []*corev1.Pod,
	pdbs map[string][]policyv1.PodDisruptionBudget, a Autoscaler) []Reason {
	var out []Reason
	if node.Annotations[annotationDoNotDisrupt] == enabled {
		out = append(out, Reason{Code: CodeNodeDoNotDisrupt, Detail: annotationDoNotDisrupt + "=true on the node"})
	}
	if p, ok := a.NodePool(pool); ok {
		switch {
		case p.ConsolidateAfter == ConsolidateAfterNever:
			out = append(out, Reason{Code: CodeConsolidationOff,
				Detail: "NodePool " + pool + " has consolidateAfter: Never"})
		case p.ConsolidationPolicy == ConsolidateWhenEmpty && len(pods) > 0:
			out = append(out, Reason{Code: CodeWhenEmpty,
				Detail: fmt.Sprintf("NodePool %s consolidates only empty nodes, and this one runs %s", pool,
					countPods(len(pods)))})
		}
	}
	for _, p := range pods {
		ref := p.Namespace + "/" + p.Name
		if p.Annotations[annotationDoNotDisrupt] == enabled {
			out = append(out, Reason{Code: CodeDoNotDisrupt, Pod: ref, Detail: annotationDoNotDisrupt + "=true"})
		}
		out = append(out, pdbReasons(ref, coveringPDBs(p, pdbs[p.Namespace]))...)
	}
	return out
}

func pdbReasons(pod string, covering []*policyv1.PodDisruptionBudget) []Reason {
	var out []Reason
	for _, pdb := range covering {
		if pdb.Status.DisruptionsAllowed == 0 {
			out = append(out, Reason{Code: CodePDB, Pod: pod,
				Detail: "PodDisruptionBudget " + pdb.Name + " allows no disruptions"})
		}
	}
	return out
}

func pdbsByNamespace(all []policyv1.PodDisruptionBudget) map[string][]policyv1.PodDisruptionBudget {
	out := map[string][]policyv1.PodDisruptionBudget{}
	for _, pdb := range all {
		out[pdb.Namespace] = append(out[pdb.Namespace], pdb)
	}
	return out
}

// coveringPDBs are the PodDisruptionBudgets whose selector matches pod. A
// budget without a selector covers nothing; an empty one covers every pod
// in its namespace.
func coveringPDBs(pod *corev1.Pod, pdbs []policyv1.PodDisruptionBudget) []*policyv1.PodDisruptionBudget {
	var out []*policyv1.PodDisruptionBudget
	for i := range pdbs {
		pdb := &pdbs[i]
		if pdb.Spec.Selector == nil {
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
		if err != nil || !selector.Matches(labels.Set(pod.Labels)) {
			continue
		}
		out = append(out, pdb)
	}
	return out
}

// unsafeLocalVolumes are pod's hostPath volumes and its emptyDirs not held
// in memory, less those safe-to-evict-local-volumes names.
func unsafeLocalVolumes(pod *corev1.Pod) []string {
	safe := map[string]bool{}
	for name := range strings.SplitSeq(pod.Annotations[annotationSafeToEvictLocalVolumes], ",") {
		safe[strings.TrimSpace(name)] = true
	}
	var out []string
	for _, v := range pod.Spec.Volumes {
		local := v.HostPath != nil || (v.EmptyDir != nil && v.EmptyDir.Medium != corev1.StorageMediumMemory)
		if local && !safe[v.Name] {
			out = append(out, v.Name)
		}
	}
	return out
}

func running(p *corev1.Pod) bool {
	return p.DeletionTimestamp == nil && p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed
}

func daemonOrMirror(p *corev1.Pod) bool {
	if _, ok := p.Annotations[annotationMirrorPod]; ok {
		return true
	}
	owner := metav1.GetControllerOf(p)
	return owner != nil && owner.Kind == "DaemonSet"
}

// PodRequests is what a pod reserves on its node, as the scheduler counts
// it: its containers and restartable init containers together, or its
// largest init container if that's more, plus its overhead.
func PodRequests(p *corev1.Pod) corev1.ResourceList {
	total := corev1.ResourceList{}
	sidecars := corev1.ResourceList{}
	initPeak := corev1.ResourceList{}
	for _, c := range p.Spec.InitContainers {
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			add(sidecars, c.Resources.Requests)
			continue
		}
		// An init container runs alongside the sidecars started before it.
		atStart := sidecars.DeepCopy()
		add(atStart, c.Resources.Requests)
		peak(initPeak, atStart)
	}
	for _, c := range p.Spec.Containers {
		add(total, c.Resources.Requests)
	}
	add(total, sidecars)
	peak(total, initPeak)
	add(total, p.Spec.Overhead)
	return total
}

func add(into, from corev1.ResourceList) {
	for name, q := range from {
		sum := into[name]
		sum.Add(q)
		into[name] = sum
	}
}

func peak(into, from corev1.ResourceList) {
	for name, q := range from {
		if current, ok := into[name]; !ok || q.Cmp(current) > 0 {
			into[name] = q.DeepCopy()
		}
	}
}

func countPods(n int) string {
	if n == 1 {
		return "1 pod"
	}
	return fmt.Sprintf("%d pods", n)
}
