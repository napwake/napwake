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
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/autoscaler"
	"github.com/napwake/napwake/internal/lifecycle"
	"github.com/napwake/napwake/internal/signal"
)

// emptyVector is what a Prometheus that has just restarted without its data
// answers a rate() query with: no series, until each has two scrapes.
const emptyVector = `{"status":"success","data":{"resultType":"vector","result":[]}}`

// The break-it table on rc.11, 27 Oct, 12:47 to 12:49 UTC: web, at
// idle-after=1m, kept awake under steady load by a Prometheus request-rate
// query. Its check at 12:47:11 found requests. Prometheus restarted on an
// emptyDir at 12:47:59 and answered [] until 12:48:22. The check at
// 12:48:11 took the empty answer as quiet, the clock ran out, and web went
// to Pausing until a request turned it back. A query that matched series at
// the last check and matches none now is unread for that check, as a failed
// one is, so web stays Running, and the next check finds the requests again.
func TestPrometheusRestart_DoesNotPauseABusyWorkload(t *testing.T) {
	cfg := startEnvtest(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	scheme := testScheme(t)
	admin, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	const ns = "team-a"
	require.NoError(t, admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	web := envtestDeployment(ns, 3)
	require.NoError(t, admin.Create(ctx, web))
	web.Status.ObservedGeneration, web.Status.Replicas, web.Status.ReadyReplicas = web.Generation, 3, 3
	require.NoError(t, admin.Status().Update(ctx, web))

	var restarted atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if restarted.Load() {
			_, _ = w.Write([]byte(emptyVector))
			return
		}
		_, _ = w.Write([]byte(promValue("0.04")))
	}))
	t.Cleanup(srv.Close)

	checked := time.Date(2026, 10, 27, 12, 47, 11, 0, time.UTC)
	now := monotonic(checked)
	mw := envtestWorkload(ns)
	mw.Spec.IdlePolicy = &v1alpha1.IdlePolicySpec{IdleAfter: &metav1.Duration{Duration: time.Minute},
		Activity: &v1alpha1.ActivitySpec{Prometheus: []v1alpha1.PrometheusActivity{
			{PromQL: `sum(rate(http_request_duration_seconds_count{namespace="team-a",app="web"}[1m]))`}}}}
	require.NoError(t, createTakenOn(ctx, admin, mw))
	evaluated := metav1.NewTime(checked.Add(-time.Minute))
	mw.Status.Activity = &v1alpha1.ActivityStatus{LastActivityTime: evaluated,
		LastActivitySource: v1alpha1.ActivitySourcePrometheus, LastEvaluatedTime: &evaluated,
		TemplateHash: podTemplateHash(web)}
	require.NoError(t, admin.Status().Update(ctx, mw))

	c := client.WithFieldOwner(indexedClient{admin}, v1alpha1.FieldManager)
	finder := autoscaler.NewFinder(c)
	idle := idleCPU
	r := &Reconciler{Client: c, Scheme: scheme, Recorder: events.NewFakeRecorder(1000), PodReader: admin,
		pauser: lifecycle.NewPauser(c, finder), autoscalers: finder, metrics: &idle,
		engines: newEngineRegistry(func() forecaster { return &stubForecaster{} }),
		clock:   func() time.Time { return now }, prometheus: signal.NewPrometheus(srv.URL)}
	remember(r, mw)
	key := client.ObjectKeyFromObject(mw)
	check := func(at time.Time) *v1alpha1.ManagedWorkload {
		t.Helper()
		now = monotonic(at)
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		require.NoError(t, err)
		var got v1alpha1.ManagedWorkload
		require.NoError(t, admin.Get(ctx, key, &got))
		return &got
	}

	got := check(checked)
	require.Equal(t, v1alpha1.PhaseRunning, got.Status.Phase)
	require.Equal(t, v1alpha1.ActivitySourcePrometheus, got.Status.Activity.LastActivitySource, "requests at 12:47:11")

	restarted.Store(true)
	got = check(checked.Add(time.Minute))
	assert.Equal(t, v1alpha1.PhaseRunning, got.Status.Phase, "the empty answer at 12:48:11 isn't quiet")
	cond := meta.FindStatusCondition(got.Status.Conditions, conditionPrometheusAvailable)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, reasonSeriesLost, cond.Reason)
	assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, conditionPrometheusQueryEmpty),
		"a query that matched a minute ago isn't misspelt")

	restarted.Store(false)
	got = check(checked.Add(2 * time.Minute))
	assert.Equal(t, v1alpha1.PhaseRunning, got.Status.Phase)
	assert.Equal(t, v1alpha1.ActivitySourcePrometheus, got.Status.Activity.LastActivitySource, "requests again")
	assert.True(t, meta.IsStatusConditionTrue(got.Status.Conditions, conditionPrometheusAvailable))
	require.NoError(t, admin.Get(ctx, client.ObjectKeyFromObject(web), web))
	assert.Equal(t, int32(3), *web.Spec.Replicas, "never scaled down")
}
