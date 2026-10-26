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
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/discovery"
	"github.com/napwake/napwake/internal/forecast"
)

// officePrometheus answers history queries with a pod requesting 100m busy
// at 40m on weekdays from 09:10 to 17:30 UTC, and at 1m otherwise.
func officePrometheus(t *testing.T) *httptest.Server {
	t.Helper()
	return officePrometheusSince(t, time.Time{})
}

// officePrometheusSince is officePrometheus with no history before since,
// as a Prometheus installed then keeps.
func officePrometheusSince(t *testing.T, since time.Time) *httptest.Server {
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
		first := start
		for first < since.Unix() {
			first += step
		}
		for at := first; at <= end; at += step {
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

// replayAt replays the forecast over the cluster objs as forecast -n
// preview-42 does, with the replay's clock at now, and returns the JSON it
// prints.
func replayAt(t *testing.T, now time.Time, promURL string, objs ...client.Object) forecastResult {
	t.Helper()
	writeKubeconfig(t, "https://127.0.0.1:1", "preview-42")
	c := newStatusClient(t, interceptor.Funcs{}, objs...)
	kube := addKubeFlags(pflag.NewFlagSet("forecast", pflag.ContinueOnError),
		func(*rest.Config) (client.Client, error) { return c, nil })
	prom, err := prometheusFrom(prometheusOptions{url: promURL})
	require.NoError(t, err)
	opts := forecastOptions{kube: kube, namespaces: namespaceFlags{names: []string{"preview-42"}}, window: "28d",
		cpuThreshold: defaultCPUThreshold, idleAfter: time.Hour, now: func() time.Time { return now }}

	result, _, err := replayCluster(context.Background(), forecastRun{window: 28 * 24 * time.Hour, prom: prom,
		mode: forecast.Balanced}, time.UTC, opts)
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, writeOutput(&out, outputJSON, result, nil))
	var decoded forecastResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &decoded))
	return decoded
}

func TestForecastJSON(t *testing.T) {
	sunday := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

	result := replayAt(t, sunday, officePrometheus(t).URL, forecastCluster()...)

	assert.Equal(t, "balanced", result.Setting)
	assert.Equal(t, "UTC", result.Timezone)
	require.Len(t, result.Workloads, 1)
	f := result.Workloads[0].Forecast
	require.NotNil(t, f)
	assert.Equal(t, 75, f.Threshold)
	assert.True(t, f.IfOptedIn.Acting, "four weeks of office hours, learned at once")
	require.NotNil(t, f.Replay)
	assert.Equal(t, time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC), f.Replay.From)
	assert.Equal(t, 10, f.Replay.UsedDays, "the two weeks from Sunday noon to Sunday noon have ten weekdays")
	assert.GreaterOrEqual(t, f.Replay.Ready, 7)
	assert.Zero(t, f.Replay.Missed)
	assert.LessOrEqual(t, f.Replay.WastedWakes, 1)
	assert.Equal(t, discovery.ReplayWakes{NoRequest: "no Service routes requests to it"}, f.Wakes,
		"what wakes it once paused, through the JSON")
}

func TestCLI_ForecastTable(t *testing.T) {
	prom := officePrometheus(t)
	out, err := runCLI(t, newStatusClient(t, interceptor.Funcs{}, forecastCluster()...), "forecast", "-n",
		"preview-42", "--prometheus-url", prom.URL, "--forecast", "eager")
	require.NoError(t, err, out)

	assert.Contains(t, out, "NAMESPACE   NAME  FORECAST           READY")
	assert.Contains(t, out, "preview-42  api   acts from day one")
	assert.Contains(t, out, ", 25m ahead  nothing\n", "no Service routes requests to it, so nothing wakes it")
	assert.Contains(t, out, "WAKES ON is what wakes\nit once it's paused")
	assert.Contains(t, out, "people alone, with wake: ask, whose wakes the forecast learns from")
	assert.Contains(t, out, "Name a workload to see it day by\nday, and for one Napwake manages, what its own "+
		"forecast is doing now.")
}

func TestCLI_ForecastOneWorkload(t *testing.T) {
	prom := officePrometheus(t)
	out, err := runCLI(t, newStatusClient(t, interceptor.Funcs{}, forecastCluster()...), "forecast", "api", "-n",
		"preview-42", "--prometheus-url", prom.URL, "--timezone", "Europe/London")
	require.NoError(t, err, out)

	assert.Contains(t, out, ": the forecast of preview-42/api, times in Europe/London\n")
	assert.Contains(t, out, "Replayed over its last 28 days of CPU history, from "+prom.URL+":\n"+
		"  Opted in today: acts from day one, at a confidence of 75%.")
	assert.NotContains(t, out, "Napwake's forecast", "Napwake doesn't manage it")
	assert.Contains(t, out, "DAY")
	assert.Contains(t, out, "FOUND IT")
	assert.Contains(t, out, "; while it's paused, nothing wakes it but a person, since no Service routes requests "+
		"to it, so the forecast doesn't learn the hours it's paused:\n")
	assert.Contains(t, out, "Ready on ")

	_, err = runCLI(t, newStatusClient(t, interceptor.Funcs{}, forecastCluster()...), "forecast", "web", "-n",
		"preview-42", "--prometheus-url", prom.URL)
	require.EqualError(t, err, "no Deployment or StatefulSet named web in preview-42; check the name and the namespace")
}

// The scan's note on the cluster's infrastructure is about what pausing
// could free; the forecast has no totals or next steps to leave it out of,
// so it doesn't say it, for the cluster or for one workload.
func TestCLI_ForecastLeavesOutTheInfrastructureNote(t *testing.T) {
	prom := officePrometheus(t)
	ingress := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "ingress-nginx-controller",
		Namespace: "preview-42", Labels: map[string]string{"app.kubernetes.io/name": "ingress-nginx"}},
		Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](1),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "ingress"}},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "ingress"}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "controller"}}}}}}
	objs := append(forecastCluster(), ingress)

	for _, args := range [][]string{{"forecast"}, {"forecast", "api"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, err := runCLI(t, newStatusClient(t, interceptor.Funcs{}, objs...),
				append(args, "-n", "preview-42", "--prometheus-url", prom.URL)...)

			require.NoError(t, err, out)
			assert.Contains(t, out, "preview-42")
			assert.NotContains(t, out, "infrastructure workload")
		})
	}
	scan, err := runCLI(t, newStatusClient(t, interceptor.Funcs{}, objs...), "scan", "-n", "preview-42",
		"--window", "0", "--open=false")
	require.NoError(t, err, scan)
	assert.Contains(t, scan, "1 infrastructure workload is left out of the totals and next steps", "scan keeps it")
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
		HistoryHours: 4, HistorySince: since, Timezone: "UTC", managed: "api -n orders"}, time.UTC))

	assert.Contains(t, out.String(), "4 hours of CPU history is too little to replay the forecast over: it needs "+
		"at least 2 days.\n  Run it again after Fri 23 Oct 02:00 UTC.\n"+
		"  To see what Napwake's own forecast of a workload it manages is doing, which needs no history, name it: "+
		"kubectl napwake forecast api -n orders\n")
	assert.NotContains(t, out.String(), "No workload has CPU history")
}

func TestCLI_ForecastNeedsHistory(t *testing.T) {
	_, err := runCLI(t, newStatusClient(t, interceptor.Funcs{}, forecastCluster()...), "forecast", "-n", "preview-42")
	require.ErrorContains(t, err, "the forecast is replayed over CPU history, and there's none to read")
	require.ErrorContains(t, err, "no Prometheus found in the cluster")
}

// --timezone's help shows no default of its own, as the zone is the
// operator's unless it's given, and UTC only when that can't be read.
func TestForecastHelp_TimezoneHasNoFalseDefault(t *testing.T) {
	out, err := runCLI(t, nil, "forecast", "--help")
	require.NoError(t, err)

	i := strings.Index(out, "--timezone string")
	require.GreaterOrEqual(t, i, 0)
	line, _, _ := strings.Cut(out[i:], "\n")
	assert.Contains(t, line, "by default the operator's, as its ManagedWorkloads record it, or UTC")
	assert.NotContains(t, line, "(default")
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
		{"managed, too irregular to act within four weeks", forecastWorkload{Managed: true,
			Forecast: &discovery.ForecastReplay{Live: &discovery.ForecastOutlook{
				Waiting: v1alpha1.ForecastEarningConfidence}}}, "not within 4 weeks"},
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

// What wakes a paused workload in the replay, as the table and the named
// workload's replay say it.
func TestReplayWakes_Shown(t *testing.T) {
	tests := []struct {
		wakes          discovery.ReplayWakes
		cell, sentence string
	}{
		{wakes: discovery.ReplayWakes{Request: true, Query: true}, cell: "request, query",
			sentence: "a request through the doorman, or its Prometheus activity query, wakes it"},
		{wakes: discovery.ReplayWakes{Request: true}, cell: "request", sentence: "a request through the doorman wakes it"},
		{wakes: discovery.ReplayWakes{Query: true, NoRequest: "no Service routes requests to it"}, cell: "query",
			sentence: "its Prometheus activity query wakes it, since no Service routes requests to it"},
		{wakes: discovery.ReplayWakes{NoRequest: "the doorman is turned off for the cluster"}, cell: "nothing",
			sentence: "nothing wakes it but a person, since the doorman is turned off for the cluster, so the " +
				"forecast doesn't learn the hours it's paused"},
		{wakes: discovery.ReplayWakes{People: true, NoRequest: "the doorman asks before waking it"}, cell: "people",
			sentence: "only people wake it, with wake: ask, so the forecast learns the hours it's paused from " +
				"whether they do"},
		{wakes: discovery.ReplayWakes{People: true, Query: true, NoRequest: "the doorman asks before waking it"},
			cell: "people, query", sentence: "people, or its Prometheus activity query, wake it, with wake: ask"},
	}
	for _, tt := range tests {
		t.Run(tt.cell, func(t *testing.T) {
			assert.Equal(t, tt.cell, replayWakesCell(tt.wakes))
			assert.Equal(t, tt.sentence, wakesSentence(tt.wakes))
		})
	}
}
