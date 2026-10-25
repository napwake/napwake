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

package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

// The pattern a range query picks a workload's pods with matches what the
// scan's matchers do, old revisions' pods and names cut short included.
func TestPodNamePattern(t *testing.T) {
	long := longDeployment
	tests := []struct {
		name string
		kind v1alpha1.TargetKind
		of   string
		pod  string
		want bool
	}{
		{"a Deployment's pod", v1alpha1.TargetKindDeployment, "api", "api-7d9f8c6b5-x7k2p", true},
		{"an older revision's pod", v1alpha1.TargetKindDeployment, "api", "api-65bd9-x2k4p", true},
		{"a hash of only digits", v1alpha1.TargetKindDeployment, "api", "api-5967487594-x2k4p", true},
		{"a longer-named Deployment's pod", v1alpha1.TargetKindDeployment, "api", "api-gateway-7d9f8c6b5-x7k2p", false},
		{"a Job's pod", v1alpha1.TargetKindDeployment, "api", "api-migrate-x7k2p", false},
		{"a CronJob's pod", v1alpha1.TargetKindDeployment, "api", "api-29312345-x7k2p", false},
		{"a DaemonSet's pod", v1alpha1.TargetKindDeployment, "api", "api-x7k2p", false},
		{"a StatefulSet's pod", v1alpha1.TargetKindDeployment, "api", "api-0", false},
		{"a hash too long", v1alpha1.TargetKindDeployment, "api", "api-7d9f8c6b5bcd-x7k2p", false},
		{"a dot in the name", v1alpha1.TargetKindDeployment, "api.v2", "apixv2-7d9f8c6b5-x7k2p", false},
		{"a long name cut through its hash", v1alpha1.TargetKindDeployment, long,
			(long + "-7d9f8c6b5-")[:58] + "x7k2p", true},
		{"a long name's short hash, not cut", v1alpha1.TargetKindDeployment, long, long + "-65bd-x7k2p",
			len(long)+1+4+1 <= 58},
		{"a name cut before its hash", v1alpha1.TargetKindDeployment, strings.Repeat("n", 60),
			strings.Repeat("n", 58) + "x7k2p", true},
		{"a StatefulSet's ordinal", v1alpha1.TargetKindStatefulSet, "db", "db-12", true},
		{"a StatefulSet's first", v1alpha1.TargetKindStatefulSet, "db", "db-0", true},
		{"an ordinal with a leading zero", v1alpha1.TargetKindStatefulSet, "db", "db-01", false},
		{"another StatefulSet's pod", v1alpha1.TargetKindStatefulSet, "db", "db-replica-0", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pattern := regexp.MustCompile("^(?:" + podNamePattern(tt.kind, tt.of) + ")$")
			assert.Equal(t, tt.want, pattern.MatchString(tt.pod), "%s against %s", tt.pod, pattern)
		})
	}
}

func TestHourlyPeaks(t *testing.T) {
	start := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	end := start.Add(3 * time.Hour)
	var asked map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/prom/api/v1/query_range", r.URL.Path)
		asked = map[string]string{}
		for k := range r.URL.Query() {
			asked[k] = r.URL.Query().Get(k)
		}
		values := [][2]any{
			{float64(start.Add(time.Hour).Unix()), "0.25"},
			{float64(end.Unix()), "0.5"},
			{float64(end.Unix() - 1800), "NaN"},
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{
			"resultType": "matrix", "result": []any{map[string]any{"metric": map[string]string{}, "values": values}}}})
	}))
	t.Cleanup(server.Close)
	p, err := NewPrometheusURL(server.URL+"/prom", server.Client(), nil)
	require.NoError(t, err)
	p.Selector = Selector{`cluster="staging"`}

	peaks, err := p.HourlyPeaks(context.Background(), WorkloadCPU{Namespace: "shop", Kind: v1alpha1.TargetKindDeployment,
		Name: "api", Containers: []string{"api", "worker.v2"}}, start, end)

	require.NoError(t, err)
	assert.Equal(t, map[int64]float64{start.Unix(): 0.25, start.Add(2 * time.Hour).Unix(): 0.5}, peaks,
		"each point is the hour that ends at it")
	assert.Equal(t, strconv.FormatInt(start.Add(time.Hour).Unix(), 10), asked["start"])
	assert.Equal(t, strconv.FormatInt(end.Unix(), 10), asked["end"])
	assert.Equal(t, "3600", asked["step"], "one point an hour")
	query := asked["query"]
	for _, want := range []string{`namespace="shop"`, `container=~"api|worker\\.v2"`, `cluster="staging"`,
		`[300s]`, `[1h:300s]`, "max_over_time", `count(max by (pod)`} {
		assert.Contains(t, query, want)
	}
}

func TestHourlyPeaks_Failures(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"error","error":"overloaded"}`))
	}))
	t.Cleanup(failing.Close)
	p, err := NewPrometheusURL(failing.URL, failing.Client(), nil)
	require.NoError(t, err)
	w := WorkloadCPU{Namespace: "shop", Kind: v1alpha1.TargetKindStatefulSet, Name: "db", Containers: []string{"db"}}

	_, err = p.HourlyPeaks(context.Background(), w, time.Unix(0, 0), time.Unix(7200, 0))
	require.ErrorContains(t, err, "overloaded")

	w.Containers = nil
	_, err = p.HourlyPeaks(context.Background(), w, time.Unix(0, 0), time.Unix(7200, 0))
	require.Error(t, err)
}
