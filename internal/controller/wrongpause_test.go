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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	opmetrics "github.com/napwake/napwake/internal/metrics"
)

// pausedFor is the named workload, with idleAfter 5m, paused ago at the end
// of a stretch in which its CPU peaked at 0.6% of its requests, and at 0.5%
// in its last minutes, as setup leaves it, its target at zero, and a
// reconciler for it whose clock the test moves.
func pausedFor(t *testing.T, name string, ago time.Duration, setup func(*v1alpha1.ManagedWorkload),
	objs ...client.Object) (*Reconciler, *time.Time) {
	t.Helper()
	now := monotonic(fixedTime)
	workload := depWorkload("default", name, v1alpha1.TargetKindDeployment, v1alpha1.PhasePaused)
	workload.Spec.IdlePolicy.IdleAfter = &metav1.Duration{Duration: 5 * time.Minute}
	pausedAt := metav1.NewTime(now.Add(-ago))
	workload.Status.Pause = &v1alpha1.PauseStatus{PreviousReplicas: 1, PausedAt: &pausedAt,
		WakeAnnotations: &v1alpha1.WakeAnnotations{}, CPUBefore: "0.6", CPUJustBefore: "0.5"}
	workload.Status.LastTransitionTime = &pausedAt
	if setup != nil {
		setup(workload)
	}
	r := depReconciler(t, &stubPauser{pauseDone: true, resumeDone: true},
		append([]client.Object{workload, targetDeploymentWithReplicas(name, "default", 0)}, objs...)...)
	r.Recorder = events.NewFakeRecorder(100)
	r.clock = func() time.Time { return now }
	return r, &now
}

func reconcileNamed(t *testing.T, r *Reconciler, name string) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), reconcileFor(name))
	require.NoError(t, err)
}

// requestCame stamps the named workload with a request at now, as the
// doorman does for one it holds.
func requestCame(t *testing.T, r *Reconciler, name string, now time.Time) {
	t.Helper()
	annotate(t, r, name, v1alpha1.AnnotationLastRequest, now.UTC().Format(time.RFC3339))
	annotate(t, r, name, v1alpha1.AnnotationLastRequestFrom, "10.244.3.8")
}

// A request that wakes a workload soon after its pause shows the pause was
// made while it was in use. Its idle clock then waits twice idleAfter
// before the next, and Napwake says so, with what to change.
func TestWrongPause_ARequestSoonAfterAPauseLengthensTheIdleClock(t *testing.T) {
	r, now := pausedFor(t, "api", 40*time.Second, nil)

	requestCame(t, r, "api", *now)
	reconcileNamed(t, r, "api")

	w := fetch(t, r, "api")
	require.Equal(t, v1alpha1.PhaseRunning, w.Status.Phase)
	require.NotNil(t, w.Status.IdleWait)
	assert.Equal(t, int32(1), w.Status.IdleWait.WrongPauses)
	last := w.Status.IdleWait.LastWrongPause
	require.NotNil(t, last)
	assert.Equal(t, v1alpha1.WokenByRequest, last.By)
	assert.Equal(t, 40*time.Second, last.After.Duration)
	assert.Equal(t, "10.244.3.8", last.From)
	assert.Equal(t, "0.6", last.InUseCPU)
	assert.Equal(t, now.Add(10*time.Minute).UTC(), w.Status.Activity.PauseAt.UTC(), "twice idleAfter")

	c := meta.FindStatusCondition(w.Status.Conditions, conditionPausedWhileInUse)
	require.NotNil(t, c)
	assert.Equal(t, metav1.ConditionTrue, c.Status)
	assert.Equal(t, reasonWokenSoonAfterPause, c.Reason)
	warned := recorded(r.Recorder.(*events.FakeRecorder), ReasonPausedWhileInUse)
	require.Len(t, warned, 1)
	assert.Contains(t, warned[0], "Warning PausedWhileInUse")
	assert.Contains(t, warned[0], "a request came 40 seconds after it paused, so it was in use: it now waits "+
		"10 minutes without activity before pausing, not 5 minutes, until a pause holds")
}

// Each wrong pause in a row doubles the wait again, up to idleAfter plus an
// hour.
func TestWrongPause_InARowDoublesAgain(t *testing.T) {
	r, now := pausedFor(t, "api", time.Minute, func(w *v1alpha1.ManagedWorkload) {
		w.Status.IdleWait = &v1alpha1.IdleWaitStatus{WrongPauses: 3}
	})

	requestCame(t, r, "api", *now)
	reconcileNamed(t, r, "api")

	w := fetch(t, r, "api")
	assert.Equal(t, int32(4), w.Status.IdleWait.WrongPauses)
	assert.Equal(t, now.Add(65*time.Minute).UTC(), w.Status.Activity.PauseAt.UTC(), "idleAfter plus an hour")
}

// Whatever a person or a tool does to wake a workload counts: a request, a
// person's click for a workload that asks first, a wake request, an
// activity annotation. A pause something called off as it began counts
// too. The wake's own event says what woke it, a person for a click (P48).
func TestWrongPause_WhatUndoesAPause(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*v1alpha1.ManagedWorkload)
		wake  func(t *testing.T, r *Reconciler, now time.Time)
		want  v1alpha1.WakeCause
		says  string
		woke  string
	}{
		{name: "a request", wake: func(t *testing.T, r *Reconciler, now time.Time) { requestCame(t, r, "api", now) },
			want: v1alpha1.WokenByRequest, says: "a request came 2 minutes after it paused",
			woke: "WokeByActivity api: a request is waiting, waking"},
		{name: "a person's click", want: v1alpha1.WokenByClick, says: "a person asked to wake it 2 minutes after it paused",
			woke:  "WokeByActivity api: a person asked to wake it, waking",
			setup: func(w *v1alpha1.ManagedWorkload) { w.Spec.Wake = &v1alpha1.WakeSpec{Mode: v1alpha1.WakeOnAsk} },
			wake:  func(t *testing.T, r *Reconciler, now time.Time) { requestCame(t, r, "api", now) }},
		{name: "a wake request", want: v1alpha1.WokenByWakeRequest, says: "a wake request came 2 minutes after it paused",
			wake: func(t *testing.T, r *Reconciler, _ time.Time) {
				annotate(t, r, "api", v1alpha1.AnnotationWakeRequested, "wake-1")
			}},
		{name: "an activity annotation", want: v1alpha1.WokenByAnnotation,
			says: "an activity annotation came 2 minutes after it paused",
			wake: func(t *testing.T, r *Reconciler, now time.Time) {
				annotate(t, r, "api", v1alpha1.AnnotationLastActivity, now.UTC().Format(time.RFC3339))
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, now := pausedFor(t, "api", 2*time.Minute, tt.setup)

			tt.wake(t, r, *now)
			reconcileNamed(t, r, "api")

			w := fetch(t, r, "api")
			require.NotNil(t, w.Status.IdleWait)
			assert.Equal(t, int32(1), w.Status.IdleWait.WrongPauses)
			assert.Equal(t, tt.want, w.Status.IdleWait.LastWrongPause.By)
			emitted := recorded(r.Recorder.(*events.FakeRecorder), "")
			warned := slices.DeleteFunc(slices.Clone(emitted), func(e string) bool {
				return !strings.Contains(e, ReasonPausedWhileInUse)
			})
			require.Len(t, warned, 1)
			assert.Contains(t, warned[0], tt.says)
			if tt.woke != "" {
				assert.Contains(t, emitted, "Normal "+tt.woke)
			}
		})
	}
}

// A pause a request calls off as it begins was made in use.
func TestWrongPause_ACancelledPauseWasWrong(t *testing.T) {
	r, _, now := pausingReconciler(t, func(w *v1alpha1.ManagedWorkload) {
		w.Spec.IdlePolicy.IdleAfter = &metav1.Duration{Duration: 5 * time.Minute}
	})
	reconcileAPI(t, r)
	stampRequest(t, r, *now)
	reconcileAPI(t, r)

	w := fetch(t, r, "api")
	require.NotNil(t, w.Status.IdleWait)
	assert.Equal(t, int32(1), w.Status.IdleWait.WrongPauses)
	assert.Equal(t, ReasonPauseCancelled, meta.FindStatusCondition(w.Status.Conditions, conditionPausedWhileInUse).Reason)
	warned := recorded(r.Recorder.(*events.FakeRecorder), ReasonPausedWhileInUse)
	require.Len(t, warned, 1)
	assert.Contains(t, warned[0], "a request came as it paused, so it was in use")
}

// What isn't the workload's own use doesn't make its pause wrong: a wake
// after the window, the forecast waking it ahead of demand, a Prometheus
// query, a pause someone asked for, and a wake for a dependent that uses
// it. Napwake decided none of those wrongly.
func TestWrongPause_WhatDoesntMakeAPauseWrong(t *testing.T) {
	web := depWorkload("default", "web", v1alpha1.TargetKindDeployment, v1alpha1.PhaseResuming,
		v1alpha1.DependencyRef{Kind: v1alpha1.TargetKindDeployment, Name: "api"})
	tests := []struct {
		name  string
		ago   time.Duration
		setup func(*v1alpha1.ManagedWorkload)
		objs  []client.Object
	}{
		{name: "a request after the window", ago: 6 * time.Minute},
		{name: "a pause asked for", ago: time.Minute,
			setup: func(w *v1alpha1.ManagedWorkload) { w.Status.Pause.Requested = true }},
		{name: "a dependent waking", ago: time.Minute, objs: []client.Object{web}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, now := pausedFor(t, "api", tt.ago, tt.setup, tt.objs...)

			annotate(t, r, "api", v1alpha1.AnnotationLastActivity, now.UTC().Format(time.RFC3339))
			reconcileNamed(t, r, "api")

			w := fetch(t, r, "api")
			require.Equal(t, v1alpha1.PhaseRunning, w.Status.Phase)
			if w.Status.IdleWait != nil {
				assert.Zero(t, w.Status.IdleWait.WrongPauses)
			}
			assert.Empty(t, recorded(r.Recorder.(*events.FakeRecorder), ReasonPausedWhileInUse))
		})
	}
	for _, why := range []*phaseEvent{{reason: ReasonAutoResume}, {reason: ReasonWokeByActivity, message: queryWakeMessage},
		{reason: reasonDryRunWake}} {
		r, _ := pausedFor(t, "api", time.Minute, nil)
		w := fetch(t, r, "api")
		if why.message == queryWakeMessage {
			r.recordQueryWake(w)
		}
		r.judgePause(context.Background(), w, why)
		assert.Nil(t, w.Status.IdleWait, "%s", why.reason)
	}
}

// A pause that holds through the window shows the clock was right: the
// back-off ends, and what the workload used in its last minutes before the
// pause is what it uses idle.
func TestWrongPause_APauseThatHoldsEndsTheBackOff(t *testing.T) {
	r, now := pausedFor(t, "api", 20*time.Minute, func(w *v1alpha1.ManagedWorkload) {
		w.Status.IdleWait = &v1alpha1.IdleWaitStatus{WrongPauses: 3}
		meta.SetStatusCondition(&w.Status.Conditions, metav1.Condition{Type: conditionPausedWhileInUse,
			Status: metav1.ConditionTrue, Reason: reasonWokenSoonAfterPause, Message: "earlier"})
	})

	requestCame(t, r, "api", *now)
	reconcileNamed(t, r, "api")

	w := fetch(t, r, "api")
	assert.Zero(t, w.Status.IdleWait.WrongPauses)
	assert.Equal(t, "0.5", w.Status.IdleWait.IdleCPU, "the most it used in its last minutes before the pause")
	assert.Equal(t, now.Add(5*time.Minute).UTC(), w.Status.Activity.PauseAt.UTC(), "idleAfter again")
	c := meta.FindStatusCondition(w.Status.Conditions, conditionPausedWhileInUse)
	require.NotNil(t, c)
	assert.Equal(t, metav1.ConditionFalse, c.Status)
	assert.Equal(t, reasonPauseHeld, c.Reason)
	assert.Empty(t, recorded(r.Recorder.(*events.FakeRecorder), ReasonPausedWhileInUse))
}

// The back-off is ended by a pause that holds whether or not it's still
// paused: status says so while it is.
func TestWrongPause_APausedWorkloadPastTheWindowEndsTheBackOff(t *testing.T) {
	r, _ := pausedFor(t, "api", 6*time.Minute, func(w *v1alpha1.ManagedWorkload) {
		w.Status.IdleWait = &v1alpha1.IdleWaitStatus{WrongPauses: 2}
	})

	reconcileNamed(t, r, "api")

	w := fetch(t, r, "api")
	assert.Equal(t, v1alpha1.PhasePaused, w.Status.Phase)
	assert.Zero(t, w.Status.IdleWait.WrongPauses)
}

// What the idle clock waits after a wrong pause is in status, so a restart,
// which restarts the clock, keeps it.
func TestWrongPause_TheBackOffOutlivesARestart(t *testing.T) {
	r, now := pausedFor(t, "api", 40*time.Second, nil)
	requestCame(t, r, "api", *now)
	reconcileNamed(t, r, "api")
	woken := targetDeploymentWithReplicas("api", "default", 0)
	require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(woken), woken))
	woken.Spec.Replicas = ptr.To(int32(1))
	require.NoError(t, r.Update(context.Background(), woken))

	restarted(r, fetch(t, r, "api"))
	*now = now.Add(time.Minute)
	reconcileNamed(t, r, "api")

	w := fetch(t, r, "api")
	assert.Equal(t, v1alpha1.ActivitySourceUnobserved, w.Status.Activity.LastActivitySource)
	assert.Equal(t, now.Add(10*time.Minute).UTC(), w.Status.Activity.PauseAt.UTC())
}

// Napwake says what to change, with a number when it can: a CPU threshold
// between what the workload used in use and idle, when the one sits clearly
// above the other; otherwise a Prometheus query that counts its requests.
// It doesn't guess an idle use it hasn't measured.
func TestWrongPause_SaysWhatToChange(t *testing.T) {
	tests := []struct {
		name            string
		inUse, idle     string
		optedInByLabel  bool
		thresholdString string
		want            string
	}{
		{name: "a threshold between in use and idle", inUse: "0.6", idle: "0.2", optedInByLabel: true,
			want: `It used up to 0.6% of its CPU requests while in use, and up to 0.2% idle: try ` +
				`napwake.dev/cpu-threshold: "0.4"`},
		{name: "written by hand, the field", inUse: "0.6", idle: "0.2",
			want: `try idlePolicy.activity.cpuThreshold: "0.4"`},
		{name: "a whole number", inUse: "6", idle: "1", optedInByLabel: true,
			want: `try napwake.dev/cpu-threshold: "3.5"`},
		{name: "idle at nothing", inUse: "0.8", idle: "0", optedInByLabel: true,
			want: `and up to 0% idle: try napwake.dev/cpu-threshold: "0.4"`},
		{name: "no idle use measured yet", inUse: "0.6",
			want: "It used up to 0.6% of its CPU requests while in use, and Napwake hasn't measured it idle yet, so " +
				"it can't suggest a CPU threshold until a pause holds; or count its requests with a Prometheus " +
				"activity query, such as its ingress's request rate: " + prometheusQueriesGuide},
		{name: "too close to tell apart", inUse: "0.3", idle: "0.25",
			want: "It used up to 0.3% of its CPU requests while in use, and up to 0.25% idle, too close for a CPU " +
				"threshold to tell apart: count its requests with a Prometheus activity query, such as its " +
				"ingress's request rate: " + prometheusQueriesGuide},
		{name: "no CPU measured", idle: "0.2",
			want: "Count its requests with a Prometheus activity query, such as its ingress's request rate: " +
				prometheusQueriesGuide},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, now := pausedFor(t, "api", time.Minute, func(w *v1alpha1.ManagedWorkload) {
				w.Status.Pause.CPUBefore = tt.inUse
				if tt.idle != "" {
					w.Status.IdleWait = &v1alpha1.IdleWaitStatus{IdleCPU: tt.idle, IdleCPUAt: ptr.To(metav1.NewTime(fixedTime))}
				}
				if tt.optedInByLabel {
					w.Labels = map[string]string{v1alpha1.LabelFromLabel: v1alpha1.True}
				}
			})

			requestCame(t, r, "api", *now)
			reconcileNamed(t, r, "api")

			warned := recorded(r.Recorder.(*events.FakeRecorder), ReasonPausedWhileInUse)
			require.Len(t, warned, 1)
			assert.Contains(t, warned[0], tt.want)
			c := meta.FindStatusCondition(fetch(t, r, "api").Status.Conditions, conditionPausedWhileInUse)
			require.NotNil(t, c)
			assert.Contains(t, c.Message, tt.want, "the condition says the same")
		})
	}
}

// What the workload used idle is the most over the last week's pauses that
// held, so one quiet night doesn't set a threshold the next would cross.
func TestWrongPause_IdleUseIsTheMostOverAWeek(t *testing.T) {
	tests := []struct {
		name     string
		measured time.Duration
		before   string
		want     string
	}{
		{name: "a higher one this week stands", measured: 2 * 24 * time.Hour, before: "0.7", want: "0.7"},
		{name: "a lower one this week gives way", measured: 2 * 24 * time.Hour, before: "0.3", want: "0.5"},
		{name: "one over a week old gives way", measured: 8 * 24 * time.Hour, before: "0.7", want: "0.5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, now := pausedFor(t, "api", 20*time.Minute, func(w *v1alpha1.ManagedWorkload) {
				w.Status.IdleWait = &v1alpha1.IdleWaitStatus{IdleCPU: tt.before,
					IdleCPUAt: ptr.To(metav1.NewTime(fixedTime.Add(-tt.measured)))}
			})

			requestCame(t, r, "api", *now)
			reconcileNamed(t, r, "api")

			assert.Equal(t, tt.want, fetch(t, r, "api").Status.IdleWait.IdleCPU)
		})
	}
}

// The idle clock measures the workload's CPU through each stretch without
// activity, and in its last minutes, which a pause records: the most it
// used, below the threshold.
func TestWrongPause_APauseRecordsTheCPUBeforeIt(t *testing.T) {
	target := clockTarget("app:v1", nil)
	workload := clockWorkload(fixedTime.Add(-4*time.Minute), target)
	workload.Spec.IdlePolicy.IdleAfter = &metav1.Duration{Duration: 30 * time.Minute}
	metrics := stubMetrics{cpuPerReplica: 1000, replicas: 1}
	pauser := &stubPauser{pauseDone: true}
	r := newAutomationReconciler(t, workload, &stubForecaster{}, automationOpts{metrics: &metrics, pauser: pauser})
	now := monotonic(fixedTime)
	r.clock = func() time.Time { return now }
	ctx := context.Background()
	// 0.9% early in the stretch, 0.3% at most in its last 15 minutes.
	for minute := range 26 {
		metrics.cpuMillis = 3
		if minute == 5 {
			metrics.cpuMillis = 9
		}
		_, err := r.reconcileAutomation(ctx, workload, target)
		require.NoError(t, err)
		require.Equal(t, v1alpha1.PhaseRunning, workload.Status.Phase, "minute %d", minute)
		now = now.Add(time.Minute)
	}
	_, err := r.reconcileAutomation(ctx, workload, target)
	require.NoError(t, err)

	require.NotEqual(t, v1alpha1.PhaseRunning, workload.Status.Phase)
	assert.Equal(t, "0.9", workload.Status.Pause.CPUBefore)
	assert.Equal(t, "0.3", workload.Status.Pause.CPUJustBefore)
}

// napwake_wrong_pauses_total counts the pauses made in use, and nothing
// else.
func TestWrongPause_IsCounted(t *testing.T) {
	counted := func(name string) float64 {
		return testutil.ToFloat64(opmetrics.WrongPauses.WithLabelValues("default", name))
	}
	wrong := counted("counted")
	r, now := pausedFor(t, "counted", time.Minute, nil)
	requestCame(t, r, "counted", *now)
	reconcileNamed(t, r, "counted")
	assert.Equal(t, wrong+1, counted("counted"), "a pause a request undid")

	held := counted("held")
	r, now = pausedFor(t, "held", time.Hour, nil)
	requestCame(t, r, "held", *now)
	reconcileNamed(t, r, "held")
	assert.Equal(t, held, counted("held"), "a pause that held")

	asked := counted("asked")
	r, now = pausedFor(t, "asked", time.Minute, func(w *v1alpha1.ManagedWorkload) {
		w.Status.Pause.Requested = true
	})
	requestCame(t, r, "asked", *now)
	reconcileNamed(t, r, "asked")
	assert.Equal(t, asked, counted("asked"), "a pause asked for")

	opmetrics.WrongPauses.WithLabelValues("default", "gone").Inc()
	opmetrics.DeleteWorkload("default", "gone")
	assert.Zero(t, counted("gone"), "a deleted workload's series goes, and one of its name starts again")
}

// A dependent that wakes stamps its dependencies only once it's Resuming,
// so a dependency reconciled for the stamp finds it awake, holding it, and
// its pause isn't judged wrong: it was the dependent's use.
func TestWrongPause_ADependentStampsItsDependenciesOnceItsWaking(t *testing.T) {
	pausedAt := metav1.NewTime(monotonic(fixedTime).Add(-time.Minute))
	web := depWorkload("default", "web", v1alpha1.TargetKindDeployment, v1alpha1.PhasePaused,
		v1alpha1.DependencyRef{Kind: v1alpha1.TargetKindDeployment, Name: "api"})
	web.Status.Pause = &v1alpha1.PauseStatus{PreviousReplicas: 1, StartedAt: &pausedAt, PausedAt: &pausedAt,
		WakeAnnotations: &v1alpha1.WakeAnnotations{}}
	web.Status.LastTransitionTime = &pausedAt
	r, now := pausedFor(t, "api", time.Minute, nil, web, targetDeploymentWithReplicas("web", "default", 0))
	var webPhase v1alpha1.WorkloadPhase
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch,
			opts ...client.PatchOption) error {
			if client.ObjectKeyFromObject(obj) == (client.ObjectKey{Namespace: "default", Name: "api"}) {
				if _, ok := obj.(*v1alpha1.ManagedWorkload); ok && webPhase == "" {
					var dependent v1alpha1.ManagedWorkload
					require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "default", Name: "web"}, &dependent))
					webPhase = dependent.Status.Phase
				}
			}
			return c.Patch(ctx, obj, patch, opts...)
		}})

	requestCame(t, r, "web", *now)
	reconcileNamed(t, r, "web")
	require.NotEmpty(t, webPhase, "the dependency was stamped")
	assert.Equal(t, v1alpha1.PhaseResuming, webPhase, "stamped once its dependent's wake is written")

	reconcileNamed(t, r, "api")
	w := fetch(t, r, "api")
	require.NotEqual(t, v1alpha1.PhasePaused, w.Status.Phase)
	if w.Status.IdleWait != nil {
		assert.Zero(t, w.Status.IdleWait.WrongPauses)
	}
}

// backedOff is the named workload, running, after two wrong pauses in a
// row, with what setup makes of it, and a reconciler for it and objs,
// which include its target, if it has one.
func backedOff(t *testing.T, name string, setup func(*v1alpha1.ManagedWorkload),
	objs ...client.Object) *Reconciler {
	t.Helper()
	now := monotonic(fixedTime)
	workload := depWorkload("default", name, v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning)
	workload.Spec.IdlePolicy.IdleAfter = &metav1.Duration{Duration: 5 * time.Minute}
	workload.Status.IdleWait = &v1alpha1.IdleWaitStatus{WrongPauses: 2}
	workload.Status.Conditions = []metav1.Condition{{Type: conditionPausedWhileInUse, Status: metav1.ConditionTrue,
		Reason: reasonWokenSoonAfterPause, LastTransitionTime: metav1.NewTime(now)}}
	if setup != nil {
		setup(workload)
	}
	r := depReconciler(t, &stubPauser{pauseDone: true, resumeDone: true}, append(objs, workload)...)
	r.Recorder = events.NewFakeRecorder(100)
	r.clock = func() time.Time { return now }
	return r
}

// Once no pause can hold, the back-off would never end, and Napwake would
// go on saying the workload was paused while in use. So dry-run,
// protection, having no idle policy, an ignored target and a target gone
// end it, and clear what Napwake said, and a would-be pause in dry-run
// waits idleAfter, as a first pause would.
func TestWrongPause_TheBackOffEndsOnceNoPauseCanHold(t *testing.T) {
	target := func() *appsv1.Deployment { return targetDeploymentWithReplicas("api", "default", 1) }
	ignored := target()
	ignored.Labels = map[string]string{v1alpha1.LabelIgnore: v1alpha1.True}
	tests := []struct {
		name  string
		setup func(*v1alpha1.ManagedWorkload)
		objs  []client.Object
	}{
		{name: "dry-run", setup: func(w *v1alpha1.ManagedWorkload) { w.Spec.DryRun = true },
			objs: []client.Object{target()}},
		{name: "protected", objs: []client.Object{target(), defaultNamespace(protectedLabel)}},
		{name: "no idle policy", setup: func(w *v1alpha1.ManagedWorkload) { w.Spec.IdlePolicy = nil },
			objs: []client.Object{target()}},
		{name: "ignored", objs: []client.Object{ignored}},
		{name: "its target gone"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := backedOff(t, "api", tt.setup, tt.objs...)

			reconcileNamed(t, r, "api")

			w := fetch(t, r, "api")
			require.NotNil(t, w.Status.IdleWait)
			assert.Zero(t, w.Status.IdleWait.WrongPauses)
			assert.False(t, meta.IsStatusConditionTrue(w.Status.Conditions, conditionPausedWhileInUse))
			assert.Equal(t, idleAfterFor(w), idleWaitFor(w))
		})
	}

	inDryRun := backedOff(t, "api", func(w *v1alpha1.ManagedWorkload) { w.Spec.DryRun = true }, target())
	assert.Equal(t, 5*time.Minute, idleWaitFor(fetch(t, inDryRun, "api")),
		"a would-be pause doesn't wait the back-off, even before it's written as ended")
}

// A dependency woken by its dependent's stamp reads its dependents past the
// cache before it judges its pause wrong, since the cache can still show
// the dependent paused when the stamp's event arrives. A dependent awake
// on the API server holds it, so its pause isn't wrong.
func TestWrongPause_ADependencyReadsItsDependentsPastTheCache(t *testing.T) {
	webKey := client.ObjectKey{Namespace: "default", Name: "web"}
	web := depWorkload("default", "web", v1alpha1.TargetKindDeployment, v1alpha1.PhasePaused,
		v1alpha1.DependencyRef{Kind: v1alpha1.TargetKindDeployment, Name: "api"})
	r, now := pausedFor(t, "api", time.Minute, nil, web)
	r.PodReader = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object,
			opts ...client.GetOption) error {
			err := c.Get(ctx, key, obj, opts...)
			if w, ok := obj.(*v1alpha1.ManagedWorkload); ok && err == nil && key == webKey {
				w.Status.Phase = v1alpha1.PhaseResuming
			}
			return err
		}})

	annotate(t, r, "api", v1alpha1.AnnotationLastActivity, now.UTC().Format(time.RFC3339))
	reconcileNamed(t, r, "api")

	w := fetch(t, r, "api")
	require.NotEqual(t, v1alpha1.PhasePaused, w.Status.Phase)
	if w.Status.IdleWait != nil {
		assert.Zero(t, w.Status.IdleWait.WrongPauses)
	}
	assert.Empty(t, recorded(r.Recorder.(*events.FakeRecorder), ReasonPausedWhileInUse))
}
