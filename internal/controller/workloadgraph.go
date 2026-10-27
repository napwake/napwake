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

package controller

import (
	"slices"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

// workloadNode is what the dependency graph keeps of a ManagedWorkload:
// only what holding, waking and waiting on dependencies read. A node is
// replaced, never changed, so one read under the graph's lock stays
// consistent after it's released.
type workloadNode struct {
	key     types.NamespacedName
	uid     types.UID
	created metav1.Time
	target  workloadID
	phase   v1alpha1.WorkloadPhase
	// waking is wakePending: paused, with a wake already asked for.
	waking bool
	// acts is actsOnTarget: one that doesn't neither holds its
	// dependencies awake nor pauses to let them go.
	acts bool
	// dryRun is whether it's measured rather than paused, as the
	// Reconciler's dryRun has it.
	dryRun bool
	// dependsOn are those its dependsOn declares, and waitsFor those of
	// them it waits to be Ready.
	dependsOn []workloadID
	waitsFor  []workloadID
	// dependencies are those declared and those learned.
	dependencies []workloadID
}

func newWorkloadNode(w *v1alpha1.ManagedWorkload, dryRun bool) *workloadNode {
	n := &workloadNode{key: client.ObjectKeyFromObject(w), uid: w.UID, created: w.CreationTimestamp,
		target: targetID(w), phase: w.Status.Phase, waking: wakePending(w), acts: actsOnTarget(w), dryRun: dryRun}
	for _, ref := range w.Spec.DependsOn {
		id := dependencyID(w, ref)
		n.dependsOn = append(n.dependsOn, id)
		if ref.WaitForReady {
			n.waitsFor = append(n.waitsFor, id)
		}
	}
	refs := dependencyRefs(w)
	n.dependencies = make([]workloadID, 0, len(refs))
	for _, ref := range refs {
		n.dependencies = append(n.dependencies, dependencyID(w, ref))
	}
	return n
}

// node is what the dependency graph keeps of w, measured rather than
// paused when r measures it.
func (r *Reconciler) node(w *v1alpha1.ManagedWorkload) *workloadNode {
	return newWorkloadNode(w, r.dryRun(w))
}

func declaredEdges(n *workloadNode) []workloadID     { return n.dependsOn }
func waitForReadyEdges(n *workloadNode) []workloadID { return n.waitsFor }
func allEdges(n *workloadNode) []workloadID          { return n.dependencies }

// workloadGraph is every ManagedWorkload Napwake can see, as nodes
// indexed by the workload each manages and by the workloads each depends
// on. SetupWithManager keeps it up to date from the ManagedWorkload watch,
// so a reconcile reads it without listing, and copying, every one.
type workloadGraph struct {
	mu    sync.RWMutex
	nodes map[types.UID]*workloadNode
	// claimants are the nodes that manage each workload: more than one when
	// one is a duplicate.
	claimants map[workloadID][]*workloadNode
	// dependents are the nodes that depend on each workload, by UID.
	dependents map[workloadID]map[types.UID]*workloadNode
	// crossed counts, for each namespace, the dependencies in it of
	// workloads in other namespaces.
	crossed map[string]int
}

func newWorkloadGraph() *workloadGraph {
	return &workloadGraph{
		nodes:      map[types.UID]*workloadNode{},
		claimants:  map[workloadID][]*workloadNode{},
		dependents: map[workloadID]map[types.UID]*workloadNode{},
		crossed:    map[string]int{},
	}
}

// workloadGraphOf is a graph of the workloads given, as r sees them.
func (r *Reconciler) workloadGraphOf(workloads []v1alpha1.ManagedWorkload) *workloadGraph {
	g := newWorkloadGraph()
	for i := range workloads {
		g.set(r.node(&workloads[i]))
	}
	return g
}

// set adds the node's workload to the graph, or replaces what the graph has
// of it. A Reconciler not set up with a manager has no graph to keep.
func (g *workloadGraph) set(n *workloadNode) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if old, ok := g.nodes[n.uid]; ok {
		g.unlink(old)
	}
	g.nodes[n.uid] = n
	g.claimants[n.target] = append(g.claimants[n.target], n)
	for _, id := range n.dependencies {
		if g.dependents[id] == nil {
			g.dependents[id] = map[types.UID]*workloadNode{}
		}
		g.dependents[id][n.uid] = n
		if id.namespace != n.key.Namespace {
			g.crossed[id.namespace]++
		}
	}
}

func (g *workloadGraph) remove(uid types.UID) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if old, ok := g.nodes[uid]; ok {
		g.unlink(old)
		delete(g.nodes, uid)
	}
}

func (g *workloadGraph) unlink(n *workloadNode) {
	claimants := slices.DeleteFunc(g.claimants[n.target], func(c *workloadNode) bool { return c.uid == n.uid })
	if len(claimants) == 0 {
		delete(g.claimants, n.target)
	} else {
		g.claimants[n.target] = claimants
	}
	for _, id := range n.dependencies {
		delete(g.dependents[id], n.uid)
		if len(g.dependents[id]) == 0 {
			delete(g.dependents, id)
		}
		if id.namespace != n.key.Namespace {
			if g.crossed[id.namespace]--; g.crossed[id.namespace] <= 0 {
				delete(g.crossed, id.namespace)
			}
		}
	}
}

// crossedNamespaces are the namespaces with a dependency of a workload in
// another namespace.
func (g *workloadGraph) crossedNamespaces() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]string, 0, len(g.crossed))
	for ns := range g.crossed {
		out = append(out, ns)
	}
	return out
}

// ownerOf is the node that acts on the workload id, by the duplicate
// check's rule when more than one manages it, or nil when none does. The
// caller holds the lock.
func (g *workloadGraph) ownerOf(id workloadID) *workloadNode {
	var owner *workloadNode
	for _, n := range g.claimants[id] {
		if owner == nil || claimedBefore(n.created, n.uid, owner.created, owner.uid) {
			owner = n
		}
	}
	return owner
}
