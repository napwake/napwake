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
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/forecast"
	opmetrics "github.com/napwake/napwake/internal/metrics"
	"github.com/napwake/napwake/internal/signal"
)

// idleCPU is usage well under the 10% default threshold: 5m of 1000m.
var idleCPU = stubMetrics{cpuMillis: 5, cpuPerReplica: 1000, replicas: 1}

func clockTarget(image string, annotations map[string]string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default", Annotations: annotations},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: image}}},
			},
		},
	}
}

// clockWorkload is a running workload whose last activity was lastActivity,
// last evaluated a moment ago, and already fingerprinted against target.
func clockWorkload(lastActivity time.Time, target *appsv1.Deployment) *v1alpha1.ManagedWorkload {
	workload := automationWorkload(v1alpha1.PhaseRunning)
	workload.Spec.IdlePolicy = &v1alpha1.IdlePolicySpec{
		IdleAfter: &metav1.Duration{Duration: time.Hour},
	}
	evaluated := metav1.NewTime(fixedTime.Add(-30 * time.Second))
	workload.Status.Activity = &v1alpha1.ActivityStatus{
		LastActivityTime:   metav1.NewTime(lastActivity),
		LastActivitySource: v1alpha1.ActivitySourceCPU,
		LastEvaluatedTime:  &evaluated,
		TemplateHash:       podTemplateHash(target),
	}
	return workload
}

// remember puts the workload's clock in the operator's memory, as a
// running operator's last check, which evaluated it, left it.
func remember(r *Reconciler, workload *v1alpha1.ManagedWorkload) {
	clock := workload.Status.Activity
	r.activityMemo.put(workload.UID, *clock.DeepCopy(), clock.LastEvaluatedTime.Time, nil)
}

// holding has the reconciler hold the clock of each ManagedWorkload it
// starts with, as a running operator does: the test constructors make one
// that has been running, and a test of a restart says so with restarted.
func holding(r *Reconciler, objs ...client.Object) *Reconciler {
	for _, obj := range objs {
		if w, ok := obj.(*v1alpha1.ManagedWorkload); ok && w != nil && w.Status.Activity != nil &&
			w.Status.Activity.LastEvaluatedTime != nil {
			remember(r, w)
		}
	}
	return r
}

// restarted has the reconciler forget the workload's clock, as an operator
// that has just started, or just been elected, holds none.
func restarted(r *Reconciler, workload *v1alpha1.ManagedWorkload) {
	r.activityMemo.forget(workload.UID)
}

type clockOpts struct {
	metrics stubMetrics
	engine  *stubForecaster
	pauser  *stubPauser
}

func runClock(t *testing.T, workload *v1alpha1.ManagedWorkload, target *appsv1.Deployment, opts clockOpts) *stubPauser {
	t.Helper()
	if opts.engine == nil {
		opts.engine = &stubForecaster{phase: forecast.Observing}
	}
	if opts.pauser == nil {
		opts.pauser = &stubPauser{pauseDone: true, resumeDone: true}
	}
	metrics := opts.metrics
	r := newAutomationReconciler(t, workload, opts.engine, automationOpts{metrics: &metrics, pauser: opts.pauser})
	_, err := r.reconcileAutomation(context.Background(), workload, target)
	require.NoError(t, err)
	return opts.pauser
}

func TestActivityClock_NewWorkloadStartsTheClock(t *testing.T) {
	target := clockTarget("app:v1", nil)
	workload := automationWorkload(v1alpha1.PhaseRunning)
	workload.Spec.IdlePolicy = &v1alpha1.IdlePolicySpec{}

	pauser := runClock(t, workload, target, clockOpts{metrics: idleCPU})

	activity := workload.Status.Activity
	require.NotNil(t, activity)
	assert.Equal(t, v1alpha1.ActivitySourceCreated, activity.LastActivitySource)
	assert.True(t, activity.PauseAt.Time.Equal(fixedTime.Add(defaultIdleAfter)), "pause is due a full idleAfter after creation")
	assert.Equal(t, podTemplateHash(target), activity.TemplateHash)
	assert.Equal(t, 0, pauser.pauseCalls)
}

func TestActivityClock_PausesAfterIdleAfter(t *testing.T) {
	target := clockTarget("app:v1", nil)
	workload := clockWorkload(fixedTime.Add(-61*time.Minute), target)

	pauser := runClock(t, workload, target, clockOpts{metrics: idleCPU})

	assert.Equal(t, 1, pauser.pauseCalls)
	assert.Equal(t, v1alpha1.PhasePaused, workload.Status.Phase)
}

// The IdleDetected event says, in words, how long the workload has been
// idle, since what, and what Napwake does about it: in dry-run, only what
// it would have done.
func TestActivityClock_IdleDetectedSaysWhatHappens(t *testing.T) {
	tests := []struct {
		name   string
		dryRun bool
		source v1alpha1.ActivitySource
		want   string
	}{
		{name: "dry-run", dryRun: true, source: v1alpha1.ActivitySourceCreated,
			want: "[dry-run] api: idle for 2 hours, since Napwake started managing it; would have paused"},
		{name: "pausing", source: v1alpha1.ActivitySourceRequest,
			want: "api: idle for 2 hours, since a request woke it; pausing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := clockTarget("app:v1", nil)
			workload := clockWorkload(fixedTime.Add(-2*time.Hour), target)
			workload.Spec.DryRun = tt.dryRun
			workload.Status.Activity.LastActivitySource = tt.source
			metrics := idleCPU
			r := newAutomationReconciler(t, workload, &stubForecaster{phase: forecast.Observing},
				automationOpts{metrics: &metrics, pauser: &stubPauser{pauseDone: true}})

			_, err := r.reconcileAutomation(context.Background(), workload, target)
			require.NoError(t, err)

			idle := recorded(r.Recorder.(*events.FakeRecorder), ReasonIdleDetected)
			require.Len(t, idle, 1)
			assert.True(t, strings.HasSuffix(idle[0], tt.want), "got %q", idle[0])
		})
	}
}

func TestActivityClock_StaysAwake(t *testing.T) {
	stale := fixedTime.Add(-61 * time.Minute)

	tests := []struct {
		name       string
		workload   func(target *appsv1.Deployment) *v1alpha1.ManagedWorkload
		target     *appsv1.Deployment
		metrics    stubMetrics
		wantSource v1alpha1.ActivitySource
	}{
		{
			name:   "idleAfter not reached",
			target: clockTarget("app:v1", nil),
			workload: func(d *appsv1.Deployment) *v1alpha1.ManagedWorkload {
				return clockWorkload(fixedTime.Add(-59*time.Minute), d)
			},
			metrics: idleCPU,
		},
		{
			name:       "CPU above threshold",
			target:     clockTarget("app:v1", nil),
			workload:   func(d *appsv1.Deployment) *v1alpha1.ManagedWorkload { return clockWorkload(stale, d) },
			metrics:    stubMetrics{cpuMillis: 500, cpuPerReplica: 1000, replicas: 1},
			wantSource: v1alpha1.ActivitySourceCPU,
		},
		{
			name:     "one pod of three serving while the others are Pending",
			target:   clockTarget("app:v1", nil),
			workload: func(d *appsv1.Deployment) *v1alpha1.ManagedWorkload { return clockWorkload(stale, d) },
			// 25% of the one pod's request; 8% of all three replicas'.
			metrics:    stubMetrics{cpuMillis: 250, cpuPerReplica: 1000, replicas: 3, measuredPods: 1},
			wantSource: v1alpha1.ActivitySourceCPU,
		},
		{
			name:   "rollout changed the pod template",
			target: clockTarget("app:v2", nil),
			workload: func(_ *appsv1.Deployment) *v1alpha1.ManagedWorkload {
				return clockWorkload(stale, clockTarget("app:v1", nil))
			},
			metrics:    idleCPU,
			wantSource: v1alpha1.ActivitySourceRollout,
		},
		{
			name: "last-activity annotation on the target",
			target: clockTarget("app:v1", map[string]string{
				v1alpha1.AnnotationLastActivity: fixedTime.Add(-5 * time.Minute).Format(time.RFC3339),
			}),
			workload:   func(d *appsv1.Deployment) *v1alpha1.ManagedWorkload { return clockWorkload(stale, d) },
			metrics:    idleCPU,
			wantSource: v1alpha1.ActivitySourceAnnotation,
		},
		{
			name:   "last-activity annotation on the ManagedWorkload",
			target: clockTarget("app:v1", nil),
			workload: func(d *appsv1.Deployment) *v1alpha1.ManagedWorkload {
				w := clockWorkload(stale, d)
				w.Annotations = map[string]string{
					v1alpha1.AnnotationLastActivity: fixedTime.Add(-5 * time.Minute).Format(time.RFC3339),
				}
				return w
			},
			metrics:    idleCPU,
			wantSource: v1alpha1.ActivitySourceAnnotation,
		},
		{
			name: "active-until hold",
			target: clockTarget("app:v1", map[string]string{
				v1alpha1.AnnotationActiveUntil: fixedTime.Add(time.Hour).Format(time.RFC3339),
			}),
			workload: func(d *appsv1.Deployment) *v1alpha1.ManagedWorkload { return clockWorkload(stale, d) },
			metrics:  idleCPU,
		},
		{
			name:   "operator wasn't watching",
			target: clockTarget("app:v1", nil),
			workload: func(d *appsv1.Deployment) *v1alpha1.ManagedWorkload {
				w := clockWorkload(fixedTime.Add(-3*time.Hour), d)
				evaluated := metav1.NewTime(fixedTime.Add(-2 * time.Hour))
				w.Status.Activity.LastEvaluatedTime = &evaluated
				return w
			},
			metrics:    idleCPU,
			wantSource: v1alpha1.ActivitySourceUnobserved,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workload := tt.workload(tt.target)

			pauser := runClock(t, workload, tt.target, clockOpts{metrics: tt.metrics})

			assert.Equal(t, 0, pauser.pauseCalls)
			assert.Equal(t, v1alpha1.PhaseRunning, workload.Status.Phase)
			if tt.wantSource != "" {
				assert.Equal(t, tt.wantSource, workload.Status.Activity.LastActivitySource)
				assert.True(t, workload.Status.Activity.PauseAt.After(fixedTime))
			}
		})
	}
}

func TestActivityClock_FutureLastActivityIsTreatedAsNow(t *testing.T) {
	target := clockTarget("app:v1", map[string]string{
		v1alpha1.AnnotationLastActivity: fixedTime.Add(30 * 24 * time.Hour).Format(time.RFC3339),
	})
	workload := clockWorkload(fixedTime.Add(-61*time.Minute), target)

	runClock(t, workload, target, clockOpts{metrics: idleCPU})

	assert.True(t, workload.Status.Activity.LastActivityTime.Time.Equal(fixedTime),
		"a far-future annotation must not hold the workload awake indefinitely")
}

func TestActivityClock_MalformedAnnotationIsIgnored(t *testing.T) {
	target := clockTarget("app:v1", map[string]string{v1alpha1.AnnotationLastActivity: "yesterday"})
	workload := clockWorkload(fixedTime.Add(-61*time.Minute), target)

	pauser := runClock(t, workload, target, clockOpts{metrics: idleCPU})

	assert.Equal(t, 1, pauser.pauseCalls)
}

// A value that isn't an RFC 3339 time, such as epoch seconds from a portal,
// never counts as activity, so the workload pauses while the tool thinks
// it's keeping it awake. It's reported once per value, not every check.
func TestActivityClock_MalformedAnnotationIsReportedOnce(t *testing.T) {
	target := clockTarget("app:v1", nil)
	workload := clockWorkload(fixedTime.Add(-30*time.Minute), target)
	workload.Annotations = map[string]string{v1alpha1.AnnotationLastActivity: "1773489600"}
	metrics := idleCPU
	r := newAutomationReconciler(t, workload, &stubForecaster{}, automationOpts{metrics: &metrics})
	check := func() {
		t.Helper()
		_, err := r.reconcileAutomation(context.Background(), workload, target)
		require.NoError(t, err)
	}

	check()
	check()
	emitted := drainEvents(t, r)
	assert.Equal(t, 1, strings.Count(emitted, ReasonInvalidActivityAnnotation), "reported once: %s", emitted)
	assert.Contains(t, emitted, `"1773489600"`)
	assert.Contains(t, emitted, "ManagedWorkload api")

	workload.Annotations[v1alpha1.AnnotationLastActivity] = "2026-03-14 11:59:00"
	check()
	assert.Contains(t, drainEvents(t, r), ReasonInvalidActivityAnnotation, "and again for a new value")
}

func TestActivityClock_DoesNotActWithoutCPUData(t *testing.T) {
	tests := []struct {
		name       string
		metrics    stubMetrics
		wantReason string
	}{
		{name: "metrics API down", metrics: stubMetrics{err: errors.New("metrics API unavailable")}, wantReason: "MetricsUnavailable"},
		{name: "no CPU requests", metrics: stubMetrics{cpuMillis: 50, replicas: 1, noCPURequests: true}, wantReason: "NoCPURequests"},
		{name: "requests that say none", metrics: stubMetrics{cpuMillis: 50, replicas: 1,
			requestsErr: fmt.Errorf("%w: the running pods request no CPU", opmetrics.ErrNoCPURequests)},
			wantReason: "NoCPURequests"},
		{name: "requests that can't be read", metrics: stubMetrics{cpuMillis: 50, replicas: 1,
			requestsErr: errors.New("listing pods: etcdserver: request timed out")}, wantReason: "MetricsUnavailable"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := clockTarget("app:v1", nil)
			workload := clockWorkload(fixedTime.Add(-61*time.Minute), target)

			pauser := runClock(t, workload, target, clockOpts{metrics: tt.metrics})

			assert.Equal(t, 0, pauser.pauseCalls, "without CPU data a busy workload looks idle, so don't act")
			cond := meta.FindStatusCondition(workload.Status.Conditions, conditionMetricsAvailable)
			require.NotNil(t, cond)
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			assert.Equal(t, tt.wantReason, cond.Reason)
		})
	}
}

// A source that can't be read hides whatever use it would have shown, so
// the time it's out isn't idle time: back, and seeing nothing, it doesn't
// find the outage idle and pause at once, but only once it has seen
// idleAfter of nothing.
func TestActivityClock_AnOutageIsNotIdleTime(t *testing.T) {
	const outage = 40 * time.Minute
	tests := []struct {
		name    string
		promQL  bool
		breakIt func(m *stubMetrics, promDown *atomic.Bool)
		fixIt   func(m *stubMetrics, promDown *atomic.Bool)
	}{
		{
			name:    "Prometheus down",
			promQL:  true,
			breakIt: func(_ *stubMetrics, promDown *atomic.Bool) { promDown.Store(true) },
			fixIt:   func(_ *stubMetrics, promDown *atomic.Bool) { promDown.Store(false) },
		},
		{
			name:    "metrics API down",
			breakIt: func(m *stubMetrics, _ *atomic.Bool) { m.err = errors.New("metrics API unavailable") },
			fixIt:   func(m *stubMetrics, _ *atomic.Bool) { m.err = nil },
		},
		{
			name:    "CPU requests unreadable",
			breakIt: func(m *stubMetrics, _ *atomic.Bool) { m.noCPURequests = true },
			fixIt:   func(m *stubMetrics, _ *atomic.Bool) { m.noCPURequests = false },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var promDown atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if promDown.Load() {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
			}))
			t.Cleanup(srv.Close)

			target := clockTarget("app:v1", nil)
			workload := clockWorkload(fixedTime.Add(-50*time.Minute), target)
			if tt.promQL {
				workload.Spec.IdlePolicy.Activity = &v1alpha1.ActivitySpec{
					Prometheus: []v1alpha1.PrometheusActivity{{PromQL: `sum(rate(http_requests_total[5m]))`}},
				}
			}
			metrics := idleCPU
			pauser := &stubPauser{pauseDone: true}
			r := newAutomationReconciler(t, workload, &stubForecaster{}, automationOpts{metrics: &metrics, pauser: pauser})
			r.prometheus = signal.NewPrometheus(srv.URL)
			now := fixedTime
			r.clock = func() time.Time { return now }
			check := func() {
				t.Helper()
				_, err := r.reconcileAutomation(context.Background(), workload, target)
				require.NoError(t, err)
			}

			tt.breakIt(&metrics, &promDown)
			for ; now.Before(fixedTime.Add(outage)); now = now.Add(activityCheckInterval) {
				check()
			}
			require.Zero(t, pauser.pauseCalls, "nothing is paused while a source is out")
			assert.Equal(t, v1alpha1.ActivitySourceUnobserved, workload.Status.Activity.LastActivitySource)

			tt.fixIt(&metrics, &promDown)
			// The client backs off a failed endpoint by the wall clock, which
			// this test's clock doesn't move.
			r.prometheus = signal.NewPrometheus(srv.URL)
			// The first check that reads every source finds nothing evaluated
			// since the outage began, so the clock starts over from it.
			recovered := now
			for ; now.Before(recovered.Add(time.Hour)); now = now.Add(activityCheckInterval) {
				check()
				require.Zero(t, pauser.pauseCalls, "paused at %s, within idleAfter of the outage ending",
					now.Sub(fixedTime))
			}
			check()
			assert.Equal(t, 1, pauser.pauseCalls, "paused once idleAfter has passed with every source seeing nothing")
			gaps := recorded(r.Recorder.(*events.FakeRecorder), ReasonUnobserved)
			require.Len(t, gaps, 1, "the outage is announced once, as it ends")
			// From the last evaluation, 30 seconds before the outage.
			assert.Contains(t, gaps[0], "Napwake couldn't read its activity for 41 minutes, "+
				"so its idle clock starts again")
		})
	}
}

// A real pause held back once the clock ran out, as by a GitOps tool's
// hour, waits awake. A gap in watching restarts its clock as for any awake
// workload, so it's no longer idle, but nothing calls the gap activity.
func TestActivityClock_AGapEndsAnIdleWorkloadsWaitAsAGap(t *testing.T) {
	target := clockTarget("app:v1", nil)
	workload := clockWorkload(fixedTime.Add(-90*time.Minute), target)
	workload.Status.Phase = v1alpha1.PhaseIdle
	idleSince := metav1.NewTime(fixedTime.Add(-30 * time.Minute))
	workload.Status.LastTransitionTime = &idleSince
	evaluated := metav1.NewTime(fixedTime.Add(-20 * time.Minute))
	workload.Status.Activity.LastEvaluatedTime = &evaluated
	metrics := idleCPU
	pauser := &stubPauser{pauseDone: true}
	r := newAutomationReconciler(t, workload, &stubForecaster{}, automationOpts{metrics: &metrics, pauser: pauser})
	remember(r, workload)

	_, err := r.reconcileAutomation(context.Background(), workload, target)
	require.NoError(t, err)

	assert.Zero(t, pauser.pauseCalls)
	assert.Equal(t, v1alpha1.PhaseRunning, workload.Status.Phase)
	emitted := drainEvents(t, r)
	assert.Contains(t, emitted,
		"Napwake wasn't watching for 20 minutes, so its idle clock starts again; no longer idle")
	assert.NotContains(t, emitted, ReasonActivityResumed)
}

// An activity annotation is stamped to the second, so one stamped in the
// second a workload went Idle ends the would-be pause: it can't be told from
// one just before, and keeping the workload awake is the safe mistake. The
// operator's own times are exact, so activity it saw before a pause request
// in the same second doesn't undo the request.
func TestActiveSinceClockRanOut_CountsTheSecondItRanOut(t *testing.T) {
	wentIdle := fixedTime.Add(400 * time.Millisecond)
	tests := []struct {
		name     string
		source   v1alpha1.ActivitySource
		activity time.Time
		want     bool
	}{
		{name: "an annotation before the second it went Idle", source: v1alpha1.ActivitySourceAnnotation,
			activity: fixedTime.Add(-time.Second)},
		{name: "an annotation stamped in that second", source: v1alpha1.ActivitySourceAnnotation,
			activity: fixedTime, want: true},
		{name: "an annotation after it", source: v1alpha1.ActivitySourceAnnotation,
			activity: fixedTime.Add(2 * time.Second), want: true},
		{name: "CPU just before it, in the same second", source: v1alpha1.ActivitySourceCPU,
			activity: fixedTime.Add(100 * time.Millisecond)},
		{name: "CPU after it", source: v1alpha1.ActivitySourceCPU, activity: wentIdle.Add(time.Second), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workload := clockWorkload(tt.activity, clockTarget("app:v1", nil))
			workload.Status.Activity.LastActivitySource = tt.source
			workload.Status.Phase = v1alpha1.PhaseIdle
			began := metav1.NewTime(wentIdle)
			workload.Status.LastTransitionTime = &began

			assert.Equal(t, tt.want, activeSinceClockRanOut(workload, wentIdle.Add(time.Minute), wentIdle))
		})
	}
}

// A woken workload's new pods have no metrics for the first scrape or so.
// That blind minute mustn't replace what woke it as its last activity: the
// wake by request is what status should show.
func TestActivityClock_AShortBlindSpotKeepsWhatWokeIt(t *testing.T) {
	target := clockTarget("app:v1", nil)
	workload := clockWorkload(fixedTime, target)
	workload.Status.Activity.LastActivitySource = v1alpha1.ActivitySourceRequest
	evaluated := metav1.NewTime(fixedTime)
	workload.Status.Activity.LastEvaluatedTime = &evaluated
	metrics := idleCPU
	metrics.err = errors.New("no pod metrics yet")
	r := newAutomationReconciler(t, workload, &stubForecaster{}, automationOpts{metrics: &metrics, pauser: &stubPauser{}})
	now := fixedTime
	r.clock = func() time.Time { return now }

	for range 2 {
		now = now.Add(activityCheckInterval / 2)
		_, err := r.reconcileAutomation(context.Background(), workload, target)
		require.NoError(t, err)
	}

	assert.Equal(t, v1alpha1.ActivitySourceRequest, workload.Status.Activity.LastActivitySource)
	assert.Equal(t, fixedTime, workload.Status.Activity.LastActivityTime.UTC())
}

// A gap in watching is never counted as idle time once it could bring the
// pause forward by much: a short idleAfter tolerates less of one, and a
// restart none at all.
func TestActivityClock_AGapInWatchingRestartsTheClock(t *testing.T) {
	tests := []struct {
		name      string
		idleAfter time.Duration
		idleFor   time.Duration
		gap       time.Duration
		inMemory  bool
		wantPause bool
	}{
		{name: "an outage longer than half a short idleAfter", idleAfter: 5 * time.Minute, idleFor: 6 * time.Minute,
			gap: 3 * time.Minute, inMemory: true},
		{name: "a delayed check within a long idleAfter", idleAfter: time.Hour, idleFor: 61 * time.Minute,
			gap: 3 * time.Minute, inMemory: true, wantPause: true},
		{name: "an outage beyond the cap on a long idleAfter", idleAfter: 2 * time.Hour, idleFor: 121 * time.Minute,
			gap: unobservedGap + time.Minute, inMemory: true},
		{name: "a restart with the written evaluation minutes old", idleAfter: time.Hour, idleFor: 61 * time.Minute,
			gap: 3 * time.Minute},
		{name: "a restart with the written evaluation moments old", idleAfter: time.Hour, idleFor: 61 * time.Minute,
			gap: 30 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := clockTarget("app:v1", nil)
			workload := clockWorkload(fixedTime.Add(-tt.idleFor), target)
			workload.Spec.IdlePolicy.IdleAfter = &metav1.Duration{Duration: tt.idleAfter}
			evaluated := metav1.NewTime(fixedTime.Add(-tt.gap))
			workload.Status.Activity.LastEvaluatedTime = &evaluated
			metrics := idleCPU
			pauser := &stubPauser{pauseDone: true}
			r := newAutomationReconciler(t, workload, &stubForecaster{}, automationOpts{metrics: &metrics, pauser: pauser})
			if !tt.inMemory {
				restarted(r, workload)
			}

			_, err := r.reconcileAutomation(context.Background(), workload, target)
			require.NoError(t, err)

			assert.Equal(t, tt.wantPause, pauser.pauseCalls == 1)
			if !tt.wantPause {
				assert.Equal(t, v1alpha1.ActivitySourceUnobserved, workload.Status.Activity.LastActivitySource)
			}
		})
	}
}

// A clock this operator doesn't hold was written by an operator before
// it, which may have seen activity it hadn't written yet: up to a tenth of
// idleAfter, and 5 minutes, of it. So however recently the clock was
// written, a restart restarts an awake workload's clock, which can delay
// its pause but never bring it forward. A clock this operator started
// itself, as when it creates a workload's or wakes one paused before it
// took over, is its own.
// A restart whose first check can't read a source still restarts the
// clock, and says so once a check can: the gap waits in memory until it's
// reported, rather than being lost to a check that, in memory, finds none.
func TestActivityClock_ARestartIsReportedOnceASourceCanBeRead(t *testing.T) {
	tests := []struct {
		name    string
		promQL  bool
		blind   int
		breakIt func(m *stubMetrics, promDown *atomic.Bool)
		fixIt   func(m *stubMetrics, promDown *atomic.Bool)
	}{
		{name: "metrics API down", breakIt: func(m *stubMetrics, _ *atomic.Bool) { m.err = errors.New("metrics API unavailable") },
			fixIt: func(m *stubMetrics, _ *atomic.Bool) { m.err = nil }},
		// Out for longer than a gap may last, the clock restarts again in
		// memory, and the restart is still what's said.
		{name: "metrics API down for 10 minutes", blind: 10,
			breakIt: func(m *stubMetrics, _ *atomic.Bool) { m.err = errors.New("metrics API unavailable") },
			fixIt:   func(m *stubMetrics, _ *atomic.Bool) { m.err = nil }},
		{name: "CPU requests unreadable", breakIt: func(m *stubMetrics, _ *atomic.Bool) { m.noCPURequests = true },
			fixIt: func(m *stubMetrics, _ *atomic.Bool) { m.noCPURequests = false }},
		{name: "Prometheus down", promQL: true,
			breakIt: func(_ *stubMetrics, promDown *atomic.Bool) { promDown.Store(true) },
			fixIt:   func(_ *stubMetrics, promDown *atomic.Bool) { promDown.Store(false) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var promDown atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if promDown.Load() {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
			}))
			t.Cleanup(srv.Close)
			target := clockTarget("app:v1", nil)
			workload := clockWorkload(fixedTime.Add(-30*time.Minute), target)
			if tt.promQL {
				workload.Spec.IdlePolicy.Activity = &v1alpha1.ActivitySpec{
					Prometheus: []v1alpha1.PrometheusActivity{{PromQL: `sum(rate(http_requests_total[5m]))`}},
				}
			}
			metrics := idleCPU
			r := newAutomationReconciler(t, workload, &stubForecaster{}, automationOpts{metrics: &metrics})
			r.prometheus = signal.NewPrometheus(srv.URL)
			restarted(r, workload)
			now := fixedTime
			r.clock = func() time.Time { return now }
			check := func() {
				t.Helper()
				_, err := r.reconcileAutomation(context.Background(), workload, target)
				require.NoError(t, err)
			}

			tt.breakIt(&metrics, &promDown)
			for range max(tt.blind, 1) {
				check()
				now = now.Add(activityCheckInterval)
			}
			assert.Equal(t, v1alpha1.ActivitySourceUnobserved, workload.Status.Activity.LastActivitySource)
			tt.fixIt(&metrics, &promDown)
			r.prometheus = signal.NewPrometheus(srv.URL)
			check()
			now = now.Add(activityCheckInterval)
			check()

			gaps := recorded(r.Recorder.(*events.FakeRecorder), ReasonUnobserved)
			require.Len(t, gaps, 1, "the restart is announced once, by the first check that can read every source")
			assert.Contains(t, gaps[0], "the operator restarted, or another took over")
		})
	}
}

func TestActivityClock_ARestartRestartsAnAwakeClock(t *testing.T) {
	target := clockTarget("app:v1", nil)
	t.Run("written moments before the restart", func(t *testing.T) {
		workload := clockWorkload(fixedTime.Add(-61*time.Minute), target)
		metrics := idleCPU
		pauser := &stubPauser{pauseDone: true}
		r := newAutomationReconciler(t, workload, &stubForecaster{}, automationOpts{metrics: &metrics, pauser: pauser})
		restarted(r, workload)

		_, err := r.reconcileAutomation(context.Background(), workload, target)
		require.NoError(t, err)

		assert.Zero(t, pauser.pauseCalls)
		assert.Equal(t, v1alpha1.ActivitySourceUnobserved, workload.Status.Activity.LastActivitySource)
		assert.True(t, workload.Status.Activity.PauseAt.Time.Equal(fixedTime.Add(time.Hour)))
		gap := recorded(r.Recorder.(*events.FakeRecorder), ReasonUnobserved)
		require.Len(t, gap, 1)
		assert.Contains(t, gap[0], "the operator restarted, or another took over, so its idle clock starts again; "+
			"it wasn't watching for up to 30 seconds")
	})
	for _, source := range []v1alpha1.ActivitySource{v1alpha1.ActivitySourceCreated, v1alpha1.ActivitySourceRequest} {
		t.Run("started by this operator: "+string(source), func(t *testing.T) {
			workload := clockWorkload(fixedTime, target)
			metrics := idleCPU
			r := newAutomationReconciler(t, workload, &stubForecaster{}, automationOpts{metrics: &metrics})
			restarted(r, workload)
			r.resetActivity(workload, source)
			r.clock = func() time.Time { return fixedTime.Add(activityCheckInterval) }

			_, err := r.reconcileAutomation(context.Background(), workload, target)
			require.NoError(t, err)

			assert.Equal(t, source, workload.Status.Activity.LastActivitySource)
			assert.Empty(t, recorded(r.Recorder.(*events.FakeRecorder), ReasonUnobserved))
		})
	}
}

func TestActivityClock_ZeroReplicasIsIdleNotMissingData(t *testing.T) {
	target := clockTarget("app:v1", nil)
	workload := clockWorkload(fixedTime.Add(-61*time.Minute), target)
	// Scaled to zero, the Metrics API has no pods to report on at all.
	metrics := &zeroReplicaMetrics{stubMetrics{err: fmt.Errorf("%w for default/api", opmetrics.ErrNoPodMetrics)}}
	pauser := &stubPauser{pauseDone: true}
	r := newAutomationReconciler(t, workload, &stubForecaster{}, automationOpts{pauser: pauser})
	r.metrics = metrics

	_, err := r.reconcileAutomation(context.Background(), workload, target)
	require.NoError(t, err)

	assert.Equal(t, 1, pauser.pauseCalls)
}

func TestActivityClock_ForecastVeto(t *testing.T) {
	tests := []struct {
		name      string
		engine    *stubForecaster
		wantPause bool
	}{
		{name: "confident forecast of demand defers the pause", engine: &stubForecaster{phase: forecast.DailyActive, predictValue: 30}},
		{name: "confident forecast of no demand", engine: &stubForecaster{phase: forecast.DailyActive, predictValue: 2}, wantPause: true},
		{name: "unconfident forecast is ignored", engine: &stubForecaster{phase: forecast.DailySuggesting, predictValue: 30}, wantPause: true},
		// autoResume would wake it straight back for the rest of this hour.
		{name: "demand forecast for the rest of this hour", engine: &stubForecaster{phase: forecast.DailyActive,
			predictByHour: map[int]float64{0: 30, 1: 2}}},
		{name: "demand forecast for the next hour", engine: &stubForecaster{phase: forecast.DailyActive,
			predictByHour: map[int]float64{0: 2, 1: 30}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := clockTarget("app:v1", nil)
			workload := clockWorkload(fixedTime.Add(-61*time.Minute), target)

			pauser := runClock(t, workload, target, clockOpts{metrics: idleCPU, engine: tt.engine})

			assert.Equal(t, tt.wantPause, pauser.pauseCalls == 1)
		})
	}
}

func vetoEvents(recorder *events.FakeRecorder) int {
	n := 0
	for len(recorder.Events) > 0 {
		if strings.Contains(<-recorder.Events, ReasonIdleVetoed) {
			n++
		}
	}
	return n
}

// A veto lasts as long as the forecast expects demand, and the clock checks
// every minute meanwhile. The condition says so throughout; the event marks
// only its beginning.
func TestActivityClock_ForecastVetoIsACondition(t *testing.T) {
	target := clockTarget("app:v1", nil)
	workload := clockWorkload(fixedTime.Add(-61*time.Minute), target)
	engine := &stubForecaster{phase: forecast.DailyActive, predictByHour: map[int]float64{0: 2, 1: 30}}
	metrics := idleCPU
	pauser := &stubPauser{pauseDone: true}
	r := newAutomationReconciler(t, workload, engine, automationOpts{metrics: &metrics, pauser: pauser})
	recorder := r.Recorder.(*events.FakeRecorder)

	for range 3 {
		_, err := r.reconcileAutomation(context.Background(), workload, target)
		require.NoError(t, err)
	}

	cond := meta.FindStatusCondition(workload.Status.Conditions, conditionIdleVetoed)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, "ForecastExpectsDemand", cond.Reason)
	assert.Contains(t, cond.Message, "30% of requests in the hour from 13:00 UTC", "says when demand is expected")
	assert.Equal(t, 1, vetoEvents(recorder), "one event for the veto, not one a minute")
	assert.Zero(t, pauser.pauseCalls)
	written := getWorkload(t, r, "api")
	assert.True(t, meta.IsStatusConditionTrue(written.Status.Conditions, conditionIdleVetoed), "and it's written")
	require.NotNil(t, written.Status.Activity.ForecastBusyFrom, "with the hour, for tools to show")
	assert.Equal(t, "13:00", written.Status.Activity.ForecastBusyFrom.UTC().Format("15:04"))

	engine.predictByHour[1] = 2
	_, err := r.reconcileAutomation(context.Background(), workload, target)
	require.NoError(t, err)

	assert.True(t, meta.IsStatusConditionFalse(workload.Status.Conditions, conditionIdleVetoed), "the veto is over")
	assert.Nil(t, workload.Status.Activity.ForecastBusyFrom)
	assert.Equal(t, 1, pauser.pauseCalls)
}

// The event follows the write that records the veto, so a conflict doesn't
// announce one veto twice.
func TestActivityClock_ForecastVetoEventFollowsTheWrite(t *testing.T) {
	target := clockTarget("app:v1", nil)
	workload := clockWorkload(fixedTime.Add(-61*time.Minute), target)
	engine := &stubForecaster{phase: forecast.DailyActive, predictValue: 300}
	metrics := idleCPU
	r := newAutomationReconciler(t, workload, engine, automationOpts{metrics: &metrics})
	recorder := r.Recorder.(*events.FakeRecorder)
	fail := true
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if fail {
				return apierrors.NewConflict(v1alpha1.GroupVersion.WithResource("managedworkloads").GroupResource(), obj.GetName(), errors.New("stale"))
			}
			return c.SubResource(sub).Update(ctx, obj, opts...)
		}})

	_, err := r.reconcileAutomation(context.Background(), workload.DeepCopy(), target)
	require.Error(t, err)
	assert.Zero(t, vetoEvents(recorder), "nothing is announced until it's recorded")

	fail = false
	_, err = r.reconcileAutomation(context.Background(), workload, target)
	require.NoError(t, err)
	assert.Equal(t, 1, vetoEvents(recorder))
}

// A dry-run workload gone Idle is measuring a would-be pause, which a real
// pause would already be. The forecast doesn't hold back a pause it's too
// late for, so it doesn't say it does.
func TestActivityClock_ForecastDoesNotVetoAPauseUnderWay(t *testing.T) {
	target := clockTarget("app:v1", nil)
	workload := clockWorkload(fixedTime.Add(-61*time.Minute), target)
	workload.Spec.DryRun = true
	workload.Status.Phase = v1alpha1.PhaseIdle
	engine := &stubForecaster{phase: forecast.DailyActive, predictValue: 300}
	metrics := idleCPU
	r := newAutomationReconciler(t, workload, engine, automationOpts{metrics: &metrics})

	_, err := r.reconcileAutomation(context.Background(), workload, target)
	require.NoError(t, err)

	assert.Equal(t, v1alpha1.PhaseIdle, workload.Status.Phase)
	assert.False(t, meta.IsStatusConditionTrue(workload.Status.Conditions, conditionIdleVetoed))
	assert.Zero(t, vetoEvents(r.Recorder.(*events.FakeRecorder)))
}

// meshMetrics is a workload with an injected sidecar: its pod requests
// twice what its own containers do.
type meshMetrics struct{ stubMetrics }

func (m *meshMetrics) PodRequestsPerReplica(_ context.Context, _ *v1alpha1.ManagedWorkload) (float64, float64, error) {
	return 2 * m.cpuPerReplica, m.memoryPerReplica, nil
}

// The veto and autoResume judge the forecast by one measure, so a forecast
// that holds an idle workload awake wakes it once it's paused. Judged
// against the whole pod's request, sidecars included, the same forecast was
// half the utilization once paused, and never woke it.
func TestActivityClock_VetoAndAutoResumeAgree(t *testing.T) {
	target := clockTarget("app:v1", nil)
	workload := clockWorkload(fixedTime.Add(-61*time.Minute), target)
	workload.Spec.IdlePolicy.AutoResume = true
	engine := &stubForecaster{phase: forecast.DailyActive, predictValue: 15}
	pauser := &stubPauser{pauseDone: true, resumeDone: true}
	r := newAutomationReconciler(t, workload, engine, automationOpts{pauser: pauser})
	r.metrics = &meshMetrics{stubMetrics{cpuMillis: 5, cpuPerReplica: 100, replicas: 1}}

	_, err := r.reconcileAutomation(context.Background(), workload, target)
	require.NoError(t, err)
	require.True(t, meta.IsStatusConditionTrue(workload.Status.Conditions, conditionIdleVetoed),
		"a forecast of 15%% holds it awake")

	engine.predictValue = 5
	_, err = r.reconcileAutomation(context.Background(), workload, target)
	require.NoError(t, err)
	require.Equal(t, v1alpha1.PhasePaused, workload.Status.Phase)

	engine.predictValue = 15
	r.clock = func() time.Time { return fixedTime.Add(5 * time.Minute) }
	_, err = r.reconcileAutomation(context.Background(), workload, target)
	require.NoError(t, err)
	assert.Equal(t, 1, pauser.resumeCalls, "the forecast that held it awake wakes it")
}

func TestActivityClock_DryRunReportsIdleOnceWithoutPausing(t *testing.T) {
	target := clockTarget("app:v1", nil)
	workload := clockWorkload(fixedTime.Add(-61*time.Minute), target)
	workload.Spec.DryRun = true
	metrics := idleCPU
	pauser := &stubPauser{pauseDone: true}
	r := newAutomationReconciler(t, workload, &stubForecaster{}, automationOpts{metrics: &metrics, pauser: pauser})

	for range 2 {
		_, err := r.reconcileAutomation(context.Background(), workload, target)
		require.NoError(t, err)
	}

	assert.Equal(t, 0, pauser.pauseCalls)
	assert.Equal(t, v1alpha1.PhaseIdle, workload.Status.Phase)
	recorder, ok := r.Recorder.(*events.FakeRecorder)
	require.True(t, ok)
	assert.Len(t, recorder.Events, 1, "the would-pause event fires when the clock runs out, not every check")
}

func TestActivityClock_ActivityTakesIdleWorkloadBackToRunning(t *testing.T) {
	target := clockTarget("app:v1", nil)
	workload := clockWorkload(fixedTime.Add(-61*time.Minute), target)
	workload.Spec.DryRun = true
	workload.Status.Phase = v1alpha1.PhaseIdle

	runClock(t, workload, target, clockOpts{metrics: stubMetrics{cpuMillis: 500, cpuPerReplica: 1000, replicas: 1}})

	assert.Equal(t, v1alpha1.PhaseRunning, workload.Status.Phase)
}

func TestActivityClock_AnnotationWakesPausedWorkload(t *testing.T) {
	pausedAt := metav1.NewTime(fixedTime.Add(-2 * time.Hour))

	tests := []struct {
		name       string
		target     *appsv1.Deployment
		onWorkload map[string]string
		// beforePause says the annotations were already there when the
		// pause began, so its record holds them.
		beforePause bool
		wantWake    bool
		wantSource  v1alpha1.ActivitySource
	}{
		{
			name: "last-activity newer than the pause",
			target: clockTarget("app:v1", map[string]string{
				v1alpha1.AnnotationLastActivity: fixedTime.Add(-time.Minute).Format(time.RFC3339),
			}),
			wantWake:   true,
			wantSource: v1alpha1.ActivitySourceWoke,
		},
		{
			name:   "a request the doorman is holding",
			target: clockTarget("app:v1", nil),
			onWorkload: map[string]string{
				v1alpha1.AnnotationLastRequest: fixedTime.Add(-time.Second).Format(time.RFC3339),
			},
			wantWake:   true,
			wantSource: v1alpha1.ActivitySourceRequest,
		},
		{
			name:   "a request from before the pause",
			target: clockTarget("app:v1", nil),
			onWorkload: map[string]string{
				v1alpha1.AnnotationLastRequest: fixedTime.Add(-3 * time.Hour).Format(time.RFC3339),
			},
			beforePause: true,
		},
		{
			name: "active-until in the future",
			target: clockTarget("app:v1", map[string]string{
				v1alpha1.AnnotationActiveUntil: fixedTime.Add(time.Hour).Format(time.RFC3339),
			}),
			wantWake:   true,
			wantSource: v1alpha1.ActivitySourceWoke,
		},
		{
			name: "last-activity from before the pause",
			target: clockTarget("app:v1", map[string]string{
				v1alpha1.AnnotationLastActivity: fixedTime.Add(-3 * time.Hour).Format(time.RFC3339),
			}),
			beforePause: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workload := clockWorkload(fixedTime.Add(-3*time.Hour), tt.target)
			workload.Annotations = tt.onWorkload
			workload.Status.Phase = v1alpha1.PhasePaused
			recorded := &v1alpha1.WakeAnnotations{}
			if tt.beforePause {
				recorded.Workload = activityAnnotationValues(workload)
				recorded.Target = activityAnnotationValues(tt.target)
			}
			workload.Status.Pause = &v1alpha1.PauseStatus{PreviousReplicas: 2, PausedAt: &pausedAt,
				WakeAnnotations: recorded}

			pauser := runClock(t, workload, tt.target, clockOpts{metrics: idleCPU})

			assert.Equal(t, tt.wantWake, pauser.resumeCalls == 1)
			if tt.wantWake {
				assert.Equal(t, v1alpha1.PhaseRunning, workload.Status.Phase)
				assert.Equal(t, tt.wantSource, workload.Status.Activity.LastActivitySource,
					"the clock records what woke it")
				assert.True(t, workload.Status.Activity.LastActivityTime.Time.Equal(fixedTime))
			}
		})
	}
}

func TestPodTemplateHash(t *testing.T) {
	base := clockTarget("app:v1", nil)

	scaled := clockTarget("app:v1", nil)
	scaled.Spec.Replicas = ptr.To(int32(0))
	scaled.Generation = 7
	assert.Equal(t, podTemplateHash(base), podTemplateHash(scaled), "pausing and resuming must not look like a deploy")

	assert.NotEqual(t, podTemplateHash(base), podTemplateHash(clockTarget("app:v2", nil)))
}

// The hash is worked out once for each version of the target, not on each
// of the checks that need it.
func TestTemplateHashes(t *testing.T) {
	var hashes templateHashes
	target := clockTarget("app:v1", nil)
	target.UID, target.ResourceVersion = "deploy-uid", "1"
	v1 := hashes.of("workload-uid", target)
	require.Equal(t, podTemplateHash(target), v1)

	target.Spec.Template.Spec.Containers[0].Image = "app:v2"
	assert.Equal(t, v1, hashes.of("workload-uid", target), "the same version isn't hashed again")

	target.ResourceVersion = "2"
	assert.Equal(t, podTemplateHash(target), hashes.of("workload-uid", target), "a new version is")
	assert.NotEqual(t, v1, hashes.of("workload-uid", target))

	recreated := clockTarget("app:v3", nil)
	recreated.UID, recreated.ResourceVersion = "recreated-uid", "2"
	assert.Equal(t, podTemplateHash(recreated), hashes.of("workload-uid", recreated), "and so is a new target")

	unsaved := clockTarget("app:v4", nil)
	assert.Equal(t, podTemplateHash(unsaved), hashes.of("workload-uid", unsaved))
}

func TestNextCheck(t *testing.T) {
	now := fixedTime
	assert.Equal(t, activityCheckInterval, nextCheck(now, now.Add(time.Hour), time.Time{}))
	assert.Equal(t, 20*time.Second, nextCheck(now, now.Add(20*time.Second), time.Time{}))
	assert.Equal(t, 10*time.Second, nextCheck(now, now.Add(time.Hour), now.Add(10*time.Second)))
	assert.Equal(t, activityCheckInterval, nextCheck(now, now.Add(-time.Minute), time.Time{}), "an overdue deadline falls back to the poll")
}

// A workload whose clock predates fingerprinting (or whose target couldn't be
// read before) has no stored hash. Seeing the template for the first time is
// not a deploy, so it must not hold the workload awake.
func TestActivityClock_FirstFingerprintIsNotARollout(t *testing.T) {
	target := clockTarget("app:v1", nil)
	workload := clockWorkload(fixedTime.Add(-61*time.Minute), target)
	workload.Status.Activity.TemplateHash = ""

	pauser := runClock(t, workload, target, clockOpts{metrics: idleCPU})

	assert.Equal(t, 1, pauser.pauseCalls)
	assert.Equal(t, podTemplateHash(target), workload.Status.Activity.TemplateHash)
}

func prometheusServer(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func promValue(v string) string {
	return `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1234567890,"` + v + `"]}]}}`
}

func TestActivityClock_PrometheusSource(t *testing.T) {
	tests := []struct {
		name       string
		endpoint   func(t *testing.T) string
		wantPause  bool
		wantSource v1alpha1.ActivitySource
		wantReason string
	}{
		{
			name:       "requests flowing keeps it awake",
			endpoint:   func(t *testing.T) string { return prometheusServer(t, http.StatusOK, promValue("3.5")) },
			wantSource: v1alpha1.ActivitySourcePrometheus,
		},
		{
			name:      "zero requests lets it pause",
			endpoint:  func(t *testing.T) string { return prometheusServer(t, http.StatusOK, promValue("0")) },
			wantPause: true,
		},
		{
			name: "empty result lets it pause",
			endpoint: func(t *testing.T) string {
				return prometheusServer(t, http.StatusOK, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
			},
			wantPause: true,
		},
		{
			name:       "no endpoint configured",
			endpoint:   func(*testing.T) string { return "" },
			wantReason: "EndpointNotConfigured",
		},
		{
			name:       "query fails",
			endpoint:   func(t *testing.T) string { return prometheusServer(t, http.StatusInternalServerError, "") },
			wantReason: "QueryFailed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := clockTarget("app:v1", nil)
			workload := clockWorkload(fixedTime.Add(-61*time.Minute), target)
			workload.Spec.IdlePolicy.Activity = &v1alpha1.ActivitySpec{
				Prometheus: []v1alpha1.PrometheusActivity{{PromQL: `sum(rate(nginx_ingress_controller_requests[5m]))`}},
			}
			metrics := idleCPU
			pauser := &stubPauser{pauseDone: true}
			r := newAutomationReconciler(t, workload, &stubForecaster{}, automationOpts{metrics: &metrics, pauser: pauser})
			r.prometheus = signal.NewPrometheus(tt.endpoint(t))

			_, err := r.reconcileAutomation(context.Background(), workload, target)
			require.NoError(t, err)

			assert.Equal(t, tt.wantPause, pauser.pauseCalls == 1)
			if tt.wantSource != "" {
				assert.Equal(t, tt.wantSource, workload.Status.Activity.LastActivitySource)
			}
			cond := meta.FindStatusCondition(workload.Status.Conditions, conditionPrometheusAvailable)
			require.NotNil(t, cond)
			if tt.wantReason == "" {
				assert.Equal(t, metav1.ConditionTrue, cond.Status)
				return
			}
			assert.Equal(t, metav1.ConditionFalse, cond.Status, "a source the clock can't read must stop it from pausing")
			assert.Equal(t, tt.wantReason, cond.Reason)
		})
	}
}

func promQueries(n int) []v1alpha1.PrometheusActivity {
	queries := make([]v1alpha1.PrometheusActivity, n)
	for i := range queries {
		queries[i] = v1alpha1.PrometheusActivity{PromQL: fmt.Sprintf(`sum(rate(http_requests_total{route="%d"}[5m]))`, i)}
	}
	return queries
}

// Prometheus failing for one check isn't asked again by the next: each
// would wait out a timeout to learn of the outage, holding a reconcile
// worker that wakes queue behind.
func TestActivityClock_PrometheusOutageFailsFast(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	target := clockTarget("app:v1", nil)
	workload := clockWorkload(fixedTime.Add(-61*time.Minute), target)
	workload.Spec.IdlePolicy.Activity = &v1alpha1.ActivitySpec{Prometheus: promQueries(3)}
	metrics := idleCPU
	pauser := &stubPauser{pauseDone: true}
	r := newAutomationReconciler(t, workload, &stubForecaster{}, automationOpts{metrics: &metrics, pauser: pauser})
	r.prometheus = signal.NewPrometheus(srv.URL)

	messages := make([]string, 0, 3)
	for range 3 {
		_, err := r.reconcileAutomation(context.Background(), workload, target)
		require.NoError(t, err)
		cond := meta.FindStatusCondition(workload.Status.Conditions, conditionPrometheusAvailable)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		messages = append(messages, cond.Message)
	}

	assert.Equal(t, int32(1), calls.Load(), "asked once, then not again until it has had time to recover")
	assert.Zero(t, pauser.pauseCalls)
	assert.Equal(t, []string{messages[0], messages[0], messages[0]}, messages,
		"the same outage reads the same whether Prometheus was asked or not, so it isn't a status write each check")
}

// A workload's queries share one time budget per check, so one with many
// slow queries holds a reconcile worker for seconds, not a timeout each.
func TestActivityClock_PrometheusQueriesShareABudget(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the budget")
	}
	const answerAfter = 1200 * time.Millisecond
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Query().Get("query"))
		mu.Unlock()
		select {
		case <-time.After(answerAfter):
			_, _ = w.Write([]byte(promValue("0")))
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	target := clockTarget("app:v1", nil)
	workload := clockWorkload(fixedTime.Add(-61*time.Minute), target)
	queries := promQueries(8)
	workload.Spec.IdlePolicy.Activity = &v1alpha1.ActivitySpec{Prometheus: queries}
	metrics := idleCPU
	pauser := &stubPauser{pauseDone: true}
	r := newAutomationReconciler(t, workload, &stubForecaster{}, automationOpts{metrics: &metrics, pauser: pauser})
	r.prometheus = signal.NewPrometheus(srv.URL)

	_, err := r.reconcileAutomation(context.Background(), workload, target)
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	// Each query takes at least answerAfter, so however slow the machine,
	// no more than this many can begin within the budget.
	most := int((prometheusBudget + answerAfter - 1) / answerAfter)
	require.NotEmpty(t, asked)
	assert.LessOrEqual(t, len(asked), most, "the queries after the budget ran out weren't asked: %v", asked)
	for i, q := range asked {
		assert.Equal(t, queries[i].PromQL, q, "asked in turn")
	}
	assert.Zero(t, pauser.pauseCalls, "queries not evaluated leave the workload awake")
	cond := meta.FindStatusCondition(workload.Status.Conditions, conditionPrometheusAvailable)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "QueryFailed", cond.Reason)
	assert.Contains(t, cond.Message, "context deadline exceeded", "the budget ran out")
}

// A query that matches no series, such as one with a misspelt metric, looks
// like a quiet workload, and would never keep it awake. It's pointed out
// once, and the clock goes on as before.
func TestActivityClock_QueryThatNeverMatchesIsPointedOut(t *testing.T) {
	var matching atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if matching.Load() {
			_, _ = w.Write([]byte(promValue("0")))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	t.Cleanup(srv.Close)
	target := clockTarget("app:v1", nil)
	workload := clockWorkload(fixedTime.Add(-30*time.Minute), target)
	query := `sum(rate(nginx_ingress_controller_requets[5m]))`
	workload.Spec.IdlePolicy.Activity = &v1alpha1.ActivitySpec{Prometheus: []v1alpha1.PrometheusActivity{{PromQL: query}}}
	metrics := idleCPU
	r := newAutomationReconciler(t, workload, &stubForecaster{}, automationOpts{metrics: &metrics})
	r.prometheus = signal.NewPrometheus(srv.URL)
	check := func() *metav1.Condition {
		t.Helper()
		_, err := r.reconcileAutomation(context.Background(), workload, target)
		require.NoError(t, err)
		return meta.FindStatusCondition(workload.Status.Conditions, conditionPrometheusQueryEmpty)
	}

	cond := check()
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Contains(t, cond.Message, query)
	assert.Contains(t, cond.Message, "or nothing has made the series yet, as with a request counter before the first request",
		"a right query on a counter nothing has counted yet looks the same (P47)")
	assert.True(t, meta.IsStatusConditionTrue(workload.Status.Conditions, conditionPrometheusAvailable),
		"the query was evaluated: it isn't a failure")
	check()
	emitted := drainEvents(t, r)
	assert.Equal(t, 1, strings.Count(emitted, ReasonPrometheusQueryEmpty), "pointed out once: %s", emitted)

	matching.Store(true)
	cond = check()
	assert.Equal(t, metav1.ConditionFalse, cond.Status, "cleared once the query matches a series")
	matching.Store(false)
	cond = check()
	assert.Equal(t, metav1.ConditionFalse, cond.Status, "a query that has matched may go quiet")
	assert.Equal(t, v1alpha1.PhaseRunning, workload.Status.Phase)
}

// lostSeriesClock is a workload at idle-after=1m measured by one Prometheus
// query, whose answer the test sets, checked at the clock it moves.
type lostSeriesClock struct {
	r        *Reconciler
	workload *v1alpha1.ManagedWorkload
	target   *appsv1.Deployment
	pauser   *stubPauser
	answer   atomic.Value
	now      time.Time
	next     time.Duration
}

func newLostSeriesClock(t *testing.T, source v1alpha1.ActivitySource, remembered bool) *lostSeriesClock {
	t.Helper()
	c := &lostSeriesClock{now: fixedTime, pauser: &stubPauser{pauseDone: true}}
	c.answer.Store(promValue("0"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(c.answer.Load().(string)))
	}))
	t.Cleanup(srv.Close)
	c.target = clockTarget("app:v1", nil)
	c.workload = clockWorkload(fixedTime.Add(-30*time.Second), c.target)
	c.workload.Spec.IdlePolicy.IdleAfter = &metav1.Duration{Duration: time.Minute}
	c.workload.Spec.IdlePolicy.Activity = &v1alpha1.ActivitySpec{Prometheus: []v1alpha1.PrometheusActivity{
		{PromQL: `sum(rate(http_requests_total{app="web"}[1m]))`}}}
	c.workload.Status.Activity.LastActivitySource = source
	metrics := idleCPU
	c.r = newAutomationReconciler(t, c.workload, &stubForecaster{}, automationOpts{metrics: &metrics, pauser: c.pauser})
	c.r.Recorder = events.NewFakeRecorder(100)
	if !remembered {
		restarted(c.r, c.workload)
	}
	c.r.clock = func() time.Time { return c.now }
	c.r.prometheus = signal.NewPrometheus(srv.URL)
	return c
}

// check answers with body at after past fixedTime, and reports whether
// the workload paused. next is when the operator asks to check again, a
// minute when it doesn't say, and a second at least.
func (c *lostSeriesClock) check(t *testing.T, after time.Duration, body string) bool {
	t.Helper()
	c.now = monotonic(fixedTime.Add(after))
	c.answer.Store(body)
	res, err := c.r.reconcileAutomation(context.Background(), c.workload, c.target)
	require.NoError(t, err)
	c.next = activityCheckInterval
	if res != nil && res.RequeueAfter > 0 {
		c.next = max(res.RequeueAfter, time.Second)
	}
	return c.pauser.pauseCalls > 0
}

// The rule the Prometheus restart in the break-it table needs, across its
// cases (P65): an empty answer is quiet unless the query matched series at
// its last check. Then it's unread, for up to seriesLostFor, and the
// workload doesn't pause on it. After that an empty answer is quiet again,
// so a workload whose series are gone for good still pauses.
func TestActivityClock_EmptyAfterSeries(t *testing.T) {
	t.Run("no series before is quiet", func(t *testing.T) {
		c := newLostSeriesClock(t, v1alpha1.ActivitySourceCPU, true)

		assert.True(t, c.check(t, 31*time.Second, emptyVector), "idle a minute, and nothing says otherwise")
	})

	t.Run("series at the last check, none now, is unread", func(t *testing.T) {
		c := newLostSeriesClock(t, v1alpha1.ActivitySourceCPU, true)
		require.False(t, c.check(t, 0, promValue("0")), "matched, though quiet")

		assert.False(t, c.check(t, 31*time.Second, emptyVector), "the clock ran out, but the answer is unread")
		cond := meta.FindStatusCondition(c.workload.Status.Conditions, conditionPrometheusAvailable)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		assert.Equal(t, reasonSeriesLost, cond.Reason)
		assert.Contains(t, cond.Message, "isn't paused before 12:05:31 UTC while they come back")
		assert.Equal(t, v1alpha1.PhaseRunning, c.workload.Status.Phase)
		assert.NotContains(t, drainEvents(t, c.r), "Warning", "expected after a restart, so no warning")
	})

	t.Run("empty to the grace's end pauses then", func(t *testing.T) {
		c := newLostSeriesClock(t, v1alpha1.ActivitySourceCPU, true)
		require.False(t, c.check(t, 0, promValue("0")))
		for at := time.Minute; at < time.Minute+seriesLostFor; at += time.Minute {
			require.False(t, c.check(t, at, emptyVector), "held %s after the series went", at-time.Minute)
			require.NotEqual(t, v1alpha1.ActivitySourceUnobserved, c.workload.Status.Activity.LastActivitySource,
				"the clock ran on, at %s", at)
		}

		assert.True(t, c.check(t, time.Minute+seriesLostFor+time.Second, emptyVector),
			"its clock ran out long ago, so it pauses at the first check after the grace")
		assert.True(t, meta.IsStatusConditionTrue(c.workload.Status.Conditions, conditionPrometheusAvailable))
	})

	t.Run("series back keeps it awake", func(t *testing.T) {
		c := newLostSeriesClock(t, v1alpha1.ActivitySourceCPU, true)
		require.False(t, c.check(t, 0, promValue("0.04")))
		require.False(t, c.check(t, time.Minute, emptyVector))

		assert.False(t, c.check(t, 110*time.Second, promValue("0.04")))
		assert.Equal(t, v1alpha1.ActivitySourcePrometheus, c.workload.Status.Activity.LastActivitySource)
		assert.True(t, meta.IsStatusConditionTrue(c.workload.Status.Conditions, conditionPrometheusAvailable))
	})
}

// A quiet workload whose query answers with series at zero at some checks
// and with none at others, as a [1m] rate over a 45s scrape does, or a
// target whose scrapes fail now and then, isn't held awake by it, and its
// clock never restarts. It pauses at the later of its idleAfter and the end
// of the grace running then: at most the grace and a check or two past its
// idleAfter, as for the [1m] rate, whose answers depend on when each check
// comes. Where the answers with series come in pairs, as at every 3rd, 4th
// or 6th check, the check where a pair clears a grace isn't held, so it
// pauses within three checks of the later of its idleAfter and its first
// grace's end.
// Before, each match renewed the grace, and the unread checks restarted
// the clock, so it never paused (the review of 8bdae63f, P65). The checks
// come when the operator asks for them, from its last activity, a
// request, at 0, and the empty answers fall at every phase of each
// pattern.
func TestActivityClock_FlappingSeriesStillPause(t *testing.T) {
	everyNth := func(n, phase int) func(check int, _ time.Duration) string {
		return func(check int, _ time.Duration) string {
			if (check+phase)%n == 0 {
				return emptyVector
			}
			return promValue("0")
		}
	}
	// A [1m] rate needs two samples in its window. Scraped every 45s from
	// fixedTime, its window holds one or two.
	rate1mOver45s := func(_ int, at time.Duration) string {
		end := at.Seconds()
		if math.Floor(end/45)-math.Floor((end-60)/45) < 2 {
			return emptyVector
		}
		return promValue("0")
	}
	type pattern struct {
		answer func(check int, at time.Duration) string
		// paired says the answers with series come in pairs, whenever the
		// checks come.
		paired bool
	}
	answers := map[string]pattern{"a [1m] rate over a 45s scrape": {answer: rate1mOver45s}}
	for _, n := range []int{3, 4, 6} {
		for phase := range n {
			answers[fmt.Sprintf("zero series at every %s check, phase %d", map[int]string{3: "3rd", 4: "4th", 6: "6th"}[n],
				phase)] = pattern{answer: everyNth(n, phase), paired: true}
		}
	}
	for name, p := range answers {
		for _, idleAfter := range []time.Duration{time.Minute, 3 * time.Minute, 5 * time.Minute, 10 * time.Minute} {
			t.Run(fmt.Sprintf("%s, idleAfter %s", name, idleAfter), func(t *testing.T) {
				c := newLostSeriesClock(t, v1alpha1.ActivitySourceCPU, true)
				c.workload.Spec.IdlePolicy.IdleAfter = &metav1.Duration{Duration: idleAfter}
				require.False(t, c.check(t, 0, promValue("0.04")), "a request at 0")
				require.Equal(t, v1alpha1.ActivitySourcePrometheus, c.workload.Status.Activity.LastActivitySource)
				bound := idleAfter + seriesLostFor + 2*time.Minute
				if p.paired {
					bound = idleAfter + 3*time.Minute
				}
				lost := false
				at := c.next
				for check := 1; at <= bound; check++ {
					body := p.answer(check, at)
					if !lost && body == emptyVector && p.paired {
						lost = true
						bound = max(bound, at+seriesLostFor+3*time.Minute)
					}
					if c.check(t, at, body) {
						return
					}
					require.NotEqual(t, v1alpha1.ActivitySourceUnobserved, c.workload.Status.Activity.LastActivitySource,
						"the clock never restarts, at %s", at)
					at += c.next
				}
				t.Fatalf("not paused by %s, idleAfter %s", bound, idleAfter)
			})
		}
	}
}

// A busy workload at idleAfter 1m, whose Prometheus restarts without its
// data at 1m and again a few minutes later, each restart answering with no
// series for a while: a 20s outage, or 4 minutes at a 2-minute scrape. The
// first restart's grace holds it, its series come back with its activity,
// and the second restart gets a grace of its own once the series have
// answered at two checks in a row. It's never paused. On d8cdbabd a new
// grace needed 5 unbroken minutes of series, so the second restart got
// none, and the workload paused at 7m, or at 7, 9 and 11m (the second
// review of 8bdae63f, P65).
func TestActivityClock_SecondRestartGetsItsOwnGrace(t *testing.T) {
	type outage struct{ from, to time.Duration }
	second := func(at, length, skew time.Duration) outage { return outage{at + skew, at + skew + length} }
	tests := map[string][]outage{}
	for _, at := range []time.Duration{3 * time.Minute, 5 * time.Minute, 6 * time.Minute, 7 * time.Minute} {
		// The check at each restart's minute answers with none.
		tests[fmt.Sprintf("20s outages, the second at %s", at)] = []outage{
			{time.Minute - 10*time.Second, time.Minute + 10*time.Second}, second(at, 20*time.Second, -10*time.Second)}
	}
	for _, at := range []time.Duration{6 * time.Minute, 8 * time.Minute, 10 * time.Minute} {
		// Two scrapes 2 minutes apart, so 4 minutes with none: the checks at
		// 1 to 4m, then the four after the second restart's minute.
		tests[fmt.Sprintf("4m outages at a 2m scrape, the second at %s", at)] = []outage{
			{time.Minute - 10*time.Second, 5*time.Minute - 10*time.Second}, second(at, 4*time.Minute, 10*time.Second)}
	}
	for name, outages := range tests {
		t.Run(name, func(t *testing.T) {
			c := newLostSeriesClock(t, v1alpha1.ActivitySourceCPU, true)
			require.False(t, c.check(t, 0, promValue("0.04")), "busy at 0")
			for at := c.next; at <= 16*time.Minute; at += c.next {
				body := promValue("0.04")
				for _, o := range outages {
					if at >= o.from && at < o.to {
						body = emptyVector
					}
				}
				require.False(t, c.check(t, at, body), "paused at %s", at)
			}
			assert.Equal(t, v1alpha1.PhaseRunning, c.workload.Status.Phase)
		})
	}
}

// The first answer after a busy workload's series come back can be series
// at zero, as rate() over the first two samples after a restart can be. One
// answer with series doesn't clear the grace, so that check is still held,
// and the next finds the activity. A grace cleared by one answer would
// pause it there, its clock having run out during the outage.
func TestActivityClock_OneAnswerWithSeriesDoesntClearTheGrace(t *testing.T) {
	c := newLostSeriesClock(t, v1alpha1.ActivitySourceCPU, true)
	require.False(t, c.check(t, 0, promValue("0.04")), "busy at 0")
	require.False(t, c.check(t, time.Minute, emptyVector), "Prometheus restarted")
	require.False(t, c.check(t, 2*time.Minute, emptyVector))

	assert.False(t, c.check(t, 3*time.Minute, promValue("0")), "series again, at zero")
	assert.False(t, c.check(t, 4*time.Minute, promValue("0.04")), "the activity shows")
	assert.Equal(t, v1alpha1.ActivitySourcePrometheus, c.workload.Status.Activity.LastActivitySource)
}

// Two queries whose graces would chain, one beginning while the other's
// runs, don't hold a quiet workload past the grace and a check after its
// clock runs out. On c5f5c009 the hold lasted until the latest grace ended,
// so two [1m] rates over a 35s scrape at different offsets held it 34
// minutes past idleAfter, and alternating per-check answers held it for
// good (the third review of 8bdae63f, P65).
func TestActivityClock_QueriesDontChainTheirGraces(t *testing.T) {
	const a, b = `sum(rate(http_requests_total{app="web"}[1m]))`, `sum(rate(grpc_server_handled_total{app="web"}[1m]))`
	// rate is a [1m] rate scraped every interval from offset: series, at
	// zero, while its window holds two samples, and none otherwise.
	rate := func(interval, offset time.Duration) func(_ int, at time.Duration) string {
		return func(_ int, at time.Duration) string {
			end, every := (at - offset).Seconds(), interval.Seconds()
			if math.Floor(end/every)-math.Floor((end-60)/every) < 2 {
				return emptyVector
			}
			return promValue("0")
		}
	}
	// lostAt answers with series, at zero, until from, and none after.
	lostAt := func(from time.Duration) func(_ int, at time.Duration) string {
		return func(_ int, at time.Duration) string {
			if at >= from {
				return emptyVector
			}
			return promValue("0")
		}
	}
	// cycle answers each check in turn from answers, with series at zero
	// for true and none for false.
	cycle := func(answers ...bool) func(check int, _ time.Duration) string {
		return func(check int, _ time.Duration) string {
			if answers[check%len(answers)] {
				return promValue("0")
			}
			return emptyVector
		}
	}
	type queries struct {
		a, b func(check int, at time.Duration) string
	}
	tests := []struct {
		name      string
		idleAfter time.Duration
		answers   queries
	}{
		{"[1m] rates over a 35s scrape at offsets 0 and 17s", time.Minute,
			queries{rate(35*time.Second, 0), rate(35*time.Second, 17*time.Second)}},
		{"[1m] rates over a 35s scrape at offsets 0 and 11s", 10 * time.Minute,
			queries{rate(35*time.Second, 0), rate(35*time.Second, 11*time.Second)}},
		{"[1m] rates over a 45s scrape at offsets 0 and 22s", time.Minute,
			queries{rate(45*time.Second, 0), rate(45*time.Second, 22*time.Second)}},
		{"A series, series, none; B series, none, series", time.Minute,
			queries{cycle(true, false, true), cycle(true, true, false)}},
		{"series lost at 1m and at 4m", time.Minute, queries{lostAt(time.Minute), lostAt(4 * time.Minute)}},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s, idleAfter %s", tt.name, tt.idleAfter), func(t *testing.T) {
			var check atomic.Int64
			var at atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				answer := tt.answers.a
				if req.URL.Query().Get("query") == b {
					answer = tt.answers.b
				}
				if check.Load() == 0 {
					_, _ = w.Write([]byte(promValue("0.04")))
					return
				}
				_, _ = w.Write([]byte(answer(int(check.Load()), time.Duration(at.Load()))))
			}))
			t.Cleanup(srv.Close)
			c := newLostSeriesClock(t, v1alpha1.ActivitySourceCPU, true)
			c.r.prometheus = signal.NewPrometheus(srv.URL)
			c.workload.Spec.IdlePolicy.IdleAfter = &metav1.Duration{Duration: tt.idleAfter}
			c.workload.Spec.IdlePolicy.Activity.Prometheus = []v1alpha1.PrometheusActivity{{PromQL: a}, {PromQL: b}}
			require.False(t, c.check(t, 0, ""), "a request at 0")
			bound := tt.idleAfter + seriesLostFor + activityCheckInterval + 5*time.Second
			for n, when := 1, c.next; when <= bound; n, when = n+1, when+c.next {
				check.Store(int64(n))
				at.Store(int64(when))
				if c.check(t, when, "") {
					return
				}
			}
			t.Fatalf("not paused by %s, idleAfter %s", bound, tt.idleAfter)
		})
	}
}

// A grace used up before a pause doesn't carry through it: the paused
// checks find the workload's own series gone, and once it's awake again two
// answers with series in a row make a new loss, as from a Prometheus
// restart soon after the wake, get a grace of its own. On d8cdbabd the
// paused checks broke the 5 unbroken minutes a new grace needed, so a
// restart soon after the wake got none (the second review of 8bdae63f,
// P65).
func TestQuerySeries_AGraceUsedUpBeforeAPauseDoesntCarryThroughIt(t *testing.T) {
	const uid, query = types.UID("web"), `sum(rate(http_requests_total{app="web"}[1m]))`
	var s querySeries
	at := func(m int) time.Time { return monotonic(fixedTime.Add(time.Duration(m) * time.Minute)) }
	answer := func(m int, matched, awake bool) seriesLost {
		return s.record(uid, []queryAnswer{{query: query, matched: matched}}, at(m), awake)
	}

	answer(0, true, true)
	require.False(t, answer(1, false, true).until.IsZero(), "the series went at 1m: a grace")
	require.True(t, answer(7, false, true).until.IsZero(), "used up by 6m, so it paused")
	for m := 8; m < 20; m++ {
		answer(m, false, false)
	}

	answer(20, true, true)
	answer(21, true, true)
	lost := answer(22, false, true)
	assert.False(t, lost.until.IsZero(), "a restart a minute after its series came back gets a grace")
	assert.True(t, lost.until.After(at(26)), "of its own, from 22m")
}

// With queries [A, B], A busy and B empty, A shows activity first at every
// check, so B is never evaluated. When Prometheus restarts without its
// data, A answers with none, in its grace, and so does B. In that check
// B's empty answer is unread too, so once A has series again B isn't
// pointed out as a query that never matches (the review of 8bdae63f, P65).
func TestActivityClock_QueryEmptyBesideALostOneIsntPointedOut(t *testing.T) {
	const busy, failures = `sum(rate(http_requests_total{app="web"}[1m]))`, `sum(rate(http_errors_total{app="web"}[1m]))`
	var restarted atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Query().Get("query") == busy && !restarted.Load() {
			_, _ = w.Write([]byte(promValue("0.04")))
			return
		}
		_, _ = w.Write([]byte(emptyVector))
	}))
	t.Cleanup(srv.Close)
	c := newLostSeriesClock(t, v1alpha1.ActivitySourceCPU, true)
	c.r.prometheus = signal.NewPrometheus(srv.URL)
	c.workload.Spec.IdlePolicy.Activity.Prometheus = []v1alpha1.PrometheusActivity{{PromQL: busy}, {PromQL: failures}}

	c.check(t, 0, "")
	restarted.Store(true)
	c.check(t, time.Minute, "")
	restarted.Store(false)
	c.check(t, 2*time.Minute, "")

	assert.Nil(t, meta.FindStatusCondition(c.workload.Status.Conditions, conditionPrometheusQueryEmpty))
	assert.NotContains(t, drainEvents(t, c.r), ReasonPrometheusQueryEmpty)
}

// PrometheusQueryEmpty points out a query that has never matched a series.
// An answer that's empty only because Prometheus restarted without its data
// isn't one: not while the operator runs on, nor when it restarted too, as
// when a drain moves both, if its clock says a query showed activity before
// (P65). A query empty for longer than the bound is pointed out as before.
func TestActivityClock_QueryEmptyAfterARestartIsntPointedOut(t *testing.T) {
	t.Run("Prometheus restarted", func(t *testing.T) {
		c := newLostSeriesClock(t, v1alpha1.ActivitySourceCPU, true)
		require.False(t, c.check(t, 0, promValue("0.04")))
		c.check(t, time.Minute, emptyVector)
		c.check(t, 2*time.Minute, promValue("0.04"))

		assert.Nil(t, meta.FindStatusCondition(c.workload.Status.Conditions, conditionPrometheusQueryEmpty))
		assert.NotContains(t, drainEvents(t, c.r), ReasonPrometheusQueryEmpty)
	})

	t.Run("the operator restarted with it", func(t *testing.T) {
		c := newLostSeriesClock(t, v1alpha1.ActivitySourcePrometheus, false)

		assert.False(t, c.check(t, 0, emptyVector))
		assert.Nil(t, meta.FindStatusCondition(c.workload.Status.Conditions, conditionPrometheusQueryEmpty),
			"the query showed activity before the restart")
		assert.Equal(t, reasonSeriesLost,
			meta.FindStatusCondition(c.workload.Status.Conditions, conditionPrometheusAvailable).Reason)
		c.check(t, time.Minute, promValue("0.04"))
		assert.Nil(t, meta.FindStatusCondition(c.workload.Status.Conditions, conditionPrometheusQueryEmpty))
		assert.NotContains(t, drainEvents(t, c.r), ReasonPrometheusQueryEmpty)
	})

	t.Run("empty past the bound is pointed out", func(t *testing.T) {
		c := newLostSeriesClock(t, v1alpha1.ActivitySourcePrometheus, false)
		for at := time.Duration(0); at < seriesLostFor; at += time.Minute {
			c.check(t, at, emptyVector)
		}
		require.Nil(t, meta.FindStatusCondition(c.workload.Status.Conditions, conditionPrometheusQueryEmpty))

		c.check(t, seriesLostFor+time.Second, emptyVector)
		assert.True(t, meta.IsStatusConditionTrue(c.workload.Status.Conditions, conditionPrometheusQueryEmpty))
	})

	t.Run("the operator restarted, with nothing to say a query matched", func(t *testing.T) {
		c := newLostSeriesClock(t, v1alpha1.ActivitySourceCPU, false)

		c.check(t, 0, emptyVector)
		assert.True(t, meta.IsStatusConditionTrue(c.workload.Status.Conditions, conditionPrometheusQueryEmpty),
			"pointed out at once, as before")
	})
}

func TestActivityClock_NoPrometheusConditionWithoutQueries(t *testing.T) {
	target := clockTarget("app:v1", nil)
	workload := clockWorkload(fixedTime.Add(-30*time.Minute), target)

	runClock(t, workload, target, clockOpts{metrics: idleCPU})

	assert.Nil(t, meta.FindStatusCondition(workload.Status.Conditions, conditionPrometheusAvailable))
}

// A paused workload wakes for any activity annotation set since the pause
// began, whatever time it states: the doorman stamps to the second, so a
// request can share the pause's second, and kubectl napwake wake stamps
// from a laptop clock that may be behind.
func TestWokenByActivity(t *testing.T) {
	pausedAt := metav1.NewTime(fixedTime.Add(-time.Hour))
	old := fixedTime.Add(-2 * time.Hour).Format(time.RFC3339)
	skewed := fixedTime.Add(-90 * time.Minute).Format(time.RFC3339)
	tests := []struct {
		name        string
		pausedAt    metav1.Time
		recorded    *v1alpha1.WakeAnnotations
		annotations map[string]string
		want        bool
	}{
		{name: "a request in the second the pause completed", pausedAt: metav1.NewTime(fixedTime.Truncate(time.Second)),
			annotations: map[string]string{v1alpha1.AnnotationLastRequest: fixedTime.Format(time.RFC3339)}, want: true},
		{name: "a wake stamped by a clock behind the operator's", pausedAt: pausedAt,
			recorded:    &v1alpha1.WakeAnnotations{Workload: map[string]string{v1alpha1.AnnotationLastActivity: old}},
			annotations: map[string]string{v1alpha1.AnnotationLastActivity: skewed}, want: true},
		{name: "the activity it was paused after", pausedAt: pausedAt,
			recorded:    &v1alpha1.WakeAnnotations{Workload: map[string]string{v1alpha1.AnnotationLastActivity: old}},
			annotations: map[string]string{v1alpha1.AnnotationLastActivity: old}},
		{name: "no annotations", pausedAt: pausedAt, recorded: &v1alpha1.WakeAnnotations{}},
		{name: "a value that isn't a time, as an awake workload ignores it", pausedAt: pausedAt,
			recorded:    &v1alpha1.WakeAnnotations{},
			annotations: map[string]string{v1alpha1.AnnotationLastActivity: strconv.FormatInt(fixedTime.Unix(), 10)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workload := pausedWorkload(v1alpha1.PhasePaused)
			workload.Status.Pause.PausedAt = &tt.pausedAt
			workload.Status.Pause.WakeAnnotations = tt.recorded
			workload.Annotations = tt.annotations
			pauser := &stubPauser{resumeDone: true}
			r := newTestReconcilerWithReplicas(t, workload, pauser, 0)

			_, err := r.Reconcile(context.Background(), reconcileFor("api"))
			require.NoError(t, err)

			assert.Equal(t, tt.want, pauser.resumeCalls > 0)
		})
	}
}

// A decimal threshold counts as activity what a whole one can't: a workload
// set to 0.5% is in use at 0.6% of its requests, and idle at 0.4%.
func TestActivityClock_ADecimalCPUThreshold(t *testing.T) {
	for millis, busy := range map[float64]bool{6: true, 4: false} {
		target := clockTarget("app:v1", nil)
		workload := clockWorkload(fixedTime.Add(-61*time.Minute), target)
		workload.Spec.IdlePolicy.Activity = &v1alpha1.ActivitySpec{CPUThreshold: ptr.To(intstr.FromString("0.5"))}

		pauser := runClock(t, workload, target, clockOpts{metrics: stubMetrics{cpuMillis: millis, cpuPerReplica: 1000,
			replicas: 1}})

		if busy {
			assert.Zero(t, pauser.pauseCalls, "%v%% is in use", millis/10)
			assert.Equal(t, v1alpha1.ActivitySourceCPU, workload.Status.Activity.LastActivitySource)
		} else {
			assert.Equal(t, 1, pauser.pauseCalls, "%v%% is idle", millis/10)
		}
	}
}
