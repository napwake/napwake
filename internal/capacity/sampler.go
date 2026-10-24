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
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/cost"
	"github.com/napwake/napwake/internal/metrics"
)

const (
	bytesPerGiB = 1024 * 1024 * 1024

	// SampleInterval is how often spare capacity is measured. Each sample
	// credits at most the interval before it, so time the operator wasn't
	// sampling, such as across a restart, isn't credited.
	SampleInterval = 5 * time.Minute

	// sampleTimeout bounds a sample's reads. They're from the cache, but the
	// first waits for the pods and nodes to be listed into it.
	sampleTimeout = time.Minute
)

// Unwatched is why savings can't be measured by an operator limited to
// some namespaces.
const Unwatched = "pods outside watchNamespaces aren't visible, so spare node capacity can't be measured"

// Options configure a Sampler.
type Options struct {
	// WatchNamespaces are the namespaces the operator sees, or all when
	// empty. Spare capacity can only be measured seeing every pod.
	WatchNamespaces []string

	// OperatorNamespace is this install's, whose ManagedWorkloads are
	// credited. Empty credits every one.
	OperatorNamespace string

	// Clock is the real clock when nil.
	Clock clock.WithTicker
}

// Sampler measures, every SampleInterval, how much of what Napwake holds
// paused each node pool's spare capacity couldn't hold, and credits each
// paused workload its share as saved resource-hours, until they're
// drained into its status.
type Sampler struct {
	reader            client.Reader
	clock             clock.WithTicker
	operatorNamespace string
	unmeasurable      string

	mu        sync.Mutex
	sampledAt time.Time
	saved     map[types.UID]Hours
	pools     map[string]bool
}

// Hours are vCPU-hours and GiB-hours of memory.
type Hours struct {
	CPU    float64
	Memory float64
}

// NewSampler returns a Sampler reading workloads, nodes and pods through
// reader, which should be the manager's cache, set up with PodCache and
// NodeCache.
func NewSampler(reader client.Reader, opts Options) *Sampler {
	s := &Sampler{
		reader:            reader,
		clock:             opts.Clock,
		operatorNamespace: opts.OperatorNamespace,
		saved:             map[types.UID]Hours{},
		pools:             map[string]bool{},
	}
	if s.clock == nil {
		s.clock = clock.RealClock{}
	}
	if len(opts.WatchNamespaces) > 0 {
		s.unmeasurable = Unwatched
	}
	return s
}

// NeedLeaderElection has only the leader sample: it's the one whose
// reconciles drain what's credited.
func (s *Sampler) NeedLeaderElection() bool { return true }

// Start samples at once and then every SampleInterval until ctx ends.
func (s *Sampler) Start(ctx context.Context) error {
	if s.unmeasurable != "" {
		return nil
	}
	logger := log.FromContext(ctx).WithName("capacity")
	sample := func() {
		if err := s.sample(ctx); err != nil && ctx.Err() == nil {
			logger.Error(err, "Could not measure spare node capacity, so the interval isn't credited as saved")
		}
	}
	sample()
	ticker := s.clock.NewTicker(SampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C():
			sample()
		}
	}
}

// Unmeasurable is why savings can't be measured, or "" when they are.
func (s *Sampler) Unmeasurable() string { return s.unmeasurable }

// Drain returns the vCPU-hours and GiB-hours credited to the workload since
// it was last drained, and forgets them.
func (s *Sampler) Drain(uid types.UID) (cpuHours, memoryHours float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.saved[uid]
	delete(s.saved, uid)
	return h.CPU, h.Memory
}

func (s *Sampler) sample(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, sampleTimeout)
	defer cancel()
	var workloads v1alpha1.ManagedWorkloadList
	var nodes corev1.NodeList
	var pods corev1.PodList
	for _, l := range []struct {
		what string
		list client.ObjectList
	}{{"managed workloads", &workloads}, {"nodes", &nodes}, {"pods", &pods}} {
		if err := s.reader.List(ctx, l.list, client.UnsafeDisableDeepCopy); err != nil {
			s.forgetSample()
			return fmt.Errorf("listing %s: %w", l.what, err)
		}
	}
	now := s.clock.Now()

	spare, rates := spareByPool(nodes.Items, pods.Items)
	paused, pausedAt := s.heldPaused(workloads.Items)
	pools, shares := realise(paused, spare)
	s.record(pools, rates)
	s.credit(now, shares, pausedAt, workloads.Items)
	return nil
}

// forgetSample has the next sample start afresh, crediting nothing for
// the interval this one failed to measure.
func (s *Sampler) forgetSample() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sampledAt = time.Time{}
}

// heldPaused is what each workload this install has paused requests of the
// node pools its pods ran in, split by how many ran in each, and when each
// was paused.
func (s *Sampler) heldPaused(workloads []v1alpha1.ManagedWorkload) ([]held, map[types.UID]time.Time) {
	var out []held
	pausedAt := map[types.UID]time.Time{}
	for i := range workloads {
		w := &workloads[i]
		p := w.Status.Pause
		if w.Status.Phase != v1alpha1.PhasePaused || w.Spec.DryRun || !s.ours(w) ||
			p == nil || p.Resources == nil || p.PausedAt == nil {
			continue
		}
		rs := p.Resources
		replicas := float64(rs.Replicas)
		total := Resources{CPU: replicas * float64(rs.CPUMillis) / 1000, Memory: replicas * float64(rs.MemoryBytes)}
		if total.CPU <= 0 && total.Memory <= 0 {
			continue
		}
		pausedAt[w.UID] = p.PausedAt.Time

		var pods int32
		for _, np := range rs.NodePools {
			pods += max(np.Pods, 0)
		}
		if pods == 0 {
			out = append(out, held{workload: w.UID, pool: anyPool, Resources: total})
			continue
		}
		for _, np := range rs.NodePools {
			if np.Pods <= 0 {
				continue
			}
			f := float64(np.Pods) / float64(pods)
			out = append(out, held{workload: w.UID, pool: np.Name,
				Resources: Resources{CPU: total.CPU * f, Memory: total.Memory * f}})
		}
	}
	return out, pausedAt
}

func (s *Sampler) ours(w *v1alpha1.ManagedWorkload) bool {
	owner := w.Annotations[v1alpha1.AnnotationOperator]
	return s.operatorNamespace == "" || owner == "" || owner == s.operatorNamespace
}

// spareByPool is what each node pool's schedulable, Ready nodes have left
// that no bound, unfinished pod requests, and what the pool's capacity
// costs at its nodes' list rates, averaged over them. A node whose pods
// request more than it has is counted as having nothing left, not as
// taking from its neighbours.
func spareByPool(nodes []corev1.Node, pods []corev1.Pod) (map[string]Resources, map[string]cost.Rates) {
	free := make(map[string]Resources, len(nodes))
	poolOf := make(map[string]string, len(nodes))
	listed := map[string][]cost.Rates{}
	for i := range nodes {
		n := &nodes[i]
		if !schedulable(n) {
			continue
		}
		pool := cost.NodePoolOf(n.Labels)
		free[n.Name] = requestsOf(n.Status.Allocatable)
		poolOf[n.Name] = pool
		if p, ok := cost.ListPriceOf(cost.NodeTypeOf(n.Labels)); ok {
			listed[pool] = append(listed[pool], p.Rates)
			listed[anyPool] = append(listed[anyPool], p.Rates)
		}
	}
	for i := range pods {
		p := &pods[i]
		room, ok := free[p.Spec.NodeName]
		if !ok || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		r := podRequests(&p.Spec)
		free[p.Spec.NodeName] = Resources{CPU: room.CPU - r.CPU, Memory: room.Memory - r.Memory}
	}

	spare := map[string]Resources{}
	for name, room := range free {
		pool := poolOf[name]
		spare[pool] = spare[pool].add(Resources{CPU: max(0, room.CPU), Memory: max(0, room.Memory)})
	}
	rates := make(map[string]cost.Rates, len(listed))
	for pool, rs := range listed {
		rates[pool] = cost.Mean(rs)
	}
	return spare, rates
}

func schedulable(n *corev1.Node) bool {
	if n.Spec.Unschedulable {
		return false
	}
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// record publishes each pool's sample, and drops the series of pools that
// have gone. A pool none of whose nodes is in the price table is priced at
// the default rates.
func (s *Sampler) record(pools map[string]poolSample, rates map[string]cost.Rates) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for pool := range s.pools {
		if _, ok := pools[pool]; !ok {
			metrics.DeletePool(pool)
			delete(s.pools, pool)
		}
	}
	for pool, p := range pools {
		r, ok := rates[pool]
		if !ok {
			r = cost.DefaultRates
		}
		metrics.CapacityPausedCPUCores.WithLabelValues(pool).Set(p.Paused.CPU)
		metrics.CapacityPausedMemoryBytes.WithLabelValues(pool).Set(p.Paused.Memory)
		metrics.CapacitySpareCPUCores.WithLabelValues(pool).Set(p.Spare.CPU)
		metrics.CapacitySpareMemoryBytes.WithLabelValues(pool).Set(p.Spare.Memory)
		metrics.SavingsRealisedDollarsPerHour.WithLabelValues(pool).Set(
			cost.ComputeHourly(p.Shortfall.CPU, p.Shortfall.Memory/bytesPerGiB, r))
		s.pools[pool] = true
	}
}

// credit adds each workload's share of the shortfall over the interval
// since the last sample, or over the part of it the workload was paused
// for, and forgets what's credited to workloads that are gone.
func (s *Sampler) credit(now time.Time, shares map[types.UID]Resources, pausedAt map[types.UID]time.Time,
	workloads []v1alpha1.ManagedWorkload) {
	s.mu.Lock()
	defer s.mu.Unlock()
	last := s.sampledAt
	s.sampledAt = now

	exists := make(map[types.UID]bool, len(workloads))
	for i := range workloads {
		exists[workloads[i].UID] = true
	}
	for uid := range s.saved {
		if !exists[uid] {
			delete(s.saved, uid)
		}
	}

	if last.IsZero() {
		return
	}
	interval := min(now.Sub(last), SampleInterval)
	for uid, share := range shares {
		covered := min(interval, now.Sub(pausedAt[uid]))
		if covered <= 0 || (share.CPU <= 0 && share.Memory <= 0) {
			continue
		}
		h := s.saved[uid]
		h.CPU += share.CPU * covered.Hours()
		h.Memory += share.Memory / bytesPerGiB * covered.Hours()
		s.saved[uid] = h
	}
}
