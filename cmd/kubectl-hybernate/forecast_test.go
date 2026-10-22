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
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
	"github.com/okedeji/hybernate/internal/discovery"
)

// officePrometheus answers history queries with a pod requesting 100m busy
// at 40m on weekdays from 09:10 to 17:30 UTC, and at 1m otherwise.
func officePrometheus(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api/v1/query") {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
			return
		}
		q := r.URL.Query()
		start, _ := strconv.ParseInt(q.Get("start"), 10, 64)
		end, _ := strconv.ParseInt(q.Get("end"), 10, 64)
		step, _ := strconv.ParseInt(q.Get("step"), 10, 64)
		var values [][2]any
		for at := start; at <= end; at += step {
			cores := 0.001
			t := time.Unix(at, 0).UTC()
			since := t.Sub(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC))
			if t.Weekday() != time.Saturday && t.Weekday() != time.Sunday &&
				since >= 9*time.Hour+10*time.Minute && since < 17*time.Hour+30*time.Minute {
				cores = 0.04
			}
			values = append(values, [2]any{float64(at), strconv.FormatFloat(cores, 'f', -1, 64)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{
			"resultType": "matrix", "result": []any{map[string]any{
				"metric": map[string]string{"pod": "api-7d9f8c6b5-x7k2p", "container": "main"}, "values": values}}}})
	}))
	t.Cleanup(server.Close)
	return server
}

func forecastCluster() []client.Object {
	return []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "preview-42"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "preview-42"},
			Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](1),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api"}},
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main",
						Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("100m")}}}}}}}},
	}
}

func TestCLI_ForecastJSON(t *testing.T) {
	prom := officePrometheus(t)
	out, err := runCLI(t, newStatusClient(t, interceptor.Funcs{}, forecastCluster()...), "forecast", "-n",
		"preview-42", "-o", "json", "--prometheus-url", prom.URL)
	require.NoError(t, err, out)

	var result forecastResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	assert.Equal(t, "balanced", result.Setting)
	assert.Equal(t, "UTC", result.Timezone)
	require.Len(t, result.Workloads, 1)
	f := result.Workloads[0].Forecast
	require.NotNil(t, f)
	assert.Equal(t, 75, f.Threshold)
	assert.True(t, f.IfOptedIn.Acting, "four weeks of office hours, learned at once")
	require.NotNil(t, f.Replay)
	assert.Equal(t, 10, f.Replay.UsedDays, "two weeks replayed")
	assert.GreaterOrEqual(t, f.Replay.Ready, 7)
	assert.Zero(t, f.Replay.Missed)
	assert.LessOrEqual(t, f.Replay.WastedWakes, 1)
}

func TestCLI_ForecastTable(t *testing.T) {
	prom := officePrometheus(t)
	out, err := runCLI(t, newStatusClient(t, interceptor.Funcs{}, forecastCluster()...), "forecast", "-n",
		"preview-42", "--prometheus-url", prom.URL, "--forecast", "eager")
	require.NoError(t, err, out)

	assert.Contains(t, out, "NAMESPACE   NAME  FORECAST           READY")
	assert.Contains(t, out, "preview-42  api   acts from day one")
	assert.Contains(t, out, ", 25m ahead")
	assert.Contains(t, out, "Name a workload to see\nit day by day.")
}

func TestCLI_ForecastOneWorkload(t *testing.T) {
	prom := officePrometheus(t)
	out, err := runCLI(t, newStatusClient(t, interceptor.Funcs{}, forecastCluster()...), "forecast", "api", "-n",
		"preview-42", "--prometheus-url", prom.URL, "--timezone", "Europe/London")
	require.NoError(t, err, out)

	assert.Contains(t, out, "preview-42/api: acts from day one, at a confidence of 75%.")
	assert.Contains(t, out, "DAY")
	assert.Contains(t, out, "FOUND IT")
	assert.Contains(t, out, "times in Europe/London")
	assert.Contains(t, out, "Ready on ")

	_, err = runCLI(t, newStatusClient(t, interceptor.Funcs{}, forecastCluster()...), "forecast", "web", "-n",
		"preview-42", "--prometheus-url", prom.URL)
	require.ErrorContains(t, err, "no workload web with CPU history")
}

// The forecast counts hours in the operator's time zone, which its
// ManagedWorkloads record, so the replay uses it unless told otherwise.
func TestCLI_ForecastInTheOperatorsTimeZone(t *testing.T) {
	prom := officePrometheus(t)
	managed := &v1alpha1.ManagedWorkload{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "preview-42"},
		Spec:   v1alpha1.ManagedWorkloadSpec{Target: v1alpha1.WorkloadRef{Kind: v1alpha1.TargetKindDeployment, Name: "web"}},
		Status: v1alpha1.ManagedWorkloadStatus{Prediction: &v1alpha1.PredictionStatus{TimeZone: "Africa/Lagos"}}}
	objs := append(forecastCluster(), managed)

	out, err := runCLI(t, newStatusClient(t, interceptor.Funcs{}, objs...), "forecast", "-n", "preview-42",
		"-o", "json", "--prometheus-url", prom.URL)
	require.NoError(t, err, out)
	var result forecastResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	assert.Equal(t, "Africa/Lagos", result.Timezone)

	out, err = runCLI(t, newStatusClient(t, interceptor.Funcs{}, objs...), "forecast", "-n", "preview-42",
		"-o", "json", "--prometheus-url", prom.URL, "--timezone", "Europe/London")
	require.NoError(t, err, out)
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	assert.Equal(t, "Europe/London", result.Timezone, "the flag wins")
}

// A cluster a few hours old has too little history to replay, which is
// said as such, with when there will be enough, rather than as no workload
// having any.
func TestWriteForecast_TooLittleHistory(t *testing.T) {
	since := time.Date(2026, 10, 21, 2, 0, 0, 0, time.UTC)
	var out strings.Builder

	require.NoError(t, writeForecast(&out, forecastResult{Cluster: "prelaunch", Prometheus: "monitoring/prometheus",
		HistoryHours: 4, HistorySince: since, Timezone: "UTC"}, false, time.UTC))

	assert.Contains(t, out.String(), "4 hours of CPU history is too little to replay the forecast over: it needs "+
		"at least 2 days.\n  Run it again after Fri 23 Oct 02:00 UTC.")
	assert.NotContains(t, out.String(), "No workload has CPU history")
}

func TestCLI_ForecastNeedsHistory(t *testing.T) {
	_, err := runCLI(t, newStatusClient(t, interceptor.Funcs{}, forecastCluster()...), "forecast", "-n", "preview-42")
	require.ErrorContains(t, err, "the forecast is replayed over CPU history, and there's none to read")
	require.ErrorContains(t, err, "no Prometheus found in the cluster")
}

func TestCLI_ForecastRejectsBadFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--window", "60d"},
		{"--window", "1d"},
		{"--forecast", "keen"},
		{"--timezone", "Mars/Olympus"},
		{"--cpu-threshold", "0"},
		{"--idle-after", "0s"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, err := runCLI(t, nil, append([]string{"forecast"}, args...)...)
			require.Error(t, err)
		})
	}
}

func TestPickWorkload(t *testing.T) {
	workloads := []forecastWorkload{
		{Namespace: "a", Kind: v1alpha1.TargetKindDeployment, Name: "api"},
		{Namespace: "b", Kind: v1alpha1.TargetKindDeployment, Name: "api"},
		{Namespace: "a", Kind: v1alpha1.TargetKindStatefulSet, Name: "db"},
	}
	got, err := pickWorkload(workloads, "statefulset/db")
	require.NoError(t, err)
	assert.Equal(t, "db", got[0].Name)
	_, err = pickWorkload(workloads, "api")
	require.ErrorContains(t, err, "api is a/deployment/api and b/deployment/api")
	_, err = pickWorkload(workloads, "deployment/db")
	require.Error(t, err)
}

func TestSpanOf(t *testing.T) {
	assert.Equal(t, "28 days", spanOf(672.98))
	assert.Equal(t, "2 days", spanOf(48))
	assert.Equal(t, "36 hours", spanOf(36.4))
	assert.Equal(t, "5 hours", spanOf(4.6), "rounded, as the scan's notes say it")
	assert.Equal(t, "1 hour", spanOf(1))
}

func TestForecastNow(t *testing.T) {
	starts := time.Date(2026, 3, 30, 16, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		w    forecastWorkload
		want string
	}{
		{"acts from day one", forecastWorkload{Forecast: &discovery.ForecastReplay{
			IfOptedIn: discovery.ForecastOutlook{Acting: true}}}, "acts from day one"},
		{"projected", forecastWorkload{Forecast: &discovery.ForecastReplay{
			IfOptedIn: discovery.ForecastOutlook{StartsActing: &starts}}}, "acts from Mon 30 Mar"},
		{"too irregular", forecastWorkload{Forecast: &discovery.ForecastReplay{
			IfOptedIn: discovery.ForecastOutlook{Waiting: "Confidence"}}}, "not within 4 weeks"},
		{"managed and acting", forecastWorkload{Managed: true, Forecast: &discovery.ForecastReplay{
			Live: &discovery.ForecastOutlook{Acting: true}}}, "acting"},
		{"managed and learning the day", forecastWorkload{Managed: true, Forecast: &discovery.ForecastReplay{
			IfOptedIn: discovery.ForecastOutlook{Acting: true}, Live: &discovery.ForecastOutlook{Waiting: "Observing"}}},
			"learning"},
		{"managed, paused with no Service", forecastWorkload{Managed: true, Forecast: &discovery.ForecastReplay{
			Live: &discovery.ForecastOutlook{Waiting: v1alpha1.ForecastNotLearning,
				Message: "not learning while paused: no Service routes requests to it; a request can't wake it, so " +
					"its demand can't be seen"}}},
			"not learning while paused: no Service routes requests to it"},
		{"managed and not learning, by an older operator", forecastWorkload{Managed: true,
			Forecast: &discovery.ForecastReplay{Live: &discovery.ForecastOutlook{Waiting: v1alpha1.ForecastNotLearning}}},
			"not learning"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, forecastNow(tt.w, time.UTC))
		})
	}
}

// status's FORECAST column says why a forecast isn't learning, as the
// operator's message leads with it.
func TestForecastCell_SaysWhyItIsntLearning(t *testing.T) {
	wl := statusWorkload{Forecast: v1alpha1.ForecastNotLearning,
		forecastMessage: "not learning while paused: no Service routes requests to it; a request can't wake it, so " +
			"its demand can't be seen"}
	assert.Equal(t, "not learning while paused: no Service routes requests to it", forecastCell(wl))
}
