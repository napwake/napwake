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
	"context"
	"fmt"
	"strings"
	"time"

	autoscalingv1 "k8s.io/api/autoscaling/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/autoscaler"
	"github.com/napwake/napwake/internal/gitops"
	"github.com/napwake/napwake/internal/human"
	"github.com/napwake/napwake/internal/metrics"
)

const (
	conditionGitOpsConflict  = "GitOpsConflict"
	conditionPauseUndone     = "PauseUndone"
	conditionControllerOwned = "ControllerOwned"

	ReasonPauseUndone         = "PauseUndone"
	ReasonPauseUndoneResolved = "PauseUndoneResolved"
)

// gitOpsRetryAfter is shared with kubectl napwake status, which shows when
// the wait ends.
const gitOpsRetryAfter = gitops.RetryAfter

// undoneWithin is how soon after a pause's scale to zero a write of the
// replicas undoes it at once, on a target with no controller owner. A
// controller that sets them does so within seconds of the change it
// watches. A deploy that comes later is a deploy.
const undoneWithin = 5 * time.Minute

// wakeOnScaleUp treats a paused workload scaled up outside Napwake as
// woken: whoever did it wants it running. One still pausing counts once
// Napwake has scaled it to zero, rather than being scaled back down. A
// GitOps tool doing it means it sets the replicas from Git and will undo
// every pause, which the workload reports with the fix. Anything else that
// undoes the pause at once, as an operator that owns the workload and sets
// its replicas does, will most likely undo the next too, so it's reported
// as PauseUndone, and Napwake waits longer to pause again for each pause in
// a row undone. A pause that holds clears either report.
func (r *Reconciler) wakeOnScaleUp(ctx context.Context, workload *v1alpha1.ManagedWorkload, target client.Object) error {
	if !scaledDownByNapwake(workload) {
		return nil
	}
	if replicasFromTarget(target) == 0 {
		return r.clearUndoneIfHeld(ctx, workload)
	}
	// Just after a pause the cache can still hold the target as it was
	// before it, which looks just like a scale-up. Only the API server can
	// tell them apart.
	replicas, err := r.liveReplicas(ctx, target)
	if err != nil {
		return err
	}
	if replicas == 0 {
		return nil
	}
	// Napwake's own resume scales it up too, and the cache can still hold
	// the workload as Paused, or Pausing, once that has begun. Releasing
	// KEDA then would let it scale the workload back down while it starts.
	if phase, err := r.livePhase(ctx, workload); err != nil {
		return err
	} else if phase != workload.Status.Phase {
		return nil
	}

	var pausedAt *metav1.Time
	if pause := workload.Status.Pause; pause != nil {
		pausedAt = pause.PausedAt
	}
	// Someone else woke it, so it's only released, never scaled.
	if _, err := r.pauser.Restore(ctx, workload); err != nil {
		return fmt.Errorf("releasing a workload scaled up outside Napwake: %w", err)
	}

	writer, found := gitops.ReplicasWriter(target.GetManagedFields())
	by := writer.Manager
	if !found {
		by = "unknown"
	}
	now := r.clockTime()
	scaledUp := &v1alpha1.ScaledUp{At: now, By: by, GitOps: string(writer.Tool), Replicas: replicas,
		Undone: undoneSoFar(workload)}
	r.resetActivity(workload, v1alpha1.ActivitySourceScaledUp)
	r.stampLastActed(workload)
	owner := metav1.GetControllerOf(target)
	var conflict, undone string
	switch {
	case writer.FromGit():
		conflict = fmt.Sprintf("%s set the replicas from Git, undoing the pause; Napwake pauses it again from %s. "+
			"To stop it, %s", writer.Tool, now.Add(gitOpsRetryAfter).UTC().Format("15:04 UTC"), gitOpsFix(writer.Tool))
		r.setCondition(workload, conditionGitOpsConflict, metav1.ConditionTrue, "PauseUndone", conflict)
	case undoneAtOnce(found, owner, pausedAt, now.Time):
		scaledUp.Undone++
		undone = pauseUndoneMessage(by, owner, pausedAt, now.Time, now.Add(gitops.UndoneRetryAfter(scaledUp.Undone)))
		r.setCondition(workload, conditionPauseUndone, metav1.ConditionTrue, "ReplicasSetBack", undone)
	}
	workload.Status.LastScaledUp = scaledUp

	r.wakeDependencies(ctx, workload)
	if err := r.transition(ctx, workload, v1alpha1.PhaseRunning, "ScaledUp"); err != nil {
		return err
	}

	log.FromContext(ctx).Info("paused workload scaled up outside Napwake",
		"workload", workload.Name, "namespace", workload.Namespace, "replicas", replicas, "by", by)
	metrics.ExternalScaleUps.WithLabelValues(workload.Namespace, workload.Name, scaledUpBy(writer)).Inc()
	r.emitEvent(workload, false, "Normal", ReasonScaledUp, actionCheckReplicas,
		"scaled up to %d replicas by %s outside Napwake; counted as activity", replicas, by)
	if conflict != "" {
		r.emitEvent(workload, false, "Warning", ReasonGitOpsConflict, actionCheckReplicas, "%s", conflict)
	}
	if undone != "" {
		r.emitEvent(workload, false, "Warning", ReasonPauseUndone, actionCheckReplicas, "%s", undone)
	}
	return nil
}

// undoneAtOnce says a scale-up that isn't a GitOps tool's undid the pause
// at once: a writer of the replicas is recorded, so it wrote the workload
// itself, and either the target has a controller owner, which reconciles
// what it owns as soon as it changes, or it came within undoneWithin of the
// scale to zero. A scale through the scale subresource, as kubectl scale,
// an HPA and KEDA make, records no writer, so it never counts.
func undoneAtOnce(found bool, owner *metav1.OwnerReference, pausedAt *metav1.Time, now time.Time) bool {
	if !found {
		return false
	}
	return owner != nil || pausedAt != nil && now.Sub(pausedAt.Time) < undoneWithin
}

// undoneSoFar is how many pauses in a row have been undone at once, until
// a pause holds.
func undoneSoFar(workload *v1alpha1.ManagedWorkload) int32 {
	s := workload.Status.LastScaledUp
	if s == nil || !meta.IsStatusConditionTrue(workload.Status.Conditions, conditionPauseUndone) {
		return 0
	}
	return s.Undone
}

const scaledUpGuide = "https://docs.napwake.dev/guides/managed-workload/#a-pause-undone-at-once"

// pauseUndoneMessage says, in a line, who set the replicas back and how
// soon after the pause, the target's controller owner, when Napwake pauses
// it again, and where the guide says what to do.
func pauseUndoneMessage(by string, owner *metav1.OwnerReference, pausedAt *metav1.Time, at, again time.Time) string {
	soon := ""
	if pausedAt != nil {
		soon = " " + human.Duration(at.Sub(pausedAt.Time))
	}
	controls, setter := "", by
	if owner != nil {
		controls, setter = fmt.Sprintf(" %s/%s controls the workload.", owner.Kind, owner.Name), "its owner"
	}
	return fmt.Sprintf("%s set the replicas back%s after the pause, undoing it.%s Napwake can pause it again from %s. "+
		"To stop this, opt the workload out, or have %s leave the replicas alone: %s", by, soon, controls,
		again.UTC().Format("15:04 UTC"), setter, scaledUpGuide)
}

const gitOpsGuide = "https://docs.napwake.dev/guides/gitops/"

// gitOpsFix says in a line what has a GitOps tool leave the replicas to
// Napwake, and where the guide gives the setting to copy. The setting
// itself is YAML, which kubectl describe flattens onto one line.
func gitOpsFix(tool gitops.Tool) string {
	switch tool {
	case gitops.ArgoCD:
		return "have Argo CD ignore replicas, as for an HPA: " + gitOpsGuide + "#argo-cd"
	case gitops.Flux:
		return "leave replicas out of what Flux applies: " + gitOpsGuide + "#flux"
	case gitops.Fleet:
		return "leave replicas out of what Fleet applies: " + gitOpsGuide + "#rancher-fleet"
	case gitops.Kapp:
		return "have kapp keep the replicas it finds: " + gitOpsGuide + "#kapp"
	case gitops.Kluctl:
		return "leave replicas out of what Kluctl applies: " + gitOpsGuide + "#kluctl"
	default:
		return "have it leave the replicas to Napwake: " + gitOpsGuide
	}
}

// scaledDownByNapwake reports whether Napwake has scaled the workload to
// zero for a pause: it's Paused, or Pausing past the scale-down, so any
// replicas it has were set by someone else.
func scaledDownByNapwake(workload *v1alpha1.ManagedWorkload) bool {
	switch workload.Status.Phase {
	case v1alpha1.PhasePaused:
		return true
	case v1alpha1.PhasePausing:
		return workload.Status.Pause != nil && workload.Status.Pause.PausedAt != nil
	default:
		return false
	}
}

const (
	conditionScaledToZero = "ScaledToZero"

	ReasonScaledToZero = "ScaledToZero"
)

// scaledToZero reports whether a target Napwake hasn't paused is at zero
// replicas: scaled there by a person, a pipeline, or KEDA scaling it to
// zero itself. Napwake leaves such a target off. Pausing it would record
// zero replicas to restore, and waking it, on a request through the doorman
// or as Napwake let go of it, would start a workload someone meant to be
// off. So it isn't paused, woken, or routed to the doorman, and a request
// to its Services fails as it would without Napwake, until its replicas
// are set above zero again. One that leaves zero is counted as active, so
// it isn't paused the moment it's back.
func (r *Reconciler) scaledToZero(ctx context.Context, workload *v1alpha1.ManagedWorkload, target client.Object) (bool, error) {
	switch workload.Status.Phase {
	case v1alpha1.PhaseRunning, v1alpha1.PhaseIdle:
	default:
		return false, nil
	}
	atZero := replicasFromTarget(target) == 0
	if atZero {
		// Just after Napwake hands a target back, the cache can still hold
		// it at zero.
		replicas, err := r.liveReplicas(ctx, target)
		if err != nil {
			return false, err
		}
		atZero = replicas == 0
	}
	if !atZero && meta.IsStatusConditionTrue(workload.Status.Conditions, conditionScaledToZero) {
		r.setCondition(workload, conditionScaledToZero, metav1.ConditionFalse, "HasReplicas", "")
		r.resetActivity(workload, v1alpha1.ActivitySourceScaledUp)
		r.stampLastActed(workload)
	}
	return atZero, nil
}

// reconcileScaledToZero leaves a target scaled to zero outside Napwake
// where it is, and says so. Its cost goes on being counted: its claims
// still cost while it has no replicas.
func (r *Reconciler) reconcileScaledToZero(ctx context.Context, workload *v1alpha1.ManagedWorkload,
	observed *v1alpha1.ManagedWorkloadStatus) (ctrl.Result, error) {
	began := !meta.IsStatusConditionTrue(workload.Status.Conditions, conditionScaledToZero)
	r.setCondition(workload, conditionScaledToZero, metav1.ConditionTrue, "ScaledToZero",
		"scaled to zero outside Napwake, so Napwake leaves it off: it doesn't pause it, wake it, or route "+
			"requests for it until its replicas are set above zero")
	if err := r.refusePauseRequest(ctx, workload, "Normal", ReasonScaledToZero,
		"it's scaled to zero outside Napwake, so it's off already"); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.refuseWakeRequest(ctx, workload, "Normal", ReasonScaledToZero,
		"it's scaled to zero outside Napwake, which leaves it off until its replicas are set above zero"); err != nil {
		return ctrl.Result{}, err
	}
	r.clearIdleVeto(workload)
	r.trackDryRun(workload)
	if workload.Status.Phase == v1alpha1.PhaseIdle {
		r.endWouldBePauseNow(workload)
		if err := r.transition(ctx, workload, v1alpha1.PhaseRunning, "ScaledToZero"); err != nil {
			return ctrl.Result{}, err
		}
	} else {
		r.accumulateCost(ctx, workload)
		if err := r.persistStatus(ctx, workload, observed); err != nil {
			return ctrl.Result{}, err
		}
	}
	if began {
		r.emitEvent(workload, false, "Normal", ReasonScaledToZero, actionCheckReplicas,
			"scaled to zero outside Napwake; left off, and not paused or woken until it has replicas again")
	}
	return ctrl.Result{RequeueAfter: targetRecheckInterval}, nil
}

// liveReplicas reads the target's replicas from the API server: the scale
// subresource is never served from the cache.
func (r *Reconciler) liveReplicas(ctx context.Context, target client.Object) (int32, error) {
	scale := &autoscalingv1.Scale{}
	if err := r.SubResource("scale").Get(ctx, target.DeepCopyObject().(client.Object), scale); err != nil {
		return 0, fmt.Errorf("reading the target's replicas: %w", err)
	}
	return scale.Spec.Replicas, nil
}

// livePhase reads the workload's phase from the API server, past the cache.
func (r *Reconciler) livePhase(ctx context.Context, workload *v1alpha1.ManagedWorkload) (v1alpha1.WorkloadPhase, error) {
	reader := r.PodReader
	if reader == nil {
		reader = r.Client
	}
	var live v1alpha1.ManagedWorkload
	if err := reader.Get(ctx, client.ObjectKeyFromObject(workload), &live); err != nil {
		return "", fmt.Errorf("reading the workload's phase: %w", err)
	}
	return live.Status.Phase, nil
}

// scaledUpBy is the external_scale_ups_total label for the GitOps tool that
// scaled a workload up, its name in lower case and hyphenated, as argo-cd,
// or other for anything else.
func scaledUpBy(w gitops.Writer) string {
	if w.Tool == "" {
		return "other"
	}
	return strings.ReplaceAll(strings.ToLower(string(w.Tool)), " ", "-")
}

// undoneHolds are how long Napwake waits to pause a workload again after
// its last pause was undone, by the condition that reports it, or zero
// without it: an hour after a GitOps tool, and for anything else, longer
// for each pause in a row it undid. Each is timed from the scale-up.
type undoneHolds struct {
	gitOps, undone time.Duration
}

func undoneHoldsOf(workload *v1alpha1.ManagedWorkload) undoneHolds {
	var holds undoneHolds
	s := workload.Status.LastScaledUp
	if s == nil {
		return holds
	}
	if meta.IsStatusConditionTrue(workload.Status.Conditions, conditionGitOpsConflict) {
		holds.gitOps = gitOpsRetryAfter
	}
	if meta.IsStatusConditionTrue(workload.Status.Conditions, conditionPauseUndone) {
		holds.undone = gitops.UndoneRetryAfter(s.Undone)
	}
	return holds
}

// pauseHold is when Napwake may pause a workload whose last pause a GitOps
// tool, or anything else at once, undid, and true until then.
func pauseHold(workload *v1alpha1.ManagedWorkload, now time.Time) (time.Time, bool) {
	holds := undoneHoldsOf(workload)
	hold := max(holds.gitOps, holds.undone)
	if hold == 0 {
		return time.Time{}, false
	}
	until := workload.Status.LastScaledUp.At.Add(hold)
	return until, now.Before(until)
}

// undoneResolved is what a pause that held cleared: a GitOps conflict, with
// the tool lastScaledUp names, if it still does, and a pause undone at once.
type undoneResolved struct {
	gitOps bool
	tool   string
	undone bool
}

// clearUndoneIfHeld clears the conflict, and a pause undone at once, once a
// pause made after it has held an hour: whatever undid it now leaves the
// replicas alone.
func (r *Reconciler) clearUndoneIfHeld(ctx context.Context, workload *v1alpha1.ManagedWorkload) error {
	resolved := r.clearUndone(workload)
	if !resolved.gitOps && !resolved.undone {
		return nil
	}
	if err := r.Status().Update(ctx, workload); err != nil {
		return fmt.Errorf("clearing the undone pause: %w", err)
	}
	r.announceUndoneResolved(workload, resolved)
	return nil
}

// clearUndone removes GitOpsConflict and PauseUndone, and the count of
// pauses undone, once the workload's pause has held an hour, which shows
// it's over, and returns what it cleared. An hour, not as long as Napwake
// waited to pause again: a workload in daily use is woken before a pause
// holds the longer waits, which would leave PauseUndone, and its count,
// long after whatever undid the pauses stopped. Scaling up clears the
// pause, so any pause since began after the scale-up that undid the last.
func (r *Reconciler) clearUndone(workload *v1alpha1.ManagedWorkload) undoneResolved {
	holds := undoneHoldsOf(workload)
	pause := workload.Status.Pause
	if holds == (undoneHolds{}) || pause == nil || pause.PausedAt == nil ||
		r.now().Sub(pause.PausedAt.Time) < gitOpsRetryAfter {
		return undoneResolved{}
	}
	var resolved undoneResolved
	if holds.gitOps > 0 {
		meta.RemoveStatusCondition(&workload.Status.Conditions, conditionGitOpsConflict)
		resolved.gitOps, resolved.tool = true, workload.Status.LastScaledUp.GitOps
	}
	if holds.undone > 0 {
		meta.RemoveStatusCondition(&workload.Status.Conditions, conditionPauseUndone)
		workload.Status.LastScaledUp.Undone = 0
		resolved.undone = true
	}
	return resolved
}

// announceUndoneResolved says what a pause that held cleared. A scale-up
// since the tool's, by anything else, such as kubectl scale, leaves
// lastScaledUp naming no tool, so the conflict's is said plainly.
func (r *Reconciler) announceUndoneResolved(workload *v1alpha1.ManagedWorkload, resolved undoneResolved) {
	switch {
	case resolved.gitOps && resolved.tool != "":
		r.emitEvent(workload, false, "Normal", ReasonGitOpsConflictResolved, actionCheckReplicas,
			"a pause held, so %s leaves the replicas to Napwake now", resolved.tool)
	case resolved.gitOps:
		r.emitEvent(workload, false, "Normal", ReasonGitOpsConflictResolved, actionCheckReplicas,
			"a pause held, so the GitOps tool that set the replicas from Git leaves them to Napwake now")
	}
	if resolved.undone {
		r.emitEvent(workload, false, "Normal", ReasonPauseUndoneResolved, actionCheckReplicas,
			"a pause held for an hour, so its pauses aren't being undone any more")
	}
}

// reportControllerOwner says, before any pause, which controller owns the
// target, as an operator owns what it makes. If it sets the replicas, it
// will undo each pause, which PauseUndone then reports.
func (r *Reconciler) reportControllerOwner(workload *v1alpha1.ManagedWorkload, target client.Object) {
	owner := metav1.GetControllerOf(target)
	if owner == nil {
		meta.RemoveStatusCondition(&workload.Status.Conditions, conditionControllerOwned)
		return
	}
	r.setCondition(workload, conditionControllerOwned, metav1.ConditionTrue, "ControllerReference",
		fmt.Sprintf("%s/%s controls it. If that sets the replicas, it will undo each pause", owner.Kind, owner.Name))
}

const conditionAutoscaled = "Autoscaled"

// reportAutoscaler says what scales the workload while it runs, and how
// Napwake pauses it alongside: an HPA stops at zero replicas, and KEDA is
// held at zero through its own annotation. One whose range starts at 0 can
// take the workload to zero itself, which Napwake leaves to it, as any
// workload already at zero, and is woken to at least one replica.
func (r *Reconciler) reportAutoscaler(ctx context.Context, workload *v1alpha1.ManagedWorkload) error {
	if r.autoscalers == nil {
		return nil
	}
	a, found, err := r.autoscalers.Find(ctx, workload.Namespace, workload.Spec.Target.Kind, workload.Spec.Target.Name)
	if err != nil {
		return fmt.Errorf("finding the workload's autoscaler: %w", err)
	}
	if !found {
		meta.RemoveStatusCondition(&workload.Status.Conditions, conditionAutoscaled)
		return nil
	}
	r.setCondition(workload, conditionAutoscaled, metav1.ConditionTrue, string(a.Kind), autoscaledMessage(a))
	return nil
}

func autoscaledMessage(a autoscaler.Autoscaler) string {
	owner, pauses := "the HPA", "Napwake pauses it at zero, where the HPA stops scaling"
	if a.Kind == autoscaler.KEDA {
		owner, pauses = "KEDA", "Napwake pauses it by having KEDA hold it at zero"
	}
	scales := fmt.Sprintf("%s %s scales it between %d and %d replicas", a.Kind, a.Name, a.Min, a.Max)
	if a.Min > 0 {
		return fmt.Sprintf("%s; %s, and resumes it within that range", scales, pauses)
	}
	resumes := fmt.Sprintf("to between 1 and %d replicas", a.Max)
	if a.Max <= 1 {
		resumes = "to 1 replica"
	}
	return fmt.Sprintf("%s, and to zero itself. Napwake leaves that zero to %s, and holds no requests there. "+
		"While it runs, %s. Napwake resumes it %s", scales, owner, pauses, resumes)
}
