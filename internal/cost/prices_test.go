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

package cost

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Each provider's labels find the node's list price. The bounds hold
// across price changes; the exact figures come from the generated table.
func TestListPriceOf(t *testing.T) {
	tests := []struct {
		name             string
		labels           map[string]string
		provider         string
		vcpu, memoryGiB  float64
		minHour, maxHour float64
	}{
		{name: "EKS", provider: "aws", vcpu: 2, memoryGiB: 8, minHour: 0.05, maxHour: 0.2,
			labels: nodeLabels("m6i.large", "us-east-1")},
		{name: "GKE", provider: "gcp", vcpu: 4, memoryGiB: 16, minHour: 0.1, maxHour: 0.4,
			labels: nodeLabels("n2-standard-4", "us-central1")},
		{name: "AKS, by ARM size name", provider: "azure", vcpu: 4, memoryGiB: 16, minHour: 0.1, maxHour: 0.4,
			labels: nodeLabels("Standard_D4s_v5", "eastus")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, ok := ListPriceOf(NodeTypeOf(tt.labels))

			require.True(t, ok)
			assert.Equal(t, tt.provider, p.Provider)
			hourly := ComputeHourly(tt.vcpu, tt.memoryGiB, p.Rates)
			assert.Greater(t, hourly, tt.minHour)
			assert.Less(t, hourly, tt.maxHour)
		})
	}
}

// An Azure constrained vCPU size is billed as its base size with fewer
// vCPUs enabled, so it's priced at the base size's hourly price, spread
// over the vCPUs it has.
func TestListPriceOf_AzureConstrainedVCPUs(t *testing.T) {
	base, ok := ListPriceOf(NodeTypeOf(nodeLabels("Standard_E4s_v5", "eastus")))
	require.True(t, ok)
	constrained, ok := ListPriceOf(NodeTypeOf(nodeLabels("Standard_E4-2s_v5", "eastus")))
	require.True(t, ok, "found as its base size, not the default rates")

	assert.Equal(t, "azure", constrained.Provider)
	assert.InDelta(t, ComputeHourly(4, 32, base.Rates), ComputeHourly(2, 32, constrained.Rates), 1e-9,
		"the same instance price, on 2 vCPUs")
}

func TestTableName(t *testing.T) {
	tests := []struct {
		instanceType string
		want         string
		vcpus        float64
	}{
		{instanceType: "m6i.large", want: "m6i.large"},
		{instanceType: "n2-standard-4", want: "n2-standard-4"},
		{instanceType: "Standard_D4s_v5", want: "d4sv5"},
		{instanceType: "Standard_E4-2s_v5", want: "e4sv5", vcpus: 2},
		{instanceType: "Standard_E64-16ds_v4", want: "e64dsv4", vcpus: 16},
		{instanceType: "Standard_M8-2ms", want: "m8ms", vcpus: 2},
		{instanceType: "Standard_DS11-1_v2", want: "ds11v2", vcpus: 1},
	}
	for _, tt := range tests {
		t.Run(tt.instanceType, func(t *testing.T) {
			name, vcpus := tableName(tt.instanceType)
			assert.Equal(t, tt.want, name)
			assert.InDelta(t, tt.vcpus, vcpus, 0)
		})
	}
}

func TestListPriceOf_NotListed(t *testing.T) {
	tests := map[string]map[string]string{
		"unknown type":         nodeLabels("custom.metal", "us-east-1"),
		"region it isn't sold": nodeLabels("m6i.large", "mars-north-1"),
		"no labels":            {},
		"kind node":            {"kubernetes.io/hostname": "kind-control-plane"},
	}
	for name, labels := range tests {
		t.Run(name, func(t *testing.T) {
			_, ok := ListPriceOf(NodeTypeOf(labels))
			assert.False(t, ok)
		})
	}
}

// The instance's whole price is split between its vCPUs and memory, so the
// rates price the instance itself at exactly its list price.
func TestSplitHourly(t *testing.T) {
	for _, shape := range []struct{ vcpu, memoryGiB float64 }{{2, 8}, {2, 16}, {8, 16}, {96, 768}} {
		r := splitHourly(0.5, shape.vcpu, shape.memoryGiB)

		assert.InDelta(t, 0.5, ComputeHourly(shape.vcpu, shape.memoryGiB, r), 1e-9)
		assert.InDelta(t, cpuToMemory, r.CPUPerHour/r.MemoryPerHour, 1e-9, "CPU and memory keep the default proportion")
		assert.Equal(t, DefaultRates.StoragePerMonth, r.StoragePerMonth)
	}
}

func TestNodeTypeOf_Spot(t *testing.T) {
	tests := map[string]struct {
		key, value string
		spot       bool
	}{
		"Karpenter":            {"karpenter.sh/capacity-type", "spot", true},
		"Karpenter, on-demand": {"karpenter.sh/capacity-type", "on-demand", false},
		"EKS node group":       {"eks.amazonaws.com/capacityType", "SPOT", true},
		"GKE spot":             {"cloud.google.com/gke-spot", "true", true},
		"GKE preemptible":      {"cloud.google.com/gke-preemptible", "true", true},
		"AKS":                  {"kubernetes.azure.com/scalesetpriority", "spot", true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			labels := nodeLabels("m6i.large", "us-east-1")
			labels[tt.key] = tt.value
			assert.Equal(t, tt.spot, NodeTypeOf(labels).Spot)
		})
	}
}

// Every line of the generated table parses, so a bad regeneration fails
// here rather than leaving nodes priced at the defaults.
func TestPriceTableParses(t *testing.T) {
	gz, err := gzip.NewReader(bytes.NewReader(pricesFile))
	require.NoError(t, err)
	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	providers, spot := map[string]int{}, map[string]int{}
	for line := 1; sc.Scan(); line++ {
		if strings.HasPrefix(sc.Text(), "#") {
			continue
		}
		fields := strings.Split(sc.Text(), ",")
		require.GreaterOrEqual(t, len(fields), 5, "line %d", line)
		for _, field := range fields[4:] {
			_, prices, ok := strings.Cut(field, "=")
			require.True(t, ok, "line %d: %q", line, field)
			l, err := parseListing(fields, prices, line, 0)
			require.NoError(t, err)
			require.Positive(t, l.onDemand.CPUPerHour, "line %d", line)
			if l.spot != nil {
				require.Positive(t, l.spot.CPUPerHour, "line %d", line)
				require.LessOrEqual(t, l.spot.CPUPerHour, l.onDemand.CPUPerHour, "line %d: spot over on-demand", line)
				spot[fields[0]]++
			}
		}
		providers[fields[0]]++
	}
	require.NoError(t, sc.Err())
	for _, provider := range []string{"aws", "azure", "gcp"} {
		assert.Greater(t, providers[provider], 100, "%s instance types", provider)
		assert.Positive(t, spot[provider], "%s spot prices", provider)
	}
}

// A spot node is priced at its type's spot price, which each provider
// publishes for the common types, and says so.
func TestListPriceOf_Spot(t *testing.T) {
	tests := map[string]map[string]string{
		"EKS":       withLabel(nodeLabels("m6i.large", "us-east-1"), "eks.amazonaws.com/capacityType", "SPOT"),
		"Karpenter": withLabel(nodeLabels("m6i.large", "us-east-1"), "karpenter.sh/capacity-type", "spot"),
		"GKE":       withLabel(nodeLabels("e2-standard-2", "europe-west1"), "cloud.google.com/gke-spot", "true"),
		"AKS": withLabel(nodeLabels("Standard_D4s_v5", "eastus"), "kubernetes.azure.com/scalesetpriority",
			"spot"),
	}
	for name, labels := range tests {
		t.Run(name, func(t *testing.T) {
			onDemand, ok := ListPriceOf(NodeType{InstanceType: labels["node.kubernetes.io/instance-type"],
				Region: labels["topology.kubernetes.io/region"]})
			require.True(t, ok)
			spot, ok := ListPriceOf(NodeTypeOf(labels))
			require.True(t, ok)

			assert.True(t, spot.Spot)
			assert.False(t, onDemand.Spot)
			assert.Equal(t, onDemand.Provider, spot.Provider)
			assert.Less(t, spot.Rates.CPUPerHour, onDemand.Rates.CPUPerHour)
			assert.Less(t, spot.Rates.MemoryPerHour, onDemand.Rates.MemoryPerHour)
		})
	}
}

// A spot node of a type without a spot price falls back to on-demand, and
// says it did. Google Cloud's M2 machines aren't sold as spot.
func TestListPriceOf_SpotWithoutASpotPrice(t *testing.T) {
	onDemand, ok := ListPriceOf(NodeType{InstanceType: "m2-ultramem-208", Region: "us-central1"})
	require.True(t, ok)

	p, ok := ListPriceOf(NodeType{InstanceType: "m2-ultramem-208", Region: "us-central1", Spot: true})

	require.True(t, ok)
	assert.False(t, p.Spot, "priced on-demand")
	assert.Equal(t, onDemand.Rates, p.Rates)
}

func TestParseListing_WithoutSpot(t *testing.T) {
	l, err := parseListing([]string{"aws", "x9.large", "2", "8"}, "0.1", 1, 0)

	require.NoError(t, err)
	assert.Nil(t, l.spot)
	assert.InDelta(t, 0.1, ComputeHourly(2, 8, l.onDemand), 1e-9)
}

func TestParseListing_Spot(t *testing.T) {
	l, err := parseListing([]string{"gcp", "e2-standard-2", "2", "8"}, "0.08/0.02", 1, 0)

	require.NoError(t, err)
	require.NotNil(t, l.spot)
	assert.InDelta(t, 0.08, ComputeHourly(2, 8, l.onDemand), 1e-9)
	assert.InDelta(t, 0.02, ComputeHourly(2, 8, *l.spot), 1e-9)

	_, err = parseListing([]string{"gcp", "e2-standard-2", "2", "8"}, "0.08/x", 7, 0)
	assert.ErrorContains(t, err, "price table line 7: spot price")
}

func TestMean(t *testing.T) {
	m := Mean([]Rates{{CPUPerHour: 0.02, MemoryPerHour: 0.002}, {CPUPerHour: 0.04, MemoryPerHour: 0.006}})

	assert.InDelta(t, 0.03, m.CPUPerHour, 1e-9)
	assert.InDelta(t, 0.004, m.MemoryPerHour, 1e-9)
	assert.Equal(t, Rates{}, Mean(nil))
}

func TestNodePoolOf(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{name: "Karpenter", labels: map[string]string{"karpenter.sh/nodepool": "general"}, want: "general"},
		{name: "GKE", labels: map[string]string{"cloud.google.com/gke-nodepool": "default-pool"}, want: "default-pool"},
		{name: "EKS managed node group", labels: map[string]string{"eks.amazonaws.com/nodegroup": "ng-1"}, want: "ng-1"},
		{name: "AKS", labels: map[string]string{"kubernetes.azure.com/agentpool": "nodepool1"}, want: "nodepool1"},
		{name: "Karpenter on EKS wins over the node group",
			labels: map[string]string{"eks.amazonaws.com/nodegroup": "ng-1", "karpenter.sh/nodepool": "general"},
			want:   "general"},
		{name: "an instance type isn't a pool", labels: nodeLabels("m6i.large", "us-east-1"), want: ""},
		{name: "no labels", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, NodePoolOf(tt.labels))
		})
	}
}

func nodeLabels(instanceType, region string) map[string]string {
	return map[string]string{
		"node.kubernetes.io/instance-type": instanceType,
		"topology.kubernetes.io/region":    region,
	}
}

func withLabel(labels map[string]string, key, value string) map[string]string {
	labels[key] = value
	return labels
}
