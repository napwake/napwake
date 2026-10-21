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
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
	"github.com/okedeji/hybernate/internal/discovery"
	"github.com/okedeji/hybernate/internal/forecast"
)

// officePeaks are four weeks of an office-hours workload's hourly peaks,
// in cores, for pods requesting 100m: 40% from 09:00 to 17:00 on weekdays,
// 1% otherwise.
func officePeaks(end time.Time) map[int64]float64 {
	peaks := map[int64]float64{}
	for t := end.Add(-historyWindow); t.Before(end); t = t.Add(time.Hour) {
		peak := 0.001
		if t.Weekday() != time.Saturday && t.Weekday() != time.Sunday && t.Hour() >= 9 && t.Hour() < 17 {
			peak = 0.04
		}
		peaks[t.Unix()] = peak
	}
	return peaks
}

type stubHistory struct {
	peaks  map[int64]float64
	err    error
	asked  []discovery.WorkloadCPU
	ranges [][2]time.Time
}

func (s *stubHistory) HourlyPeaks(_ context.Context, w discovery.WorkloadCPU, start, end time.Time) (
	map[int64]float64, error) {
	s.asked = append(s.asked, w)
	s.ranges = append(s.ranges, [2]time.Time{start, end})
	return s.peaks, s.err
}

func historyTarget() client.Object {
	target := clockTarget("app:v1", nil)
	target.Spec.Template.Spec.InitContainers = []corev1.Container{
		{Name: "proxy", RestartPolicy: new(corev1.ContainerRestartPolicyAlways)},
		{Name: "migrate"},
	}
	return target
}

func newHistoryLearner(t *testing.T, history historyReader, metrics *stubMetrics, objs ...client.Object) (
	*historyLearner, *v1alpha1.ManagedWorkload) {
	t.Helper()
	workload := forecastWorkload()
	workload.Status.Prediction = nil
	c := fakeClientBuilder(testScheme(t)).WithStatusSubresource(&v1alpha1.ManagedWorkload{}).
		WithObjects(append([]client.Object{workload}, objs...)...).Build()
	return &historyLearner{client: c, history: history, metrics: metrics, engines: realEngineRegistry(),
		timezone: time.UTC, clock: func() time.Time { return fixedTime.Add(20 * time.Minute) }}, workload
}

func learnHistory(t *testing.T, l *historyLearner, workload *v1alpha1.ManagedWorkload) ctrl.Result {
	t.Helper()
	result, err := l.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(workload)})
	require.NoError(t, err)
	return result
}

// A new workload's history is read in one query of the last four weeks, up
// to the hour under way, of its own containers, native sidecars included,
// and learned as live: four weeks of office hours, and its forecast acts.
func TestHistoryLearner_LearnsFourWeeks(t *testing.T) {
	end := fixedTime
	history := &stubHistory{peaks: officePeaks(end)}
	l, workload := newHistoryLearner(t, history, &stubMetrics{cpuPerReplica: 100}, historyTarget())

	learnHistory(t, l, workload)

	require.Len(t, history.asked, 1)
	assert.Equal(t, discovery.WorkloadCPU{Namespace: "default", Kind: v1alpha1.TargetKindDeployment, Name: "api",
		Containers: []string{"app", "proxy"}}, history.asked[0])
	assert.Equal(t, [2]time.Time{end.Add(-historyWindow), end}, history.ranges[0])
	seed, first, ok := l.engines.adoptSeed(workload.UID, forecast.Settings{Threshold: 75})
	require.True(t, ok)
	assert.True(t, first)
	assert.Equal(t, HistoryLearned, seed.history.Reason)
	assert.Equal(t, 672, seed.history.Hours)
	assert.Equal(t, "the forecast learned from 4 weeks of the workload's CPU history in Prometheus",
		seed.history.Message)
	assert.True(t, end.Add(-historyWindow).Equal(seed.history.From.Time))
	assert.True(t, end.Equal(seed.history.To.Time))
	assert.GreaterOrEqual(t, seed.engine.GetPhase(), forecast.DailyActive, "acting on day one")

	learnHistory(t, l, workload)
	assert.Len(t, history.asked, 1, "read once")
}

func TestHistoryLearner_WithoutHistory(t *testing.T) {
	tests := []struct {
		name       string
		history    *stubHistory
		metrics    *stubMetrics
		wantReason string
	}{
		{name: "Prometheus has none", history: &stubHistory{peaks: map[int64]float64{}}, metrics: &stubMetrics{},
			wantReason: HistoryNone},
		{name: "no CPU requests to measure it against", history: &stubHistory{peaks: officePeaks(fixedTime)},
			metrics: &stubMetrics{noCPURequests: true}, wantReason: HistoryNoCPURequests},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, workload := newHistoryLearner(t, tt.history, tt.metrics, historyTarget())
			learnHistory(t, l, workload)
			seed, _, ok := l.engines.adoptSeed(workload.UID, forecast.Settings{})
			require.True(t, ok)
			assert.Equal(t, tt.wantReason, seed.history.Reason)
			assert.Nil(t, seed.engine)
		})
	}
}

// A Prometheus that fails is asked again a couple of times, minutes apart,
// before the forecast settles for learning live.
func TestHistoryLearner_RetriesAFailingPrometheus(t *testing.T) {
	history := &stubHistory{err: errors.New("connection refused")}
	l, workload := newHistoryLearner(t, history, &stubMetrics{}, historyTarget())

	for range historyAttempts - 1 {
		assert.Equal(t, historyRetry, learnHistory(t, l, workload).RequeueAfter)
		assert.False(t, l.engines.seeded(workload.UID))
	}
	assert.Zero(t, learnHistory(t, l, workload).RequeueAfter)
	seed, _, ok := l.engines.adoptSeed(workload.UID, forecast.Settings{})
	require.True(t, ok)
	assert.Equal(t, HistoryQueryFailed, seed.history.Reason)
	assert.Contains(t, seed.history.Message, "connection refused")
	assert.Len(t, history.asked, historyAttempts)
}

// A workload whose target doesn't exist yet is read once it does; one whose
// history has been read isn't read again.
func TestHistoryLearner_WaitsForTheTargetAndReadsOnce(t *testing.T) {
	history := &stubHistory{peaks: officePeaks(fixedTime)}
	l, workload := newHistoryLearner(t, history, &stubMetrics{})
	learnHistory(t, l, workload)
	assert.Empty(t, history.asked)
	assert.False(t, l.engines.seeded(workload.UID))

	read, _ := newHistoryLearner(t, history, &stubMetrics{}, historyTarget())
	require.NoError(t, read.client.Get(context.Background(), client.ObjectKeyFromObject(workload), workload))
	workload.Status.Prediction = &v1alpha1.PredictionStatus{History: &v1alpha1.PredictionHistory{Reason: HistoryNone}}
	require.NoError(t, read.client.Status().Update(context.Background(), workload))
	learnHistory(t, read, workload)
	assert.Empty(t, history.asked)
}

// The workload's reconcile takes up the learned forecast in place of the
// one learning live, says so once in an event, and records it in status.
func TestTakeUpHistory(t *testing.T) {
	workload := forecastWorkload()
	workload.Spec.IdlePolicy = &v1alpha1.IdlePolicySpec{AutoResume: true}
	fr := newForecastReconciler(t, workload, nil)
	recorder := events.NewFakeRecorder(20)
	fr.Recorder = recorder
	learned := forecast.NewEngine(forecast.DefaultParams(), forecast.Settings{})
	_, err := learned.Learn(historyHours(officePeaks(fixedTime), 100, fixedTime), 10)
	require.NoError(t, err)
	fr.history = &stubHistory{}
	from, to := metav1.NewTime(fixedTime.Add(-historyWindow)), metav1.NewTime(fixedTime)
	fr.engines.offerSeed(workload.UID, historySeed{engine: learned, history: v1alpha1.PredictionHistory{
		Reason: HistoryLearned, Hours: 672, From: &from, To: &to,
		Message: "the forecast learned from 4 weeks of the workload's CPU history in Prometheus"}})

	fr.reconcile(t, workload)
	fr.now = fr.now.Add(time.Minute)
	fr.reconcile(t, workload)

	p := workload.Status.Prediction
	require.NotNil(t, p.History)
	assert.Equal(t, HistoryLearned, p.History.Reason)
	assert.True(t, p.Acting)
	assert.Equal(t, v1alpha1.ForecastActing, p.Reason)
	assert.Contains(t, p.Message, "acting, on 4 weeks of CPU history and what it has seen since")
	restored, err := forecast.ImportEngine(p.State, forecast.Settings{})
	require.NoError(t, err)
	assert.Equal(t, learned.GetDataPoints(), restored.GetDataPoints(), "the learned forecast is the one saved")
	assert.Equal(t, 1, countEvents(recorder, ReasonForecastLearnedHistory), "announced once")
}

func TestTakeUpHistory_WithoutPrometheus(t *testing.T) {
	workload := forecastWorkload()
	fr := newForecastReconciler(t, workload, nil)

	fr.reconcile(t, workload)

	h := workload.Status.Prediction.History
	require.NotNil(t, h)
	assert.Equal(t, HistoryNoPrometheusURL, h.Reason)
	assert.Contains(t, h.Message, "--prometheus-url")
}

func TestTakeUpHistory_UnavailableIsAWarning(t *testing.T) {
	workload := forecastWorkload()
	fr := newForecastReconciler(t, workload, nil)
	recorder := events.NewFakeRecorder(20)
	fr.Recorder = recorder
	fr.history = &stubHistory{}
	fr.engines.offerSeed(workload.UID, historySeed{history: v1alpha1.PredictionHistory{Reason: HistoryQueryFailed,
		Message: "can't read the workload's CPU history from Prometheus, so the forecast learns from now: timeout"}})

	fr.reconcile(t, workload)

	assert.Equal(t, HistoryQueryFailed, workload.Status.Prediction.History.Reason)
	assert.Equal(t, 1, countEvents(recorder, ReasonForecastHistoryUnavailable))
}

func countEvents(recorder *events.FakeRecorder, reason string) int {
	n := 0
	for {
		select {
		case e := <-recorder.Events:
			if strings.HasPrefix(e, "Normal "+reason+" ") || strings.HasPrefix(e, "Warning "+reason+" ") {
				n++
			}
		default:
			return n
		}
	}
}

// End to end against a Prometheus API: the history learner's query, as the
// operator makes it with --prometheus-url, is read and learned, and the new
// workload's forecast acts from its first reconcile.
func TestWarmStart_FromAPrometheusAPI(t *testing.T) {
	peaks := officePeaks(fixedTime)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		start, _ := strconv.ParseInt(q.Get("start"), 10, 64)
		end, _ := strconv.ParseInt(q.Get("end"), 10, 64)
		var values [][2]any
		for at := start; at <= end; at += 3600 {
			if v, ok := peaks[at-3600]; ok {
				values = append(values, [2]any{float64(at), strconv.FormatFloat(v, 'f', -1, 64)})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{
			"resultType": "matrix", "result": []any{map[string]any{"metric": map[string]string{}, "values": values}}}})
	}))
	t.Cleanup(server.Close)
	prom, err := discovery.NewPrometheusURL(server.URL, server.Client(), nil)
	require.NoError(t, err)

	workload := forecastWorkload()
	workload.Status.Prediction = nil
	c := fakeClientBuilder(testScheme(t)).WithStatusSubresource(&v1alpha1.ManagedWorkload{}).
		WithObjects(workload, historyTarget()).Build()
	fr := newForecastReconciler(t, workload, c)
	fr.history = prom
	fr.metrics = &stubMetrics{cpuPerReplica: 100, cpuMillis: 2}
	l := &historyLearner{client: c, history: prom, metrics: fr.metrics, engines: fr.engines, timezone: time.UTC,
		clock: func() time.Time { return fr.now }}

	learnHistory(t, l, workload)
	fr.reconcile(t, workload)

	p := workload.Status.Prediction
	assert.Equal(t, HistoryLearned, p.History.Reason)
	assert.Equal(t, 672, p.History.Hours)
	assert.True(t, p.Acting, "%s", p.Message)
}

func TestHistoryHours(t *testing.T) {
	start := fixedTime
	peaks := map[int64]float64{start.Unix(): 0.05, start.Add(2 * time.Hour).Unix(): 0.02}
	hours := historyHours(peaks, 200, start.Add(4*time.Hour))
	require.Len(t, hours, 4, "from the first hour it ran up to the hour under way")
	assert.InDelta(t, 25, hours[0].Peak, 1e-9, "50m of 200m requested")
	assert.Zero(t, hours[1].Peak, "an hour without its pods is quiet")
	assert.InDelta(t, 10, hours[2].Peak, 1e-9)
	assert.Zero(t, hours[3].Peak)
	assert.Empty(t, historyHours(nil, 200, start))
}

func TestHistorySpan(t *testing.T) {
	for hours, want := range map[int]string{1: "1 hour", 20: "20 hours", 24: "1 day", 24 * 9: "9 days",
		24 * 14: "2 weeks", 672: "4 weeks", 600: "3 weeks"} {
		assert.Equal(t, want, historySpan(hours), "%d hours", hours)
	}
}
