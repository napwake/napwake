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

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
	"github.com/okedeji/hybernate/internal/discovery"
	"github.com/okedeji/hybernate/internal/forecast"
)

// officeHour is whether officePrometheus's pod is busy at some point in
// the hour from at: weekdays, 09:00 to 18:00 UTC.
func officeHour(at time.Time) bool {
	at = at.UTC()
	return at.Weekday() != time.Saturday && at.Weekday() != time.Sunday && at.Hour() >= 9 && at.Hour() < 18
}

// learnedState is the state a forecast saves having learned hours of
// officePrometheus's demand, up to the hour before the one until is in.
func learnedState(t *testing.T, hours int, until time.Time, loc *time.Location) string {
	t.Helper()
	e := forecast.NewEngine(forecast.DefaultParams(), forecast.Settings{Threshold: 75, Location: loc})
	start := forecast.HourStart(until, loc).Add(-time.Duration(hours) * time.Hour)
	seen := make([]forecast.Hour, 0, hours)
	for h := range hours {
		at := start.Add(time.Duration(h) * time.Hour)
		peak := 1.0
		if officeHour(at) {
			peak = 40
		}
		seen = append(seen, forecast.Hour{Start: at, Peak: peak})
	}
	_, err := e.Learn(seen, defaultCPUThreshold)
	require.NoError(t, err)
	state, err := e.Export()
	require.NoError(t, err)
	return state
}

// learningAPI is preview-42/api's ManagedWorkload as GKE's orders/api had
// it: managed since since, paused, its forecast learning from the 6 hours
// of history it took up when Hybernate began managing it, and none since.
func learningAPI(t *testing.T, since time.Time) *v1alpha1.ManagedWorkload {
	t.Helper()
	lagos, err := time.LoadLocation("Africa/Lagos")
	require.NoError(t, err)
	return &v1alpha1.ManagedWorkload{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "preview-42", CreationTimestamp: metav1.NewTime(since)},
		Spec: v1alpha1.ManagedWorkloadSpec{Target: v1alpha1.WorkloadRef{Kind: v1alpha1.TargetKindDeployment, Name: "api"},
			IdlePolicy: &v1alpha1.IdlePolicySpec{}},
		Status: v1alpha1.ManagedWorkloadStatus{Phase: v1alpha1.PhaseRunning, Prediction: &v1alpha1.PredictionStatus{
			DailyPhase: "Observing", WeeklyPhase: "Observing", TimeZone: "Africa/Lagos", Threshold: 75,
			Reason: v1alpha1.ForecastObserving, Message: "learning: it has seen 6 of the 24 hours of the day, and " +
				"can't act until it has seen them all",
			History: &v1alpha1.PredictionHistory{Reason: historyLearned, Hours: 6,
				Message: "the forecast learned from 6 hours of the workload's CPU history in Prometheus"},
			State: learnedState(t, 6, since, lagos)}},
	}
}

// A workload Hybernate manages has a forecast of its own, which forecast
// NAME shows however much history Prometheus has. On GKE, with 32 hours of
// it, forecast api refused orders/api as having no CPU history to replay.
func TestCLI_ForecastManagedWithTooLittleHistory(t *testing.T) {
	now := time.Now()
	prom := officePrometheusSince(t, now.Add(-32*time.Hour))
	objs := append(forecastCluster(), learningAPI(t, now.Add(-32*time.Hour)))

	out, err := runCLI(t, newStatusClient(t, interceptor.Funcs{}, objs...), "forecast", "api", "-n", "preview-42",
		"--prometheus-url", prom.URL)

	require.NoError(t, err, out)
	assert.Contains(t, out, ": the forecast of preview-42/api, times in Africa/Lagos\n\n"+
		"Hybernate's forecast, from ManagedWorkload api: learning\n"+
		"  learning: it has seen 6 of the 24 hours of the day, and can't act until it has seen them all\n",
		"in the operator's time zone, with its message as it wrote it")
	assert.Regexp(t, `\n  Phase:\s+daily Observing, weekly Observing\n`, out)
	assert.Regexp(t, `\n  Hours seen:\s+6 of the 24 hours of the day, 6 of the 168 hours of the week\n`, out)
	assert.Regexp(t, `\n  Learned:\s+6 hours, all from its CPU history in Prometheus; none live in the 32 hours `+
		`Hybernate has managed it\n`, out)
	assert.Regexp(t, `\n  Confidence:\s+0% right over the last day, 0% over the last week; it acts from 75%\n`, out)
	assert.Regexp(t, `\n  Acts from:\s+not known until it has seen every hour of the day\n`, out)
	assert.Regexp(t, `\n  Busy hours:\s+none yet: it forecasts the hours it acts on once it's acting\n`, out)
	assert.Regexp(t, `\n  autoResume:\s+off: .* spec.idlePolicy.autoResume: true on its ManagedWorkload turns it on\n`,
		out)
	assert.Contains(t, out, "\nNo replay of its history: Prometheus has 32 hours of its CPU history, and the replay "+
		"needs at least 2 days, a day to learn from and a day to replay; run it again after ")

	out, err = runCLI(t, newStatusClient(t, interceptor.Funcs{}, objs...), "forecast", "api", "-n", "preview-42",
		"--prometheus-url", prom.URL, "-o", "json")
	require.NoError(t, err, out)
	var result forecastResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.Len(t, result.Workloads, 1)
	w := result.Workloads[0]
	assert.True(t, w.Managed)
	require.NotNil(t, w.Operator, "JSON has what the terminal shows")
	assert.Equal(t, v1alpha1.ForecastObserving, w.Operator.Reason)
	assert.Equal(t, 75, w.Operator.Threshold)
	require.NotNil(t, w.Operator.Learned)
	assert.Equal(t, forecastLearned{Hours: 6, FromHistory: 6, HoursOfDay: 6, HoursOfWeek: 6, Busy: []busySpan{}},
		*w.Operator.Learned)
	assert.Nil(t, w.Forecast)
	assert.Contains(t, w.NoReplay, "run it again after")
}

// Without a Prometheus to read, a managed workload's own forecast is still
// shown, with why there's no replay; an unmanaged one has nothing to show
// and fails, as before.
func TestCLI_ForecastNamedWithoutPrometheus(t *testing.T) {
	objs := append(forecastCluster(), learningAPI(t, time.Now().Add(-32*time.Hour)))

	out, err := runCLI(t, newStatusClient(t, interceptor.Funcs{}, objs...), "forecast", "api", "-n", "preview-42")

	require.NoError(t, err, out)
	assert.Contains(t, out, "Hybernate's forecast, from ManagedWorkload api: learning\n")
	assert.Contains(t, out, "\nNo replay of its history: the forecast is replayed over CPU history, and there's none "+
		"to read: no Prometheus found in the cluster")

	_, err = runCLI(t, newStatusClient(t, interceptor.Funcs{}, forecastCluster()...), "forecast", "api", "-n",
		"preview-42")
	require.ErrorContains(t, err, "no Prometheus found in the cluster")
}

// A workload with a forecast of its own and enough history has both: what
// the operator's forecast is doing, then the replay.
func TestCLI_ForecastManagedWithHistory(t *testing.T) {
	prom := officePrometheus(t)
	objs := append(forecastCluster(), learningAPI(t, time.Now().Add(-32*time.Hour)))

	out, err := runCLI(t, newStatusClient(t, interceptor.Funcs{}, objs...), "forecast", "api", "-n", "preview-42",
		"--prometheus-url", prom.URL)

	require.NoError(t, err, out)
	operator := strings.Index(out, "Hybernate's forecast, from ManagedWorkload api: learning")
	replay := strings.Index(out, "Replayed over its last 28 days of CPU history")
	require.NotEqual(t, -1, operator, out)
	require.Greater(t, replay, operator, out)
	assert.Contains(t, out, "Ready on ")
	assert.NotContains(t, out, "No replay")
}

// The operator's forecast, once acting, expects office hours busy, as its
// saved state shows, so forecast NAME can say when.
func TestLearnedFrom_BusyHours(t *testing.T) {
	monday := time.Date(2026, 11, 2, 6, 30, 0, 0, time.UTC)
	p := &v1alpha1.PredictionStatus{TimeZone: "UTC", State: learnedState(t, 28*24, monday, time.UTC),
		History: &v1alpha1.PredictionHistory{Reason: historyLearned, Hours: 24}}

	l, unread := learnedFrom(p, 75, defaultCPUThreshold, monday)

	require.Empty(t, unread)
	require.NotNil(t, l)
	assert.Equal(t, 28*24, l.Hours)
	assert.Equal(t, 24, l.FromHistory)
	assert.Equal(t, 24, l.HoursOfDay)
	assert.Equal(t, 168, l.HoursOfWeek)
	require.Len(t, l.Busy, 1, "today's office hours, and none of tomorrow's within the day ahead")
	assert.Equal(t, time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC), l.Busy[0].From)
	assert.Equal(t, time.Date(2026, 11, 2, 18, 0, 0, 0, time.UTC), l.Busy[0].To)
	assert.InDelta(t, 40, l.Busy[0].Peak, 5)

	l, unread = learnedFrom(&v1alpha1.PredictionStatus{TimeZone: "UTC", State: "bm90IGEgc3RhdGU="}, 75, 10, monday)
	assert.Nil(t, l)
	assert.Contains(t, unread, "this plugin can't read the forecast's saved state")
}

func TestOperatorForecast_Pending(t *testing.T) {
	mw := &v1alpha1.ManagedWorkload{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "orders"}}
	var out strings.Builder
	p := &printer{w: &out}

	writeOperatorForecast(p, operatorForecastOf(mw, time.Now()), time.UTC, time.Now())

	assert.Equal(t, "Hybernate's forecast: none yet. Hybernate hasn't recorded a forecast for it yet. It does on "+
		"its first look at the workload while it's running, within a minute of managing it; if it doesn't, kubectl "+
		"describe managedworkload api -n orders says what's in the way.\n\n", out.String())
}

func TestWriteOperatorForecast_Acting(t *testing.T) {
	monday := time.Date(2026, 11, 2, 6, 30, 0, 0, time.UTC)
	o := &operatorForecast{ManagedWorkload: "api", Acting: true, Reason: v1alpha1.ForecastActing,
		Message: "acting: its forecasts of the last day were 92% right", DailyPhase: "Active", WeeklyPhase: "Active",
		DailyConfidence: 92, WeeklyConfidence: 88, Threshold: 75, AutoResume: true,
		ManagedSince: monday.Add(-30 * 24 * time.Hour),
		Learned: &forecastLearned{Hours: 700, FromHistory: 672, HoursOfDay: 24, HoursOfWeek: 168, Busy: []busySpan{
			{From: monday.Add(150 * time.Minute), To: monday.Add(690 * time.Minute), Peak: 41},
			{From: monday.Add(990 * time.Minute), To: monday.Add(1110 * time.Minute), Peak: 20},
		}}}
	var out strings.Builder

	writeOperatorForecast(&printer{w: &out}, o, time.UTC, monday)

	got := out.String()
	assert.Contains(t, got, "Hybernate's forecast, from ManagedWorkload api: acting\n")
	assert.Regexp(t, `\n  Learned:\s+700 hours: 672 from its CPU history in Prometheus, and 28 live in the 30 days `+
		`Hybernate has managed it\n`, got)
	assert.Regexp(t, `\n  Acts from:\s+now\n`, got)
	assert.Regexp(t, `\n  Busy hours:\s+Mon 2 Nov 09:00–18:00, up to 41% of its CPU requests; `+
		`Mon 2 Nov 23:00 – Tue 3 Nov 01:00, up to 20% of its CPU requests\n`, got)
	assert.Regexp(t, `\n  autoResume:\s+on: it holds off pauses before the hours it expects busy, and wakes the `+
		`workload 15 minutes ahead of them\n`, got)

	o.Learned.Busy = []busySpan{}
	out.Reset()
	writeOperatorForecast(&printer{w: &out}, o, time.UTC, monday)
	assert.Regexp(t, `\n  Busy hours:\s+none in the next 24 hours\n`, out.String())
}

func TestActsFrom(t *testing.T) {
	starts := time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		o    operatorForecast
		want string
	}{
		{"acting", operatorForecast{Acting: true}, "now"},
		{"projected", operatorForecast{StartsActing: &starts}, "around Thu 29 Oct 09:00, if the coming days repeat " +
			"the last week"},
		{"hasn't seen the day", operatorForecast{Learned: &forecastLearned{HoursOfDay: 6}},
			"not known until it has seen every hour of the day"},
		{"too irregular", operatorForecast{Learned: &forecastLearned{HoursOfDay: 24}},
			"not expected within 4 weeks, if the coming days repeat the last week"},
		{"observing, its state unread", operatorForecast{Reason: v1alpha1.ForecastObserving},
			"not known until it has scored a day of its forecasts"},
		{"observing all day", operatorForecast{Reason: v1alpha1.ForecastObserving,
			Learned: &forecastLearned{HoursOfDay: 24}}, "not known until it has scored a day of its forecasts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, actsFrom(&tt.o, time.UTC))
		})
	}
}

func TestPickNamed(t *testing.T) {
	scan := scanResult{ClusterReport: &discovery.ClusterReport{Workloads: []Workload{
		{Namespace: "a", Kind: v1alpha1.TargetKindDeployment, Name: "api"},
		{Namespace: "b", Kind: v1alpha1.TargetKindDeployment, Name: "api", Managed: true},
		{Namespace: "a", Kind: v1alpha1.TargetKindStatefulSet, Name: "db"},
	}}}
	mw := func(namespace, name string) v1alpha1.ManagedWorkload {
		return v1alpha1.ManagedWorkload{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: v1alpha1.ManagedWorkloadSpec{Target: v1alpha1.WorkloadRef{Kind: v1alpha1.TargetKindDeployment,
				Name: name}}}
	}

	got, err := pickNamed(scan, nil, kindStatefulSet, "db", "statefulset/db")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "db", got[0].Name)

	_, err = pickNamed(scan, []v1alpha1.ManagedWorkload{mw("b", "api")}, "", "api", "api")
	require.EqualError(t, err, "api is a/deployment/api and b/deployment/api; name one with -n or kind/name")

	got, err = pickNamed(scan, []v1alpha1.ManagedWorkload{mw("c", "gone")}, "", "gone", "gone")
	require.NoError(t, err)
	require.Len(t, got, 1, "a ManagedWorkload whose workload the scan didn't read is still named")
	assert.True(t, got[0].Managed)
	assert.NotNil(t, got[0].mw)
	assert.Nil(t, got[0].scanned)

	got, err = pickNamed(scan, nil, kindDeployment, "db", "deployment/db")
	require.NoError(t, err)
	assert.Empty(t, got)

	const ownName = "api-mw"
	byOwnName := mw("b", "api")
	byOwnName.Name = ownName
	got, err = pickNamed(scan, []v1alpha1.ManagedWorkload{byOwnName}, "", ownName, ownName)
	require.NoError(t, err)
	require.Len(t, got, 1, "a ManagedWorkload named apart from its workload, by its own name")
	assert.Equal(t, "api", got[0].Name)
	assert.NotNil(t, got[0].scanned, "with its workload as the scan read it")
}

// A workload labelled hybernate.io/ignore exists, though the scan leaves it
// out, so forecast NAME says why it can't show it.
func TestCLI_ForecastIgnoredWorkload(t *testing.T) {
	objs := forecastCluster()
	objs[1].(*appsv1.Deployment).Labels = map[string]string{v1alpha1.LabelIgnore: v1alpha1.True}
	prom := officePrometheus(t)

	_, err := runCLI(t, newStatusClient(t, interceptor.Funcs{}, objs...), "forecast", "api", "-n", "preview-42",
		"--prometheus-url", prom.URL)

	require.EqualError(t, err, "preview-42/deployment/api is labelled hybernate.io/ignore=true, which keeps "+
		"Hybernate and its forecast away from it; remove the label to see its forecast")
}

// inNamespace is objs moved to namespace.
func inNamespace(namespace string, objs ...client.Object) []client.Object {
	for _, obj := range objs {
		if _, ok := obj.(*corev1.Namespace); ok {
			obj.SetName(namespace)
			continue
		}
		obj.SetNamespace(namespace)
	}
	return objs
}

// Someone who can read ManagedWorkloads only namespace by namespace still
// sees a workload's own forecast: the scan finds it managed in its
// namespace, and its ManagedWorkload is read there, not only in the
// context's namespace.
func TestCLI_ForecastManagedListedByNamespace(t *testing.T) {
	objs := inNamespace("orders", append(forecastCluster(), learningAPI(t, time.Now().Add(-32*time.Hour)))...)
	c := newStatusClient(t, forbidClusterWide(), objs...)

	out, err := runCLI(t, c, "forecast", "api")

	require.NoError(t, err, out)
	assert.Contains(t, out, "the forecast of orders/api")
	assert.Contains(t, out, "Hybernate's forecast, from ManagedWorkload api: learning\n")
}

// A workload labelled hybernate.io/ignore under a ManagedWorkload written
// by hand isn't forecast by Hybernate, whatever its status last said, and
// forecast NAME says why, as status NEXT does, rather than that it wasn't
// read.
func TestCLI_ForecastIgnoredWithItsOwnManagedWorkload(t *testing.T) {
	objs := forecastCluster()
	objs[1].(*appsv1.Deployment).Labels = map[string]string{v1alpha1.LabelIgnore: v1alpha1.True}
	mw := learningAPI(t, time.Now().Add(-32*time.Hour))
	mw.Status.Conditions = []metav1.Condition{{Type: "TargetAvailable", Status: metav1.ConditionFalse,
		Reason: "TargetIgnored", Message: "Deployment api has the hybernate.io/ignore label"}}
	prom := officePrometheus(t)

	out, err := runCLI(t, newStatusClient(t, interceptor.Funcs{}, append(objs, mw)...), "forecast", "api", "-n",
		"preview-42", "--prometheus-url", prom.URL)

	require.NoError(t, err, out)
	assert.Contains(t, out, "Hybernate's forecast: none: deployment/api is labelled hybernate.io/ignore=true, which "+
		"keeps Hybernate and its forecast away from it; remove the label to see its forecast.\n")
	assert.NotContains(t, out, "learning", "not the forecast it last recorded")
	assert.Contains(t, out, "No replay of its history: it's labelled hybernate.io/ignore=true, which the forecast "+
		"leaves out, as Hybernate does.\n")
	assert.NotContains(t, out, "was read")
}

// How to turn autoResume on depends on where the ManagedWorkload's
// settings come from: the workload's annotations for one the label opt-in
// made, its spec for one written by hand, which no annotation changes.
func TestOperatorForecast_AutoResumeAdvice(t *testing.T) {
	written := learningAPI(t, time.Now().Add(-32*time.Hour))
	fromLabel := written.DeepCopy()
	fromLabel.Labels = map[string]string{v1alpha1.LabelFromLabel: v1alpha1.True}

	assert.Equal(t, "off: it holds off pauses before the hours it expects busy, but doesn't wake the workload "+
		"for them; spec.idlePolicy.autoResume: true on its ManagedWorkload turns it on",
		autoResumeCell(operatorForecastOf(written, time.Now())))
	assert.Equal(t, "off: it holds off pauses before the hours it expects busy, but doesn't wake the workload "+
		"for them; the workload's hybernate.io/auto-resume: \"true\" annotation turns it on",
		autoResumeCell(operatorForecastOf(fromLabel, time.Now())))
}
