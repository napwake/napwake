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

package capacity

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"
)

const gib = bytesPerGiB

func cores(cpu float64) Resources { return Resources{CPU: cpu} }

func TestRealise(t *testing.T) {
	tests := []struct {
		name      string
		paused    []held
		spare     map[string]Resources
		want      map[types.UID]Resources
		shortfall map[string]Resources
	}{
		{
			name:   "spare capacity holds it all: no autoscaler took the nodes, nothing came off the bill",
			paused: []held{{workload: "a", pool: "", Resources: Resources{CPU: 2, Memory: 4 * gib}}},
			spare:  map[string]Resources{"": {CPU: 10, Memory: 40 * gib}},
			want:   map[types.UID]Resources{"a": {}},
		},
		{
			name:   "no spare capacity: all it freed came off the bill",
			paused: []held{{workload: "a", pool: "", Resources: Resources{CPU: 2, Memory: 4 * gib}}},
			spare:  map[string]Resources{"": {}},
			want:   map[types.UID]Resources{"a": {CPU: 2, Memory: 4 * gib}},
		},
		{
			name:   "the pool's nodes are all gone",
			paused: []held{{workload: "a", pool: "batch", Resources: Resources{CPU: 2, Memory: 4 * gib}}},
			spare:  map[string]Resources{"general": {CPU: 10, Memory: 40 * gib}},
			want:   map[types.UID]Resources{"a": {CPU: 2, Memory: 4 * gib}},
		},
		{
			name:   "partly held by spare capacity",
			paused: []held{{workload: "a", pool: "", Resources: Resources{CPU: 4, Memory: 8 * gib}}},
			spare:  map[string]Resources{"": {CPU: 1, Memory: 2 * gib}},
			want:   map[types.UID]Resources{"a": {CPU: 3, Memory: 6 * gib}},
		},
		{
			name:   "each dimension on its own: CPU fits, memory doesn't",
			paused: []held{{workload: "a", pool: "", Resources: Resources{CPU: 2, Memory: 16 * gib}}},
			spare:  map[string]Resources{"": {CPU: 8, Memory: 4 * gib}},
			want:   map[types.UID]Resources{"a": {Memory: 12 * gib}},
		},
		{
			name: "pools don't lend each other spare capacity",
			paused: []held{
				{workload: "a", pool: "general", Resources: cores(2)},
				{workload: "b", pool: "gpu", Resources: cores(2)},
			},
			spare: map[string]Resources{"general": cores(10), "gpu": {}},
			want:  map[types.UID]Resources{"a": {}, "b": cores(2)},
		},
		{
			name: "an unknown pool meets what every pool has left over",
			paused: []held{
				{workload: "a", pool: "general", Resources: cores(1)},
				{workload: "b", pool: "gpu", Resources: cores(2)},
				{workload: "c", pool: anyPool, Resources: cores(3)},
			},
			spare:     map[string]Resources{"general": cores(3), "gpu": {}},
			want:      map[types.UID]Resources{"a": {}, "b": cores(2), "c": cores(1)},
			shortfall: map[string]Resources{"general": {}, "gpu": cores(2), anyPool: cores(1)},
		},
		{
			name:   "in a cluster without pools, an unknown pool is the cluster",
			paused: []held{{workload: "a", pool: anyPool, Resources: cores(4)}},
			spare:  map[string]Resources{"": cores(1)},
			want:   map[types.UID]Resources{"a": cores(3)},
		},
		{
			name: "a pool's shortfall is shared in proportion to what each holds paused there",
			paused: []held{
				{workload: "a", pool: "", Resources: Resources{CPU: 3, Memory: 1 * gib}},
				{workload: "b", pool: "", Resources: Resources{CPU: 1, Memory: 3 * gib}},
			},
			spare: map[string]Resources{"": {CPU: 2, Memory: 2 * gib}},
			want: map[types.UID]Resources{
				"a": {CPU: 1.5, Memory: 0.5 * gib},
				"b": {CPU: 0.5, Memory: 1.5 * gib},
			},
		},
		{
			name: "a workload across pools has a share of each",
			paused: []held{
				{workload: "a", pool: "general", Resources: cores(1)},
				{workload: "a", pool: "spot", Resources: cores(1)},
				{workload: "b", pool: "spot", Resources: cores(1)},
			},
			spare: map[string]Resources{"general": {}, "spot": cores(1)},
			want:  map[types.UID]Resources{"a": cores(1.5), "b": cores(0.5)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pools, shares := realise(tt.paused, tt.spare)

			assert.Len(t, shares, len(tt.want))
			for uid, want := range tt.want {
				assert.InDelta(t, want.CPU, shares[uid].CPU, 1e-9, "%s CPU", uid)
				assert.InDelta(t, want.Memory, shares[uid].Memory, 1, "%s memory", uid)
			}
			for pool, want := range tt.shortfall {
				assert.InDelta(t, want.CPU, pools[pool].Shortfall.CPU, 1e-9, "pool %q", pool)
			}
		})
	}
}

// What's credited never exceeds what's paused, whatever the spare capacity.
func TestRealise_NeverMoreThanPaused(t *testing.T) {
	paused := []held{
		{workload: "a", pool: "general", Resources: Resources{CPU: 2, Memory: 2 * gib}},
		{workload: "b", pool: anyPool, Resources: Resources{CPU: 1, Memory: 1 * gib}},
	}
	for _, spare := range []Resources{{}, cores(1), {CPU: 100, Memory: 100 * gib}} {
		_, shares := realise(paused, map[string]Resources{"general": spare})
		assert.LessOrEqual(t, shares["a"].CPU, 2.0)
		assert.LessOrEqual(t, shares["a"].Memory, 2.0*gib)
		assert.LessOrEqual(t, shares["b"].CPU, 1.0)
	}
}
