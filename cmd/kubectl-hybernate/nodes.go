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
	"cmp"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
	"github.com/okedeji/hybernate/internal/nodescale"
)

// nodeScalingGuide is the docs page on making node autoscalers remove the
// capacity pausing frees.
const nodeScalingGuide = "https://okedeji.io/hybernate/guides/cluster-autoscaler/"

// poolLabels name the node pool a node belongs to, on each autoscaler and
// cloud, in the order they're read.
var poolLabels = []string{
	"karpenter.sh/nodepool",
	"eks.amazonaws.com/nodegroup",
	"cloud.google.com/gke-nodepool",
	"kubernetes.azure.com/agentpool",
}

// blockersShown caps the reasons a node's row lists; -o json has them all.
const blockersShown = 2

type nodesOptions struct {
	output  string
	timeout time.Duration
}

func nodesCmd(kube *kubeFlags) *cobra.Command {
	opts := nodesOptions{timeout: time.Minute}
	cmd := &cobra.Command{
		Use:   "nodes",
		Short: "Show why nodes aren't scaling down",
		Long: `Nodes shows each node in the cluster, what its pods request of it, and what
keeps the node autoscaler from removing it however little it's used. Pausing
a workload frees what its pods requested, but that comes off the bill only
once a node autoscaler removes the node it ran on.

Above the table it names the node autoscaler it found, Karpenter, Cluster
Autoscaler, EKS Auto Mode or GKE Autopilot, with Karpenter's consolidation
settings, and what Hybernate's paused workloads would request if they were
awake. Below it, how to fix each thing in the way.

It only reads: nodes, pods and PodDisruptionBudgets in every namespace,
Karpenter's NodePools, Cluster Autoscaler's status ConfigMap and the
Deployments in kube-system, and ManagedWorkloads. What your access doesn't
allow reading is left out, and the notes say what.

Examples:
  kubectl hybernate nodes
  kubectl hybernate nodes --context staging -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkOutput(opts.output); err != nil {
				return err
			}
			if err := checkPositive("timeout", opts.timeout); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), opts.timeout)
			defer cancel()
			k8s, at, err := kube.client(ctx)
			if err != nil {
				return fmt.Errorf("building kubernetes client: %w", err)
			}
			result, err := clusterNodes(ctx, k8s)
			if err != nil {
				return timedOut(ctx, err, opts.timeout)
			}
			result.Context = at.context
			result.Cluster = clusterName(at.context)
			return writeOutput(cmd.OutOrStdout(), opts.output, result,
				func() error { return writeNodesTable(cmd.OutOrStdout(), result) })
		},
	}
	addOutputFlag(cmd, &opts.output)
	addTimeoutFlag(cmd, &opts.timeout, "How long to wait for the cluster before giving up")
	return cmd
}

type nodesResult struct {
	Context    string               `json:"context"`
	Cluster    string               `json:"cluster"`
	Autoscaler nodescale.Autoscaler `json:"autoscaler"`
	Nodes      []nodeRow            `json:"nodes"`
	// Paused is what the workloads Hybernate has paused would request if
	// they were awake. Where their pods ran isn't recorded, so it's for
	// the whole cluster rather than by node.
	Paused *pausedRequests `json:"paused,omitempty"`
	Notes  []string        `json:"notes,omitempty"`
}

type nodeRow struct {
	Name                   string `json:"name"`
	Pool                   string `json:"pool,omitempty"`
	InstanceType           string `json:"instanceType,omitempty"`
	AllocatableCPUMillis   int64  `json:"allocatableCPUMillis"`
	AllocatableMemoryBytes int64  `json:"allocatableMemoryBytes"`
	RequestedCPUMillis     int64  `json:"requestedCPUMillis"`
	RequestedMemoryBytes   int64  `json:"requestedMemoryBytes"`
	// Utilisation is the larger of the CPU and memory requested, as a
	// percentage of what the node can allocate: the measure Cluster
	// Autoscaler compares with its scale-down threshold.
	Utilisation int                `json:"utilisationPercent"`
	BlockedBy   []nodescale.Reason `json:"blockedBy"`
}

type pausedRequests struct {
	Workloads   int   `json:"workloads"`
	CPUMillis   int64 `json:"cpuMillis"`
	MemoryBytes int64 `json:"memoryBytes"`
}

func clusterNodes(ctx context.Context, c client.Client) (nodesResult, error) {
	autoscaler, err := nodescale.Detect(ctx, c)
	if err != nil {
		return nodesResult{}, fmt.Errorf("finding the node autoscaler: %w", err)
	}
	cluster, err := nodescale.Read(ctx, c)
	if err != nil {
		return nodesResult{}, err
	}
	result := nodesResult{Autoscaler: autoscaler, Nodes: []nodeRow{}}
	for _, what := range cluster.Unread {
		result.Notes = append(result.Notes, unreadNote(what))
	}
	blockers := cluster.Blockers(autoscaler)
	for i := range cluster.Nodes {
		result.Nodes = append(result.Nodes, nodeRowOf(&cluster.Nodes[i], cluster.PodsOn(cluster.Nodes[i].Name),
			blockers[cluster.Nodes[i].Name]))
	}
	slices.SortStableFunc(result.Nodes, func(a, b nodeRow) int {
		return cmp.Or(cmp.Compare(a.Utilisation, b.Utilisation), cmp.Compare(a.Name, b.Name))
	})

	paused, note, err := pausedWorkloadRequests(ctx, c)
	if err != nil {
		return nodesResult{}, err
	}
	result.Paused = paused
	if note != "" {
		result.Notes = append(result.Notes, note)
	}
	return result, nil
}

func unreadNote(what string) string {
	switch what {
	case "nodes":
		return "your access doesn't allow listing nodes, so none are shown; ask an admin for list on nodes"
	case "pods in every namespace":
		return "your access doesn't allow listing pods in every namespace, so what's requested of each node, " +
			"and what its pods keep it from scaling down, leave them out"
	}
	return "your access doesn't allow listing " + what + ", so the reasons that come from them are left out"
}

func nodeRowOf(n *corev1.Node, pods []corev1.Pod, blockedBy []nodescale.Reason) nodeRow {
	row := nodeRow{
		Name:                   n.Name,
		InstanceType:           n.Labels[corev1.LabelInstanceTypeStable],
		AllocatableCPUMillis:   n.Status.Allocatable.Cpu().MilliValue(),
		AllocatableMemoryBytes: n.Status.Allocatable.Memory().Value(),
		BlockedBy:              blockedBy,
	}
	if row.BlockedBy == nil {
		row.BlockedBy = []nodescale.Reason{}
	}
	for _, label := range poolLabels {
		if pool := n.Labels[label]; pool != "" {
			row.Pool = pool
			break
		}
	}
	for i := range pods {
		requests := nodescale.PodRequests(&pods[i])
		row.RequestedCPUMillis += requests.Cpu().MilliValue()
		row.RequestedMemoryBytes += requests.Memory().Value()
	}
	row.Utilisation = max(percent(row.RequestedCPUMillis, row.AllocatableCPUMillis),
		percent(row.RequestedMemoryBytes, row.AllocatableMemoryBytes))
	return row
}

func percent(part, whole int64) int {
	if whole <= 0 {
		return 0
	}
	return int((part*100 + whole/2) / whole)
}

// pausedWorkloadRequests sums what the workloads Hybernate has paused
// requested before their pause. Without Hybernate, or access to its
// ManagedWorkloads, there's nothing to sum, and the note says why.
func pausedWorkloadRequests(ctx context.Context, c client.Client) (*pausedRequests, string, error) {
	var list v1alpha1.ManagedWorkloadList
	err := c.List(ctx, &list)
	switch {
	case meta.IsNoMatchError(err):
		return nil, "", nil
	case apierrors.IsForbidden(err):
		return nil, "your access doesn't allow listing ManagedWorkloads in every namespace, so what paused " +
			"workloads would request isn't shown", nil
	case err != nil:
		return nil, "", fmt.Errorf("listing ManagedWorkloads: %w", err)
	}
	paused := &pausedRequests{}
	for i := range list.Items {
		cpu, memory, ok := freedByPause(&list.Items[i])
		if !ok {
			continue
		}
		paused.Workloads++
		paused.CPUMillis += cpu
		paused.MemoryBytes += memory
	}
	return paused, "", nil
}

// freedByPause is what a paused workload's pods requested before the
// pause: what the operator recorded it freed, or its replicas times what
// one requested.
func freedByPause(w *v1alpha1.ManagedWorkload) (cpuMillis, memoryBytes int64, ok bool) {
	if w.Status.Phase != v1alpha1.PhasePaused || w.Status.Pause == nil {
		return 0, 0, false
	}
	if c := w.Status.Cost; c != nil && c.ResourceReduction != nil {
		return c.ResourceReduction.CPUMillis, c.ResourceReduction.MemoryBytes, true
	}
	if r := w.Status.Pause.Resources; r != nil {
		replicas := int64(w.Status.Pause.PreviousReplicas)
		return r.CPUMillis * replicas, r.MemoryBytes * replicas, true
	}
	return 0, 0, true
}

func writeNodesTable(w io.Writer, result nodesResult) error {
	p := &printer{w: w}
	p.line("%s: %s", result.Cluster, countOf(len(result.Nodes), "node"))
	p.line("%s", nodeAutoscalerLine(result.Autoscaler))
	if pr := result.Paused; pr != nil && pr.Workloads > 0 {
		p.line("Hybernate's %s would request %s and %s of memory if awake.", countOf(pr.Workloads, "paused workload"),
			vcpu(pr.CPUMillis), gib(pr.MemoryBytes))
		p.line("Where their pods ran isn't recorded, so that's for the whole cluster rather than by node.")
	}
	p.line("")

	if len(result.Nodes) > 0 {
		tw := tabwriter.NewWriter(p, 0, 0, 3, ' ', 0)
		_, _ = fmt.Fprintln(tw, "  NODE\tPOOL\tINSTANCE TYPE\tCPU REQUESTED\tMEMORY REQUESTED\tUTILISATION\tBLOCKED BY")
		for _, n := range result.Nodes {
			_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%.1f of %s\t%.1f of %s\t%d%%\t%s\n", n.Name, cmp.Or(n.Pool, "-"),
				cmp.Or(n.InstanceType, "-"), float64(n.RequestedCPUMillis)/1000, vcpu(n.AllocatableCPUMillis),
				float64(n.RequestedMemoryBytes)/(1<<30), gib(n.AllocatableMemoryBytes), n.Utilisation,
				blockedCell(n.BlockedBy))
		}
		_ = tw.Flush() // writes through p, which keeps the first error
		p.line("")
	}

	if len(result.Notes) > 0 {
		p.line("Notes:")
		for _, n := range result.Notes {
			p.line("  - %s", oneLine(n))
		}
		p.line("")
	}
	writeNodeNextSteps(p, result.Nodes)
	return p.err
}

func blockedCell(reasons []nodescale.Reason) string {
	if len(reasons) == 0 {
		return "-"
	}
	cells := make([]string, 0, blockersShown+1)
	for i, r := range reasons {
		if i == blockersShown {
			cells = append(cells, fmt.Sprintf("and %d more", len(reasons)-blockersShown))
			break
		}
		cell := blockerLabels[r.Code]
		if r.Pod != "" {
			cell += " (" + r.Pod + ")"
		}
		cells = append(cells, oneLine(cell))
	}
	return strings.Join(cells, "; ")
}

// blockerLabels are how a reason is named in a node's row.
var blockerLabels = map[nodescale.Code]string{
	nodescale.CodeSafeToEvict:       "safe-to-evict=false",
	nodescale.CodeDoNotDisrupt:      "do-not-disrupt",
	nodescale.CodeNodeDoNotDisrupt:  "do-not-disrupt on the node",
	nodescale.CodePDB:               "PDB allows no disruption",
	nodescale.CodeLocalStorage:      "local storage",
	nodescale.CodeNoController:      "bare pod",
	nodescale.CodeSystemPod:         "kube-system pod without PDB",
	nodescale.CodeScaleDownDisabled: "scale-down disabled",
	nodescale.CodeWhenEmpty:         "NodePool consolidates only empty nodes",
	nodescale.CodeConsolidationOff:  "NodePool never consolidates",
	nodescale.CodeNotAutoscaled:     "not autoscaled",
}

// blockerFixes say how to clear each reason, in the order next steps list
// them: those about the whole cluster or a node pool first.
var blockerFixes = []struct {
	code nodescale.Code
	fix  string
}{
	{nodescale.CodeNotAutoscaled, "No node autoscaler scales these nodes, so freed capacity stays on the bill. " +
		"Enable Cluster Autoscaler on their node group, or move them into a Karpenter NodePool."},
	{nodescale.CodeConsolidationOff, "A NodePool has consolidateAfter: Never. Set spec.disruption.consolidateAfter " +
		"to a duration, such as 5m."},
	{nodescale.CodeWhenEmpty, "A NodePool consolidates only empty nodes, which a node running anything never is. " +
		"Set spec.disruption.consolidationPolicy: WhenEmptyOrUnderutilized."},
	{nodescale.CodeScaleDownDisabled, "A node is annotated cluster-autoscaler.kubernetes.io/scale-down-disabled. " +
		"Remove it: kubectl annotate node NODE cluster-autoscaler.kubernetes.io/scale-down-disabled-"},
	{nodescale.CodeNodeDoNotDisrupt, "A node is annotated karpenter.sh/do-not-disrupt. Remove it: " +
		"kubectl annotate node NODE karpenter.sh/do-not-disrupt-"},
	{nodescale.CodePDB, "A PodDisruptionBudget allows no disruptions, so its pods can't be moved. Raise " +
		"maxUnavailable, lower minAvailable, or run more replicas than it requires."},
	{nodescale.CodeSafeToEvict, "A pod is annotated cluster-autoscaler.kubernetes.io/safe-to-evict: \"false\". " +
		"Remove it from the pod template of anything that can be restarted elsewhere."},
	{nodescale.CodeDoNotDisrupt, "A pod is annotated karpenter.sh/do-not-disrupt. Remove it from the pod " +
		"template, or set a duration, such as 30m, to protect pods only while they start."},
	{nodescale.CodeLocalStorage, "A pod has an emptyDir or hostPath volume, which Cluster Autoscaler won't move " +
		"while --skip-nodes-with-local-storage is on, its default upstream. Name the volumes it can lose in the " +
		"pod template's cluster-autoscaler.kubernetes.io/safe-to-evict-local-volumes annotation."},
	{nodescale.CodeNoController, "A pod has no controller to recreate it elsewhere. Run it from a Deployment or a " +
		"Job, delete it, or annotate it cluster-autoscaler.kubernetes.io/safe-to-evict: \"true\"."},
	{nodescale.CodeSystemPod, "A kube-system pod has no PodDisruptionBudget, which Cluster Autoscaler needs to " +
		"move it while --skip-nodes-with-system-pods is on, its default. Give its workload a " +
		"PodDisruptionBudget that allows a disruption."},
}

func writeNodeNextSteps(p *printer, nodes []nodeRow) {
	examples := map[nodescale.Code]nodescale.Reason{}
	for _, n := range nodes {
		for _, r := range n.BlockedBy {
			if _, seen := examples[r.Code]; !seen {
				examples[r.Code] = r
			}
		}
	}
	if len(examples) == 0 {
		return
	}
	p.line("Next steps:")
	step := 0
	for _, f := range blockerFixes {
		r, ok := examples[f.code]
		if !ok {
			continue
		}
		step++
		p.line("  %d. %s", step, f.fix)
		p.line("     For example: %s", oneLine(strings.TrimPrefix(r.Pod+": "+r.Detail, ": ")))
	}
	p.line("  See %s", nodeScalingGuide)
	p.line("")
}

// nodeAutoscalerLine names what scales the cluster's nodes, for the top of
// status, scan and nodes, since it decides whether freed capacity ever
// comes off the bill.
func nodeAutoscalerLine(a nodescale.Autoscaler) string {
	if !a.Known() {
		return fmt.Sprintf("Node autoscaler: unknown, as your access doesn't allow reading %s; freed capacity "+
			"comes off the bill only as nodes scale down", joinOr(a.Unread))
	}
	if len(a.Scalers) == 0 {
		return "Node autoscaler: none found — freed capacity won't lower the bill until nodes scale down; see " +
			nodeScalingGuide
	}
	parts := make([]string, 0, len(a.Scalers))
	for _, s := range a.Scalers {
		parts = append(parts, describeScaler(s, a.NodePools))
	}
	return "Node autoscaler: " + strings.Join(parts, " and ")
}

func describeScaler(s nodescale.Scaler, pools []nodescale.NodePool) string {
	switch s.Kind {
	case nodescale.Karpenter, nodescale.EKSAutoMode:
		if c := consolidation(pools); c != "" {
			return fmt.Sprintf("%s (%s)", s.Kind, c)
		}
	case nodescale.GKEAutopilot:
		return string(s.Kind) + " (scales nodes itself)"
	case nodescale.ClusterAutoscaler:
		name := string(s.Kind)
		if s.Provider != "" {
			name += " on " + s.Provider
		}
		if s.Hint {
			name += fmt.Sprintf(" (from %s; it may not be running)", s.Evidence)
		}
		return name
	}
	return string(s.Kind)
}

// consolidation says how Karpenter's NodePools consolidate: once, when
// they all do it alike, or pool by pool.
func consolidation(pools []nodescale.NodePool) string {
	if len(pools) == 0 {
		return ""
	}
	described := make([]string, len(pools))
	for i, p := range pools {
		described[i] = consolidates(p)
	}
	if len(slices.Compact(slices.Clone(described))) == 1 {
		return described[0]
	}
	parts := make([]string, len(pools))
	for i, p := range pools {
		parts[i] = "NodePool " + p.Name + " " + described[i]
	}
	return strings.Join(parts, "; ")
}

func consolidates(p nodescale.NodePool) string {
	if p.ConsolidateAfter == nodescale.ConsolidateAfterNever {
		return "never consolidates"
	}
	var s string
	switch p.ConsolidationPolicy {
	case nodescale.ConsolidateWhenEmpty:
		s = "consolidates only empty nodes"
	case nodescale.ConsolidateBalanced:
		s = "consolidates underutilized nodes when that's worth the disruption"
	default:
		s = "consolidates when underutilized"
	}
	if after := p.ConsolidateAfter; after != "" && after != "0s" {
		s += " after " + after
	}
	return s
}

func joinOr(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
}
