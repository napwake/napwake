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
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckCounts(t *testing.T) {
	healthy := map[string]counts{"aws": {1400, 1200}, "azure": {1300, 1250}, "gcp": {550, 540}}
	tests := []struct {
		name     string
		previous map[string]counts
		current  map[string]counts
		wantErr  string
	}{
		{name: "as before", previous: healthy, current: healthy},
		{name: "first table", current: healthy},
		{
			name:     "a few gone",
			previous: healthy,
			current:  map[string]counts{"aws": {1300, 1100}, "azure": {1300, 1250}, "gcp": {500, 490}},
		},
		{
			name:     "emptied feed",
			previous: healthy,
			current:  map[string]counts{"aws": {1400, 1200}, "gcp": {550, 540}},
			wantErr:  "azure has 0 instance types",
		},
		{
			name:    "too few with nothing to compare",
			current: map[string]counts{"aws": {1400, 1200}, "azure": {1300, 1250}, "gcp": {99, 99}},
			wantErr: "gcp has 99 instance types, fewer than 100",
		},
		{
			name:     "over a fifth gone",
			previous: healthy,
			current:  map[string]counts{"aws": {1100, 1000}, "azure": {1300, 1250}, "gcp": {550, 540}},
			wantErr:  "aws has 1100 instance types, down from 1400",
		},
		{
			name:    "spot field renamed",
			current: map[string]counts{"aws": {1400, 0}, "azure": {1300, 1250}, "gcp": {550, 540}},
			wantErr: `aws has spot prices for 0 of 1400 instance types; is "spot_avg" still the spot field`,
		},
		{
			name:     "over a fifth of spot prices gone",
			previous: healthy,
			current:  map[string]counts{"aws": {1400, 1200}, "azure": {1300, 900}, "gcp": {550, 540}},
			wantErr:  "azure has spot prices for 900 instance types, down from 1250",
		},
		{
			name:     "spot prices for the first time",
			previous: map[string]counts{"aws": {1400, 0}, "azure": {1300, 0}, "gcp": {550, 0}},
			current:  healthy,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkCounts(tt.previous, tt.current)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestCountRows(t *testing.T) {
	table := "# provider,instance type,vCPUs,memory GiB,region=on-demand[/spot] USD per hour...\n" +
		"aws,m5.large,2,8,us-east-1=0.096/0.0439\n" +
		"aws,m5.xlarge,4,16,us-east-1=0.192\n" +
		"gcp,e2-small,2,2,us-central1=0.0167,us-east1=0.0167/0.0051\n"
	got, err := countRows(strings.NewReader(table))
	require.NoError(t, err)
	assert.Equal(t, map[string]counts{"aws": {types: 2, spot: 1}, "gcp": {types: 1, spot: 1}}, got)
}

// Each feed's spot price is read from its own field, and written after the
// on-demand price; a region without one has only its on-demand price.
func TestRowFor(t *testing.T) {
	gcp := source{provider: "gcp", spotKey: "spot"}
	var in instance
	require.NoError(t, json.Unmarshal([]byte(`{"instance_type": "e2-standard-2", "vCPU": 2, "memory": 8,
		"pricing": {
			"europe-west1": {"linux": {"ondemand": "0.07371546", "spot": "0.037808"}},
			"us-central1": {"linux": {"ondemand": "0.067"}},
			"asia-east1": {"linux": {"ondemand": "0.07", "spot": "0.09"}},
			"nowhere": {"linux": {"spot": "0.01"}}
		}}`), &in))

	r, ok := rowFor(gcp, in)

	require.True(t, ok)
	assert.Equal(t, []regionPrice{
		{region: "asia-east1", onDemand: 0.07, spot: 0.07},
		{region: "europe-west1", onDemand: 0.07371546, spot: 0.037808},
		{region: "us-central1", onDemand: 0.067},
	}, r.prices, "spot is capped at on-demand, and a region without on-demand is left out")
	assert.Equal(t, "# provider,instance type,vCPUs,memory GiB,region=on-demand[/spot] USD per hour...\n"+
		"gcp,e2-standard-2,2,8,asia-east1=0.07/0.07,europe-west1=0.0737155/0.037808,us-central1=0.067\n",
		string(table([]row{r})))
}

// Azure's prices are numbers rather than strings, under its own spot field,
// and its regions are named as nodes are labelled.
func TestRowFor_Azure(t *testing.T) {
	azure := source{provider: "azure", spotKey: "spot_min"}
	var in instance
	require.NoError(t, json.Unmarshal([]byte(`{"instance_type": "d4sv5", "vcpu": 4, "memory": 16,
		"regions": {"us-east-2": "East US 2"},
		"pricing": {"us-east-2": {"linux": {"ondemand": 0.192, "spot_min": 0.040531}}}}`), &in))

	r, ok := rowFor(azure, in)

	require.True(t, ok)
	assert.Equal(t, []regionPrice{{region: "eastus2", onDemand: 0.192, spot: 0.040531}}, r.prices)
}

func TestCountsInCommittedTable(t *testing.T) {
	got, err := countsIn(filepath.Join("..", "..", "internal", "cost", "prices.csv.gz"))
	require.NoError(t, err)
	assert.NoError(t, checkCounts(nil, got), "the committed table fails its own check")
}

func TestCountsInMissingTable(t *testing.T) {
	got, err := countsIn(filepath.Join(t.TempDir(), "prices.csv.gz"))
	require.NoError(t, err)
	assert.Empty(t, got)
}
