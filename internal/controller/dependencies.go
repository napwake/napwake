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

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/human"
	"github.com/napwake/napwake/internal/lifecycle"
)

const (
	conditionHeldByDependents       = "HeldByDependents"
	conditionWaitingForDependencies = "WaitingForDependencies"
	conditionDependencyCycle        = "DependencyCycle"
	conditionDependencyNotFound     = "DependencyNotFound"
	conditionDependencyNotAllowed   = "DependencyNotAllowed"

	// dependencyReadyCheckInterval is how often a resume waiting on a
	// dependency re-checks, as a fallback to the watch on the dependency.
	dependencyReadyCheckInterval = 10 * time.Second

	// dependencyTimeout bounds each dependency step of a reconcile: the
	// namespaces it reads and a read or write per dependency.
	dependencyTimeout = 15 * time.Second
)

// workloadID identifies a Deployment or StatefulSet across namespaces.
type workloadID struct {
	namespace string
	kind      v1alpha1.TargetKind
	name      string
}

// String is namespace/kind/name, as napwake.dev/depends-on names it, since
// a Deployment and a StatefulSet can share a name.
func (id workloadID) String() string {
	return id.namespace + "/" + strings.ToLower(string(id.kind)) + "/" + id.name
}

func targetID(workload *v1alpha1.ManagedWorkload) workloadID {
	return workloadID{namespace: workload.Namespace, kind: workload.Spec.Target.Kind, name: workload.Spec.Target.Name}
}

// dependencyID resolves a dependency, which defaults to the dependent's namespace.
func dependencyID(dependent *v1alpha1.ManagedWorkload, ref v1alpha1.DependencyRef) workloadID {
	namespace := ref.Namespace
	if namespace == "" {
		namespace = dependent.Namespace
	}
	return workloadID{namespace: namespace, kind: ref.Kind, name: ref.Name}
}

// dependencyGraph is the workload graph as one reconcile reads it, with
// the napwake.dev/dependents-from of each namespace a dependency in
// another namespace is in, when it has one.
type dependencyGraph struct {
	*workloadGraph
	dependentsFrom map[string]string
}

// loadDependencyGraph reads the graph SetupWithManager keeps, or, for a
// Reconciler not set up with a manager, builds one from a list.
func (r *Reconciler) loadDependencyGraph(ctx context.Context) (*dependencyGraph, error) {
	graph := r.graph
	if graph == nil {
		var list v1alpha1.ManagedWorkloadList
		if err := r.List(ctx, &list, client.UnsafeDisableDeepCopy); err != nil {
			return nil, fmt.Errorf("listing managed workloads: %w", err)
		}
		graph = workloadGraphOf(list.Items)
	}
	g := &dependencyGraph{workloadGraph: graph, dependentsFrom: map[string]string{}}
	for _, ns := range graph.crossedNamespaces() {
		value, err := r.dependentsFromOf(ctx, ns)
		if err != nil {
			return nil, err
		}
		if value != "" {
			g.dependentsFrom[ns] = value
		}
	}
	return g, nil
}

// dependentsFromOf is the namespace's napwake.dev/dependents-from, or ""
// for none or no such namespace.
func (r *Reconciler) dependentsFromOf(ctx context.Context, namespace string) (string, error) {
	var ns corev1.Namespace
	err := r.Get(ctx, client.ObjectKey{Name: namespace}, &ns)
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading namespace %s: %w", namespace, err)
	}
	return ns.Annotations[v1alpha1.AnnotationDependentsFrom], nil
}

// allowsDependentsFrom says a napwake.dev/dependents-from value lets
// workloads in namespace depend on the workloads beside it.
func allowsDependentsFrom(value, namespace string) bool {
	for entry := range strings.SplitSeq(value, ",") {
		if entry = strings.TrimSpace(entry); entry == "*" || entry == namespace {
			return true
		}
	}
	return false
}

// allows reports whether a workload in namespace may depend on the
// workload id: always in its own namespace, and in another only when that
// namespace's napwake.dev/dependents-from lets it. A dependency it doesn't
// allow is no edge: never held, woken or waited for.
func (g *dependencyGraph) allows(namespace string, id workloadID) bool {
	return id.namespace == namespace || allowsDependentsFrom(g.dependentsFrom[id.namespace], namespace)
}

// owner is the node that acts on the workload id, or nil when no
// ManagedWorkload manages it.
func (g *dependencyGraph) owner(id workloadID) *workloadNode {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.ownerOf(id)
}

// reaches reports whether following the edges from n leads to the workload
// to. A workload listed as its own dependency is no edge: it's awake
// whenever it is. The caller holds the lock.
func (g *dependencyGraph) reaches(n *workloadNode, to workloadID, edges func(*workloadNode) []workloadID) bool {
	visited := map[workloadID]bool{}
	queue := []*workloadNode{n}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		for _, id := range edges(next) {
			if id == next.target || !g.allows(next.key.Namespace, id) {
				continue
			}
			if id == to {
				return true
			}
			if visited[id] {
				continue
			}
			visited[id] = true
			if managed := g.ownerOf(id); managed != nil {
				queue = append(queue, managed)
			}
		}
	}
	return false
}

// waitsOn reports whether n waits, through waitForReady, for the workload
// id, directly or through others.
func (g *dependencyGraph) waitsOn(n *workloadNode, id workloadID) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.reaches(n, id, waitForReadyEdges)
}

// holds reports whether dependent, while awake, keeps workload from
// pausing. Declaring it in dependsOn always does. Having only learned it
// doesn't when workload in turn depends on dependent, directly or through
// others: two services that call each other would otherwise learn a cycle
// and keep each other awake forever. Each pauses when idle instead, and
// waking either wakes the other. The caller holds the lock.
func (g *dependencyGraph) holds(dependent, workload *workloadNode) bool {
	if slices.Contains(dependent.dependsOn, workload.target) {
		return true
	}
	return !g.reaches(workload, dependent.target, allEdges)
}

// holdsOf finds what keeps workload from pausing for the workloads around
// it. Following dependsOn from it back to it is a cycle, in which each
// holds the other awake so neither could pause; learned dependencies don't
// count, as for holds. Otherwise it's held by its awake dependents,
// declared or learned, where it allows them.
func (g *dependencyGraph) holdsOf(workload *v1alpha1.ManagedWorkload) dependencyHolds {
	self := newWorkloadNode(workload)
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.reaches(self, self.target, declaredEdges) {
		return dependencyHolds{cycle: true}
	}
	var h dependencyHolds
	for _, d := range g.dependents[self.target] {
		if d.uid == self.uid || !g.allows(d.key.Namespace, self.target) {
			continue
		}
		if d.acts && awakeFor(d, self) && g.holds(d, self) {
			h.holders = append(h.holders, d.key.String())
		}
	}
	slices.Sort(h.holders)
	return h
}

// dependentsOf lists the workloads that would hold workload awake were they
// awake, whatever phase the graph has them in.
func (g *dependencyGraph) dependentsOf(workload *v1alpha1.ManagedWorkload) []types.NamespacedName {
	self := newWorkloadNode(workload)
	g.mu.RLock()
	defer g.mu.RUnlock()
	var keys []types.NamespacedName
	for _, d := range g.dependents[self.target] {
		if d.uid != self.uid && d.acts && g.allows(d.key.Namespace, self.target) && g.holds(d, self) {
			keys = append(keys, d.key)
		}
	}
	return keys
}

// heldByDependentsNow reports whether one of the workload's dependents is
// awake, holding it, as the API server has it rather than the cache. A
// dependent's wake stamps the workload once its Resuming is written, and
// the stamp's event can reach this workload before the cache, or the
// graph, has that write. One Get each, for the rare check that needs it.
func (r *Reconciler) heldByDependentsNow(ctx context.Context, workload *v1alpha1.ManagedWorkload) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, dependencyTimeout)
	defer cancel()
	g, err := r.loadDependencyGraph(ctx)
	if err != nil {
		return false, err
	}
	self := newWorkloadNode(workload)
	for _, key := range g.dependentsOf(workload) {
		var dependent v1alpha1.ManagedWorkload
		err := r.apiReader().Get(ctx, key, &dependent)
		switch {
		case apierrors.IsNotFound(err):
			continue
		case err != nil:
			return false, fmt.Errorf("getting dependent %s: %w", key, err)
		}
		if awakeFor(newWorkloadNode(&dependent), self) {
			return true, nil
		}
	}
	return false, nil
}

// isAwake reports whether a workload is, or is about to be, running. Pausing
// counts: its pods may still be serving.
func isAwake(phase v1alpha1.WorkloadPhase) bool {
	switch phase {
	case v1alpha1.PhasePaused:
		return false
	default:
		return true
	}
}

// awakeFor reports whether dependent counts as awake for its dependency
// workload. A dry-run workload's would-be pause, Idle, leaves its pods up,
// so it holds a dependency that pauses for real. A dependency in dry-run too
// is measured as it would be live, where the dependent would have paused and
// let it go.
func awakeFor(dependent, workload *workloadNode) bool {
	if dependent.dryRun && workload.dryRun && dependent.phase == v1alpha1.PhaseIdle {
		return false
	}
	return isAwake(dependent.phase)
}

// dependencyHold reports why a workload whose clock has run out must stay
// up anyway, and records it in conditions. A nil result means it may act.
func (r *Reconciler) dependencyHold(ctx context.Context, workload *v1alpha1.ManagedWorkload) (*ctrl.Result, error) {
	h, err := r.heldByDependencies(ctx, workload)
	if err != nil {
		return nil, err
	}

	if h.cycle {
		if !meta.IsStatusConditionTrue(workload.Status.Conditions, conditionDependencyCycle) {
			r.emitEvent(workload, workload.Spec.DryRun, "Warning", conditionDependencyCycle, actionEvaluateIdle, "%s",
				h.message())
		}
		r.setCondition(workload, conditionDependencyCycle, metav1.ConditionTrue, conditionDependencyCycle, h.message())
		recordHolders(workload, nil)
		return &ctrl.Result{RequeueAfter: activityCheckInterval}, nil
	}
	r.clearCondition(workload, conditionDependencyCycle, "NoCycle")
	recordHolders(workload, h.holders)

	if len(h.holders) == 0 {
		r.clearCondition(workload, conditionHeldByDependents, "NoAwakeDependents")
		return nil, nil
	}
	if !meta.IsStatusConditionTrue(workload.Status.Conditions, conditionHeldByDependents) {
		r.emitEvent(workload, workload.Spec.DryRun, "Normal", conditionHeldByDependents, actionEvaluateIdle,
			"idle, but %s", h.message())
	}
	r.setCondition(workload, conditionHeldByDependents, metav1.ConditionTrue, "DependentsAwake", h.message())
	return &ctrl.Result{RequeueAfter: activityCheckInterval}, nil
}

// recordHolders keeps the dependents holding the workload awake in its
// activity status, for tools to show without reading the condition's
// message: the first maxListed, as the message names.
func recordHolders(workload *v1alpha1.ManagedWorkload, holders []string) {
	a := workload.Status.Activity
	if a == nil {
		return
	}
	a.HeldByDependents = slices.Clone(holders[:min(len(holders), maxListed)])
	if len(a.HeldByDependents) == 0 {
		a.HeldByDependents = nil
	}
}

// dependencyHolds is what keeps a workload from pausing for the workloads
// around it: a dependsOn cycle through it, or awake dependents.
type dependencyHolds struct {
	cycle   bool
	holders []string
}

func (h dependencyHolds) held() bool {
	return h.cycle || len(h.holders) > 0
}

// condition is the condition type, and the event reason, that reports h.
func (h dependencyHolds) condition() string {
	if h.cycle {
		return conditionDependencyCycle
	}
	return conditionHeldByDependents
}

func (h dependencyHolds) message() string {
	if h.cycle {
		return "dependsOn forms a cycle through this workload, so it won't pause until the cycle is removed"
	}
	return "kept awake for " + listNames(h.holders)
}

// maxListed bounds how many names a condition or event message lists: a
// condition's message is at most 32768 bytes, and the API server rejects
// the whole status write past that, and an event's note past 1024.
const maxListed = 10

// listNames joins names, listing at most maxListed and counting the rest.
func listNames(names []string) string {
	if len(names) <= maxListed {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:maxListed], ", "), len(names)-maxListed)
}

// heldByDependencies finds what keeps workload from pausing for the
// workloads around it, without recording it.
func (r *Reconciler) heldByDependencies(ctx context.Context, workload *v1alpha1.ManagedWorkload) (dependencyHolds, error) {
	ctx, cancel := context.WithTimeout(ctx, dependencyTimeout)
	defer cancel()

	g, err := r.loadDependencyGraph(ctx)
	if err != nil {
		return dependencyHolds{}, err
	}
	return g.holdsOf(workload), nil
}

// actsOnTarget reports whether a ManagedWorkload manages a workload that's
// there. One being deleted, one whose target another ManagedWorkload
// manages, and one whose target is gone have no running workload of their
// own to keep a dependency up for, and never pause to let it go.
func actsOnTarget(w *v1alpha1.ManagedWorkload) bool {
	return w.DeletionTimestamp.IsZero() &&
		!meta.IsStatusConditionTrue(w.Status.Conditions, conditionDuplicateTarget) &&
		!conditionFalseWith(w, conditionTargetAvailable, ReasonTargetNotFound)
}

// clearCondition flips a condition to False if it's present, so a resolved
// problem is shown as resolved rather than disappearing.
func (r *Reconciler) clearCondition(workload *v1alpha1.ManagedWorkload, condType, reason string) {
	if meta.FindStatusCondition(workload.Status.Conditions, condType) != nil {
		r.setCondition(workload, condType, metav1.ConditionFalse, reason, "")
	}
}

// wakeDependencies stamps an activity annotation on each dependency that
// Napwake manages, which wakes it through the annotation wake path. A
// dependency that's already awake gets its clock reset, since this workload
// is about to use it.
//
// Waking them is best-effort: a dependency that can't be woken, such as
// one whose ManagedWorkload a policy won't let Napwake annotate, must
// never keep this workload from waking, being handed back, or being
// deleted. Each failure is reported in a Warning event on this workload,
// and every dependency is tried.
func (r *Reconciler) wakeDependencies(ctx context.Context, workload *v1alpha1.ManagedWorkload) {
	r.stampDependencies(ctx, workload, func(*workloadNode) bool { return true })
}

// wakePausedDependencies is wakeDependencies for a resume under way: it
// stamps those of the workload's dependencies still paused, which its wake
// may not have stamped, as when the operator restarted between writing
// Resuming and stamping them.
func (r *Reconciler) wakePausedDependencies(ctx context.Context, workload *v1alpha1.ManagedWorkload) {
	r.stampDependencies(ctx, workload, func(dep *workloadNode) bool { return dep.phase == v1alpha1.PhasePaused })
}

// wakeDryRunDependencies is wakeDependencies for a dry-run would-be pause
// that has ended. Live, the wake it stands for would have woken the
// workload's dependencies, so those in dry-run too, which its would-be
// pause let go, are stamped: their own would-be pauses end as that wake
// would end them, and their clocks restart. A dependency that pauses for
// real was held all along, and isn't stamped.
func (r *Reconciler) wakeDryRunDependencies(ctx context.Context, workload *v1alpha1.ManagedWorkload) {
	r.stampDependencies(ctx, workload, func(dep *workloadNode) bool { return dep.dryRun })
}

// stampDependencies stamps those of the workload's dependencies that Napwake
// manages and that stamp picks.
func (r *Reconciler) stampDependencies(ctx context.Context, workload *v1alpha1.ManagedWorkload,
	stamp func(*workloadNode) bool) {
	refs := dependencyRefs(workload)
	if len(refs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, dependencyTimeout)
	defer cancel()

	g, err := r.loadDependencyGraph(ctx)
	if err != nil {
		r.reportWakeFailure(ctx, workload, "its dependencies", err)
		return
	}
	for _, ref := range refs {
		id := dependencyID(workload, ref)
		dep := g.owner(id)
		if dep == nil || dep.uid == workload.UID || !g.allows(workload.Namespace, id) || !stamp(dep) {
			continue
		}
		if err := r.stampActivity(ctx, dep.key); err != nil {
			r.reportWakeFailure(ctx, workload, dep.key.String(), err)
		}
	}
}

// ReasonDependencyWakeFailed reports a dependency Napwake couldn't wake
// along with its dependent.
const ReasonDependencyWakeFailed = "DependencyWakeFailed"

func (r *Reconciler) reportWakeFailure(ctx context.Context, workload *v1alpha1.ManagedWorkload, what string, err error) {
	log.FromContext(ctx).Info("couldn't wake a dependency", "workload", workload.Name,
		"namespace", workload.Namespace, "dependency", what, "error", err.Error())
	r.emitEvent(workload, false, "Warning", ReasonDependencyWakeFailed, actionResume,
		"couldn't wake %s, so it may still be paused: %v", what, err)
}

func (r *Reconciler) stampActivity(ctx context.Context, dep types.NamespacedName) error {
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{
		v1alpha1.AnnotationLastActivity: r.now().UTC().Format(time.RFC3339),
	}}})
	if err != nil {
		return fmt.Errorf("encoding the wake of dependency %s: %w", dep, err)
	}
	obj := &v1alpha1.ManagedWorkload{ObjectMeta: metav1.ObjectMeta{Namespace: dep.Namespace, Name: dep.Name}}
	if err := r.Patch(ctx, obj, client.RawPatch(types.MergePatchType, patch)); err != nil {
		return fmt.Errorf("waking dependency %s: %w", dep, err)
	}
	return nil
}

// waitForDependencies holds a resume until every waitForReady dependency
// serves, by servesDependents. It doesn't wait for one that won't become
// Ready by waiting, which would leave this workload at zero for good: the
// workload itself, listed as its own dependency; one that doesn't exist or
// can't be seen (reported by checkDependenciesExist); one scaled to zero
// outside Napwake; one that waits for this workload in turn, through
// waitForReady, which would wait for each other forever; and one that
// isn't Ready by its startDeadline, such as one crash-looping or whose
// image can't be pulled. A managed one still paused is woken again, in
// case the first wake was missed.
func (r *Reconciler) waitForDependencies(ctx context.Context, workload *v1alpha1.ManagedWorkload) (*ctrl.Result, error) {
	refs := slices.DeleteFunc(slices.Clone(workload.Spec.DependsOn), func(ref v1alpha1.DependencyRef) bool {
		return !ref.WaitForReady || dependencyID(workload, ref) == targetID(workload)
	})
	if len(refs) == 0 {
		r.clearCondition(workload, conditionWaitingForDependencies, "DependenciesReady")
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, dependencyTimeout)
	defer cancel()

	g, err := r.loadDependencyGraph(ctx)
	if err != nil {
		return nil, err
	}
	// The dependent waits from the moment it starts resuming.
	waited := r.sinceTransition(workload)
	var waiting, skipped []string
	reason := reasonDependencyWontStart
	for _, ref := range refs {
		id := dependencyID(workload, ref)
		if !g.allows(workload.Namespace, id) {
			continue
		}
		target, state, err := r.getDependency(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("checking dependency %s: %w", id, err)
		}
		dep := g.owner(id)
		if state != dependencyFound || servesDependents(dep, target, waited) {
			continue
		}
		if why := wontStart(dep, target); why != "" {
			skipped = append(skipped, fmt.Sprintf("%s (%s)", id, why))
			continue
		}
		if dep != nil && g.waitsOn(dep, targetID(workload)) {
			skipped = append(skipped, fmt.Sprintf("%s (it waits for this workload to be Ready too)", id))
			reason = conditionDependencyCycle
			continue
		}
		if deadline := startDeadline(target); waited >= deadline {
			skipped = append(skipped, fmt.Sprintf("%s (it isn't Ready after %s)", id, human.Duration(deadline)))
			continue
		}
		if dep != nil && dep.uid != workload.UID && !isAwake(dep.phase) {
			// Retried every check while it waits, so it isn't announced:
			// the wake that failed first was.
			if err := r.stampActivity(ctx, dep.key); err != nil {
				log.FromContext(ctx).Info("couldn't wake a dependency again", "workload", workload.Name,
					"namespace", workload.Namespace, "dependency", id.String(), "error", err.Error())
			}
		}
		waiting = append(waiting, fmt.Sprintf("%s (%d ready)", id, lifecycle.ReadyReplicas(target)))
	}

	if len(waiting) == 0 {
		if len(skipped) > 0 {
			r.setCondition(workload, conditionWaitingForDependencies, metav1.ConditionFalse, reason,
				"not waiting for "+strings.Join(skipped, ", "))
		} else {
			r.clearCondition(workload, conditionWaitingForDependencies, "DependenciesReady")
		}
		return nil, nil
	}
	// The resume path returns before the reconcile's single status write, so
	// write here, but only when the condition changes: this re-checks every
	// dependencyReadyCheckInterval.
	msg := "waiting for " + strings.Join(waiting, ", ")
	if prev := meta.FindStatusCondition(workload.Status.Conditions, conditionWaitingForDependencies); prev == nil ||
		prev.Status != metav1.ConditionTrue || prev.Message != msg {
		r.setCondition(workload, conditionWaitingForDependencies, metav1.ConditionTrue, "DependencyNotReady", msg)
		if err := r.Status().Update(ctx, workload); err != nil {
			return nil, fmt.Errorf("updating dependency wait condition: %w", err)
		}
	}
	return &ctrl.Result{RequeueAfter: dependencyReadyCheckInterval}, nil
}

// servesDependents reports whether a dependency is far enough up for a
// dependent waiting on it to start: every replica Ready, or some Ready once
// they're all they'll have for now. A managed dependency is there once its
// own resume has settled, by the same rule as any resume, and an unmanaged
// one once the dependent has waited the time a resume gives the rest of its
// replicas. Waiting for every replica instead would hold the dependent at
// zero for good behind a dependency with one crash-looping pod.
func servesDependents(dep *workloadNode, target client.Object, waited time.Duration) bool {
	if lifecycle.IsReady(target) {
		return true
	}
	if lifecycle.ReadyReplicas(target) == 0 {
		return false
	}
	if dep != nil {
		return dep.phase == v1alpha1.PhaseRunning || dep.phase == v1alpha1.PhaseIdle
	}
	return waited >= lifecycle.ServingWait
}

const (
	reasonDependencyWontStart = "DependencyWontStart"

	// statefulSetStartDeadline is how long a dependent waits for a
	// StatefulSet to be Ready, which unlike a Deployment has no progress
	// deadline of its own. It's the default progress deadline, since a
	// database replaying its log can take minutes.
	statefulSetStartDeadline = 10 * time.Minute
)

// startDeadline is how long a dependent waits for the dependency to be
// Ready before starting without it: a Deployment's progress deadline, past
// which Kubernetes itself reports it failed, or statefulSetStartDeadline.
func startDeadline(target client.Object) time.Duration {
	if d, ok := target.(*appsv1.Deployment); ok && d.Spec.ProgressDeadlineSeconds != nil {
		return time.Duration(*d.Spec.ProgressDeadlineSeconds) * time.Second
	}
	return statefulSetStartDeadline
}

// wontStart says why a dependency that isn't Ready won't become Ready by
// waiting, or "" when it may. dep is its ManagedWorkload, if it has one.
func wontStart(dep *workloadNode, target client.Object) string {
	if dep != nil {
		switch dep.phase {
		case v1alpha1.PhasePaused, v1alpha1.PhasePausing, v1alpha1.PhaseResuming:
			return ""
		}
	}
	if replicasFromTarget(target) == 0 {
		return "it's scaled to zero"
	}
	return ""
}

// checkDependenciesExist reports dependencies in a namespace that doesn't
// allow this workload's, and declared dependencies whose workload doesn't
// exist or can't be seen. Only unmanaged ones are checked for existence: a
// managed dependency reports a missing target through its own
// TargetAvailable condition. One not allowed is never looked for, and is
// reported the same whether it exists or not, so dependsOn can't be used
// to probe another namespace. It never stops the lifecycle: a dependency
// that can't be checked is left out of the report.
func (r *Reconciler) checkDependenciesExist(ctx context.Context, workload *v1alpha1.ManagedWorkload) error {
	refs := dependencyRefs(workload)
	if len(refs) == 0 {
		r.clearCondition(workload, conditionDependencyNotFound, "NoDependencies")
		r.clearCondition(workload, conditionDependencyNotAllowed, "NoDependencies")
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, dependencyTimeout)
	defer cancel()

	g, err := r.loadDependencyGraph(ctx)
	if err != nil {
		return err
	}
	var notAllowed []string
	for _, ref := range refs {
		if id := dependencyID(workload, ref); !g.allows(workload.Namespace, id) {
			notAllowed = append(notAllowed, id.String())
		}
	}
	if len(notAllowed) == 0 {
		r.clearCondition(workload, conditionDependencyNotAllowed, "DependenciesAllowed")
	} else {
		r.setCondition(workload, conditionDependencyNotAllowed, metav1.ConditionTrue, conditionDependencyNotAllowed,
			fmt.Sprintf("not in a namespace that allows dependents from %s with %s, so not held or woken: %s",
				workload.Namespace, v1alpha1.AnnotationDependentsFrom, listNames(notAllowed)))
	}

	var missing, hidden []string
	for _, ref := range workload.Spec.DependsOn {
		id := dependencyID(workload, ref)
		if g.owner(id) != nil || !g.allows(workload.Namespace, id) {
			continue
		}
		_, state, err := r.getDependency(ctx, id)
		if err != nil {
			log.FromContext(ctx).Info("couldn't check a dependency", "workload", workload.Name,
				"namespace", workload.Namespace, "dependency", id.String(), "error", err.Error())
			continue
		}
		name := id.String()
		switch state {
		case dependencyMissing:
			missing = append(missing, name)
		case dependencyUnwatched:
			hidden = append(hidden, fmt.Sprintf("%s (Napwake doesn't watch namespace %s)", name, id.namespace))
		case dependencyForbidden:
			hidden = append(hidden, fmt.Sprintf("%s (Napwake isn't allowed to read it)", name))
		case dependencyFound:
		}
	}
	if len(missing) == 0 && len(hidden) == 0 {
		r.clearCondition(workload, conditionDependencyNotFound, "DependenciesFound")
		return nil
	}
	var parts []string
	reason := conditionDependencyNotFound
	if len(missing) > 0 {
		parts = append(parts, "not found: "+strings.Join(missing, ", "))
	} else {
		reason = "DependencyNotVisible"
	}
	if len(hidden) > 0 {
		parts = append(parts, "can't be seen, so it isn't held or woken: "+strings.Join(hidden, ", "))
	}
	r.setCondition(workload, conditionDependencyNotFound, metav1.ConditionTrue, reason, strings.Join(parts, "; "))
	return nil
}

// dependencyState is what reading a dependency's workload found.
type dependencyState int

const (
	dependencyFound dependencyState = iota
	dependencyMissing
	// dependencyUnwatched is a dependency in a namespace outside
	// watchNamespaces, which the operator's cache can't read at all.
	dependencyUnwatched
	dependencyForbidden
)

// getDependency reads a dependency's workload. One Napwake can't see,
// because of watchNamespaces or RBAC, is reported as such rather than as an
// error, so it never stops the dependent's lifecycle.
func (r *Reconciler) getDependency(ctx context.Context, id workloadID) (client.Object, dependencyState, error) {
	if len(r.WatchNamespaces) > 0 && !slices.Contains(r.WatchNamespaces, id.namespace) {
		return nil, dependencyUnwatched, nil
	}
	target, err := r.getWorkload(ctx, id)
	switch {
	case err == nil:
		return target, dependencyFound, nil
	case apierrors.IsNotFound(err):
		return nil, dependencyMissing, nil
	case apierrors.IsForbidden(err):
		return nil, dependencyForbidden, nil
	}
	return nil, dependencyMissing, err
}

func (r *Reconciler) getWorkload(ctx context.Context, id workloadID) (client.Object, error) {
	var obj client.Object
	switch id.kind {
	case v1alpha1.TargetKindStatefulSet:
		obj = &appsv1.StatefulSet{}
	default:
		obj = &appsv1.Deployment{}
	}
	err := r.Get(ctx, types.NamespacedName{Namespace: id.namespace, Name: id.name}, obj)
	return obj, err
}
