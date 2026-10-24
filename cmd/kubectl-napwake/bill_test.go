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
	"html"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/napwake/napwake/internal/discovery"
	"github.com/napwake/napwake/internal/nodescale"
)

// The Prices line is short where a short line says every fact, and the full
// sentence where it can't: some nodes priced and others not, the nodes
// unread, a workload on spot priced at on-demand, prices from more than one
// place, or one price the user's and the other assumed.
func TestPricing(t *testing.T) {
	assumed := prices{CPUPerHour: 0.031, MemoryPerHour: 0.004, CPUAssumed: true, MemoryAssumed: true}
	m6i := discovery.NodeTypePrice{Provider: "aws", InstanceType: "m6i.large", Region: "us-east-1", Nodes: 3, Listed: true}
	r6i := discovery.NodeTypePrice{Provider: "aws", InstanceType: "r6i.xlarge", Region: "us-east-1", Nodes: 1,
		Listed: true}
	c6i := discovery.NodeTypePrice{Provider: "aws", InstanceType: "c6i.large", Region: "us-east-1", Nodes: 1,
		Listed: true}
	m7i := discovery.NodeTypePrice{Provider: "aws", InstanceType: "m7i.large", Region: "us-east-1", Nodes: 1,
		Listed: true}
	m6iSpot := discovery.NodeTypePrice{Provider: "aws", InstanceType: "m6i.large", Region: "us-east-1", Nodes: 2,
		Listed: true, Spot: true, AtSpot: true}
	n2 := discovery.NodeTypePrice{Provider: "gcp", InstanceType: "n2-standard-4", Region: "us-central1", Nodes: 1,
		Listed: true}
	hugeSpot := discovery.NodeTypePrice{Provider: "aws", InstanceType: "u7in-32tb.224xlarge", Region: "us-east-1",
		Nodes: 1, Listed: true, Spot: true}
	metal := discovery.NodeTypePrice{InstanceType: "custom.metal", Region: "us-east-1", Nodes: 2}
	unlabelled := discovery.NodeTypePrice{Nodes: 1}
	tests := []struct {
		name      string
		prices    prices
		nodes     discovery.Prices
		workloads []discovery.Workload
		want      *pricing
		line      string
	}{
		{name: "one node type", prices: assumed, nodes: discovery.Prices{NodeTypes: []discovery.NodeTypePrice{m6i}},
			want: &pricing{Source: pricedAtList, Provider: "AWS", Region: "us-east-1", NodeTypes: []string{"m6i.large"}},
			line: "Prices: AWS us-east-1 list prices · m6i.large"},
		{name: "spot and on-demand", prices: assumed,
			nodes:     discovery.Prices{NodeTypes: []discovery.NodeTypePrice{m6i, r6i, c6i, m6iSpot}},
			workloads: []discovery.Workload{{OnSpot: true}, {OnSpot: true}, {OnSpot: true}, {}},
			want: &pricing{Source: pricedAtList, Provider: "AWS", Region: "us-east-1", Spot: 3,
				NodeTypes: []string{"m6i.large", "r6i.xlarge", "c6i.large"}},
			line: "Prices: AWS us-east-1 list prices · 3 node types · 3 workloads on spot, priced at spot"},
		{name: "one on spot", prices: assumed, nodes: discovery.Prices{NodeTypes: []discovery.NodeTypePrice{m6iSpot}},
			workloads: []discovery.Workload{{OnSpot: true}},
			want: &pricing{Source: pricedAtList, Provider: "AWS", Region: "us-east-1", Spot: 1,
				NodeTypes: []string{"m6i.large"}},
			line: "Prices: AWS us-east-1 list prices · m6i.large · 1 workload on spot, priced at spot"},
		{name: "many node types", prices: assumed,
			nodes: discovery.Prices{NodeTypes: []discovery.NodeTypePrice{m6i, r6i, c6i, m7i}},
			want: &pricing{Source: pricedAtList, Provider: "AWS", Region: "us-east-1",
				NodeTypes: []string{"m6i.large", "r6i.xlarge", "c6i.large", "1 more"}},
			line: "Prices: AWS us-east-1 list prices · 4 node types"},
		{name: "yours", prices: prices{CPUPerHour: 0.045, MemoryPerHour: 0.006},
			nodes: discovery.Prices{NodeTypes: []discovery.NodeTypePrice{m6iSpot}},
			want:  &pricing{Source: pricedYours, CPUPrice: "$0.045", MemoryPrice: "$0.006"},
			line:  "Prices: yours, $0.045 per vCPU-hour and $0.006 per GiB-hour"},
		{name: "none listed", prices: assumed, nodes: discovery.Prices{NodeTypes: []discovery.NodeTypePrice{unlabelled}},
			want: &pricing{Source: pricedAssumed, Provider: "AWS", Region: "us-east-1", CPUPrice: "$0.031",
				MemoryPrice: "$0.004"},
			line: "Prices: assumed, AWS us-east-1 on-demand at $0.031 per vCPU-hour and $0.004 per GiB-hour, as your " +
				"nodes have no list price"},
		{name: "some without a list price", prices: assumed,
			nodes: discovery.Prices{NodeTypes: []discovery.NodeTypePrice{m6i, metal}}},
		{name: "two clouds", prices: assumed, nodes: discovery.Prices{NodeTypes: []discovery.NodeTypePrice{m6i, n2}}},
		{name: "your CPU price", prices: prices{CPUPerHour: 0.05, MemoryPerHour: 0.004, MemoryAssumed: true},
			nodes: discovery.Prices{NodeTypes: []discovery.NodeTypePrice{m6i}}},
		{name: "nodes not read", prices: assumed,
			nodes: discovery.Prices{NodesUnread: "your access doesn't allow listing nodes"}},
		{name: "no nodes to price", prices: assumed},
		{name: "spot without a spot price", prices: assumed,
			nodes:     discovery.Prices{NodeTypes: []discovery.NodeTypePrice{m6i, hugeSpot}},
			workloads: []discovery.Workload{{SpotAtOnDemand: true}, {}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := scanResult{Prices: tt.prices,
				ClusterReport: &discovery.ClusterReport{NodePrices: tt.nodes, Workloads: tt.workloads}}
			assert.Equal(t, tt.want, r.pricing())
			if tt.want == nil {
				assert.Equal(t, r.costsSentence(), r.pricesLine(), "the full sentence")
				return
			}
			assert.Equal(t, tt.line, r.pricesLine())
		})
	}
}

// The Your bill line says what turns freed capacity into money: a node
// autoscaler removing nodes, or, on GKE Autopilot, the pods stopping, since
// it bills what pods request. Without one found, the bill won't drop until
// something removes nodes, and the guide says how; when the user's access
// can't tell, it says what it couldn't read.
func TestNodeScale(t *testing.T) {
	tests := []struct {
		name string
		a    nodescale.Autoscaler
		want nodeScale
		line string
	}{
		{name: "Karpenter", a: nodescale.Autoscaler{Scalers: []nodescale.Scaler{{Kind: nodescale.Karpenter}},
			NodePools: []nodescale.NodePool{{Name: "default", ConsolidateAfter: "1m"}}},
			want: nodeScale{State: scaleFound, Kinds: []string{"Karpenter"},
				Detail: "it consolidates when underutilized after 1m"},
			line: "Your bill: drops only as Karpenter removes nodes (it consolidates when underutilized after 1m)"},
		{name: "Karpenter's NodePools alike in nothing", a: nodescale.Autoscaler{
			Scalers: []nodescale.Scaler{{Kind: nodescale.Karpenter}},
			NodePools: []nodescale.NodePool{{Name: "batch", ConsolidationPolicy: nodescale.ConsolidateWhenEmpty},
				{Name: "default", ConsolidateAfter: nodescale.ConsolidateAfterNever}}},
			want: nodeScale{State: scaleFound, Kinds: []string{"Karpenter"},
				Detail: "NodePool batch consolidates only empty nodes; NodePool default never consolidates"},
			line: "Your bill: drops only as Karpenter removes nodes (NodePool batch consolidates only empty nodes; " +
				"NodePool default never consolidates)"},
		{name: "Cluster Autoscaler", a: nodescale.Autoscaler{Scalers: []nodescale.Scaler{
			{Kind: nodescale.ClusterAutoscaler, Provider: "GKE"}}},
			want: nodeScale{State: scaleFound, Kinds: []string{"Cluster Autoscaler on GKE"}},
			line: "Your bill: drops only as Cluster Autoscaler on GKE removes nodes"},
		{name: "a hint of Cluster Autoscaler", a: nodescale.Autoscaler{Scalers: []nodescale.Scaler{
			{Kind: nodescale.ClusterAutoscaler, Evidence: "a Deployment named cluster-autoscaler in kube-system, which " +
				"has written no status", Hint: true}}},
			want: nodeScale{State: scaleFound, Kinds: []string{"Cluster Autoscaler"},
				Detail: "it may not be running, as all that shows it is a Deployment named cluster-autoscaler in " +
					"kube-system, which has written no status"},
			line: "Your bill: drops only as Cluster Autoscaler removes nodes (it may not be running, as all that " +
				"shows it is a Deployment named cluster-autoscaler in kube-system, which has written no status)"},
		{name: "Karpenter, and a hint of Cluster Autoscaler", a: nodescale.Autoscaler{Scalers: []nodescale.Scaler{
			{Kind: nodescale.Karpenter}, {Kind: nodescale.ClusterAutoscaler, Evidence: "a Deployment named " +
				"cluster-autoscaler in kube-system, which has written no status", Hint: true}},
			NodePools: []nodescale.NodePool{{Name: "default"}}},
			want: nodeScale{State: scaleFound, Kinds: []string{"Karpenter", "Cluster Autoscaler"},
				Detail: "Karpenter consolidates when underutilized; Cluster Autoscaler may not be running, as all " +
					"that shows it is a Deployment named cluster-autoscaler in kube-system, which has written no status"},
			line: "Your bill: drops only as Karpenter or Cluster Autoscaler removes nodes (Karpenter consolidates " +
				"when underutilized; Cluster Autoscaler may not be running, as all that shows it is a Deployment " +
				"named cluster-autoscaler in kube-system, which has written no status)"},
		{name: "both", a: nodescale.Autoscaler{Scalers: []nodescale.Scaler{{Kind: nodescale.Karpenter},
			{Kind: nodescale.ClusterAutoscaler}}, NodePools: []nodescale.NodePool{{Name: "default"}}},
			want: nodeScale{State: scaleFound, Kinds: []string{"Karpenter", "Cluster Autoscaler"},
				Detail: "Karpenter consolidates when underutilized"},
			line: "Your bill: drops only as Karpenter or Cluster Autoscaler removes nodes (Karpenter consolidates " +
				"when underutilized)"},
		{name: "GKE Autopilot", a: nodescale.Autoscaler{Scalers: []nodescale.Scaler{{Kind: nodescale.GKEAutopilot}}},
			want: nodeScale{State: scalePods, Kinds: []string{"GKE Autopilot"}},
			line: "Your bill: drops as pods stop, since GKE Autopilot bills what pods request"},
		{name: "none found", want: nodeScale{State: scaleNone, FixURL: nodeScalingGuide},
			line: "Your bill: won't drop until something removes nodes, and no node autoscaler was found. See " +
				nodeScalingGuide},
		{name: "unknown", a: nodescale.Autoscaler{Unread: []string{"Karpenter NodePools", "nodes"}},
			want: nodeScale{State: scaleUnknown, Detail: "Karpenter NodePools or nodes"},
			line: "Your bill: your access can't read the node autoscaler (Karpenter NodePools or nodes)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, nodeScaleFor(tt.a))
			assert.Equal(t, tt.line, billLine(tt.a))
		})
	}
}

var tags = regexp.MustCompile(`<[^>]+>`)

// rowText is what a page's labelled row says, as a reader sees it.
func rowText(t *testing.T, page, label string) string {
	t.Helper()
	m := regexp.MustCompile(`<span class="row-k">` + label + `</span><p class="prices">(.*?)</p>`).
		FindStringSubmatch(page)
	require.Len(t, m, 2, "the page has a %s row", label)
	return html.UnescapeString(tags.ReplaceAllString(m[1], ""))
}

// The pages' Prices and Your bill rows say what the terminal says, built
// from the same fields.
func TestPages_PricesAndBillRows(t *testing.T) {
	m6i := discovery.NodeTypePrice{Provider: "aws", InstanceType: "m6i.large", Region: "us-east-1", Nodes: 3, Listed: true}
	m6iSpot := discovery.NodeTypePrice{Provider: "aws", InstanceType: "m6i.xlarge", Region: "us-east-1", Nodes: 2,
		Listed: true, Spot: true, AtSpot: true}
	karpenter := nodescale.Autoscaler{Scalers: []nodescale.Scaler{{Kind: nodescale.Karpenter}},
		NodePools: []nodescale.NodePool{{Name: "default", ConsolidateAfter: "1m"}}}
	tests := []struct {
		name   string
		prices prices
		nodes  discovery.Prices
		a      nodescale.Autoscaler
		price  string
		bill   string
	}{
		{name: "list prices and Karpenter", prices: prices{CPUPerHour: 0.031, MemoryPerHour: 0.004, CPUAssumed: true,
			MemoryAssumed: true}, nodes: discovery.Prices{NodeTypes: []discovery.NodeTypePrice{m6i, m6iSpot}}, a: karpenter,
			price: "AWS us-east-1 list prices · m6i.large, m6i.xlarge · 1 workload on spot, priced at spot",
			bill:  "drops only as Karpenter removes nodes (it consolidates when underutilized after 1m)"},
		{name: "yours, and no autoscaler", prices: prices{CPUPerHour: 0.045, MemoryPerHour: 0.006},
			price: "yours, $0.045 per vCPU-hour and $0.006 per GiB-hour",
			bill:  "won't drop until something removes nodes, and no node autoscaler was found. How to fix it"},
		{name: "assumed, and unknown", prices: prices{CPUPerHour: 0.031, MemoryPerHour: 0.004, CPUAssumed: true,
			MemoryAssumed: true}, nodes: discovery.Prices{NodeTypes: []discovery.NodeTypePrice{{Nodes: 2}}},
			a: nodescale.Autoscaler{Unread: []string{"nodes"}},
			price: "assumed, AWS us-east-1 on-demand at $0.031 per vCPU-hour and $0.004 per GiB-hour, as your nodes " +
				"have no list price",
			bill: "your access can't read the node autoscaler (nodes)"},
		{name: "a mix only the sentence can say", prices: prices{CPUPerHour: 0.05, MemoryPerHour: 0.004,
			MemoryAssumed: true}, nodes: discovery.Prices{NodeTypes: []discovery.NodeTypePrice{m6i}},
			a: nodescale.Autoscaler{Scalers: []nodescale.Scaler{{Kind: nodescale.GKEAutopilot}}},
			price: "Costs use the on-demand list price of the cluster's nodes (m6i.large, AWS us-east-1). CPU at your " +
				"price, $0.050 per vCPU-hour. 1 workload runs on spot nodes, priced at spot.",
			bill: "drops as pods stop, since GKE Autopilot bills what pods request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := historyResult()
			result.Prices, result.NodePrices, result.NodeAutoscaler = tt.prices, tt.nodes, tt.a
			result.Workloads[0].OnSpot = true

			scan := renderHTML(t, result)
			assert.Equal(t, tt.price, rowText(t, scan, "Prices"))
			assert.Equal(t, tt.bill, rowText(t, scan, "Your bill"))

			var out bytes.Buffer
			require.NoError(t, writeStatusHTML(&out, statusResult{Cluster: "staging", NodeAutoscaler: tt.a},
				24*time.Hour, statusNow))
			assert.Equal(t, tt.bill, rowText(t, out.String(), "Your bill"), "the status page's")
			if tt.a.Known() && len(tt.a.Scalers) == 0 {
				assert.Contains(t, out.String(), `<a href="`+nodeScalingGuide+`">How to fix it</a>`)
			}
		})
	}
}

// The scan's table says what the bill rests on at the top, and what the
// costs are priced at at the end, in the short lines.
func TestWriteScan_PricesAndBillLines(t *testing.T) {
	result := historyResult()
	result.NodePrices = discovery.Prices{NodeTypes: []discovery.NodeTypePrice{{Provider: "aws",
		InstanceType: "m6i.large", Region: "us-east-1", Nodes: 3, Listed: true}}}
	result.NodeAutoscaler = nodescale.Autoscaler{Scalers: []nodescale.Scaler{{Kind: nodescale.ClusterAutoscaler,
		Provider: "EKS"}}}
	var out bytes.Buffer

	require.NoError(t, writeScan(&out, result, scanOptions{output: "table"}))

	got := out.String()
	assert.True(t, strings.HasPrefix(got, "staging (EKS us-east-1): 5 workloads in 5 namespaces\n"+
		"Your bill: drops only as Cluster Autoscaler on EKS removes nodes\nHistory: "), got)
	assert.Contains(t, got, "\nPrices: AWS us-east-1 list prices · m6i.large\nPass --cpu-price and --memory-price "+
		"for yours.\n")
	assert.NotContains(t, got, "Node autoscaler:")
	assert.NotContains(t, got, "Costs use")
	assert.NotContains(t, got, "What pausing frees comes off the bill", "Your bill says it")
}
