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

package discovery

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
)

// officeCores is a pod requesting 100m busy at 40m on weekdays from 09:10
// to 17:30, and at 1m otherwise.
func officeCores(t time.Time) float64 {
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	since := t.Sub(day)
	if t.Weekday() != time.Saturday && t.Weekday() != time.Sunday &&
		since >= 9*time.Hour+10*time.Minute && since < 17*time.Hour+30*time.Minute {
		return 0.04
	}
	return 0.001
}

func forecastScan(t *testing.T, objs ...client.Object) Workload {
	t.Helper()
	window := 28 * 24 * time.Hour
	f := &fakePrometheus{from: scanTime.Add(-window), byNamespace: map[string][]series{testNamespace: {
		{pod: "api-7d9f8c6b5-abcde", container: "main", cores: officeCores},
	}}}
	opts := scanOptions(testNamespace)
	opts.History, opts.Window, opts.IdleAfter = newFakePrometheus(t, f), window, time.Hour
	opts.Forecast = &ForecastOptions{Location: time.UTC}
	runtimeObjs := deploymentWithRollout("api", testNamespace, 1, 60*24*time.Hour)
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithRuntimeObjects(runtimeObjs...).WithObjects(objs...).Build()

	report, err := NewScanner(c, c).ScanCluster(context.Background(), opts)
	require.NoError(t, err)
	return byName(report)["api"]
}

// Four weeks of office hours: opted in now, the forecast acts at once, and
// over the last two weeks, having learned the first two, it would have had
// the workload up ahead of people every weekday morning.
func TestScanCluster_ReplaysTheForecast(t *testing.T) {
	w := forecastScan(t)

	require.NotNil(t, w.Forecast)
	f := w.Forecast
	assert.Equal(t, 75, f.Threshold)
	assert.True(t, f.IfOptedIn.Acting)
	assert.Nil(t, f.Live)
	require.NotNil(t, f.Replay)
	assert.Equal(t, 10, f.Replay.UsedDays)
	assert.Equal(t, 10, f.Replay.Ready, "%+v", f.Replay)
	assert.Zero(t, f.Replay.Missed, "the forecast acting never sleeps through a morning")
	assert.Zero(t, f.Replay.WastedWakes)
	assert.Equal(t, 25*time.Minute, f.Replay.MedianLead, "woken at 08:45, people at 09:10")
	for _, d := range f.Replay.Days {
		if d.WokeAt != nil {
			assert.Equal(t, "08:45", d.WokeAt.Format("15:04"))
		}
	}
}

// A workload Hybernate manages is replayed with its own settings, and its
// forecast's own outlook is shown beside the replay.
func TestScanCluster_ReplaysAManagedWorkloadsForecast(t *testing.T) {
	starts := metav1.NewTime(scanTime.Add(48 * time.Hour))
	mw := &v1alpha1.ManagedWorkload{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: testNamespace},
		Spec: v1alpha1.ManagedWorkloadSpec{
			Target:     v1alpha1.WorkloadRef{Kind: v1alpha1.TargetKindDeployment, Name: "api"},
			Prediction: v1alpha1.PredictionSpec{Mode: v1alpha1.PredictionCautious},
		},
		Status: v1alpha1.ManagedWorkloadStatus{Phase: v1alpha1.PhaseRunning, Prediction: &v1alpha1.PredictionStatus{
			DailyConfidence: 62, Reason: "EarningConfidence", Message: "learning", StartsActing: &starts}},
	}

	w := forecastScan(t, mw)

	require.NotNil(t, w.Forecast)
	assert.Equal(t, 85, w.Forecast.Threshold, "its own mode's")
	require.NotNil(t, w.Forecast.Live)
	assert.Equal(t, "EarningConfidence", w.Forecast.Live.Waiting)
	assert.Equal(t, 62, w.Forecast.Live.Confidence)
	require.NotNil(t, w.Forecast.Live.StartsActing)
	assert.True(t, starts.Time.Equal(*w.Forecast.Live.StartsActing))
}
