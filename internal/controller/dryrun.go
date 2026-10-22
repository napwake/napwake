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
	"fmt"
	"time"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
	"github.com/okedeji/hybernate/internal/cost"
	"github.com/okedeji/hybernate/internal/human"
)

// trackDryRun starts the dry-run summary when a workload enters dry-run and
// drops it when it leaves, since it describes a measurement that's over.
func (r *Reconciler) trackDryRun(workload *v1alpha1.ManagedWorkload) {
	if !workload.Spec.DryRun {
		workload.Status.DryRun = nil
		return
	}
	if workload.Status.DryRun == nil {
		workload.Status.DryRun = &v1alpha1.DryRunStatus{Since: r.clockTime(), EstimatedFreed: cost.FormatDollars(0)}
	}
	r.estimateDryRun(workload)
}

// estimateDryRun prices what the would-be pauses would have freed: the
// finished ones' hours, and the one under way up to now. A workload idle
// for days would otherwise show nothing measured until its next activity.
// The hours, which are the record, still count finished pauses only.
func (r *Reconciler) estimateDryRun(workload *v1alpha1.ManagedWorkload) {
	d := workload.Status.DryRun
	hours := cost.Hours{CPU: d.FreedCPUHours.AsApproximateFloat64(), Memory: d.FreedMemoryHours.AsApproximateFloat64()}
	if began := workload.Status.LastTransitionTime; workload.Status.Phase == v1alpha1.PhaseIdle &&
		d.Resources != nil && began != nil {
		cores, gib := requested(d.Resources)
		underWay := max(r.now().Sub(began.Time), 0).Hours()
		hours.CPU += cores * underWay
		hours.Memory += gib * underWay
	}
	d.EstimatedFreed = cost.FormatDollars(hours.Price(resolveCostRates(workload)))
}

// beginWouldBePause counts a pause dry-run held back, and records what the
// workload runs now, which the pause would have freed.
func (r *Reconciler) beginWouldBePause(ctx context.Context, workload *v1alpha1.ManagedWorkload) {
	r.trackDryRun(workload)
	workload.Status.DryRun.Pauses++
	workload.Status.DryRun.Resources = r.captureResourceSnapshot(ctx, workload)
	workload.Status.DryRun.QueryActiveSincePause = false
}

// inWouldBePause reports whether the workload is in a pause dry-run holds
// back: Idle in dry-run, where it stands for a paused workload.
func inWouldBePause(workload *v1alpha1.ManagedWorkload) bool {
	return workload.Spec.DryRun && workload.Status.Phase == v1alpha1.PhaseIdle
}

// heldByWouldBePause reports whether a Prometheus activity query's
// activity at this check is held back from ending the workload's would-be
// pause, as it would be from waking a real one: the queries have shown
// activity at every check since the pause was asked for. A check that finds
// them quiet releases it.
func heldByWouldBePause(workload *v1alpha1.ManagedWorkload, active bool) bool {
	d := workload.Status.DryRun
	if !workload.Spec.DryRun || workload.Status.Phase != v1alpha1.PhaseIdle || d == nil || !d.QueryActiveSincePause {
		return false
	}
	d.QueryActiveSincePause = active
	return active
}

// endWouldBePause adds the would-be pause that began when the workload went
// Idle and ended at its latest activity, when a paused workload would have
// been woken. It returns how long that pause lasted and what it would have
// freed, and false when dry-run wasn't measuring as it began, such as for a
// workload already Idle when the operator was upgraded.
func (r *Reconciler) endWouldBePause(workload *v1alpha1.ManagedWorkload) (time.Duration, float64, bool) {
	var lastActivity time.Time
	if a := workload.Status.Activity; a != nil {
		lastActivity = a.LastActivityTime.Time
	}
	return r.endWouldBePauseAt(workload, lastActivity)
}

// endWouldBePauseNow ends an Idle workload's would-be pause now, as
// Hybernate stops measuring it: it lets go of the workload, or the workload
// was scaled to zero outside it. Its activity can be hours old, and the
// pause lasted until now regardless.
func (r *Reconciler) endWouldBePauseNow(workload *v1alpha1.ManagedWorkload) {
	if workload.Status.Phase == v1alpha1.PhaseIdle {
		r.endWouldBePauseAt(workload, r.now())
	}
}

// endWouldBePauseAt is endWouldBePause for a would-be pause that ended at
// end.
func (r *Reconciler) endWouldBePauseAt(workload *v1alpha1.ManagedWorkload, end time.Time) (time.Duration, float64, bool) {
	d := workload.Status.DryRun
	if !workload.Spec.DryRun || d == nil || d.Pauses == 0 || workload.Status.LastTransitionTime == nil {
		return 0, 0, false
	}
	slept := max(end.Sub(workload.Status.LastTransitionTime.Time), 0)
	rates := resolveCostRates(workload)
	var freed cost.Hours
	if rs := d.Resources; rs != nil {
		cores, gib := requested(rs)
		freed.CPU = addHours(&d.FreedCPUHours, cores, slept)
		freed.Memory = addHours(&d.FreedMemoryHours, gib, slept)
	}
	d.Slept.Duration += slept
	d.Resources = nil
	d.QueryActiveSincePause = false
	r.estimateDryRun(workload)
	return slept, freed.Price(rates), true
}

// dryRunSummary describes what dry-run has measured so far, for events.
func dryRunSummary(d *v1alpha1.DryRunStatus) string {
	times := "times"
	if d.Pauses == 1 {
		times = "time"
	}
	return fmt.Sprintf("since %s: would have paused %d %s, slept %s, freeing %s",
		d.Since.Format("Jan 2"), d.Pauses, times, human.Duration(d.Slept.Duration), d.EstimatedFreed)
}
