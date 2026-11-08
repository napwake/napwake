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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/forecast"
)

// shopMetrics measures each of a shop's workloads, by name, at the CPU
// percent of the 1000m its one replica requests that the test sets.
type shopMetrics struct {
	percent map[string]float64
}

func (m *shopMetrics) WorkloadCPUMillis(_ context.Context, w *v1alpha1.ManagedWorkload) (float64, int, error) {
	return m.percent[w.Name] * 10, 1, nil
}

func (m *shopMetrics) CPURequestPerReplica(context.Context, *v1alpha1.ManagedWorkload) (float64, error) {
	return 1000, nil
}

func (m *shopMetrics) PodRequestsPerReplica(context.Context, *v1alpha1.ManagedWorkload) (cpuMillis, memBytes float64,
	err error) {
	return 1000, 0, nil
}

func (m *shopMetrics) Replicas(context.Context, *v1alpha1.ManagedWorkload) (int32, error) {
	return 1, nil
}

func (m *shopMetrics) TotalPVCBytes(context.Context, *v1alpha1.ManagedWorkload) (float64, error) {
	return 0, nil
}

// shopService is one of a shop's workloads: the workloads it calls, and its
// CPU while the shop's load runs, as a percentage of what it requests. It's
// 0.5% otherwise.
type shopService struct {
	name  string
	calls []string
	busy  float64
	// autoResume is whether its own forecast wakes it ahead, once the
	// shop's is turned on. Without, only its dependents' wakes do.
	autoResume bool
}

func shopTarget(name string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: name, Image: name + ":v1"}}},
			},
		},
	}
}

func shopWorkload(s shopService) *v1alpha1.ManagedWorkload {
	deps := make([]v1alpha1.DependencyRef, 0, len(s.calls))
	for _, c := range s.calls {
		deps = append(deps, v1alpha1.DependencyRef{Kind: v1alpha1.TargetKindDeployment, Name: c})
	}
	return &v1alpha1.ManagedWorkload{
		ObjectMeta: metav1.ObjectMeta{Name: s.name, Namespace: "default", UID: types.UID("shop-" + s.name)},
		Spec: v1alpha1.ManagedWorkloadSpec{
			Target:     v1alpha1.WorkloadRef{Kind: v1alpha1.TargetKindDeployment, Name: s.name},
			IdlePolicy: &v1alpha1.IdlePolicySpec{IdleAfter: &metav1.Duration{Duration: time.Hour}},
			DependsOn:  deps,
		},
		Status: v1alpha1.ManagedWorkloadStatus{Phase: v1alpha1.PhaseRunning},
	}
}

// shopWake is a wake of one of the shop's workloads, by the reason its event
// gives.
type shopWake struct {
	at     time.Time
	reason string
}

// shopRun is what a shopDays run saw of each workload, by name.
type shopRun struct {
	engines map[string]*soakEngine
	wakes   map[string][]shopWake
	regimes map[string][]time.Time
	// mornings is, for each load day by its date, the workloads awake at
	// the load's first call.
	mornings map[time.Time][]string
}

// shopDays runs a shop for days from monday, every workload behind the
// doorman with its forecast at the balanced confidence, reconciled as the
// operator would: each at the requeue it asks for, and at once when
// another's wake stamps it, those due at the same moment in the order
// given. The load generator calls the shop's entry on weekdays from 09:00
// to 17:59 UTC, and nothing else from outside; the entry calls the rest as
// it serves. From autoResumeFrom, the forecasts of the services that ask
// for it wake them ahead of the load.
func shopDays(t *testing.T, monday time.Time, days int, autoResumeFrom time.Time, entry string,
	services []shopService) shopRun {
	t.Helper()
	sh := newShop(t, monday, entry, services)
	switched := false
	for end := monday.AddDate(0, 0, days); ; {
		s := sh.due()
		now := sh.next[s.name]
		if !now.Before(end) {
			break
		}
		sh.fr.now = now
		if !switched && !now.Before(autoResumeFrom) {
			switched = true
			sh.turnOnAutoResume(t)
		}
		sh.recordMorning(t, now)
		sh.step(t, s, now)
	}
	return sh.run
}

// shop is a shopDays run under way.
type shop struct {
	ctx      context.Context
	fr       *forecastReconciler
	metrics  *shopMetrics
	recorder *events.FakeRecorder
	entry    string
	services []shopService
	targets  map[string]*appsv1.Deployment
	// next is when each workload is next reconciled, and seen the version
	// of it its last reconcile left.
	next map[string]time.Time
	seen map[string]string
	run  shopRun
}

func newShop(t *testing.T, monday time.Time, entry string, services []shopService) *shop {
	t.Helper()
	objs := make([]client.Object, 0, 1+3*len(services))
	objs = append(objs, doormanEndpoints("10.0.0.7"))
	sh := &shop{ctx: context.Background(), entry: entry, services: services,
		targets: map[string]*appsv1.Deployment{}, next: map[string]time.Time{}, seen: map[string]string{},
		metrics: &shopMetrics{percent: map[string]float64{}}, recorder: events.NewFakeRecorder(100),
		run: shopRun{engines: map[string]*soakEngine{}, wakes: map[string][]shopWake{},
			regimes: map[string][]time.Time{}, mornings: map[time.Time][]string{}}}
	for _, s := range services {
		sh.targets[s.name] = shopTarget(s.name)
		objs = append(objs, shopWorkload(s), sh.targets[s.name],
			service(s.name, map[string]string{"app": s.name}, corev1.ServicePort{Name: "http", Port: 80}))
	}
	c := fakeClientBuilder(testScheme(t)).WithStatusSubresource(&v1alpha1.ManagedWorkload{}).
		WithObjects(objs...).Build()
	sh.fr = newForecastReconciler(t, nil, c)
	sh.fr.now = monday
	sh.fr.pauser = &clockedPauser{stubPauser: stubPauser{pauseDone: true, resumeDone: true}, now: sh.fr.Reconciler.now}
	sh.fr.metrics = sh.metrics
	sh.fr.Recorder = sh.recorder
	routeThroughDoorman(sh.fr.Reconciler)
	for _, s := range services {
		e := &soakEngine{Engine: forecast.NewEngine(forecast.DefaultParams(), forecast.Settings{}),
			fed: map[time.Time]float64{}}
		sh.run.engines[s.name] = e
		sh.fr.engines.engines[types.UID("shop-"+s.name)] = e
		sh.next[s.name] = monday
	}
	return sh
}

// due is the workload due to be reconciled first.
func (sh *shop) due() shopService {
	s := sh.services[0]
	for _, o := range sh.services[1:] {
		if sh.next[o.name].Before(sh.next[s.name]) {
			s = o
		}
	}
	return s
}

func (sh *shop) turnOnAutoResume(t *testing.T) {
	t.Helper()
	for _, o := range sh.services {
		w := fetch(t, sh.fr.Reconciler, o.name)
		w.Spec.IdlePolicy.AutoResume = o.autoResume
		require.NoError(t, sh.fr.Update(sh.ctx, w))
		sh.seen[o.name] = w.ResourceVersion
	}
}

// recordMorning notes, at a load day's first call, which workloads are
// awake for it.
func (sh *shop) recordMorning(t *testing.T, now time.Time) {
	t.Helper()
	day := now.Truncate(24 * time.Hour)
	if _, recorded := sh.run.mornings[day]; recorded || !loadgenCalls(loadHours, now) {
		return
	}
	sh.run.mornings[day] = []string{}
	for _, o := range sh.services {
		if phase := fetch(t, sh.fr.Reconciler, o.name).Status.Phase; phase != v1alpha1.PhasePaused &&
			phase != v1alpha1.PhasePausing {
			sh.run.mornings[day] = append(sh.run.mornings[day], o.name)
		}
	}
}

// step reconciles s at now, at the CPU its use gives it, with the load
// generator's call stamped on the entry if it's pausing or paused, and
// schedules what comes next.
func (sh *shop) step(t *testing.T, s shopService, now time.Time) {
	t.Helper()
	w := fetch(t, sh.fr.Reconciler, s.name)
	phase := w.Status.Phase
	sh.metrics.percent[s.name] = 0.5
	if loadHours(now) {
		sh.metrics.percent[s.name] = s.busy
	}
	entry := s.name == sh.entry
	if woke := w.Status.LastActedAt; entry && phase == v1alpha1.PhaseRunning && woke != nil &&
		now.Sub(woke.Time) < 3*time.Minute {
		sh.metrics.percent[s.name] = 80
	}
	if entry && loadgenCalls(loadHours, now) && (phase == v1alpha1.PhasePausing || phase == v1alpha1.PhasePaused) {
		w.Annotations = map[string]string{v1alpha1.AnnotationLastRequest: now.Format(time.RFC3339)}
		require.NoError(t, sh.fr.Update(sh.ctx, w))
	}

	observed := w.Status.DeepCopy()
	result, err := operate(sh.ctx, sh.fr.Reconciler, w, sh.targets[s.name])
	require.NoError(t, err, "%s at %s", s.name, now)
	require.NoError(t, sh.fr.persistStatus(sh.ctx, w, observed))
	sh.drainEvents(s.name, now)
	sh.seen[s.name] = fetch(t, sh.fr.Reconciler, s.name).ResourceVersion
	for _, o := range sh.services {
		if o.name != s.name && fetch(t, sh.fr.Reconciler, o.name).ResourceVersion != sh.seen[o.name] {
			sh.next[o.name] = now
		}
	}

	require.NotNil(t, result, "%s at %s", s.name, now)
	sh.next[s.name] = now.Add(max(result.RequeueAfter, time.Second))
	if entry && w.Status.Phase == v1alpha1.PhasePaused {
		if call := nextCall(loadHours, now); !call.IsZero() && call.Before(sh.next[s.name]) {
			sh.next[s.name] = call
		}
	}
}

// drainEvents records the wakes and regime changes of the workload just
// reconciled.
func (sh *shop) drainEvents(name string, now time.Time) {
	for len(sh.recorder.Events) > 0 {
		e := <-sh.recorder.Events
		for _, reason := range []string{ReasonAutoResume, ReasonWokeByActivity} {
			if strings.HasPrefix(e, "Normal "+reason+" ") {
				sh.run.wakes[name] = append(sh.run.wakes[name], shopWake{at: now, reason: reason})
			}
		}
		if strings.HasPrefix(e, "Warning "+ReasonRegimeChange+" ") {
			sh.run.regimes[name] = append(sh.run.regimes[name], now)
		}
	}
}

// leftActing is when the workload's forecast stopped acting, from from
// until until: it acted once fed one hour and not once fed the next.
func (e *soakEngine) leftActing(from, until time.Time) []time.Time {
	var left []time.Time
	for i := 1; i < len(e.acting); i++ {
		h := e.acting[i]
		if !h.start.Before(from) && h.start.Before(until) && e.acting[i-1].acting && !h.acting {
			left = append(left, h.start)
		}
	}
	return left
}

// actingAt reports whether the forecast acted once fed the last hour before
// at.
func (e *soakEngine) actingAt(at time.Time) bool {
	acting := false
	for _, h := range e.acting {
		if !h.start.Before(at) {
			break
		}
		acting = h.acting
	}
	return acting
}

// The test cluster's shop, P84: a front end the load generator calls, which
// calls the rest. Each weekday for two weeks the load's first request wakes
// the front end, and its wake wakes its dependencies. Then autoResume is
// turned on, and the forecasts wake the shop ahead of the load: adservice by
// its own forecast, cartservice by the front end's wake, and redis-cart by
// cartservice's. Only the front end's own CPU shows the load; the others'
// stays under the threshold, as the shop's services did.
//
// The front end in use is its dependencies' use, and its wake isn't. So
// each dependency learns every load hour as busy, and the hour it's woken
// ahead in as quiet, whether a request or the forecast woke the shop, and
// its forecast acts on through the mornings woken ahead and the weekend
// between them, without a regime change or a wake of its own an hour early.
// Before, a dependency learned the hour a request woke the shop in as busy
// and the rest from its CPU, and the hour its dependent's wake woke it in as
// busy. On the first morning the forecast woke the shop ahead, the hour of
// the load's first call came in far quieter than forecast, and the hour
// before far busier: 9 of the test cluster's 11 forecasts stopped acting
// that day.
func TestDependencies_LearnTheirDependentsUse(t *testing.T) {
	if testing.Short() {
		t.Skip("simulates weeks of reconciles")
	}
	t.Parallel()
	monday := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	autoResumeFrom := monday.AddDate(0, 0, 14)
	const days = 22
	shop := []shopService{
		{name: "adservice", busy: 5, autoResume: true},
		{name: "frontend", calls: []string{"adservice", "cartservice"}, busy: 40, autoResume: true},
		{name: "cartservice", calls: []string{"redis-cart"}, busy: 5},
		{name: "redis-cart", busy: 3, autoResume: true},
	}
	run := shopDays(t, monday, days, autoResumeFrom, "frontend", shop)
	end := monday.AddDate(0, 0, days)
	busy := forecast.RequestDemand(defaultCPUThreshold)

	for _, dep := range []string{"adservice", "cartservice", "redis-cart"} {
		e := run.engines[dep]
		require.True(t, e.actingAt(autoResumeFrom), "%s's forecast acts after two weeks of mornings woken by a "+
			"request", dep)
		assert.Empty(t, e.leftActing(autoResumeFrom, end), "%s's forecast stopped acting", dep)
		assert.Empty(t, run.regimes[dep], "%s's forecast made a regime change", dep)

		for hour, peak := range e.fed {
			if hour.Before(monday.AddDate(0, 0, 7)) || hour.Weekday() == time.Saturday ||
				hour.Weekday() == time.Sunday {
				continue
			}
			switch {
			case loadHours(hour):
				assert.GreaterOrEqual(t, peak, busy, "%s, the hour from %s, when the front end served the load",
					dep, hour)
			case hour.Hour() == 8:
				assert.Less(t, peak, float64(defaultCPUThreshold), "%s, the hour from %s, which the shop was "+
					"woken ahead in, or not at all", dep, hour)
			}
		}
		for _, w := range run.wakes[dep] {
			assert.False(t, w.at.Weekday() == time.Saturday || w.at.Weekday() == time.Sunday,
				"%s woken at the weekend, at %s, by %s", dep, w.at, w.reason)
			assert.False(t, w.reason == ReasonAutoResume && w.at.Hour() < 8,
				"%s woken by its own forecast at %s, an hour or more before the shop", dep, w.at)
		}
	}

	var mornings []time.Time
	for day := range run.mornings {
		if !day.Before(autoResumeFrom) {
			mornings = append(mornings, day)
		}
	}
	slices.SortFunc(mornings, func(a, b time.Time) int { return a.Compare(b) })
	require.Len(t, mornings, 6, "the load days with autoResume on")
	for _, day := range mornings {
		assert.ElementsMatch(t, []string{"frontend", "adservice", "cartservice", "redis-cart"}, run.mornings[day],
			"the shop awake for the load's first call on %s", day.Format("Mon 2 Jan"))
	}
	for _, w := range run.wakes["frontend"] {
		if !w.at.Before(autoResumeFrom) {
			assert.Equal(t, ReasonAutoResume, w.reason, "the front end woken ahead at %s", w.at)
		}
	}
}

// A dependency's demand, awake and warmed up, at 3% of its CPU, with its
// dependents as each case has them: a dependent holding it and in use, from
// the end of its own warm-up, is its use, directly or through another
// dependency, and a dependent's wake, which stamps it, isn't. A person's
// activity annotation on it, with no dependent awake, is.
func TestAwakeDemand_ADependentsUse(t *testing.T) {
	now := fixedTime.Add(30 * time.Minute)
	clock := func(source v1alpha1.ActivitySource, at time.Time) *v1alpha1.ActivityStatus {
		evaluated := metav1.NewTime(now.Add(-30 * time.Second))
		return &v1alpha1.ActivityStatus{LastActivityTime: metav1.NewTime(at), LastActivitySource: source,
			LastEvaluatedTime: &evaluated}
	}
	ref := func(name string) v1alpha1.DependencyRef {
		return v1alpha1.DependencyRef{Kind: v1alpha1.TargetKindDeployment, Name: name}
	}
	workload := func(name string, phase v1alpha1.WorkloadPhase, woke time.Time, activity *v1alpha1.ActivityStatus,
		deps ...string) *v1alpha1.ManagedWorkload {
		refs := make([]v1alpha1.DependencyRef, 0, len(deps))
		for _, d := range deps {
			refs = append(refs, ref(d))
		}
		w := depWorkload("default", name, v1alpha1.TargetKindDeployment, phase, refs...)
		w.Status.LastActedAt = &metav1.Time{Time: woke}
		w.Status.Activity = activity
		return w
	}
	longAgo := fixedTime.Add(-2 * time.Hour)
	quiet := clock(v1alpha1.ActivitySourceWoke, longAgo)
	tests := []struct {
		name       string
		dependency *v1alpha1.ActivityStatus
		dependents []*v1alpha1.ManagedWorkload
		want       float64
	}{
		{name: "a dependent in use", dependency: quiet, want: 20, dependents: []*v1alpha1.ManagedWorkload{
			workload("api", v1alpha1.PhaseRunning, longAgo, clock(v1alpha1.ActivitySourceCPU, now.Add(-time.Minute)),
				"postgres")}},
		{name: "a dependent last in use before the hour", dependency: quiet, want: 3,
			dependents: []*v1alpha1.ManagedWorkload{workload("api", v1alpha1.PhaseRunning, longAgo,
				clock(v1alpha1.ActivitySourceCPU, fixedTime.Add(-10*time.Minute)), "postgres")}},
		{name: "a dependent starting up", dependency: quiet, want: 3, dependents: []*v1alpha1.ManagedWorkload{
			workload("api", v1alpha1.PhaseRunning, now.Add(-5*time.Minute),
				clock(v1alpha1.ActivitySourceCPU, now.Add(-time.Minute)), "postgres")}},
		{name: "a dependent's use seen by a Prometheus query", dependency: quiet, want: 20,
			dependents: []*v1alpha1.ManagedWorkload{workload("api", v1alpha1.PhaseRunning, longAgo,
				clock(v1alpha1.ActivitySourcePrometheus, now.Add(-time.Minute)), "postgres")}},
		{name: "a dependent woken ahead, which stamped it", want: 3,
			dependency: clock(v1alpha1.ActivitySourceAnnotation, now.Add(-5*time.Minute)),
			dependents: []*v1alpha1.ManagedWorkload{workload("api", v1alpha1.PhaseRunning, now.Add(-5*time.Minute),
				clock(v1alpha1.ActivitySourceWoke, now.Add(-5*time.Minute)), "postgres")}},
		{name: "a person's activity annotation, no dependent awake", want: 20,
			dependency: clock(v1alpha1.ActivitySourceAnnotation, now.Add(-5*time.Minute)),
			dependents: []*v1alpha1.ManagedWorkload{workload("api", v1alpha1.PhasePaused, longAgo,
				clock(v1alpha1.ActivitySourceCPU, fixedTime.Add(-90*time.Minute)), "postgres")}},
		{name: "a dependent paused", dependency: quiet, want: 3, dependents: []*v1alpha1.ManagedWorkload{
			workload("api", v1alpha1.PhasePaused, longAgo, clock(v1alpha1.ActivitySourceCPU, now.Add(-time.Minute)),
				"postgres")}},
		{name: "in use through a dependent's dependent", dependency: quiet, want: 20,
			dependents: []*v1alpha1.ManagedWorkload{
				workload("api", v1alpha1.PhaseRunning, longAgo, quiet, "postgres"),
				workload("web", v1alpha1.PhaseRunning, longAgo, clock(v1alpha1.ActivitySourceCPU, now.Add(-time.Minute)),
					"api"),
			}},
		{name: "a cycle of dependents, none in use", dependency: quiet, want: 3,
			dependents: []*v1alpha1.ManagedWorkload{
				workload("api", v1alpha1.PhaseRunning, longAgo, quiet, "postgres", "web"),
				workload("web", v1alpha1.PhaseRunning, longAgo, quiet, "api"),
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			postgres := workload("postgres", v1alpha1.PhaseRunning, longAgo, tt.dependency)
			objs := make([]client.Object, 0, 1+len(tt.dependents))
			objs = append(objs, postgres)
			for _, d := range tt.dependents {
				objs = append(objs, d)
			}
			r := depReconciler(t, &stubPauser{}, objs...)
			r.clock = func() time.Time { return now }

			assert.InDelta(t, tt.want, r.awakeDemand(context.Background(), postgres, 3), 1e-9)
		})
	}
}

// A paused dependency woken by its dependent's wake, which stamps it with
// an activity annotation, learns nothing from that wake: autoResume wakes
// the dependent ahead of people, and the hour before theirs would be learned
// busy. Woken by a person's annotation, with no dependent awake, it learns
// the hour as busy, as before.
func TestPausedDemand_ADependentsWakeIsntDemand(t *testing.T) {
	for _, tt := range []struct {
		name      string
		dependent v1alpha1.WorkloadPhase
		want      float64
	}{
		{name: "its dependent waking", dependent: v1alpha1.PhaseResuming, want: 0},
		{name: "its dependent paused", dependent: v1alpha1.PhasePaused, want: 20},
	} {
		t.Run(tt.name, func(t *testing.T) {
			postgres := askedToWake(depWorkload("default", "postgres", v1alpha1.TargetKindStatefulSet,
				v1alpha1.PhasePaused), v1alpha1.AnnotationLastActivity)
			withCondition(postgres, conditionWakeOnRequest, metav1.ConditionTrue, "DoormanRouted")
			api := depWorkload("default", "api", v1alpha1.TargetKindDeployment, tt.dependent, postgresRef())
			r := depReconciler(t, &stubPauser{resumeDone: true}, postgres, api, postgresTarget(0, 0))
			r.engines = realEngineRegistry()

			_, err := r.reconcileWake(context.Background(), postgres, postgresTarget(0, 0))
			require.NoError(t, err)

			require.Equal(t, v1alpha1.PhaseRunning, postgres.Status.Phase, "woken")
			hour := r.engines.hours[postgres.UID]
			assert.Equal(t, forecast.HourStart(fixedTime, nil), hour.Start)
			assert.InDelta(t, tt.want, hour.Peak, 1e-9)
		})
	}
}
