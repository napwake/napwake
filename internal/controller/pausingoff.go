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

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

const (
	// conditionPausingOff is True, reason DisabledForTheInstall, on every
	// workload while pausing is off for the install that manages it, and
	// removed once it's on again: there's nothing to say about one that
	// pauses. Several installs can share a cluster, each with its own.
	conditionPausingOff         = "PausingOff"
	reasonDisabledForTheInstall = "DisabledForTheInstall"

	// reasonPausingOff answers a pause request while pausing is off, as
	// reasonDryRun does in dry-run.
	reasonPausingOff = "PausingOff"

	reasonPausingOffWake = "PausingOffWake"

	pausingOffMessage = "pausing is off for this install (pausing.enabled: false, or the operator's " +
		"--pausing=false): nothing it manages pauses, and everything wakes. Napwake measures this workload as in " +
		"dry-run"
)

// reportPausing says on the workload whether pausing is off for this
// install, writing its status when that changes it. It's written apart, as
// the reconcile begins, so every workload says so whatever else its
// reconcile does: one protected, or whose target is gone, writes nothing
// more. The condition only tells, so a write that fails is logged and made
// again by the next reconcile, rather than holding up this one, which may
// be waking the workload.
func (r *Reconciler) reportPausing(ctx context.Context, workload *v1alpha1.ManagedWorkload) {
	reported := meta.FindStatusCondition(workload.Status.Conditions, conditionPausingOff) != nil
	if reported == r.PausingOff {
		return
	}
	if r.PausingOff {
		r.setCondition(workload, conditionPausingOff, metav1.ConditionTrue, reasonDisabledForTheInstall,
			pausingOffMessage)
	} else {
		meta.RemoveStatusCondition(&workload.Status.Conditions, conditionPausingOff)
	}
	if err := r.Status().Update(ctx, workload); err != nil {
		log.FromContext(ctx).Info("couldn't record whether pausing is off for this install",
			"workload", workload.Name, "namespace", workload.Namespace, "error", err.Error())
	}
}

// whyMeasured is why a workload is measured rather than paused, as a
// condition's reason and in words. Pausing off for this install is named
// first: it holds whatever the workload's own dry-run says.
func (r *Reconciler) whyMeasured() (reason, because string) {
	if r.PausingOff {
		return reasonPausingOff, "pausing is off for this install"
	}
	return reasonDryRun, "dry-run is on"
}

// measuredWake says why a workload measured rather than paused is woken
// from the pause it's in.
func (r *Reconciler) measuredWake() *phaseEvent {
	reason := reasonDryRunWake
	if r.PausingOff {
		reason = reasonPausingOffWake
	}
	_, because := r.whyMeasured()
	return &phaseEvent{reason: reason, message: because + ", so Napwake wakes the workload it had paused"}
}
