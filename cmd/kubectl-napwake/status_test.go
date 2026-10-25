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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

var statusNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// testCluster is the cluster status names, from the kubeconfig context.
const testCluster = "staging (EKS us-east-1)"

func before(d time.Duration) *metav1.Time {
	t := metav1.NewTime(statusNow.Add(-d))
	return &t
}

func statusWorkloadObj(namespace, name string, kind v1alpha1.TargetKind, phase v1alpha1.WorkloadPhase,
	inPhase time.Duration) *v1alpha1.ManagedWorkload {
	return &v1alpha1.ManagedWorkload{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: testUID(namespace, name)},
		Spec: v1alpha1.ManagedWorkloadSpec{Target: v1alpha1.WorkloadRef{Kind: kind, Name: name},
			IdlePolicy: &v1alpha1.IdlePolicySpec{}},
		Status: v1alpha1.ManagedWorkloadStatus{Phase: phase, LastTransitionTime: before(inPhase)},
	}
}

func activity(source v1alpha1.ActivitySource, at time.Duration, pauseIn time.Duration) *v1alpha1.ActivityStatus {
	pauseAt := metav1.NewTime(statusNow.Add(pauseIn))
	return &v1alpha1.ActivityStatus{LastActivityTime: *before(at), LastActivitySource: source, PauseAt: &pauseAt}
}

func condition(condType string, status metav1.ConditionStatus, reason, message string) metav1.Condition {
	return metav1.Condition{Type: condType, Status: status, Reason: reason, Message: message}
}

// testUID is the UID of the ManagedWorkload statusWorkloadObj makes, which
// its events from statusEvent name.
func testUID(namespace, name string) types.UID {
	return types.UID(namespace + "-" + name)
}

func statusEvent(namespace, workload, reason, message string, at time.Duration) *corev1.Event {
	return &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: workload + "." + reason + "." + at.String(), Namespace: namespace},
		InvolvedObject: corev1.ObjectReference{Kind: "ManagedWorkload", Namespace: namespace, Name: workload,
			UID: testUID(namespace, workload)},
		Reason:    reason,
		Message:   message,
		EventTime: metav1.NewMicroTime(statusNow.Add(-at)),
	}
}

func statusCluster() []client.Object {
	api := statusWorkloadObj("preview-42", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhasePaused, 3*time.Hour)
	api.Status.Doorman = []v1alpha1.DoormanRoute{{Service: "api", DoormanPort: 20000}}
	api.Status.Activity = activity(v1alpha1.ActivitySourceRequest, 4*time.Hour, -3*time.Hour)
	api.Status.Cost = &v1alpha1.CostStatus{FreedThisMonth: "$12.40", SavedThisMonth: "$10.00"}

	postgres := statusWorkloadObj("preview-42", "postgres", v1alpha1.TargetKindStatefulSet, v1alpha1.PhaseRunning,
		5*time.Hour)
	postgres.Status.Activity = activity(v1alpha1.ActivitySourceCPU, 2*time.Hour, -time.Hour)
	postgres.Status.Conditions = []metav1.Condition{
		condition("HeldByDependents", metav1.ConditionTrue, "DependentsAwake", "kept awake for preview-7/worker"),
	}
	postgres.Status.Activity.HeldByDependents = []string{"preview-7/worker"}
	postgres.Status.Cost = &v1alpha1.CostStatus{FreedThisMonth: "$30.05", SavedThisMonth: "$25.00"}

	web := statusWorkloadObj("preview-7", "web", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning, 2*time.Hour)
	web.Spec.DryRun = true
	web.Status.Activity = activity(v1alpha1.ActivitySourceRollout, 15*time.Minute, 45*time.Minute)
	web.Status.Cost = &v1alpha1.CostStatus{FreedThisMonth: "$0.00", SavedThisMonth: "$0.00"}
	web.Status.DryRun = &v1alpha1.DryRunStatus{Since: *before(99 * time.Hour), Pauses: 4, EstimatedFreed: "$7.40"}
	for _, w := range []*v1alpha1.ManagedWorkload{api, postgres, web} {
		w.Status.Prediction = &v1alpha1.PredictionStatus{TimeZone: "Europe/London"}
	}
	api.Status.Prediction.Reason = v1alpha1.ForecastActing
	web.Status.Prediction.Reason = v1alpha1.ForecastEarningConfidence
	web.Status.Prediction.StartsActing = &metav1.Time{Time: statusNow.Add(72 * time.Hour)}

	worker := statusWorkloadObj("preview-7", "worker", v1alpha1.TargetKindDeployment, v1alpha1.PhaseResuming,
		25*time.Minute)
	worker.Status.Conditions = []metav1.Condition{
		condition("WaitingForDependencies", metav1.ConditionTrue, "DependencyNotReady", "waiting for preview-42/postgres"),
		condition("GitOpsConflict", metav1.ConditionTrue, "PauseUndone", "Argo CD set the replicas from Git"),
		condition("WakeOnRequest", metav1.ConditionFalse, "NoServices", "no Service routes TCP traffic to this workload"),
		condition("MetricsAvailable", metav1.ConditionTrue, "MetricsReported", ""),
	}

	return []client.Object{api, postgres, web, worker,
		statusEvent("preview-42", "api", "Paused", "api: paused", 3*time.Hour),
		statusEvent("preview-42", "api", "Resumed", "api: resumed", 5*time.Hour-time.Second),
		statusEvent("preview-42", "api", "WokeByActivity", "api: a request is waiting, waking", 5*time.Hour),
		statusEvent("preview-42", "api", "WokenByRequest", "request on Service api, waking", 5*time.Hour+time.Second),
		statusEvent("preview-7", "web", "WokeByActivity",
			"[dry-run] web: activity annotation is newer than the pause, waking", time.Hour),
		statusEvent("preview-7", "worker", "ScaledUp",
			"worker: scaled up to 2 replicas by argocd-controller outside Napwake; counted as activity", 30*time.Minute),
		statusEvent("preview-7", "worker", "Paused", "worker: paused", 50*time.Hour),
		statusEvent("preview-42", "api", "IdleDetected", "api: no activity for 1h", 3*time.Hour+time.Minute),
	}
}

func newStatusClient(t *testing.T, funcs interceptor.Funcs, objs ...client.Object) client.Client {
	t.Helper()
	return withoutKarpenter(newClusterClient(t, funcs, objs...))
}

// newClusterClient is a cluster with Karpenter's CRD, as far as the fake
// client goes: it lists any kind it's asked for.
func newClusterClient(t *testing.T, funcs interceptor.Funcs, objs ...client.Object) client.WithWatch {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithInterceptorFuncs(funcs).
		WithIndex(&corev1.Event{}, "involvedObject.kind", func(o client.Object) []string {
			return []string{o.(*corev1.Event).InvolvedObject.Kind}
		}).Build()
}

// withoutKarpenter answers for Karpenter's kinds as an API server without
// its CRD does.
func withoutKarpenter(c client.WithWatch) client.Client {
	return interceptor.NewClient(c, interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch,
		list client.ObjectList, opts ...client.ListOption) error {
		if gk := list.GetObjectKind().GroupVersionKind().GroupKind(); gk.Group == "karpenter.sh" {
			return &meta.NoKindMatchError{GroupKind: gk}
		}
		return c.List(ctx, list, opts...)
	}})
}

func statusOpts() statusOptions {
	return statusOptions{since: 24 * time.Hour, now: func() time.Time { return statusNow }}
}

func runStatus(t *testing.T, c client.Client, opts statusOptions) string {
	t.Helper()
	result, err := clusterStatus(context.Background(), c, "preview-42", opts)
	require.NoError(t, err)
	result.Cluster = testCluster
	var out bytes.Buffer
	require.NoError(t, writeStatus(&out, result, "table", statusNow))
	return out.String()
}

func TestStatus(t *testing.T) {
	out := runStatus(t, newStatusClient(t, interceptor.Funcs{}, statusCluster()...), statusOpts())

	//nolint:lll // the expected screen is as wide as the command prints it
	assert.Equal(t, `staging (EKS us-east-1): Napwake manages 4 workloads.
  1 paused, 2 running, 1 resuming; 1 in dry-run, never paused: pausing it would have freed $7 since Oct 1
  Your bill: won't drop until something removes nodes, and no node autoscaler was found. See https://docs.napwake.dev/guides/cluster-autoscaler/
  Forecast time zone: Europe/London
  Freed $42 this month, and saved $35 of it.

Needs attention:
  preview-7/worker   GitOpsConflict   Argo CD set the replicas from Git
  preview-7/worker   Stuck            resuming for 25m: waiting for preview-42/postgres

  NAMESPACE    WORKLOAD               STATE               FOR   LAST ACTIVITY      NEXT                                        FORECAST                    FREED THIS MONTH   SAVED   WOULD FREE
  preview-42   deployment/api         paused              3h    request, 4h ago    wakes on a request or activity              acting                      $12                $10     -
  preview-42   statefulset/postgres   running             5h    cpu, 2h ago        held awake while preview-7/worker uses it   -                           $30                $25     -
  preview-7    deployment/web         running (dry-run)   2h    rollout, 15m ago   would pause in 45m                          learning, acts from Oct 8   -                  -       $7
  preview-7    deployment/worker      resuming            25m   -                  -                                           -                           $0                 $0      -

Recent pauses and wakes:
  30m ago   preview-7/worker   scaled up to 2 replicas by argocd-controller outside Napwake; counted as activity
  3h ago    preview-42/api     paused
  5h ago    preview-42/api     request on Service api, waking
`, out)
}

// While the operator has pausing off for the whole cluster, as the Pausing
// condition on its workloads says, status says so first, and in -o json.
func TestStatus_SaysFirstThatPausingIsOff(t *testing.T) {
	objs := statusCluster()
	api, ok := objs[0].(*v1alpha1.ManagedWorkload)
	require.True(t, ok)
	api.Status.Conditions = append(api.Status.Conditions, condition("Pausing", metav1.ConditionFalse,
		"TurnedOffForTheCluster", "pausing is off for the whole cluster"))

	out := runStatus(t, newStatusClient(t, interceptor.Funcs{}, objs...), statusOpts())

	first, rest, _ := strings.Cut(out, "\n")
	assert.Equal(t, "Pausing is off for the whole cluster (pausing.enabled: false); nothing pauses, and everything "+
		"wakes.", first)
	assert.True(t, strings.HasPrefix(rest, testCluster+": Napwake manages 4 workloads.\n"), rest)
	result, err := clusterStatus(context.Background(), newStatusClient(t, interceptor.Funcs{}, objs...),
		"preview-42", statusOpts())
	require.NoError(t, err)
	assert.True(t, result.PausingOff)

	assert.NotContains(t, runStatus(t, newStatusClient(t, interceptor.Funcs{}, statusCluster()...), statusOpts()),
		"Pausing is off", "a cluster that pauses doesn't say so")
}

// A workload whose doorman asks before waking it says so in NEXT, and the
// requests it turned away during its pause are listed under the table:
// something automated may be calling it.
func TestStatus_ShowsWhatAskingTurnedAway(t *testing.T) {
	web := statusWorkloadObj("preview-42", "web", v1alpha1.TargetKindDeployment, v1alpha1.PhasePaused, 3*time.Hour)
	web.Spec.Wake = &v1alpha1.WakeSpec{Mode: v1alpha1.WakeOnAsk}
	web.Status.Doorman = []v1alpha1.DoormanRoute{{Service: "web", DoormanPort: 20001}}
	web.Status.TurnedAway = &v1alpha1.TurnedAway{PausedAt: *before(3 * time.Hour), Requests: 312}
	quiet := statusWorkloadObj("preview-42", "docs", v1alpha1.TargetKindDeployment, v1alpha1.PhasePaused, time.Hour)
	quiet.Spec.Wake = &v1alpha1.WakeSpec{Mode: v1alpha1.WakeOnAsk}
	quiet.Status.Doorman = []v1alpha1.DoormanRoute{{Service: "docs", DoormanPort: 20002}}
	quiet.Status.TurnedAway = &v1alpha1.TurnedAway{PausedAt: *before(time.Hour)}

	out := runStatus(t, newStatusClient(t, interceptor.Funcs{}, web, quiet), statusOpts())

	assert.Contains(t, out, "wakes on a click, a wake request or activity")
	assert.Contains(t, out, "Turned away while asleep, by a doorman that asks before waking: something automated may "+
		"call these.\n  preview-42/web   312 requests during the pause from 3h ago\n")
	assert.NotContains(t, out, "preview-42/docs ", "one that turned none away isn't listed")

	result, err := clusterStatus(context.Background(), newStatusClient(t, interceptor.Funcs{}, web), "preview-42",
		statusOpts())
	require.NoError(t, err)
	require.NotNil(t, result.Workloads[0].TurnedAway)
	assert.Equal(t, int64(312), result.Workloads[0].TurnedAway.Requests, "and in -o json")
}

func TestNextFor(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*v1alpha1.ManagedWorkload)
		want   string
	}{
		{name: "pauses after idleAfter", mutate: func(w *v1alpha1.ManagedWorkload) {
			w.Status.Activity = activity(v1alpha1.ActivitySourceCPU, 0, 90*time.Minute)
		}, want: "pauses in 1h"},
		{name: "idle, about to pause", mutate: func(w *v1alpha1.ManagedWorkload) {
			w.Status.Phase = v1alpha1.PhaseIdle
			w.Status.Activity = activity(v1alpha1.ActivitySourceCPU, 2*time.Hour, -time.Hour)
		}, want: "pauses now"},
		{name: "held by active-until", mutate: func(w *v1alpha1.ManagedWorkload) {
			w.Annotations = map[string]string{v1alpha1.AnnotationActiveUntil: statusNow.Add(2 * time.Hour).Format(time.RFC3339)}
			w.Status.Activity = activity(v1alpha1.ActivitySourceCPU, 2*time.Hour, -time.Hour)
		}, want: "held awake for 2h (active-until)"},
		{name: "an expired active-until doesn't hold", mutate: func(w *v1alpha1.ManagedWorkload) {
			w.Annotations = map[string]string{v1alpha1.AnnotationActiveUntil: statusNow.Add(-time.Hour).Format(time.RFC3339)}
			w.Status.Activity = activity(v1alpha1.ActivitySourceCPU, 0, 10*time.Minute)
		}, want: "pauses in 10m"},
		{name: "paused without the doorman", mutate: func(w *v1alpha1.ManagedWorkload) {
			w.Status.Phase = v1alpha1.PhasePaused
		}, want: "wakes on activity"},
		{name: "paused, its doorman asking before waking it", mutate: func(w *v1alpha1.ManagedWorkload) {
			w.Status.Phase = v1alpha1.PhasePaused
			w.Status.Doorman = []v1alpha1.DoormanRoute{{Service: "api", DoormanPort: 20000}}
			w.Spec.Wake = &v1alpha1.WakeSpec{Mode: v1alpha1.WakeOnAsk}
		}, want: "wakes on a click, a wake request or activity"},
		{name: "paused, waking on request", mutate: func(w *v1alpha1.ManagedWorkload) {
			w.Status.Phase = v1alpha1.PhasePaused
			w.Status.Doorman = []v1alpha1.DoormanRoute{{Service: "api", DoormanPort: 20000}}
		}, want: "wakes on a request or activity"},
		{name: "no activity clock yet", mutate: func(*v1alpha1.ManagedWorkload) {}, want: "-"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := statusWorkloadObj("ns", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning, time.Hour)
			tt.mutate(w)

			assert.Equal(t, tt.want, nextFor(w, time.Time{}, statusNow))
		})
	}
}

// NEXT never says a workload pauses when the operator won't pause it, and
// says why instead.
func TestNextFor_WhatKeepsItUp(t *testing.T) {
	overdue := func(w *v1alpha1.ManagedWorkload) {
		w.Status.Activity = activity(v1alpha1.ActivitySourceCPU, 2*time.Hour, -time.Hour)
	}
	withCondition := func(c metav1.Condition) func(*v1alpha1.ManagedWorkload) {
		return func(w *v1alpha1.ManagedWorkload) {
			overdue(w)
			w.Status.Conditions = []metav1.Condition{c}
		}
	}
	vetoed := condition("IdleVetoed", metav1.ConditionTrue, "ForecastExpectsDemand", "")
	heldBy := func(names ...string) func(*v1alpha1.ManagedWorkload) {
		return func(w *v1alpha1.ManagedWorkload) {
			withCondition(condition("HeldByDependents", metav1.ConditionTrue, "DependentsAwake", ""))(w)
			w.Status.Activity.HeldByDependents = names
		}
	}
	tests := []struct {
		name              string
		mutate            func(*v1alpha1.ManagedWorkload)
		targetActiveUntil time.Time
		want              string
	}{
		{name: "no idlePolicy, with a pause time left from when it had one", mutate: func(w *v1alpha1.ManagedWorkload) {
			overdue(w)
			w.Spec.IdlePolicy = nil
		}, want: "never pauses: no idlePolicy"},
		{name: "a protected namespace",
			mutate: withCondition(condition("Protected", metav1.ConditionTrue, "ProtectedNamespace", "")),
			want:   "never pauses: namespace protected"},
		{name: "Napwake's own operator or doorman",
			mutate: withCondition(condition("Protected", metav1.ConditionTrue, "NapwakeComponent", "")),
			want:   "never pauses: part of Napwake"},
		{name: "a StatefulSet that deletes its PVCs when scaled down",
			mutate: withCondition(condition("Protected", metav1.ConditionTrue, "PVCsDeletedOnScaleDown", "")),
			want:   "never pauses: PVCs deleted on scale-down"},
		{name: "an ignored workload",
			mutate: withCondition(condition("TargetAvailable", metav1.ConditionFalse, "TargetIgnored", "")),
			want:   "nothing: labelled napwake.dev/ignore"},
		{name: "a missing workload",
			mutate: withCondition(condition("TargetAvailable", metav1.ConditionFalse, "TargetNotFound", "")),
			want:   "nothing: workload not found"},
		{name: "another ManagedWorkload has it",
			mutate: withCondition(condition("DuplicateTarget", metav1.ConditionTrue, "DuplicateTarget", "")),
			want:   "nothing: another ManagedWorkload has it"},
		{name: "no CPU metrics",
			mutate: withCondition(condition("MetricsAvailable", metav1.ConditionFalse, "MetricsUnavailable", "")),
			want:   "won't pause: no CPU metrics"},
		{name: "a failing Prometheus query",
			mutate: withCondition(condition("PrometheusAvailable", metav1.ConditionFalse, "QueryFailed", "")),
			want:   "won't pause: Prometheus failing"},
		{name: "a dependsOn cycle",
			mutate: withCondition(condition("DependencyCycle", metav1.ConditionTrue, "DependencyCycle", "")),
			want:   "won't pause: dependsOn cycle"},
		{name: "vetoed by the forecast, saying when it expects demand", mutate: func(w *v1alpha1.ManagedWorkload) {
			withCondition(vetoed)(w)
			w.Status.Activity.ForecastBusyFrom = &metav1.Time{Time: time.Date(2026, 10, 5, 13, 0, 0, 0, time.UTC)}
		}, want: "held awake by the forecast: busy from 13:00 UTC"},
		{name: "vetoed by the forecast, without saying when", mutate: withCondition(vetoed),
			want: "held awake by the forecast"},
		{name: "held by a dependent", mutate: heldBy("shop/web"), want: "held awake while shop/web uses it"},
		{name: "held by two dependents", mutate: heldBy("shop/web", "shop/worker"),
			want: "held awake while shop/web and shop/worker use it"},
		{name: "held by dependents", mutate: heldBy("shop/web", "shop/worker", "shop/cron"),
			want: "held awake while shop/web and 2 others use it"},
		{name: "held by dependents it doesn't name", mutate: heldBy(), want: "held awake by its dependents"},
		{name: "idle, after a GitOps tool undid its last pause", mutate: func(w *v1alpha1.ManagedWorkload) {
			undoneBy(w, "Argo CD", 10*time.Minute)
			w.Status.Phase = v1alpha1.PhaseIdle
		}, want: "pauses at 12:50 UTC (after GitOps conflict)"},
		{name: "its clock runs out before Napwake pauses again after a GitOps conflict",
			mutate: func(w *v1alpha1.ManagedWorkload) {
				undoneBy(w, "Flux", 10*time.Minute)
				w.Status.Activity = activity(v1alpha1.ActivitySourceScaledUp, 10*time.Minute, 20*time.Minute)
			}, want: "pauses at 12:50 UTC (after GitOps conflict)"},
		{name: "its clock runs out after Napwake would pause again after a GitOps conflict",
			mutate: func(w *v1alpha1.ManagedWorkload) {
				undoneBy(w, "Flux", 10*time.Minute)
				w.Status.Activity = activity(v1alpha1.ActivitySourceScaledUp, 10*time.Minute, 2*time.Hour)
			}, want: "pauses in 2h"},
		{name: "the hour after a GitOps conflict is over", mutate: func(w *v1alpha1.ManagedWorkload) {
			undoneBy(w, "Argo CD", 2*time.Hour)
			w.Status.Phase = v1alpha1.PhaseIdle
		}, want: "pauses now"},
		{name: "the forecast holds it before the hour after a GitOps conflict", mutate: func(w *v1alpha1.ManagedWorkload) {
			undoneBy(w, "Argo CD", 10*time.Minute)
			w.Status.Conditions = append(w.Status.Conditions,
				condition("IdleVetoed", metav1.ConditionTrue, "ForecastExpectsDemand", ""))
		}, want: "held awake by the forecast"},
		{name: "a veto that's over doesn't hold",
			mutate: withCondition(condition("IdleVetoed", metav1.ConditionFalse, "NotVetoed", "")),
			want:   "overdue to pause"},
		{name: "activity since the veto",
			mutate: func(w *v1alpha1.ManagedWorkload) {
				w.Status.Activity = activity(v1alpha1.ActivitySourceCPU, 0, 20*time.Minute)
				w.Status.Conditions = []metav1.Condition{
					condition("IdleVetoed", metav1.ConditionTrue, "ForecastExpectsDemand", "")}
			},
			want: "pauses in 20m"},
		{name: "active-until on the workload itself", mutate: overdue,
			targetActiveUntil: statusNow.Add(3 * time.Hour), want: "held awake for 3h (active-until)"},
		{name: "idle", mutate: func(w *v1alpha1.ManagedWorkload) {
			overdue(w)
			w.Status.Phase = v1alpha1.PhaseIdle
		}, want: "pauses now"},
		{name: "idle in dry-run", mutate: func(w *v1alpha1.ManagedWorkload) {
			overdue(w)
			w.Status.Phase = v1alpha1.PhaseIdle
			w.Spec.DryRun = true
		}, want: "would pause now"},
		// A pause request runs the clock out without rewriting when it would
		// have: Idle is due now, not in the 40 minutes it shows.
		{name: "pause requested before the clock ran out", mutate: func(w *v1alpha1.ManagedWorkload) {
			w.Status.Activity = activity(v1alpha1.ActivitySourceCPU, 20*time.Minute, 40*time.Minute)
			w.Status.Phase = v1alpha1.PhaseIdle
		}, want: "pauses now"},
		{name: "pause requested in dry-run before the clock ran out", mutate: func(w *v1alpha1.ManagedWorkload) {
			w.Status.Activity = activity(v1alpha1.ActivitySourceCPU, 20*time.Minute, 40*time.Minute)
			w.Status.Phase = v1alpha1.PhaseIdle
			w.Spec.DryRun = true
		}, want: "would pause now"},
		// The clock in status is written every five minutes, so a pause time
		// just past may only mean activity status doesn't show yet.
		{name: "running, its pause time just past", mutate: func(w *v1alpha1.ManagedWorkload) {
			w.Status.Activity = activity(v1alpha1.ActivitySourceCPU, time.Hour, -3*time.Minute)
		}, want: "pauses soon"},
		{name: "running, long past its pause time", mutate: overdue, want: "overdue to pause"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := statusWorkloadObj("ns", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning, time.Hour)
			tt.mutate(w)

			assert.Equal(t, tt.want, nextFor(w, tt.targetActiveUntil, statusNow))
		})
	}
}

// undoneBy has a GitOps tool undo the workload's last pause ago, as the
// operator reports it, leaving its clock run out.
func undoneBy(w *v1alpha1.ManagedWorkload, tool string, ago time.Duration) {
	w.Status.Activity = activity(v1alpha1.ActivitySourceScaledUp, 2*time.Hour, -time.Hour)
	w.Status.LastScaledUp = &v1alpha1.ScaledUp{At: *before(ago), By: "argocd-controller", GitOps: tool, Replicas: 2}
	w.Status.Conditions = []metav1.Condition{condition("GitOpsConflict", metav1.ConditionTrue, "PauseUndone",
		tool+" set the replicas from Git, undoing the pause")}
}

// A GitOps conflict needs attention while the tool would still undo a
// pause. The operator clears it only once a pause holds, which a workload
// in dry-run never makes, so status checks the tool itself: once it's told
// to leave the replicas, the conflict isn't listed, and once Napwake's
// wait is over, it's told as past rather than as a time gone by.
func TestStatus_GitOpsConflictAsItIsNow(t *testing.T) {
	forbidArgo := replicasSetBy("argocd-controller")
	get := forbidArgo.Get
	forbidArgo.Get = func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object,
		opts ...client.GetOption) error {
		if key.Namespace == "argocd" {
			return apierrors.NewForbidden(schema.GroupResource{}, key.Name, nil)
		}
		return get(ctx, c, key, obj, opts...)
	}
	forbidArgo.List = func(ctx context.Context, c client.WithWatch, list client.ObjectList,
		opts ...client.ListOption) error {
		if _, ok := list.(*unstructured.UnstructuredList); ok {
			return apierrors.NewForbidden(schema.GroupResource{Group: "argoproj.io", Resource: "applications"}, "", nil)
		}
		return c.List(ctx, list, opts...)
	}
	unfixed := []client.Object{argoApplication(map[string]any{}), argoFixed()[1]}
	tests := []struct {
		name  string
		ago   time.Duration
		funcs interceptor.Funcs
		argo  []client.Object
		want  string
	}{
		{name: "fixed since", ago: 10 * time.Minute, funcs: replicasSetBy("argocd-controller"), argo: argoFixed()},
		{name: "unfixed, while Napwake waits", ago: 10 * time.Minute, funcs: replicasSetBy("argocd-controller"),
			argo: unfixed, want: "Argo CD set the replicas from Git, undoing the pause"},
		{name: "unfixed, once Napwake's wait is over", ago: 3 * time.Hour,
			funcs: replicasSetBy("argocd-controller"), argo: unfixed,
			want: "Argo CD set the replicas from Git at 09:00 UTC, undoing the last pause, and isn't told to leave " +
				"them, so it will undo the next: https://docs.napwake.dev/guides/gitops/#argo-cd"},
		{name: "can't be checked", ago: 3 * time.Hour, funcs: forbidArgo, argo: argoFixed(),
			want: "Argo CD set the replicas from Git at 09:00 UTC, undoing the last pause; whether it's told to " +
				"leave them now couldn't be checked: your access doesn't allow reading Argo CD's Applications. If " +
				"it isn't, it will undo the next: https://docs.napwake.dev/guides/gitops/#argo-cd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := statusWorkloadObj("preview-42", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseIdle, time.Hour)
			undoneBy(w, "Argo CD", tt.ago)
			w.Spec.DryRun = true
			api := trackedByArgoCD(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "preview-42"}})
			c := newStatusClient(t, tt.funcs, append([]client.Object{w, api}, tt.argo...)...)

			result, err := clusterStatus(context.Background(), c, "preview-42", statusOpts())

			require.NoError(t, err)
			i := slices.IndexFunc(result.Problems, func(p problem) bool { return p.Type == "GitOpsConflict" })
			if tt.want == "" {
				assert.Negative(t, i, "not listed once Argo CD leaves the replicas")
				return
			}
			require.GreaterOrEqual(t, i, 0)
			assert.Equal(t, tt.want, result.Problems[i].Message)
		})
	}
}

// A pause the forecast brought forward, before idleAfter ran out, says so,
// and until when the forecast expected quiet, so a workload paused 20
// minutes after its last activity isn't a puzzle.
func TestNextFor_PausedEarly(t *testing.T) {
	quietUntil := &metav1.Time{Time: time.Date(2026, 10, 5, 14, 0, 0, 0, time.UTC)}
	early := func(phase v1alpha1.WorkloadPhase, dryRun bool) *v1alpha1.ManagedWorkload {
		w := statusWorkloadObj("ns", "api", v1alpha1.TargetKindDeployment, phase, time.Hour)
		w.Spec.DryRun = dryRun
		w.Status.Activity = activity(v1alpha1.ActivitySourceCPU, 20*time.Minute, 40*time.Minute)
		w.Status.Activity.ForecastQuietUntil = quietUntil
		return w
	}
	paused := early(v1alpha1.PhasePaused, false)
	paused.Status.Doorman = []v1alpha1.DoormanRoute{{Service: "api", DoormanPort: 20000}}
	assert.Equal(t, "paused early: the forecast expected quiet until Mon 14:00 UTC; wakes on a request or activity",
		nextFor(paused, time.Time{}, statusNow), "what it expected when it paused, and still true all weekend")
	assert.Equal(t, "would pause now, early: the forecast expects quiet until Mon 14:00 UTC",
		nextFor(early(v1alpha1.PhaseIdle, true), time.Time{}, statusNow), "a would-be pause in dry-run")
	assert.Equal(t, "pauses now, early: the forecast expects quiet until Mon 14:00 UTC",
		nextFor(early(v1alpha1.PhaseIdle, false), time.Time{}, statusNow), "not the clock's 40 minutes")

	// A would-be pause in dry-run stays Idle until activity, with its quiet
	// hours recorded, so once they're over it says what the forecast
	// expected, not what it expects.
	weekend := early(v1alpha1.PhaseIdle, true)
	weekend.Status.Activity.ForecastQuietUntil = &metav1.Time{Time: time.Date(2026, 10, 2, 20, 0, 0, 0, time.UTC)}
	assert.Equal(t, "would pause now, early: the forecast expected quiet until Fri 20:00 UTC",
		nextFor(weekend, time.Time{}, statusNow), "read on Monday, of a would-be pause made on Friday")
	weekend.Spec.DryRun = false
	assert.Equal(t, "pauses now, early: the forecast expected quiet until Fri 20:00 UTC",
		nextFor(weekend, time.Time{}, statusNow), "the same for a pause not yet made")

	waiting := early(v1alpha1.PhaseIdle, false)
	undoneBy(waiting, "Argo CD", 10*time.Minute)
	waiting.Status.Activity = activity(v1alpha1.ActivitySourceCPU, 20*time.Minute, 70*time.Minute)
	waiting.Status.Activity.ForecastQuietUntil = quietUntil
	assert.Equal(t, "pauses at 12:50 UTC (after GitOps conflict)", nextFor(waiting, time.Time{}, statusNow),
		"the hour Napwake waits, though the clock would run out after it")

	clock := early(v1alpha1.PhasePaused, false)
	clock.Status.Activity.ForecastQuietUntil = nil
	assert.Equal(t, "wakes on activity", nextFor(clock, time.Time{}, statusNow), "the clock's own pause")
}

// A workload Idle is in a pause, or a would-be pause in dry-run, due now,
// whether or not it has an idle policy, as one whose pause was requested
// may not, and whatever the clock recorded of when it would run out.
func TestNextFor_IdleWithoutTheClock(t *testing.T) {
	requested := statusWorkloadObj("ns", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseIdle, time.Hour)
	requested.Spec.IdlePolicy, requested.Spec.DryRun = nil, true
	requested.Status.Activity = &v1alpha1.ActivityStatus{LastActivityTime: *before(10 * time.Minute)}
	assert.Equal(t, "would pause now", nextFor(requested, time.Time{}, statusNow), "a requested would-be pause")

	noPauseAt := statusWorkloadObj("ns", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseIdle, time.Hour)
	noPauseAt.Status.Activity = &v1alpha1.ActivityStatus{LastActivityTime: *before(10 * time.Minute)}
	assert.Equal(t, "pauses now", nextFor(noPauseAt, time.Time{}, statusNow))

	afterConflict := noPauseAt.DeepCopy()
	afterConflict.Spec.IdlePolicy = nil
	undoneBy(afterConflict, "Argo CD", 10*time.Minute)
	assert.Equal(t, "pauses at 12:50 UTC (after GitOps conflict)", nextFor(afterConflict, time.Time{}, statusNow),
		"the hour Napwake waits after a GitOps conflict still holds it")
}

// In dry-run, Napwake never pauses, so a GitOps conflict holds nothing
// back.
func TestNextFor_DryRunAfterAGitOpsConflict(t *testing.T) {
	w := statusWorkloadObj("ns", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseIdle, time.Hour)
	undoneBy(w, "Argo CD", 10*time.Minute)
	w.Spec.DryRun = true

	assert.Equal(t, "would pause now", nextFor(w, time.Time{}, statusNow))
}

func deployment(namespace, name string, annotations map[string]string) *appsv1.Deployment {
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Annotations: annotations}}
}

func onlyWorkload(t *testing.T, c client.Client) statusWorkload {
	t.Helper()
	result, err := clusterStatus(context.Background(), c, "shop", statusOpts())
	require.NoError(t, err)
	require.Len(t, result.Workloads, 1)
	return result.Workloads[0]
}

// The operator honours active-until on the Deployment too, which status
// reads from the Deployment's metadata.
func TestStatus_ActiveUntilOnTheWorkload(t *testing.T) {
	w := statusWorkloadObj("shop", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning, time.Hour)
	w.Status.Activity = activity(v1alpha1.ActivitySourceCPU, 2*time.Hour, -time.Hour)
	c := newStatusClient(t, interceptor.Funcs{}, w, deployment("shop", "api",
		map[string]string{v1alpha1.AnnotationActiveUntil: statusNow.Add(2 * time.Hour).Format(time.RFC3339)}))

	assert.Equal(t, "held awake for 2h (active-until)", onlyWorkload(t, c).Next)
}

// The veto is read from the ManagedWorkload's condition, so it shows for as
// long as it lasts, after its one event has aged out, and without access to
// events.
func TestStatus_ForecastVeto(t *testing.T) {
	w := statusWorkloadObj("shop", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning, time.Hour)
	w.Status.Activity = activity(v1alpha1.ActivitySourceCPU, 2*time.Hour, -time.Hour)
	w.Status.Conditions = []metav1.Condition{condition("IdleVetoed", metav1.ConditionTrue, "ForecastExpectsDemand",
		"idle, but the forecast expects demand at 40% of requests in the hour from 13:00 UTC; not pausing yet")}
	w.Status.Activity.ForecastBusyFrom = &metav1.Time{Time: time.Date(2026, 10, 5, 13, 0, 0, 0, time.UTC)}
	forbidEvents := interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*corev1.EventList); ok {
				return apierrors.NewForbidden(corev1.Resource("events"), "", errors.New("no access"))
			}
			return c.List(ctx, list, opts...)
		}}
	c := newStatusClient(t, forbidEvents, w)

	assert.Equal(t, "held awake by the forecast: busy from 13:00 UTC", onlyWorkload(t, c).Next)
}

// What starts the clock without being activity, a gap in watching, the
// start of managing a workload, or its waking, isn't shown as activity:
// LAST ACTIVITY says there's been none since.
func TestStatus_WhatStartsTheClockIsntActivity(t *testing.T) {
	at := statusNow.Add(-3 * time.Minute)
	cell := func(source v1alpha1.ActivitySource) string {
		return activityCell(statusWorkload{ActivitySource: string(source), LastActivity: &at}, statusNow)
	}

	assert.Equal(t, "cpu, 3m ago", cell(v1alpha1.ActivitySourceCPU))
	assert.Equal(t, "request, 3m ago", cell(v1alpha1.ActivitySourceRequest))
	assert.Equal(t, "unobserved until 3m ago", cell(v1alpha1.ActivitySourceUnobserved))
	assert.Equal(t, "none since Napwake began managing it 3m ago", cell(v1alpha1.ActivitySourceCreated))
	assert.Equal(t, "none since it woke 3m ago", cell(v1alpha1.ActivitySourceWoke))
}

// The operator starts a new month's savings at its first reconcile in the
// month; until then status holds last month's.
func TestStatus_SavingsFromLastMonth(t *testing.T) {
	w := statusWorkloadObj("shop", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhasePaused, time.Hour)
	lastMonth := metav1.NewTime(time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC))
	w.Status.Cost = &v1alpha1.CostStatus{SavedThisMonth: "$80.00", LastAccumulatedAt: &lastMonth}
	c := newStatusClient(t, interceptor.Funcs{}, w)

	assert.Zero(t, onlyWorkload(t, c).SavedThisMonth)

	thisMonth := metav1.NewTime(statusNow.Add(-time.Hour))
	w.Status.Cost.LastAccumulatedAt = &thisMonth
	c = newStatusClient(t, interceptor.Funcs{}, w)

	assert.InDelta(t, 80.0, onlyWorkload(t, c).SavedThisMonth, 0.001)
}

func TestStatus_RepeatedNamespaceCountsOnce(t *testing.T) {
	opts := statusOpts()
	opts.namespaces = namespaceFlags{names: []string{"preview-7", "preview-7", " preview-7"}}

	result, err := clusterStatus(context.Background(), newStatusClient(t, interceptor.Funcs{}, statusCluster()...),
		"preview-42", opts)
	require.NoError(t, err)

	assert.Len(t, result.Workloads, 2)
	assert.Len(t, result.Recent, 1)
}

// A wake that the operator records only as Resumed, such as a hand-back on
// protecting the namespace, is still listed.
func TestStatus_WakeWithoutACause(t *testing.T) {
	w := statusWorkloadObj("shop", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning, time.Hour)
	out := runStatus(t, newStatusClient(t, interceptor.Funcs{}, w,
		statusEvent("shop", "api", "Paused", "api: paused", 3*time.Hour),
		statusEvent("shop", "api", "Resumed", "api: resumed", time.Hour),
		statusEvent("shop", "api", "Paused", "api: paused", 50*time.Minute),
		statusEvent("shop", "api", "WokeByActivity", "api: activity annotation is newer than the pause, waking",
			30*time.Minute),
		statusEvent("shop", "api", "Resumed", "api: resumed", 29*time.Minute),
	), statusOpts())

	assert.Contains(t, out, `Recent pauses and wakes:
  30m ago   shop/api   activity annotation is newer than the pause, waking
  50m ago   shop/api   paused
  1h ago    shop/api   resumed
  3h ago    shop/api   paused
`)
}

// A wake asked for with napwake.dev/wake-requested, as kubectl napwake
// wake asks, is listed as what woke the workload.
func TestStatus_WakeRequested(t *testing.T) {
	w := statusWorkloadObj("shop", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning, time.Hour)
	out := runStatus(t, newStatusClient(t, interceptor.Funcs{}, w,
		statusEvent("shop", "api", "Paused", "api: paused", 3*time.Hour),
		statusEvent("shop", "api", "WakeRequested", "api: wake requested, waking", 30*time.Minute),
		statusEvent("shop", "api", "Resumed", "api: resumed", 29*time.Minute),
	), statusOpts())

	assert.Contains(t, out, `Recent pauses and wakes:
  30m ago   shop/api   wake requested, waking
  3h ago    shop/api   paused
`)
}

// A pause a request called off is listed, with what came, and the resume
// that follows it adds nothing.
func TestStatus_PauseCancelled(t *testing.T) {
	w := statusWorkloadObj("shop", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning, time.Hour)
	out := runStatus(t, newStatusClient(t, interceptor.Funcs{}, w,
		statusEvent("shop", "api", "Paused", "api: paused", 3*time.Hour),
		statusEvent("shop", "api", "PauseCancelled", "api: a request came as it paused; the pause is cancelled",
			30*time.Minute),
		statusEvent("shop", "api", "Resumed", "api: resumed", 29*time.Minute),
	), statusOpts())

	assert.Contains(t, out, `Recent pauses and wakes:
  30m ago   shop/api   a request came as it paused; the pause is cancelled
  3h ago    shop/api   paused
`)
}

// A pause made while the workload was in use needs attention, with what to
// change, until a pause holds.
func TestStatus_PausedWhileInUse(t *testing.T) {
	w := statusWorkloadObj("shop", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning, time.Hour)
	w.Status.Conditions = []metav1.Condition{condition("PausedWhileInUse", metav1.ConditionTrue, "WokenSoonAfterPause",
		`a request came 40 seconds after it paused, so it was in use: it now waits 10 minutes without activity `+
			`before pausing, not 5 minutes, until a pause holds. It used up to 0.6% of its CPU requests while in `+
			`use, and up to 0.2% idle: try napwake.dev/cpu-threshold: "0.4"`)}

	problems := problemsOf(w, statusNow)

	require.Len(t, problems, 1)
	assert.Equal(t, "PausedWhileInUse", problems[0].Type)
	assert.Contains(t, problems[0].Message, `try napwake.dev/cpu-threshold: "0.4"`)

	w.Status.Conditions = []metav1.Condition{condition("PausedWhileInUse", metav1.ConditionFalse, "PauseHeld", "")}
	assert.Empty(t, problemsOf(w, statusNow), "a pause that held needs nobody")
}

// A workload deliberately labelled napwake.dev/ignore needs nobody's
// attention.
func TestStatus_IgnoredIsntAProblem(t *testing.T) {
	w := statusWorkloadObj("shop", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning, time.Hour)
	w.Status.Conditions = []metav1.Condition{condition("TargetAvailable", metav1.ConditionFalse, "TargetIgnored",
		"Deployment api has napwake.dev/ignore label")}

	assert.Empty(t, problemsOf(w, statusNow))
}

func TestStatus_MessagesStayOnOneLine(t *testing.T) {
	w := statusWorkloadObj("shop", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning, time.Hour)
	w.Status.Conditions = []metav1.Condition{condition("GitOpsConflict", metav1.ConditionTrue, "PauseUndone",
		"Argo CD set\tthe replicas\nfrom Git")}

	out := runStatus(t, newStatusClient(t, interceptor.Funcs{}, w,
		statusEvent("shop", "api", "RequestNotServed", "api: a request\twas closed\r\nafter 2m", time.Minute)),
		statusOpts())

	assert.Contains(t, out, "shop/api   GitOpsConflict   Argo CD set the replicas from Git\n")
	assert.Contains(t, out, "1m ago   shop/api   a request was closed after 2m\n")
}

// A ManagedWorkload written by hand under another name shows that name
// beside its workload's, since wake and deps take either.
func TestStatus_HandWrittenNamedApart(t *testing.T) {
	w := statusWorkloadObj("shop", "api-mw", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning, time.Hour)
	w.Spec.Target.Name = "api"

	out := runStatus(t, newStatusClient(t, interceptor.Funcs{}, w), statusOpts())

	assert.Contains(t, out, "  shop        deployment/api (api-mw)   running")
}

func forbidClusterWide() interceptor.Funcs {
	return interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList,
		opts ...client.ListOption) error {
		if _, ok := list.(*v1alpha1.ManagedWorkloadList); ok && listNamespace(opts) == "" {
			return apierrors.NewForbidden(schema.GroupResource{Group: "napwake.dev", Resource: "managedworkloads"}, "",
				nil)
		}
		return c.List(ctx, list, opts...)
	}}
}

func listNamespace(opts []client.ListOption) string {
	var o client.ListOptions
	o.ApplyOptions(opts)
	return o.Namespace
}

// Without access to every namespace, status falls back to the context's,
// and says so, unless -A asked for all of them.
func TestStatus_CantListEveryNamespace(t *testing.T) {
	c := newStatusClient(t, forbidClusterWide(), statusCluster()...)

	result, err := clusterStatus(context.Background(), c, "preview-7", statusOpts())
	require.NoError(t, err)

	assert.Len(t, result.Workloads, 2)
	assert.Equal(t, "Only preview-7, the context's namespace, is shown: your access doesn't allow listing "+
		"ManagedWorkloads in every namespace. Name others with -n.", result.Note)

	opts := statusOpts()
	opts.namespaces.all = true
	_, err = clusterStatus(context.Background(), c, "preview-7", opts)

	require.Error(t, err)
	assert.True(t, apierrors.IsForbidden(err))
	assert.Contains(t, err.Error(), "pass -n for the namespaces you can read")
}

// A failure listing every namespace says that's where it was listing, not
// a blank namespace.
func TestStatus_ListingEveryNamespaceFails(t *testing.T) {
	funcs := interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList,
		opts ...client.ListOption) error {
		if _, ok := list.(*v1alpha1.ManagedWorkloadList); ok {
			return apierrors.NewServiceUnavailable("etcd is down")
		}
		return c.List(ctx, list, opts...)
	}}

	_, err := clusterStatus(context.Background(), newStatusClient(t, funcs), "default", statusOpts())

	require.Error(t, err)
	assert.True(t, apierrors.IsServiceUnavailable(err))
	assert.Contains(t, err.Error(), "listing ManagedWorkloads in every namespace: etcd is down")
}

func TestStatus_NotInstalled(t *testing.T) {
	funcs := interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList,
		opts ...client.ListOption) error {
		if _, ok := list.(*v1alpha1.ManagedWorkloadList); ok {
			return &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "napwake.dev", Kind: "ManagedWorkload"}}
		}
		return c.List(ctx, list, opts...)
	}}

	_, err := clusterStatus(context.Background(), newStatusClient(t, funcs), "default", statusOpts())

	assert.ErrorIs(t, err, errNotInstalled)
}

// A pause or wake that's taking a few minutes isn't a problem yet.
func TestProblemsOf_NotStuckYet(t *testing.T) {
	w := statusWorkloadObj("ns", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhasePausing, 2*time.Minute)

	assert.Empty(t, problemsOf(w, statusNow))
}

func TestStatus_NothingManaged(t *testing.T) {
	out := runStatus(t, newStatusClient(t, interceptor.Funcs{}), statusOpts())

	assert.Equal(t, "staging (EKS us-east-1): Napwake manages no workloads here.\n"+
		"Your bill: won't drop until something removes nodes, and no node autoscaler was found. See "+
		"https://docs.napwake.dev/guides/cluster-autoscaler/\n"+
		"kubectl napwake scan finds idle ones; label one napwake.dev/managed=true for Napwake to manage it.\n", out)
}

func TestStatus_Namespaces(t *testing.T) {
	opts := statusOpts()
	opts.namespaces = namespaceFlags{names: []string{"preview-7"}}

	out := runStatus(t, newStatusClient(t, interceptor.Funcs{}, statusCluster()...), opts)

	assert.Contains(t, out, "Napwake manages 2 workloads.")
	assert.NotContains(t, out, "deployment/api")
	assert.NotContains(t, out, "preview-42/api")
}

// A namespace named with -n that doesn't exist is an error, not one
// Napwake manages nothing in; one that exists, or can't be checked, isn't.
func TestStatus_NamespaceThatDoesntExist(t *testing.T) {
	empty := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "empty"}}
	opts := statusOpts()

	opts.namespaces = namespaceFlags{names: []string{"preview-7", "nope"}}
	_, err := clusterStatus(context.Background(), newStatusClient(t, interceptor.Funcs{}, statusCluster()...),
		"default", opts)
	require.EqualError(t, err, "namespace nope doesn't exist; check the name")

	opts.namespaces = namespaceFlags{names: []string{"empty"}}
	_, err = clusterStatus(context.Background(), newStatusClient(t, interceptor.Funcs{}, empty), "default", opts)
	require.NoError(t, err)

	cantGet := interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object,
		...client.GetOption) error {
		return apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "nope", nil)
	}}
	opts.namespaces = namespaceFlags{names: []string{"nope"}}
	_, err = clusterStatus(context.Background(), newStatusClient(t, cantGet), "default", opts)
	require.NoError(t, err)
}

// Money reads as scan shows it, in whole dollars with thousands
// separators, and an amount under a dollar isn't $0.
func TestStatus_FreedAndSavedInDollars(t *testing.T) {
	big := statusWorkloadObj("preview-42", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhasePaused, time.Hour)
	big.Status.Cost = &v1alpha1.CostStatus{FreedThisMonth: "$1234.56", SavedThisMonth: "$1000.10"}
	small := statusWorkloadObj("preview-42", "web", v1alpha1.TargetKindDeployment, v1alpha1.PhasePaused, time.Hour)
	small.Status.Cost = &v1alpha1.CostStatus{FreedThisMonth: "$0.20", SavedThisMonth: "$0.20"}

	out := runStatus(t, newStatusClient(t, interceptor.Funcs{}, big, small), statusOpts())

	assert.Contains(t, out, "  Freed $1,235 this month, and saved $1,000 of it.\n")
	assert.Regexp(t, `deployment/api .* \$1,235\s+\$1,000\n`, out)
	assert.Regexp(t, `deployment/web .* <\$1\s+<\$1\n`, out)
}

// Dry-run never pauses, so what a workload in dry-run now has freed this
// month is what its pauses freed before dry-run was turned on, and counts.
func TestStatus_FreedBeforeDryRun(t *testing.T) {
	w := statusWorkloadObj("preview-7", "web", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning, time.Hour)
	w.Spec.DryRun = true
	w.Status.Cost = &v1alpha1.CostStatus{FreedThisMonth: "$9.00", SavedThisMonth: "$9.00"}
	w.Status.DryRun = &v1alpha1.DryRunStatus{Since: *before(time.Hour), EstimatedFreed: "$0.00"}

	out := runStatus(t, newStatusClient(t, interceptor.Funcs{}, w), statusOpts())

	assert.Contains(t, out, "  Freed $9 this month, and saved $9 of it.\n")
	assert.Regexp(t, `deployment/web\s+running \(dry-run\) .* \$9\s+\$9\s+\$0\n`, out)
}

// Dry-run is how people come to trust Napwake, so status leads with what
// it measured: in the summary, and in a column of its own, which only
// workloads in dry-run fill. One measuring that hasn't found a pause yet
// shows $0, not the dash of one that isn't measuring.
func TestStatus_DryRunLeadsWithWhatItWouldFree(t *testing.T) {
	web := statusWorkloadObj("preview-7", "web", v1alpha1.TargetKindDeployment, v1alpha1.PhaseIdle, time.Hour)
	web.Spec.DryRun = true
	web.Status.DryRun = &v1alpha1.DryRunStatus{Since: *before(50 * time.Hour), Pauses: 3, EstimatedFreed: "$12.60"}
	api := statusWorkloadObj("preview-7", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning, time.Hour)
	api.Spec.DryRun = true
	api.Status.DryRun = &v1alpha1.DryRunStatus{Since: *before(2 * time.Hour), EstimatedFreed: "$0.00"}
	live := statusWorkloadObj("preview-7", "worker", v1alpha1.TargetKindDeployment, v1alpha1.PhasePaused, time.Hour)
	c := newStatusClient(t, interceptor.Funcs{}, web, api, live)

	result, err := clusterStatus(context.Background(), c, "preview-7", statusOpts())
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, writeStatus(&out, result, "table", statusNow))

	assert.Contains(t, out.String(), "  1 paused, 1 running, 1 idle; 2 in dry-run, never paused: pausing them "+
		"would have freed $13 since Oct 3\n")
	assert.Regexp(t, `FREED THIS MONTH\s+SAVED\s+WOULD FREE\n`, out.String())
	assert.Regexp(t, `deployment/api\s+running \(dry-run\) .*-\s+-\s+\$0\n`, out.String())
	assert.Regexp(t, `deployment/web\s+idle \(dry-run\) .*-\s+-\s+\$13\n`, out.String())
	assert.Regexp(t, `deployment/worker\s+paused .*-\s+\$0\s+\$0\s+-\n`, out.String(),
		"a live workload that has freed nothing yet shows $0, as scan does; only dry-run's show -")
	assert.InDelta(t, 12.6, result.WouldFree, 0.001)
	require.NotNil(t, result.Workloads[1].WouldFree)
	assert.InDelta(t, 12.6, *result.Workloads[1].WouldFree, 0.001)
	assert.Nil(t, result.Workloads[2].WouldFree, "a live workload has nothing in dry-run to show")

	out.Reset()
	live.Spec.DryRun = false
	result, err = clusterStatus(context.Background(), newStatusClient(t, interceptor.Funcs{}, live), "preview-7",
		statusOpts())
	require.NoError(t, err)
	require.NoError(t, writeStatus(&out, result, "table", statusNow))
	assert.NotContains(t, out.String(), "WOULD FREE", "no column without a workload in dry-run")
}

// A would-be pause that has freed less than a cent reads $0.00 in the
// operator's estimate. It has freed something, so it reads <$1, as any
// amount above zero does; $0 is for a workload still waiting for its first.
func TestStatus_WouldFreeBelowACent(t *testing.T) {
	small := statusWorkloadObj("preview-7", "redis", v1alpha1.TargetKindDeployment, v1alpha1.PhaseIdle, time.Hour)
	small.Spec.DryRun = true
	small.Status.DryRun = &v1alpha1.DryRunStatus{Since: *before(3 * time.Hour), Pauses: 1, EstimatedFreed: "$0.00"}
	waiting := statusWorkloadObj("preview-7", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning, time.Hour)
	waiting.Spec.DryRun = true
	waiting.Status.DryRun = &v1alpha1.DryRunStatus{Since: *before(2 * time.Hour), EstimatedFreed: "$0.00"}

	out := runStatus(t, newStatusClient(t, interceptor.Funcs{}, small, waiting), statusOpts())

	assert.Regexp(t, `deployment/redis\s+idle \(dry-run\) .*-\s+-\s+<\$1\n`, out)
	assert.Regexp(t, `deployment/api\s+running \(dry-run\) .*-\s+-\s+\$0\n`, out)
	assert.Contains(t, out, "pausing them would have freed <$1 since")
}

// Status says which zone the forecast counts hours in, as the operator
// recorded it, and how to change the default, which is rarely where people
// are.
func TestStatus_ForecastTimeZone(t *testing.T) {
	tests := []struct {
		name  string
		zones []string
		want  string
	}{
		{name: "set", zones: []string{"Europe/London"}, want: "  Forecast time zone: Europe/London\n"},
		{name: "the default", zones: []string{"UTC", ""},
			want: "  Forecast time zone: UTC, the default; if your users are elsewhere, set the Helm value timezone " +
				"to theirs, such as Europe/London, so wakes follow their clock\n"},
		{name: "two installs", zones: []string{"UTC", "America/New_York", "UTC"},
			want: "  Forecast time zones: America/New_York, UTC, as the installs differ\n"},
		{name: "not recorded yet", zones: []string{""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := make([]client.Object, 0, len(tt.zones))
			for i, zone := range tt.zones {
				w := statusWorkloadObj("shop", fmt.Sprintf("w%d", i), v1alpha1.TargetKindDeployment,
					v1alpha1.PhaseRunning, time.Hour)
				if zone != "" {
					w.Status.Prediction = &v1alpha1.PredictionStatus{TimeZone: zone}
				}
				objs = append(objs, w)
			}

			out := runStatus(t, newStatusClient(t, interceptor.Funcs{}, objs...), statusOpts())

			if tt.want == "" {
				assert.NotContains(t, out, "Forecast time zone")
				return
			}
			assert.Contains(t, out, tt.want)
		})
	}
}

// Status says when its money figures are at spot prices, as the operator
// priced them, so they aren't read against on-demand prices; a workload's
// own rates win over its nodes', so it isn't counted.
func TestStatus_PricedAtSpot(t *testing.T) {
	cpu, memory := resource.MustParse("30m"), resource.MustParse("4m")
	tests := []struct {
		name  string
		price []v1alpha1.ListPrice
		rates *v1alpha1.CostRates
		want  string
	}{
		{name: "on-demand", price: []v1alpha1.ListPrice{v1alpha1.ListPriceOnDemand}},
		{name: "one on spot", price: []v1alpha1.ListPrice{v1alpha1.ListPriceSpot, v1alpha1.ListPriceOnDemand},
			want: "  1 workload runs on spot nodes, priced at spot.\n"},
		{name: "partly on spot", price: []v1alpha1.ListPrice{v1alpha1.ListPriceSpot,
			v1alpha1.ListPriceSpotAndOnDemand}, want: "  2 workloads run on spot nodes, priced at spot.\n"},
		{name: "own rates win", price: []v1alpha1.ListPrice{v1alpha1.ListPriceSpot},
			rates: &v1alpha1.CostRates{CPUPerHour: &cpu, MemoryPerHour: &memory}},
		{name: "own CPU rate only", price: []v1alpha1.ListPrice{v1alpha1.ListPriceSpot},
			rates: &v1alpha1.CostRates{CPUPerHour: &cpu}, want: "  1 workload runs on spot nodes, priced at spot.\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := make([]client.Object, 0, len(tt.price))
			for i, price := range tt.price {
				w := statusWorkloadObj("shop", fmt.Sprintf("w%d", i), v1alpha1.TargetKindDeployment,
					v1alpha1.PhaseRunning, time.Hour)
				w.Status.Cost = &v1alpha1.CostStatus{ListPrice: price}
				if tt.rates != nil {
					w.Spec.CostTracking = &v1alpha1.CostTrackingSpec{Rates: tt.rates}
				}
				objs = append(objs, w)
			}

			out := runStatus(t, newStatusClient(t, interceptor.Funcs{}, objs...), statusOpts())

			if tt.want == "" {
				assert.NotContains(t, out, "spot")
				return
			}
			assert.Contains(t, out, tt.want)
		})
	}
}

// Freed well above saved means nodes aren't scaling down, which status
// raises, pointing at nodes; a workload whose savings can't be measured
// says why, and isn't counted as not saving.
func TestStatus_FreedWellAboveSaved(t *testing.T) {
	tests := []struct {
		name      string
		costs     []*v1alpha1.CostStatus
		attention bool
	}{
		{name: "most of it saved", costs: []*v1alpha1.CostStatus{{FreedThisMonth: "$40.00", SavedThisMonth: "$30.00"}}},
		{name: "little of it saved", attention: true,
			costs: []*v1alpha1.CostStatus{{FreedThisMonth: "$40.00", SavedThisMonth: "$5.00"}}},
		{name: "too little freed to matter",
			costs: []*v1alpha1.CostStatus{{FreedThisMonth: "$0.60", SavedThisMonth: "$0.00"}}},
		{name: "unmeasured savings aren't missing savings",
			costs: []*v1alpha1.CostStatus{{FreedThisMonth: "$40.00", SavingsUnknown: "nodes can't be read"},
				{FreedThisMonth: "$10.00", SavedThisMonth: "$9.00"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := make([]client.Object, 0, len(tt.costs))
			for i, cost := range tt.costs {
				w := statusWorkloadObj("shop", fmt.Sprintf("w%d", i), v1alpha1.TargetKindDeployment,
					v1alpha1.PhasePaused, time.Hour)
				w.Status.Cost = cost
				objs = append(objs, w)
			}

			result, err := clusterStatus(context.Background(), newStatusClient(t, interceptor.Funcs{}, objs...),
				"shop", statusOpts())
			require.NoError(t, err)
			var out bytes.Buffer
			require.NoError(t, writeStatus(&out, result, "table", statusNow))

			assert.Equal(t, tt.attention, result.NodesNotScalingDown)
			if tt.attention {
				assert.Contains(t, out.String(), "Needs attention:\n  Pausing freed $40 this month, but only $5 of "+
					"it came off the bill: nodes aren't scaling down.\n  kubectl napwake nodes shows why.\n")
			} else {
				assert.NotContains(t, out.String(), "kubectl napwake nodes")
			}
		})
	}
}

func TestStatus_SavingsUnknown(t *testing.T) {
	a := statusWorkloadObj("shop", "a", v1alpha1.TargetKindDeployment, v1alpha1.PhasePaused, time.Hour)
	a.Status.Cost = &v1alpha1.CostStatus{FreedThisMonth: "$4.00", SavingsUnknown: "the operator can't list nodes"}
	b := statusWorkloadObj("shop", "b", v1alpha1.TargetKindDeployment, v1alpha1.PhasePaused, time.Hour)
	b.Status.Cost = &v1alpha1.CostStatus{FreedThisMonth: "$2.00", SavingsUnknown: "the operator can't list nodes"}

	result, err := clusterStatus(context.Background(), newStatusClient(t, interceptor.Funcs{}, a, b), "shop",
		statusOpts())
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, writeStatus(&out, result, "table", statusNow))

	assert.Equal(t, []string{"the operator can't list nodes"}, result.SavingsUnknown)
	assert.Contains(t, out.String(),
		"  What's saved can't be measured for some workloads: the operator can't list nodes\n")
	assert.Regexp(t, `deployment/a .* \$4\s+unknown\n`, out.String())
}

// The Your bill line comes from what status can see of the cluster's node
// autoscaler.
func TestStatus_NodeAutoscaler(t *testing.T) {
	ca := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cluster-autoscaler-status", Namespace: "kube-system"}}
	objs := append(statusCluster(), ca)

	out := runStatus(t, newStatusClient(t, interceptor.Funcs{}, objs...), statusOpts())

	assert.Contains(t, out, "  Your bill: drops only as Cluster Autoscaler removes nodes\n")
}

func TestStatus_Since(t *testing.T) {
	opts := statusOpts()
	opts.since = time.Hour

	out := runStatus(t, newStatusClient(t, interceptor.Funcs{}, statusCluster()...), opts)

	assert.Contains(t, out, "30m ago   preview-7/worker")
	assert.NotContains(t, out, "3h ago")
}

func TestStatus_EventsForbidden(t *testing.T) {
	funcs := interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList,
		opts ...client.ListOption) error {
		if _, ok := list.(*corev1.EventList); ok {
			return apierrors.NewForbidden(schema.GroupResource{Resource: "events"}, "", nil)
		}
		return c.List(ctx, list, opts...)
	}}

	out := runStatus(t, newStatusClient(t, funcs, statusCluster()...), statusOpts())

	assert.Contains(t, out, "Recent pauses and wakes aren't shown: your access doesn't allow reading events.")
}

func TestStatus_JSON(t *testing.T) {
	result, err := clusterStatus(context.Background(), newStatusClient(t, interceptor.Funcs{}, statusCluster()...),
		"preview-42", statusOpts())
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, writeStatus(&out, result, "json", statusNow))

	var got statusResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	assert.Len(t, got.Workloads, 4)
	assert.Len(t, got.Problems, 2)
	for _, pr := range got.Problems {
		assert.Equal(t, "deployment/worker", pr.Target, "a problem names the workload as its row does")
	}
	assert.Len(t, got.Recent, 3)
	assert.InDelta(t, 42.45, got.FreedThisMonth, 0.001)
	assert.InDelta(t, 35.0, got.SavedThisMonth, 0.001)
	assert.Empty(t, got.NodeAutoscaler.Scalers)
}

// inZone runs the rest of a test with the machine's time zone set to an
// hour east of UTC, as the API client reads times into the local zone.
func inZone(t *testing.T) {
	t.Helper()
	local := time.Local
	time.Local = time.FixedZone("CET", 3600)
	t.Cleanup(func() { time.Local = local })
}

// JSON and YAML give times in UTC, as scan does, whatever the machine's
// zone, so output from different machines compares.
func TestStatus_TimesInUTC(t *testing.T) {
	inZone(t)
	result, err := clusterStatus(context.Background(), newStatusClient(t, interceptor.Funcs{}, statusCluster()...),
		"preview-42", statusOpts())
	require.NoError(t, err)
	var out bytes.Buffer

	require.NoError(t, writeStatus(&out, result, "json", statusNow))

	require.NotEmpty(t, result.Recent)
	assert.Contains(t, out.String(), `Z"`)
	assert.NotContains(t, out.String(), "+01:00")
}

// A workload opted in with its label may have a ManagedWorkload named
// otherwise; status names it by the workload itself everywhere.
func TestStatus_NamedByTarget(t *testing.T) {
	w := statusWorkloadObj("shop", "deployment-api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning, time.Hour)
	w.Labels = map[string]string{v1alpha1.LabelFromLabel: v1alpha1.True}
	w.Spec.Target.Name = "api"
	w.Status.Conditions = []metav1.Condition{
		condition("MetricsAvailable", metav1.ConditionFalse, "MetricsUnavailable", "metrics-server isn't answering"),
	}

	out := runStatus(t, newStatusClient(t, interceptor.Funcs{}, w,
		statusEvent("shop", "deployment-api", "Paused", "api: paused", time.Minute)), statusOpts())

	assert.Contains(t, out, "shop/api   MetricsAvailable   metrics-server isn't answering")
	assert.Contains(t, out, "1m ago   shop/api   paused")
	assert.NotContains(t, out, "deployment-api")
}

// A resume that stopped short needs attention; Napwake leaving its own
// operator and doorman alone doesn't.
func TestStatus_WhatNeedsAttention(t *testing.T) {
	tests := []struct {
		name      string
		condition metav1.Condition
		want      bool
	}{
		{name: "a resume with replicas not Ready",
			condition: condition("ResumeIncomplete", metav1.ConditionTrue, "ReplicasNotReady", "1 of 3 Ready"), want: true},
		{name: "a resume that finished",
			condition: condition("ResumeIncomplete", metav1.ConditionFalse, "AllReplicasReady", "")},
		{name: "a pause with pods still running",
			condition: condition("PauseIncomplete", metav1.ConditionTrue, "PodsStillRunning", "1 pod running"), want: true},
		{name: "a pause whose pods stopped",
			condition: condition("PauseIncomplete", metav1.ConditionFalse, "PodsStopped", "")},
		{name: "Napwake's own workload",
			condition: condition("Protected", metav1.ConditionTrue, "NapwakeComponent", "")},
		{name: "an awake workload, which isn't routed to the doorman",
			condition: condition("WakeOnRequest", metav1.ConditionFalse, "NotPaused", "")},
		{name: "the doorman turned off for the cluster",
			condition: condition("WakeOnRequest", metav1.ConditionFalse, "DoormanDisabled", "")},
		{name: "a routing problem its last pause found, while it runs", want: true,
			condition: condition("WakeOnRequest", metav1.ConditionFalse, "UnsupportedLoadBalancer",
				"at its last pause: no Service can be routed through the doorman")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := statusWorkloadObj("shop", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning, time.Hour)
			w.Status.Conditions = []metav1.Condition{tt.condition}

			assert.Equal(t, tt.want, len(problemsOf(w, statusNow)) > 0)
		})
	}
}
