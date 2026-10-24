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

// Package capacity measures what pausing has taken off the bill: how much
// of what Napwake holds paused the cluster's nodes couldn't hold if it
// came back now. That much capacity would have to be added for it, so it
// costs at least what that capacity does, whatever made the nodes go.
package capacity

import "k8s.io/apimachinery/pkg/types"

// anyPool stands for the replicas whose node pool wasn't recorded. A label
// value can't contain "*", so it can't be a real pool's name.
const anyPool = "*"

// Resources are vCPUs and bytes of memory.
type Resources struct {
	CPU    float64
	Memory float64
}

func (r Resources) add(o Resources) Resources {
	return Resources{CPU: r.CPU + o.CPU, Memory: r.Memory + o.Memory}
}

// beyond is how much of r doesn't fit in room, in each dimension.
func (r Resources) beyond(room Resources) Resources {
	return Resources{CPU: max(0, r.CPU-room.CPU), Memory: max(0, r.Memory-room.Memory)}
}

// held is what one paused workload requests of one node pool.
type held struct {
	workload types.UID
	pool     string
	Resources
}

// poolSample is one node pool's paused requests, spare capacity, and how
// much of what's paused its spare capacity can't hold.
type poolSample struct {
	Paused    Resources
	Spare     Resources
	Shortfall Resources
}

// realise works out each node pool's shortfall, and each workload's share
// of the shortfalls, in proportion to what it holds paused in each pool,
// per dimension.
//
// Each dimension is set against spare capacity on its own, and spare
// capacity is summed across a pool's nodes, as though paused pods could be
// split across nodes and CPU without memory were of use. Real packing only
// needs more, so the shortfall is a lower bound on the capacity holding
// the paused replicas would add.
//
// Replicas whose pool isn't known are set against what every pool has left
// once its own paused replicas are held: wherever they ran, they'd need at
// least that much more.
func realise(paused []held, spare map[string]Resources) (map[string]poolSample, map[types.UID]Resources) {
	pools := make(map[string]poolSample, len(spare)+1)
	for name, s := range spare {
		pools[name] = poolSample{Spare: s}
	}
	for _, h := range paused {
		p := pools[h.pool]
		p.Paused = p.Paused.add(h.Resources)
		pools[h.pool] = p
	}

	var left Resources
	for name, p := range pools {
		if name == anyPool {
			continue
		}
		p.Shortfall = p.Paused.beyond(p.Spare)
		left = left.add(p.Spare.beyond(p.Paused))
		pools[name] = p
	}
	if p, ok := pools[anyPool]; ok {
		p.Spare = left
		p.Shortfall = p.Paused.beyond(left)
		pools[anyPool] = p
	}

	shares := make(map[types.UID]Resources)
	for _, h := range paused {
		p := pools[h.pool]
		s := shares[h.workload]
		if p.Paused.CPU > 0 {
			s.CPU += p.Shortfall.CPU * h.CPU / p.Paused.CPU
		}
		if p.Paused.Memory > 0 {
			s.Memory += p.Shortfall.Memory * h.Memory / p.Paused.Memory
		}
		shares[h.workload] = s
	}
	return pools, shares
}
