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
	"math"
	"strconv"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/human"
	opmetrics "github.com/napwake/napwake/internal/metrics"
)

// A pause is wrong when the workload's own use undoes it soon after it
// begins: a request, a person's click, a wake request or an activity
// annotation within wrongPauseWindow. The idle clock saw no activity, so
// the use went unseen, and the next idle clock waits longer, by
// v1alpha1.IdleWait, until a pause holds through the window.
const (
	conditionPausedWhileInUse = "PausedWhileInUse"

	ReasonPausedWhileInUse    = "PausedWhileInUse"
	reasonWokenSoonAfterPause = "WokenSoonAfterPause"
	reasonPauseHeld           = "PauseHeld"
	reasonNoIdlePolicy        = "NoIdlePolicy"

	// idleCPUKept is how long what a workload used idle stands while lower
	// readings come in, so one quiet night doesn't set a threshold that the
	// next, busier one would cross.
	idleCPUKept = 7 * 24 * time.Hour

	// maxWrongPauses caps the count well past where the idle wait stops
	// growing.
	maxWrongPauses = 32

	prometheusQueriesGuide = "https://docs.napwake.dev/guides/prometheus-signals/#example-queries"
)

// wrongPauseWindow is how soon after a pause the workload's own use makes
// it wrong, as v1alpha1.WrongPauseWindow has it for its idleAfter.
func wrongPauseWindow(workload *v1alpha1.ManagedWorkload) time.Duration {
	return v1alpha1.WrongPauseWindow(idleAfterFor(workload))
}

// idleWaitFor is how long the workload's idle clock waits without activity
// before a pause: idleAfter, longer after wrong pauses. It's never shorter
// than idleAfter. A would-be pause in dry-run waits idleAfter, since
// dry-run judges no pause wrong.
func idleWaitFor(workload *v1alpha1.ManagedWorkload) time.Duration {
	var wrong int32
	if w := workload.Status.IdleWait; w != nil && !workload.Spec.DryRun {
		wrong = w.WrongPauses
	}
	return v1alpha1.IdleWait(idleAfterFor(workload), wrong)
}

// endBackOff ends the back-off of a workload no pause can hold for now: one
// in dry-run, protected, or without an idle policy, for which no pause
// would ever end it. Its next pause, whenever one comes, waits idleAfter,
// and Napwake no longer says it was paused while in use. What it measured
// of the workload's CPU, and its last wrong pause, are kept. It reports
// whether that changed the workload's status.
func (r *Reconciler) endBackOff(workload *v1alpha1.ManagedWorkload, reason string) bool {
	changed := false
	if w := workload.Status.IdleWait; w != nil && w.WrongPauses > 0 {
		w.WrongPauses = 0
		changed = true
	}
	if meta.IsStatusConditionTrue(workload.Status.Conditions, conditionPausedWhileInUse) {
		r.clearCondition(workload, conditionPausedWhileInUse, reason)
		changed = true
	}
	return changed
}

// pauseBegan is when the workload's pause began: when it was scaled to
// zero, or, while it's still pausing without having been, when it started.
func (r *Reconciler) pauseBegan(workload *v1alpha1.ManagedWorkload) time.Time {
	if p := workload.Status.Pause; p != nil && p.PausedAt != nil {
		return p.PausedAt.Time
	}
	if workload.Status.Phase == v1alpha1.PhasePausing && workload.Status.LastTransitionTime != nil {
		return workload.Status.LastTransitionTime.Time
	}
	return r.now()
}

// judgePause judges the pause a wake ends, as the wake begins, and returns
// what to announce once the wake is written. A pause the workload's own use
// undid within the window was wrong: the count of wrong pauses grows, and a
// warning says what to change. One that lasted the window held: the count
// starts again. Anything else, such as the forecast waking it, says nothing
// either way, nor does a pause someone asked for, which the idle clock
// didn't decide, or a wake for awake dependents, whose use it was.
func (r *Reconciler) judgePause(ctx context.Context, workload *v1alpha1.ManagedWorkload, why *phaseEvent) func() {
	pause := workload.Status.Pause
	nothing := func() {}
	if pause == nil || why == nil || workload.Spec.DryRun {
		return nothing
	}
	began := r.pauseBegan(workload)
	after := max(r.now().Sub(began), 0)
	if after >= wrongPauseWindow(workload) {
		r.pauseHeld(workload)
		return nothing
	}
	if why.cause == "" || pause.Requested {
		return nothing
	}
	holds, err := r.heldByDependencies(ctx, workload)
	if err != nil {
		log.FromContext(ctx).Info("couldn't tell whether the workload woke for its dependents, so its pause "+
			"isn't judged", "workload", workload.Name, "namespace", workload.Namespace, "error", err.Error())
		return nothing
	}
	if holds.held() {
		return nothing
	}

	wait := workload.Status.IdleWait
	if wait == nil {
		wait = &v1alpha1.IdleWaitStatus{}
		workload.Status.IdleWait = wait
	}
	wait.WrongPauses = min(wait.WrongPauses+1, maxWrongPauses)
	wait.LastWrongPause = &v1alpha1.WrongPause{PausedAt: metav1.NewTime(began), After: metav1.Duration{Duration: after},
		By: why.cause, InUseCPU: pause.CPUBefore}
	if why.cause == v1alpha1.WokenByRequest || why.cause == v1alpha1.WokenByClick {
		wait.LastWrongPause.From = workload.Annotations[v1alpha1.AnnotationLastRequestFrom]
	}
	when := " " + human.Duration(after) + " after it paused"
	reason := reasonWokenSoonAfterPause
	if why.reason == ReasonPauseCancelled {
		when, reason = " as it paused", ReasonPauseCancelled
	}
	idleAfter := idleAfterFor(workload)
	message := fmt.Sprintf("%s%s, so it was in use: it now waits %s without activity before pausing, not %s, "+
		"until a pause holds. %s", wakeCauses[why.cause], when, human.Duration(idleWaitFor(workload)),
		human.Duration(idleAfter), r.whatToChange(workload, pause.CPUBefore))
	r.setCondition(workload, conditionPausedWhileInUse, metav1.ConditionTrue, reason, message)
	return func() {
		opmetrics.WrongPauses.WithLabelValues(workload.Namespace, workload.Name).Inc()
		r.emitEvent(workload, false, "Warning", ReasonPausedWhileInUse, actionEvaluateIdle, "%s", message)
	}
}

// wakeCauses say what undid a pause.
var wakeCauses = map[v1alpha1.WakeCause]string{
	v1alpha1.WokenByRequest:     "a request came",
	v1alpha1.WokenByClick:       "a person asked to wake it",
	v1alpha1.WokenByWakeRequest: "a wake request came",
	v1alpha1.WokenByAnnotation:  "an activity annotation came",
}

// endBackOffIfHeld ends the back-off of a paused workload whose pause has
// lasted the window, so status says so while it's still paused.
func (r *Reconciler) endBackOffIfHeld(workload *v1alpha1.ManagedWorkload) {
	if workload.Status.Pause == nil || workload.Status.Pause.PausedAt == nil || workload.Spec.DryRun ||
		r.now().Sub(r.pauseBegan(workload)) < wrongPauseWindow(workload) {
		return
	}
	r.pauseHeld(workload)
}

// pauseHeld records a pause that lasted the window: the idle clock was
// right, so the back-off ends, and what the workload used in the last
// minutes before it is what it uses idle.
func (r *Reconciler) pauseHeld(workload *v1alpha1.ManagedWorkload) {
	pause := workload.Status.Pause
	wait := workload.Status.IdleWait
	if idle, err := strconv.ParseFloat(pause.CPUJustBefore, 64); err == nil {
		if wait == nil {
			wait = &v1alpha1.IdleWaitStatus{}
			workload.Status.IdleWait = wait
		}
		measured := metav1.NewTime(r.pauseBegan(workload).Truncate(time.Second))
		before, err := strconv.ParseFloat(wait.IdleCPU, 64)
		current := err == nil && wait.IdleCPUAt != nil && measured.Sub(wait.IdleCPUAt.Time) < idleCPUKept
		if !current || idle > before {
			wait.IdleCPU, wait.IdleCPUAt = v1alpha1.FormatCPUThreshold(idle), &measured
		}
	}
	if wait != nil {
		wait.WrongPauses = 0
	}
	r.clearCondition(workload, conditionPausedWhileInUse, reasonPauseHeld)
}

// whatToChange says what would have kept the workload awake while in use:
// a CPU threshold between what it used in use and what it uses idle, when
// the one is clearly above the other, or else a Prometheus activity query
// that counts its requests. Without a measure of its idle use, it says so
// rather than guess one.
func (r *Reconciler) whatToChange(workload *v1alpha1.ManagedWorkload, inUseCPU string) string {
	const query = "a Prometheus activity query, such as its ingress's request rate: " + prometheusQueriesGuide
	inUse, err := strconv.ParseFloat(inUseCPU, 64)
	if err != nil {
		return "Count its requests with " + query
	}
	used := fmt.Sprintf("It used up to %s%% of its CPU requests while in use", v1alpha1.FormatCPUThreshold(inUse))
	var idle float64
	if wait := workload.Status.IdleWait; wait != nil {
		idle, err = strconv.ParseFloat(wait.IdleCPU, 64)
	}
	if workload.Status.IdleWait == nil || err != nil {
		return used + ", and Napwake hasn't measured it idle yet, so it can't suggest a CPU threshold until a " +
			"pause holds; or count its requests with " + query
	}
	used += fmt.Sprintf(", and up to %s%% idle", v1alpha1.FormatCPUThreshold(idle))
	suggested, ok := suggestThreshold(inUse, idle, cpuThresholdFor(workload))
	if !ok {
		return used + ", too close for a CPU threshold to tell apart: count its requests with " + query
	}
	setting := "idlePolicy.activity.cpuThreshold"
	if workload.Labels[v1alpha1.LabelFromLabel] == v1alpha1.True {
		setting = v1alpha1.AnnotationCPUThreshold
	}
	return fmt.Sprintf("%s: try %s: %q", used, setting, v1alpha1.FormatCPUThreshold(suggested))
}

// suggestThreshold is a CPU threshold halfway between what a workload used
// in use and idle, to a tenth, when its use in use is clearly above its
// idle use: at least twice it, and at least 0.2 points more, so the
// threshold sits apart from both. One that isn't below the current
// threshold, or the lowest the CRD takes, would change nothing.
func suggestThreshold(inUse, idle, current float64) (float64, bool) {
	if inUse < 2*idle || inUse-idle < 0.2 {
		return 0, false
	}
	suggested := math.Round((inUse+idle)/2*10) / 10
	if suggested < v1alpha1.MinCPUThreshold || suggested >= current || suggested <= idle || suggested >= inUse {
		return 0, false
	}
	return suggested, true
}

// recordCPUBefore records in the pause about to begin what the idle clock
// measured of the workload's CPU through the stretch that ran it out, and
// in its last minutes, for its judging once it ends.
func (r *Reconciler) recordCPUBefore(workload *v1alpha1.ManagedWorkload) {
	a, pause := workload.Status.Activity, workload.Status.Pause
	if a == nil || pause == nil {
		return
	}
	all, last, ok := r.quietCPU.peaks(workload.UID, a.LastActivityTime.Time, r.now(), wrongPauseWindow(workload))
	if !ok {
		return
	}
	pause.CPUBefore, pause.CPUJustBefore = v1alpha1.FormatCPUThreshold(all), v1alpha1.FormatCPUThreshold(last)
}

// quietCPU keeps, for each awake workload, the CPU it has used through the
// stretch since its clock last moved: the most, and the readings of the last
// minutes. A reading above the threshold moves the clock, so these are all
// below it. It's in memory only: a restart restarts the clock too.
type quietCPU struct {
	mu        sync.Mutex
	stretches map[types.UID]*cpuStretch
}

type cpuStretch struct {
	since  time.Time
	peak   float64
	recent []cpuReading
}

type cpuReading struct {
	at      time.Time
	percent float64
}

// see adds a reading at at to the stretch since since, keeping the readings
// of the last keep.
func (q *quietCPU) see(uid types.UID, since, at time.Time, percent float64, keep time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stretches == nil {
		q.stretches = map[types.UID]*cpuStretch{}
	}
	s := q.stretches[uid]
	if s == nil || !s.since.Equal(since) {
		s = &cpuStretch{since: since}
		q.stretches[uid] = s
	}
	s.peak = max(s.peak, percent)
	s.recent = append(s.recent, cpuReading{at: at, percent: percent})
	for len(s.recent) > 0 && at.Sub(s.recent[0].at) > keep {
		s.recent = s.recent[1:]
	}
}

// peaks is the most CPU the workload used in the stretch since since, and
// in its last within before now, if it was read in it.
func (q *quietCPU) peaks(uid types.UID, since, now time.Time, within time.Duration) (all, last float64, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	s := q.stretches[uid]
	if s == nil || !s.since.Equal(since) || len(s.recent) == 0 {
		return 0, 0, false
	}
	for _, reading := range s.recent {
		if now.Sub(reading.at) <= within {
			last = max(last, reading.percent)
		}
	}
	return s.peak, last, true
}

func (q *quietCPU) forget(uid types.UID) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.stretches, uid)
}
