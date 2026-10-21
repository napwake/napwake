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
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
	"github.com/okedeji/hybernate/internal/forecast"
)

// fakeClientBuilder is a fake API server with the field indexes
// SetupWithManager gives the manager's cache, so every test's lookups by
// index behave as the operator's do.
func fakeClientBuilder(scheme *runtime.Scheme) *fake.ClientBuilder {
	b := fake.NewClientBuilder().WithScheme(scheme)
	for field, index := range workloadIndexes() {
		b = b.WithIndex(&v1alpha1.ManagedWorkload{}, field, index)
	}
	return b
}

// indexedClient answers ManagedWorkload lists by the field indexes
// SetupWithManager gives the cache, for a client that reads the API server
// directly, which serves none of them.
type indexedClient struct {
	client.Client
}

func (c indexedClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	var o client.ListOptions
	o.ApplyOptions(opts)
	workloads, ok := list.(*v1alpha1.ManagedWorkloadList)
	if !ok || o.FieldSelector == nil {
		return c.Client.List(ctx, list, opts...)
	}
	requirements := o.FieldSelector.Requirements()
	o.FieldSelector = nil
	if err := c.Client.List(ctx, workloads, &o); err != nil {
		return err
	}
	indexes := workloadIndexes()
	workloads.Items = slices.DeleteFunc(workloads.Items, func(w v1alpha1.ManagedWorkload) bool {
		return slices.ContainsFunc(requirements, func(req fields.Requirement) bool {
			return !slices.Contains(indexes[req.Field](&w), req.Value)
		})
	})
	return nil
}

func realEngineRegistry() *engineRegistry {
	return newEngineRegistry(func() forecaster {
		return forecast.NewEngine(forecast.DefaultParams(), forecast.Settings{})
	})
}

// forecastReconciler runs automation with the real forecast engine against
// a fake API server, at a clock the test moves.
type forecastReconciler struct {
	*Reconciler
	now time.Time
}

func newForecastReconciler(t *testing.T, workload *v1alpha1.ManagedWorkload, c client.Client) *forecastReconciler {
	t.Helper()
	fr := &forecastReconciler{now: fixedTime}
	if c == nil {
		c = fakeClientBuilder(testScheme(t)).
			WithStatusSubresource(&v1alpha1.ManagedWorkload{}).WithObjects(workload).Build()
	}
	fr.Reconciler = &Reconciler{
		Client:   c,
		Scheme:   testScheme(t),
		Recorder: events.NewFakeRecorder(50),
		pauser:   &stubPauser{},
		metrics:  &stubMetrics{cpuMillis: 250},
		engines:  realEngineRegistry(),
		clock:    func() time.Time { return fr.now },
	}
	return fr
}

// reconcile runs automation and writes status the way Reconcile does.
func (fr *forecastReconciler) reconcile(t *testing.T, workload *v1alpha1.ManagedWorkload) {
	t.Helper()
	require.NoError(t, fr.Get(context.Background(), client.ObjectKeyFromObject(workload), workload))
	observed := workload.Status.DeepCopy()
	_, err := fr.reconcileAutomation(context.Background(), workload, nil)
	require.NoError(t, err)
	require.NoError(t, fr.persistStatus(context.Background(), workload, observed))
}

func forecastWorkload() *v1alpha1.ManagedWorkload {
	w := automationWorkload(v1alpha1.PhaseRunning)
	w.UID = "uid-1"
	// A recent activity evaluation, so only a meaningful change is written
	// before the next status flush.
	w.Status.Activity = &v1alpha1.ActivityStatus{LastEvaluatedTime: &metav1.Time{Time: fixedTime}}
	return w
}

// watch reconciles the workload once a minute until until, inclusive.
func (fr *forecastReconciler) watch(t *testing.T, workload *v1alpha1.ManagedWorkload, until time.Time) {
	t.Helper()
	for ; !fr.now.After(until); fr.now = fr.now.Add(time.Minute) {
		fr.reconcile(t, workload)
	}
	fr.now = until
}

func TestForecastState_LivesInStatusNotConfigMaps(t *testing.T) {
	workload := forecastWorkload()
	fr := newForecastReconciler(t, workload, nil)

	fr.watch(t, workload, fixedTime.Add(time.Hour))

	require.NotNil(t, workload.Status.Prediction)
	assert.NotEmpty(t, workload.Status.Prediction.State)
	restored, err := forecast.ImportEngine(workload.Status.Prediction.State, forecast.Settings{})
	require.NoError(t, err)
	assert.Equal(t, 1, restored.GetDataPoints())

	var cms corev1.ConfigMapList
	require.NoError(t, fr.List(context.Background(), &cms))
	assert.Empty(t, cms.Items, "the operator writes no ConfigMaps")
}

func TestForecastState_ChangesOnceAnHour(t *testing.T) {
	workload := forecastWorkload()
	fr := newForecastReconciler(t, workload, nil)
	fr.reconcile(t, workload)
	state := workload.Status.Prediction.State

	var changedAt []time.Time
	for fr.now = fixedTime.Add(time.Minute); !fr.now.After(fixedTime.Add(2 * time.Hour)); fr.now = fr.now.Add(time.Minute) {
		fr.reconcile(t, workload)
		if workload.Status.Prediction.State != state {
			state = workload.Status.Prediction.State
			changedAt = append(changedAt, fr.now)
		}
	}

	assert.Equal(t, []time.Time{fixedTime.Add(time.Hour), fixedTime.Add(2 * time.Hour)}, changedAt,
		"as each hour ends")
}

// TestForecastState_SurvivesARestart: a restarted operator, or a new leader,
// picks up from status. The hour the restart interrupted is skipped rather
// than learned from the part of it seen after.
func TestForecastState_SurvivesARestart(t *testing.T) {
	workload := forecastWorkload()
	fr := newForecastReconciler(t, workload, nil)
	fr.watch(t, workload, fixedTime.Add(2*time.Hour))

	restarted := newForecastReconciler(t, workload, fr.Client)
	restarted.now = fixedTime.Add(2*time.Hour + 30*time.Minute)
	restarted.watch(t, workload, fixedTime.Add(3*time.Hour))

	engine, err := restarted.engines.getOrCreate(workload.UID, forecast.Settings{}, "")
	require.NoError(t, err)
	assert.Equal(t, 2, engine.GetDataPoints(), "the hours before the restart are restored, and the one it interrupted skipped")
	assert.True(t, engine.Observed(fixedTime.Add(time.Hour)))
	assert.False(t, engine.Observed(fixedTime.Add(2*time.Hour)))

	restarted.watch(t, workload, fixedTime.Add(4*time.Hour))
	assert.Equal(t, 3, engine.GetDataPoints())
}

func TestForecastState_UnreadableStateStartsAfreshWithAWarning(t *testing.T) {
	gzipped := func(s string) string {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write([]byte(s))
		_ = zw.Close()
		return base64.StdEncoding.EncodeToString(buf.Bytes())
	}
	for name, state := range map[string]string{
		"garbage":                        "not state",
		"an older version":               gzipped(`{"v":1}`),
		"a hand edit out of range":       gzipped(`{"v":2,"p":7}`),
		"a hand edit with broken arrays": gzipped(`{"v":2,"n":3,"lh":1,"ae":[1,2],"ay":[1]}`),
	} {
		t.Run(name, func(t *testing.T) {
			workload := forecastWorkload()
			workload.Status.Prediction = &v1alpha1.PredictionStatus{State: state}
			fr := newForecastReconciler(t, workload, nil)

			fr.reconcile(t, workload)

			restored, err := forecast.ImportEngine(workload.Status.Prediction.State, forecast.Settings{})
			require.NoError(t, err, "the bad state is replaced")
			assert.Zero(t, restored.GetDataPoints(), "the engine started afresh")
			assert.Contains(t, drainEvents(t, fr.Reconciler), "Warning "+ReasonForecastReset)
		})
	}
}

func TestEngineRegistry_AppliesSettingsEveryTime(t *testing.T) {
	engine := &stubForecaster{}
	reg := newEngineRegistry(func() forecaster { return engine })
	london, err := time.LoadLocation("Europe/London")
	require.NoError(t, err)

	_, err = reg.getOrCreate("uid", forecast.Settings{Threshold: 85}, "")
	require.NoError(t, err)
	_, err = reg.getOrCreate("uid", forecast.Settings{Threshold: 95, Location: london}, "")
	require.NoError(t, err)

	assert.Equal(t, forecast.Settings{Threshold: 95, Location: london}, engine.settings,
		"a changed spec.prediction.confidence applies to the engine already running")
}

func TestReconciler_AppliesTimezoneToTheForecast(t *testing.T) {
	workload := forecastWorkload()
	engine := &stubForecaster{phase: forecast.Observing}
	r := newAutomationReconciler(t, workload, engine, automationOpts{})
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	require.NoError(t, err)
	r.Timezone = tokyo
	r.engines.engines[workload.UID] = engine

	_, err = r.reconcileAutomation(context.Background(), workload, nil)
	require.NoError(t, err)

	assert.Equal(t, tokyo, engine.settings.Location)
	assert.Equal(t, 85, engine.settings.Threshold)
}

func TestConfidenceFor(t *testing.T) {
	tests := []struct {
		name string
		spec v1alpha1.PredictionSpec
		want int
	}{
		{name: "the default", want: 75},
		{name: "eager", spec: v1alpha1.PredictionSpec{Mode: v1alpha1.PredictionEager}, want: 60},
		{name: "balanced", spec: v1alpha1.PredictionSpec{Mode: v1alpha1.PredictionBalanced}, want: 75},
		{name: "cautious", spec: v1alpha1.PredictionSpec{Mode: v1alpha1.PredictionCautious}, want: 85},
		{name: "a confidence overrides the mode",
			spec: v1alpha1.PredictionSpec{Mode: v1alpha1.PredictionEager, Confidence: 90}, want: 90},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workload := forecastWorkload()
			workload.Spec.Prediction = tt.spec
			assert.Equal(t, tt.want, confidenceFor(workload))
		})
	}
}

func TestReconciler_AppliesTheModeToTheForecast(t *testing.T) {
	workload := forecastWorkload()
	workload.Spec.Prediction = v1alpha1.PredictionSpec{Mode: v1alpha1.PredictionEager}
	engine := &stubForecaster{phase: forecast.Observing}
	r := newAutomationReconciler(t, workload, engine, automationOpts{})
	r.engines.engines[workload.UID] = engine

	_, err := r.reconcileAutomation(context.Background(), workload, nil)
	require.NoError(t, err)

	assert.Equal(t, 60, engine.settings.Threshold)
	assert.Equal(t, 60, workload.Status.Prediction.Threshold)
}

// What the forecast waits for is in status, with when it's expected to
// start acting.
func TestPredictionStatus_Outlook(t *testing.T) {
	starts := fixedTime.Add(50 * time.Hour)
	workload := forecastWorkload()
	engine := &stubForecaster{phase: forecast.DailySuggesting, dataPoints: 40, outlook: forecast.Outlook{
		Waiting: forecast.WaitingForConfidence, HoursOfDaySeen: 24, Confidence: 62, Threshold: 85,
		StartsActing: starts}}
	r := newAutomationReconciler(t, workload, engine, automationOpts{})

	r.updatePredictionStatus(context.Background(), workload, engine)

	p := workload.Status.Prediction
	assert.False(t, p.Acting)
	assert.Equal(t, 85, p.Threshold)
	assert.Equal(t, ForecastEarningConfidence, p.Reason)
	assert.Equal(t, "learning: its forecasts of the last day were 62% right, and it acts from 85%; it hasn't seen "+
		"a whole week yet; expected to start acting around Mon 16 Mar 14:00 UTC, if the coming days repeat the last "+
		"week", p.Message)
	require.NotNil(t, p.StartsActing)
	assert.True(t, starts.Equal(p.StartsActing.Time))
}

// Projecting the outlook runs the engine on for up to four weeks, so it's
// done again only once the engine learns another hour or its threshold
// changes.
func TestPredictionStatus_OutlookIsProjectedOnceAnHour(t *testing.T) {
	workload := forecastWorkload()
	engine := &countingOutlook{stubForecaster: stubForecaster{phase: forecast.DailySuggesting, dataPoints: 40}}
	r := newAutomationReconciler(t, workload, &engine.stubForecaster, automationOpts{})

	for range 3 {
		r.updatePredictionStatus(context.Background(), workload, engine)
	}
	assert.Equal(t, 1, engine.outlooks)
	engine.dataPoints++
	r.updatePredictionStatus(context.Background(), workload, engine)
	workload.Spec.Prediction.Confidence = 60
	r.updatePredictionStatus(context.Background(), workload, engine)
	assert.Equal(t, 3, engine.outlooks)
}

type countingOutlook struct {
	stubForecaster
	outlooks int
}

func (c *countingOutlook) Outlook() forecast.Outlook {
	c.outlooks++
	return c.stubForecaster.Outlook()
}

func TestForecastOutlook(t *testing.T) {
	paused := func(w *v1alpha1.ManagedWorkload) { w.Status.Phase = v1alpha1.PhasePaused }
	routed := func(w *v1alpha1.ManagedWorkload) {
		paused(w)
		meta.SetStatusCondition(&w.Status.Conditions, metav1.Condition{Type: conditionWakeOnRequest,
			Status: metav1.ConditionTrue, Reason: "DoormanRouted"})
	}
	autoResume := func(w *v1alpha1.ManagedWorkload) {
		w.Spec.IdlePolicy = &v1alpha1.IdlePolicySpec{AutoResume: true}
	}
	starts := time.Date(2026, 3, 30, 16, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		outlook     forecast.Outlook
		workload    func(*v1alpha1.ManagedWorkload)
		wantReason  string
		wantMessage string
	}{
		{name: "acting with autoResume", outlook: forecast.Outlook{Acting: true, Confidence: 82, Threshold: 75},
			workload: autoResume, wantReason: ForecastActing,
			wantMessage: "acting: its forecasts of the last day were 82% right, and it acts from 75%, so it holds " +
				"off pauses before hours it expects to be busy and wakes the workload 15 minutes ahead of them"},
		{name: "acting without autoResume", outlook: forecast.Outlook{Acting: true, Confidence: 82, Threshold: 75},
			wantReason: ForecastActing,
			wantMessage: "acting: its forecasts of the last day were 82% right, and it acts from 75%, so it holds " +
				"off pauses before hours it expects to be busy; turn on autoResume to have it wake the workload " +
				"ahead of them too"},
		{name: "a new forecast", outlook: forecast.Outlook{Waiting: forecast.WaitingForDay, HoursOfDaySeen: 14},
			wantReason: ForecastObserving,
			wantMessage: "learning: it has seen 14 of the 24 hours of the day, and starts scoring its forecasts once " +
				"it has seen them all"},
		{name: "needs another weekend", outlook: forecast.Outlook{Waiting: forecast.WaitingForConfidence,
			WeekSeen: true, Confidence: 40, Threshold: 75, StartsActing: starts},
			wantReason: ForecastEarningConfidence,
			wantMessage: "learning: its forecasts of the last day were 40% right, and it acts from 75%; expected to " +
				"start acting around Mon 30 Mar 16:00 UTC, if the coming days repeat the last week"},
		{name: "too irregular to act", outlook: forecast.Outlook{Waiting: forecast.WaitingForConfidence,
			WeekSeen: true, Confidence: 30, Threshold: 85},
			wantReason: ForecastEarningConfidence,
			wantMessage: "learning: its forecasts of the last day were 30% right, and it acts from 85%; not expected " +
				"to start within four weeks if the coming days repeat the last week"},
		{name: "relearning", outlook: forecast.Outlook{Waiting: forecast.WaitingToRelearn, Confidence: 0,
			Threshold: 75, StartsActing: starts},
			wantReason: ForecastRelearning,
			wantMessage: "relearning: demand stopped matching what it had learned, so it's earning its confidence " +
				"again; its forecasts of the last day were 0% right, and it acts from 75%; expected to start acting " +
				"around Mon 30 Mar 16:00 UTC, if the coming days repeat the last week"},
		{name: "paused without the doorman", outlook: forecast.Outlook{Waiting: forecast.WaitingForDay},
			workload: paused, wantReason: ForecastNotLearning,
			wantMessage: "not learning: the workload is paused without wake on request, so a request can't wake it " +
				"and its demand can't be seen; the forecast learns while it's awake, and from every hour with wake " +
				"on request"},
		{name: "paused behind the doorman learns", outlook: forecast.Outlook{Waiting: forecast.WaitingForDay,
			HoursOfDaySeen: 20}, workload: routed, wantReason: ForecastObserving,
			wantMessage: "learning: it has seen 20 of the 24 hours of the day, and starts scoring its forecasts once " +
				"it has seen them all"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workload := forecastWorkload()
			workload.Spec.IdlePolicy = nil
			if tt.workload != nil {
				tt.workload(workload)
			}
			reason, message := forecastOutlook(workload, tt.outlook)
			assert.Equal(t, tt.wantReason, reason)
			assert.Equal(t, tt.wantMessage, message)
		})
	}
}

func TestEngineRegistry_ForgetsADeletedWorkload(t *testing.T) {
	workload := lifecycleWorkload("forgotten", v1alpha1.PhaseRunning)
	workload.UID = "uid-deleted"
	workload.Finalizers = []string{finalizerName}
	deleting := metav1.NewTime(fixedTime)
	workload.DeletionTimestamp = &deleting
	r := newTestReconciler(t, workload, &stubPauser{})
	old, err := r.engines.getOrCreate(workload.UID, forecast.Settings{}, "")
	require.NoError(t, err)

	_, err = r.Reconcile(context.Background(), reconcileFor("forgotten"))
	require.NoError(t, err)

	again, err := r.engines.getOrCreate(workload.UID, forecast.Settings{}, "")
	require.NoError(t, err)
	assert.NotSame(t, old, again, "a deleted workload's engine is dropped")
}

func TestForecast_RejectedObservationDoesNotBlockAutomation(t *testing.T) {
	workload := automationWorkload(v1alpha1.PhaseRunning)
	engine := &stubForecaster{phase: forecast.Observing, observeErr: forecast.ErrInvalidObservation}
	metrics := &stubMetrics{}
	r := newAutomationReconciler(t, workload, engine, automationOpts{metrics: metrics})

	lookEveryMinute(t, r, workload, metrics, fixedTime, fixedTime.Add(time.Hour), func(time.Time) float64 { return 10 })

	assert.Zero(t, engine.observeCalls)
	assert.NotNil(t, workload.Status.Prediction)
}

func TestForecast_ExportFailureKeepsTheLastSavedState(t *testing.T) {
	workload := automationWorkload(v1alpha1.PhaseRunning)
	workload.Status.Prediction = &v1alpha1.PredictionStatus{State: "saved"}
	engine := &stubForecaster{phase: forecast.DailyActive, exportErr: errors.New("boom")}
	r := newAutomationReconciler(t, workload, engine, automationOpts{})

	_, err := r.reconcileAutomation(context.Background(), workload, nil)
	require.NoError(t, err)

	assert.Equal(t, "saved", workload.Status.Prediction.State)
	assert.Equal(t, "Active", workload.Status.Prediction.DailyPhase)
}

// Status says which zone the forecast counts hours in, so kubectl hybernate
// status can show it without access to the operator's flags.
func TestPredictionStatus_NamesTheTimeZone(t *testing.T) {
	london, err := time.LoadLocation("Europe/London")
	require.NoError(t, err)
	tests := []struct {
		name string
		zone *time.Location
		want string
	}{
		{name: "the operator's --timezone", zone: london, want: "Europe/London"},
		{name: "the default", want: "UTC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workload := automationWorkload(v1alpha1.PhaseRunning)
			engine := &stubForecaster{phase: forecast.Observing}
			r := newAutomationReconciler(t, workload, engine, automationOpts{})
			r.Timezone = tt.zone

			r.updatePredictionStatus(context.Background(), workload, engine)

			assert.Equal(t, tt.want, workload.Status.Prediction.TimeZone)
		})
	}
}

// TestForecastState_IsEncodedOnlyOnceItChanges: encoding the state
// compresses all of it, and a reconcile publishes it every minute while it
// changes once an hour.
func TestForecastState_IsEncodedOnlyOnceItChanges(t *testing.T) {
	workload := automationWorkload(v1alpha1.PhaseRunning)
	engine := &stubForecaster{phase: forecast.DailyActive, dataPoints: 10, state: "learned"}
	r := newAutomationReconciler(t, workload, engine, automationOpts{})

	for range 3 {
		r.updatePredictionStatus(context.Background(), workload, engine)
	}
	assert.Equal(t, 1, engine.exportCalls)
	assert.Equal(t, "learned", workload.Status.Prediction.State)

	engine.dataPoints, engine.state = 11, "learned more"
	r.updatePredictionStatus(context.Background(), workload, engine)
	r.updatePredictionStatus(context.Background(), workload, engine)
	assert.Equal(t, 2, engine.exportCalls, "encoded again once an hour is observed")
	assert.Equal(t, "learned more", workload.Status.Prediction.State)

	restarted := &stubForecaster{phase: forecast.Observing, dataPoints: 11, state: "afresh"}
	r.updatePredictionStatus(context.Background(), workload, restarted)
	assert.Equal(t, "afresh", workload.Status.Prediction.State, "a new engine is never mistaken for the old one")
}

// BenchmarkUpdatePredictionStatus is a reconcile's publishing of a trained
// forecast: once a minute for every awake workload, though what it learned
// changes once an hour.
func BenchmarkUpdatePredictionStatus(b *testing.B) {
	engine := forecast.NewEngine(forecast.DefaultParams(), forecast.Settings{Threshold: 75})
	start := fixedTime.Truncate(time.Hour)
	for i := range 4 * forecast.WeeklySeason {
		at := start.Add(time.Duration(i) * time.Hour)
		if _, err := engine.Observe(float64(i%24), at); err != nil {
			b.Fatal(err)
		}
	}
	r := &Reconciler{engines: realEngineRegistry()}
	workload := forecastWorkload()
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		r.updatePredictionStatus(ctx, workload, engine)
	}
}
