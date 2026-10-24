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
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/controller"
)

// What woke a workload is read from the latest wake's event, as the
// operator and the doorman write them, and holds until a pause.
func TestStatus_WokenBy(t *testing.T) {
	tests := []struct {
		name       string
		phase      v1alpha1.WorkloadPhase
		events     []*eventAt
		want       string
		wantListed []string
	}{
		{name: "a request the doorman held", events: []*eventAt{
			{"WokenByRequest", "request on Service web, waking", 2 * time.Minute},
			{"WokeByActivity", "web: a request is waiting, waking", time.Minute},
		}, want: "request", wantListed: []string{"request"}},
		{name: "a person's click, on a workload that asks", events: []*eventAt{
			{"WokenByRequest", "a person asked on Service web, waking", time.Minute},
		}, want: "request", wantListed: []string{"request"}},
		{name: "a request, without the doorman's own event", events: []*eventAt{
			{"WokeByActivity", "web: a request is waiting, waking", time.Minute},
		}, want: "request"},
		{name: "an activity annotation", events: []*eventAt{
			{"WokeByActivity", "web: activity annotation is newer than the pause, waking", time.Minute},
		}, want: "activity", wantListed: []string{"activity"}},
		{name: "a Prometheus activity query", events: []*eventAt{
			{"WokeByActivity", "web: a Prometheus activity query shows activity, waking", time.Minute},
		}, want: "activity", wantListed: []string{"activity"}},
		{name: "a wake request", events: []*eventAt{
			{"WakeRequested", "web: wake requested, waking", time.Minute},
		}, want: "wakeRequest", wantListed: []string{"wakeRequest"}},
		{name: "the forecast", events: []*eventAt{
			{"AutoResume", "web: forecast expects 40% utilization this hour (threshold 10%), waking ahead of demand",
				time.Minute},
		}, want: "forecast", wantListed: []string{"forecast"}},
		{name: "a scale-up outside Napwake", events: []*eventAt{
			{"ScaledUp", "web: scaled up to 2 replicas by argocd-controller outside Napwake; counted as activity",
				time.Minute},
		}, want: "outside", wantListed: []string{"outside"}},
		{name: "a pause called off", events: []*eventAt{
			{"PauseCancelled", "web: a request came as it paused; the pause is cancelled", time.Minute},
		}, want: "pauseCancelled", wantListed: []string{"pauseCancelled"}},
		{name: "the resume after a cause adds nothing", events: []*eventAt{
			{"AutoResume", "web: forecast expects 40% utilization", 2 * time.Minute},
			{"Resumed", "web: resumed", time.Minute},
		}, want: "forecast", wantListed: []string{"forecast"}},
		{name: "a resume with no cause", events: []*eventAt{
			{"AutoResume", "web: forecast expects 40% utilization", time.Hour},
			{"Resumed", "web: resumed", 59 * time.Minute},
			{"Paused", "web: paused", 30 * time.Minute},
			{"Resumed", "web: resumed", time.Minute},
		}, want: "", wantListed: []string{"", "forecast"}},
		{name: "a pause since", phase: v1alpha1.PhasePaused, events: []*eventAt{
			{"WokenByRequest", "request on Service web, waking", time.Hour},
			{"Paused", "web: paused", time.Minute},
		}, want: "", wantListed: []string{"request"}},
		{name: "a wake before --since", events: []*eventAt{
			{"Paused", "web: paused", 31 * time.Hour},
			{"AutoResume", "web: forecast expects 40% utilization", 30 * time.Hour},
		}, want: "forecast"},
		{name: "dry-run", events: []*eventAt{
			{"WokeByActivity", "[dry-run] web: activity annotation is newer than the pause, waking", time.Minute},
		}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := statusWorkloadObj("shop", "web", v1alpha1.TargetKindDeployment, cmp.Or(tt.phase, v1alpha1.PhaseRunning),
				time.Minute)
			objs := make([]client.Object, 0, 1+len(tt.events))
			objs = append(objs, w)
			for _, e := range tt.events {
				objs = append(objs, statusEvent("shop", "web", e.reason, e.message, e.ago))
			}

			result, err := clusterStatus(context.Background(), newStatusClient(t, interceptor.Funcs{}, objs...),
				"shop", statusOpts())

			require.NoError(t, err)
			assert.Equal(t, tt.want, result.Workloads[0].WokenBy)
			var listed []string
			for _, h := range result.Recent {
				if h.Kind == happeningWake {
					listed = append(listed, h.WokenBy)
				}
			}
			assert.Equal(t, tt.wantListed, listed, "each wake listed, with what woke it")
		})
	}
}

type eventAt struct {
	reason, message string
	ago             time.Duration
}

// Events outlive their ManagedWorkload, so what woke one deleted and made
// again under the same name, as a preview environment's is on each push,
// says nothing of what woke the new one. The recent list still names the
// old one's wake as it was.
func TestStatus_WokenByIsTheManagedWorkloadsOwn(t *testing.T) {
	w := statusWorkloadObj("shop", "web", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning, 10*time.Minute)
	w.UID = "web-recreated"
	old := []*corev1.Event{
		statusEvent("shop", "web", "WokenByRequest", "request on Service web, waking", 2*time.Hour+time.Second),
		statusEvent("shop", "web", "WokeByActivity", "web: a request is waiting, waking", 2*time.Hour),
	}
	objs := make([]client.Object, 0, 1+len(old))
	objs = append(objs, w)
	for _, ev := range old {
		ev.InvolvedObject.UID = "web-deleted"
		objs = append(objs, ev)
	}

	result, err := clusterStatus(context.Background(), newStatusClient(t, interceptor.Funcs{}, objs...), "shop",
		statusOpts())

	require.NoError(t, err)
	assert.Empty(t, result.Workloads[0].WokenBy, "the request woke the ManagedWorkload since deleted")
	var listed []string
	for _, h := range result.Recent {
		if h.Kind == happeningWake {
			listed = append(listed, h.Workload+" "+h.WokenBy)
		}
	}
	assert.Equal(t, []string{"web request"}, listed, "the old one's wake is still listed")
}

// -o json gives what woke each workload, and each recent event's kind and
// what woke it, from the same events as the table.
func TestStatus_JSONSaysWhatWokeEach(t *testing.T) {
	result, err := clusterStatus(context.Background(), newStatusClient(t, interceptor.Funcs{}, statusCluster()...),
		"preview-42", statusOpts())
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, writeStatus(&out, result, "json", statusNow))

	var got struct {
		Workloads []struct {
			Target  string `json:"target"`
			WokenBy string `json:"wokenBy"`
		} `json:"workloads"`
		Recent []struct {
			Workload string `json:"workload"`
			Kind     string `json:"kind"`
			WokenBy  string `json:"wokenBy"`
		} `json:"recent"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	woken := map[string]string{}
	for _, w := range got.Workloads {
		woken[w.Target] = w.WokenBy
	}
	assert.Equal(t, map[string]string{"deployment/api": "", "statefulset/postgres": "", "deployment/web": "",
		"deployment/worker": "outside"}, woken, "api is paused; worker was scaled up by Argo CD")
	recent := make([]string, 0, len(got.Recent))
	for _, r := range got.Recent {
		recent = append(recent, r.Workload+" "+r.Kind+" "+r.WokenBy)
	}
	assert.Equal(t, []string{"worker wake outside", "api pause ", "api wake request"}, recent)
}

// What status reads a wake's cause from is the reason of the operator's
// own events, so a reason renamed there is a test that fails here.
func TestWakeCauses_AreTheOperatorsReasons(t *testing.T) {
	operators := []string{controller.ReasonWokeByActivity, controller.ReasonAutoResume, controller.ReasonScaledUp,
		controller.ReasonWakeRequested, controller.ReasonPauseCancelled}
	for _, reason := range operators {
		assert.Contains(t, wakeCauses, reason)
		assert.True(t, happenings[reason], reason)
	}
	doormans := slices.DeleteFunc(slices.Collect(maps.Keys(wakeCauses)), func(r string) bool {
		return slices.Contains(operators, r)
	})
	assert.Equal(t, []string{"WokenByRequest"}, doormans, "the doorman's own, which it writes on a request")
	assert.True(t, happenings[controller.ReasonPaused])
	assert.True(t, happenings[controller.ReasonResumed])
}
