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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/napwake/napwake/internal/discovery"
)

func TestPricesSentence_NodePrices(t *testing.T) {
	assumed := prices{CPUPerHour: 0.031, MemoryPerHour: 0.004, CPUAssumed: true, MemoryAssumed: true}
	m6i := discovery.NodeTypePrice{Provider: "aws", InstanceType: "m6i.large", Region: "us-east-1", Nodes: 3, Listed: true}
	r6i := discovery.NodeTypePrice{Provider: "aws", InstanceType: "r6i.xlarge", Region: "us-east-1", Nodes: 1,
		Listed: true}
	n2 := discovery.NodeTypePrice{Provider: "gcp", InstanceType: "n2-standard-4", Region: "us-central1", Nodes: 1,
		Listed: true}
	m6iSpot := discovery.NodeTypePrice{Provider: "aws", InstanceType: "m6i.large", Region: "us-east-1", Nodes: 2,
		Listed: true, Spot: true, AtSpot: true}
	e2Spot := discovery.NodeTypePrice{Provider: "gcp", InstanceType: "e2-standard-2", Region: "europe-west1",
		Nodes: 3, Listed: true, Spot: true, AtSpot: true}
	hugeSpot := discovery.NodeTypePrice{Provider: "aws", InstanceType: "u7in-32tb.224xlarge", Region: "us-east-1",
		Nodes: 1, Listed: true, Spot: true}
	metal := discovery.NodeTypePrice{InstanceType: "custom.metal", Region: "us-east-1", Nodes: 2}
	unlabelled := discovery.NodeTypePrice{Nodes: 1}
	tests := []struct {
		name      string
		prices    prices
		nodes     discovery.Prices
		workloads []discovery.Workload
		want      string
	}{
		{name: "one node type", prices: assumed, nodes: discovery.Prices{NodeTypes: []discovery.NodeTypePrice{m6i}},
			want: "The on-demand list price of the cluster's nodes (m6i.large, AWS us-east-1)."},
		{name: "several", prices: assumed, nodes: discovery.Prices{NodeTypes: []discovery.NodeTypePrice{m6i, r6i}},
			want: "The on-demand list prices of the cluster's 2 node types (m6i.large and r6i.xlarge, AWS us-east-1)."},
		{name: "two clouds", prices: assumed, nodes: discovery.Prices{NodeTypes: []discovery.NodeTypePrice{m6i, n2}},
			want: "The on-demand list prices of the cluster's 2 node types (m6i.large and n2-standard-4, " +
				"AWS us-east-1 and Google Cloud us-central1)."},
		{name: "some without a list price", prices: assumed,
			nodes: discovery.Prices{NodeTypes: []discovery.NodeTypePrice{m6i, metal}},
			want: "The on-demand list price of the cluster's nodes (m6i.large, AWS us-east-1). 2 nodes of a type without a " +
				"list price (custom.metal) use the assumed $0.031 per vCPU-hour and $0.004 per GiB-hour of memory " +
				"(AWS on-demand, us-east-1)."},
		{name: "nodes without an instance type", prices: assumed,
			nodes: discovery.Prices{NodeTypes: []discovery.NodeTypePrice{m6i, unlabelled}},
			want: "The on-demand list price of the cluster's nodes (m6i.large, AWS us-east-1). 1 node without an instance " +
				"type uses the assumed $0.031 per vCPU-hour and $0.004 per GiB-hour of memory (AWS on-demand, us-east-1)."},
		{name: "your CPU price", prices: prices{CPUPerHour: 0.05, MemoryPerHour: 0.004, MemoryAssumed: true},
			nodes: discovery.Prices{NodeTypes: []discovery.NodeTypePrice{m6i}},
			want: "The on-demand list price of the cluster's nodes (m6i.large, AWS us-east-1). CPU at your price, " +
				"$0.050 per vCPU-hour."},
		{name: "none listed", prices: assumed, nodes: discovery.Prices{NodeTypes: []discovery.NodeTypePrice{unlabelled}},
			want: "Assumed list prices: $0.031 per vCPU-hour and $0.004 per GiB-hour of memory, from AWS on-demand in " +
				"us-east-1. None of the nodes has a list price: 1 node without an instance type."},
		{name: "nodes without a region", prices: assumed, nodes: discovery.Prices{NodeTypes: []discovery.NodeTypePrice{
			{InstanceType: "m6i.large", Nodes: 2}}},
			want: "Assumed list prices: $0.031 per vCPU-hour and $0.004 per GiB-hour of memory, from AWS on-demand in " +
				"us-east-1. None of the nodes has a list price: 2 nodes without a region label (m6i.large)."},
		{name: "nodes not read", prices: assumed,
			nodes: discovery.Prices{NodesUnread: "your access doesn't allow listing nodes"},
			want: "Assumed list prices: $0.031 per vCPU-hour and $0.004 per GiB-hour of memory, from AWS on-demand in " +
				"us-east-1. The nodes weren't read: your access doesn't allow listing nodes."},
		{name: "your prices win", prices: prices{CPUPerHour: 0.05, MemoryPerHour: 0.006},
			nodes: discovery.Prices{NodeTypes: []discovery.NodeTypePrice{m6i}},
			want:  "Your prices: $0.050 per vCPU-hour and $0.006 per GiB-hour of memory."},
		{name: "spot and on-demand", prices: assumed,
			nodes:     discovery.Prices{NodeTypes: []discovery.NodeTypePrice{m6i, m6iSpot}},
			workloads: []discovery.Workload{{OnSpot: true}, {}},
			want: "The on-demand and spot list prices of the cluster's nodes (m6i.large, AWS us-east-1). 1 workload " +
				"runs on spot nodes, priced at spot."},
		{name: "all spot", prices: assumed, nodes: discovery.Prices{NodeTypes: []discovery.NodeTypePrice{e2Spot}},
			workloads: []discovery.Workload{{OnSpot: true}, {OnSpot: true}},
			want: "The spot list price of the cluster's nodes (e2-standard-2, Google Cloud europe-west1). 2 workloads " +
				"run on spot nodes, priced at spot."},
		{name: "spot without a spot price", prices: assumed,
			nodes:     discovery.Prices{NodeTypes: []discovery.NodeTypePrice{m6i, hugeSpot}},
			workloads: []discovery.Workload{{SpotAtOnDemand: true}, {}},
			want: "The on-demand list prices of the cluster's 2 node types (m6i.large and u7in-32tb.224xlarge, AWS " +
				"us-east-1). 1 workload runs on spot nodes of a type without a spot price (u7in-32tb.224xlarge), " +
				"priced at on-demand, so it costs less than shown."},
		{name: "your prices, spot or not", prices: prices{CPUPerHour: 0.05, MemoryPerHour: 0.006},
			nodes:     discovery.Prices{NodeTypes: []discovery.NodeTypePrice{m6iSpot}},
			workloads: []discovery.Workload{{OnSpot: true}},
			want:      "Your prices: $0.050 per vCPU-hour and $0.006 per GiB-hour of memory."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := scanResult{Prices: tt.prices,
				ClusterReport: &discovery.ClusterReport{NodePrices: tt.nodes, Workloads: tt.workloads}}
			assert.Equal(t, tt.want, r.pricesSentence())
		})
	}
}

func TestNodeTypesPhrase_Many(t *testing.T) {
	names := []string{"m6i.large", "m6i.xlarge", "r6i.large", "c6i.large", "c6i.xlarge"}
	types := make([]discovery.NodeTypePrice, 0, len(names))
	for _, name := range names {
		types = append(types, discovery.NodeTypePrice{Provider: "aws", InstanceType: name, Region: "eu-west-1"})
	}
	assert.Equal(t,
		"on-demand list prices of the cluster's 5 node types (m6i.large, m6i.xlarge, r6i.large and 2 more, "+
			"AWS eu-west-1)",
		nodeTypesPhrase(types))
}
