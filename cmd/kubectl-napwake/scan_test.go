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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/discovery"
	"github.com/napwake/napwake/internal/nodescale"
)

func sampleCluster() scanResult {
	workloads := []discovery.Workload{
		{Namespace: "preview-42", Kind: v1alpha1.TargetKindDeployment, Name: "checkout-api", Replicas: 2,
			State: discovery.StateIdle, CPUPercent: ptr.To(2), Reason: "CPU 2% of its request",
			MonthlyCost: 1240.4, HourlyCost: 1.6992,
			PodCPURequestMillis: 500, PodMemoryRequestBytes: 1 << 30, Clues: []string{"last deployed 23 days ago"}},
		{Namespace: "preview-42", Kind: v1alpha1.TargetKindStatefulSet, Name: "postgres", Replicas: 1,
			State: discovery.StateActive, CPUPercent: ptr.To(64), MonthlyCost: 96, HourlyCost: 0.1315},
	}
	return scanResult{Context: "staging", Cluster: "staging", ClusterReport: &discovery.ClusterReport{
		Mode: discovery.ModeSnapshot, Namespaces: 1, Workloads: workloads,
		Notes: []string{"skipped namespace locked: forbidden"},
		Totals: discovery.Totals{Workloads: 2, Idle: 1, IdleCPUMillis: 1000, IdleMemoryBytes: 2 << 30,
			MonthlyCost: 1336.4, IdleMonthlyCost: 1240.4, IdleHourlyCost: 1.6992},
	}}
}

func TestWriteTable(t *testing.T) {
	cluster := sampleCluster()
	result := cluster
	var out bytes.Buffer

	require.NoError(t, writeScan(&out, result, scanOptions{output: "table", limit: 25}))

	got := out.String()
	assert.Contains(t, got, "staging: 2 workloads in 1 namespace")
	assert.Contains(t, got, "CPU 2% of its request; last deployed 23 days ago", "why it's idle, then the facts")
	assert.Contains(t, got, "1 workload is idle right now, reserving 1.0 vCPU and 2.0 GiB of memory.")
	assert.Contains(t, got, "It costs $1,240/month while running, $1.70 an hour: each hour asleep frees that.")
	assert.Contains(t, got, "$1.70", "cost per hour in the table")
	assert.NotContains(t, got, "save up to", "a moment can't show savings")
	assert.Contains(t, got, "depends on how often they'd be woken")
	assert.Contains(t, got, "deployment/checkout-api")
	assert.Contains(t, got, "last deployed 23 days ago")
	assert.Contains(t, got, "statefulset/postgres")
	assert.Contains(t, got, "  - skipped namespace locked: forbidden")
	assert.Contains(t, got, "kubectl annotate deployment checkout-api -n preview-42 napwake.dev/dry-run=true\n"+
		"       kubectl label deployment checkout-api -n preview-42 napwake.dev/managed=true\n",
		"next steps name a real idle workload, putting it in dry-run before opting it in, which alone would "+
			"have it paused")
	assert.NotContains(t, got, "Napwake Hub", "it has no page to send anyone to yet")
}

// The first line counts the namespaces the workloads listed are in, not
// those holding only infrastructure, which the notes count apart.
func TestWriteTable_CountsTheWorkloadsNamespaces(t *testing.T) {
	cluster := sampleCluster()
	cluster.Namespaces = 15
	cluster.Workloads = append(cluster.Workloads, discovery.Workload{Namespace: "orders", Name: "api",
		Kind: v1alpha1.TargetKindDeployment, State: discovery.StateActive})
	cluster.Totals.Workloads = 3
	cluster.Platform = []discovery.Workload{{Namespace: "argocd", Name: "argocd-server", Platform: "argocd"}}
	var out bytes.Buffer

	require.NoError(t, writeScan(&out, cluster, scanOptions{output: "table"}))

	assert.Contains(t, out.String(), "staging: 3 workloads in 2 namespaces\n")
}

func TestWriteTable_OnlyInfrastructure(t *testing.T) {
	report := &discovery.ClusterReport{Mode: discovery.ModeSnapshot, Namespaces: 2, Workloads: []discovery.Workload{},
		Platform: []discovery.Workload{{Namespace: "argocd", Name: "argocd-server", Platform: "argocd"}},
		Notes:    []string{"1 infrastructure workload is left out of the totals and next steps: argocd (1)"}}
	var out bytes.Buffer

	require.NoError(t, writeScan(&out, scanResult{Cluster: "staging", ClusterReport: report},
		scanOptions{output: "table"}))

	assert.Contains(t, out.String(), "staging: 2 namespaces scanned\n")
	assert.Contains(t, out.String(), "Only the cluster's own infrastructure was found")
}

func TestWriteTable_Limit(t *testing.T) {
	result := sampleCluster()
	var out bytes.Buffer

	require.NoError(t, writeScan(&out, result, scanOptions{output: "table", limit: 1}))

	assert.NotContains(t, out.String(), "statefulset/postgres")
	assert.Contains(t, out.String(), "...and 1 more; --limit 0 lists them all")
}

func TestWriteTable_NothingIdle(t *testing.T) {
	cluster := sampleCluster()
	cluster.Totals = discovery.Totals{Workloads: 2}
	cluster.Workloads[0].State = discovery.StateActive
	var out bytes.Buffer

	require.NoError(t, writeScan(&out, cluster, scanOptions{output: "table"}))

	assert.Contains(t, out.String(), "No running workloads are idle right now.")
	assert.NotContains(t, out.String(), "Next steps:")
}

func TestWriteScan_JSON(t *testing.T) {
	cluster := sampleCluster()
	var out bytes.Buffer

	require.NoError(t, writeScan(&out, cluster,
		scanOptions{output: "json"}))

	var decoded struct {
		Context   string `json:"context"`
		Mode      string `json:"mode"`
		Workloads []struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"workloads"`
		Totals struct {
			IdleMonthlyCost float64 `json:"idleMonthlyCost"`
		} `json:"totals"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &decoded))
	assert.Equal(t, "staging", decoded.Context)
	assert.Equal(t, "snapshot", decoded.Mode)
	assert.Equal(t, "checkout-api", decoded.Workloads[0].Name)
	assert.Equal(t, "idle", decoded.Workloads[0].State)
	assert.InDelta(t, 1240.4, decoded.Totals.IdleMonthlyCost, 0.001)
	assert.NotContains(t, out.String(), "potentialSavings")
	assert.NotContains(t, out.String(), "Napwake Hub", "machine-readable output carries no promotion")
}

func TestCents(t *testing.T) {
	tests := map[float64]string{0: "$0.00", 0.0036: "<$0.01", 0.005: "$0.01", 1.6992: "$1.70"}
	for in, want := range tests {
		assert.Equal(t, want, cents(in))
	}
}

func TestDollars(t *testing.T) {
	tests := map[float64]string{0: "$0", 12.4: "$12", 999.5: "$1,000", 1240.4: "$1,240", 1234567.8: "$1,234,568",
		-123: "-$123", -1234.4: "-$1,234", -999999.6: "-$1,000,000", -0.2: "$0",
		0.03: "<$1", 0.49: "<$1", 0.5: "$1"}
	for in, want := range tests {
		assert.Equal(t, want, dollars(in))
	}
}

func TestHoursTotal(t *testing.T) {
	assert.Equal(t, "0 hours", hoursTotal(0.3), "hours aren't money")
	assert.Equal(t, "6,120 hours", hoursTotal(6120.4))
}

func TestShortClusterName(t *testing.T) {
	tests := map[string]string{
		"arn:aws:eks:us-east-1:123456789012:cluster/staging": "staging (EKS us-east-1)",
		"arn:aws:eks:eu-west-2:123456789012:cluster/my-dev":  "my-dev (EKS eu-west-2)",
		"gke_myproject_europe-west1_dev":                     "dev (GKE europe-west1)",
		"gke_my-project-123_us-central1-a_dev":               "dev (GKE us-central1-a)",
		"kind-napwake-scan":                                  "kind-napwake-scan",
		"staging-admin":                                      "staging-admin",
		"arn:aws:eks:us-east-1:123:nodegroup/x":              "arn:aws:eks:us-east-1:123:nodegroup/x",
		"gke_only_two":                                       "gke_only_two",
		"gke_a_b_c_d":                                        "gke_a_b_c_d",
	}
	for in, want := range tests {
		assert.Equal(t, want, shortClusterName(in), in)
	}
}

func TestClusterName(t *testing.T) {
	assert.Equal(t, "staging (EKS us-east-1)", clusterName("arn:aws:eks:us-east-1:111111111111:cluster/staging"))
	assert.Equal(t, "current context", clusterName(""))
}

func TestWriteTable_Prices(t *testing.T) {
	cluster := sampleCluster()
	var assumed, own bytes.Buffer

	withAssumed, withOwn := cluster, cluster
	withAssumed.Prices = prices{CPUPerHour: 0.031, MemoryPerHour: 0.004, CPUAssumed: true, MemoryAssumed: true}
	withOwn.Prices = prices{CPUPerHour: 0.05, MemoryPerHour: 0.006}
	require.NoError(t, writeScan(&assumed, withAssumed, scanOptions{output: "table"}))
	require.NoError(t, writeScan(&own, withOwn, scanOptions{output: "table"}))

	assert.Contains(t, assumed.String(), "Costs use assumed list prices: $0.031 per vCPU-hour and $0.004 per GiB-hour")
	assert.NotContains(t, own.String(), "assumed", "the user's own prices need no caveat")
}

func TestPricesSentence(t *testing.T) {
	tests := []struct {
		prices prices
		want   string
	}{
		{prices{CPUPerHour: 0.031, MemoryPerHour: 0.004, CPUAssumed: true, MemoryAssumed: true},
			"Assumed list prices: $0.031 per vCPU-hour and $0.004 per GiB-hour of memory, from AWS on-demand in us-east-1."},
		{prices{CPUPerHour: 0.045, MemoryPerHour: 0.006},
			"Your prices: $0.045 per vCPU-hour and $0.006 per GiB-hour of memory."},
		{prices{CPUPerHour: 0.045, MemoryPerHour: 0.004, MemoryAssumed: true},
			"Your CPU price, $0.045 per vCPU-hour, and the assumed memory price, $0.004 per GiB-hour of memory " +
				"(AWS on-demand, us-east-1)."},
		{prices{CPUPerHour: 0.031, MemoryPerHour: 0.006, CPUAssumed: true},
			"Your memory price, $0.006 per GiB-hour of memory, and the assumed CPU price, $0.031 per vCPU-hour " +
				"(AWS on-demand, us-east-1)."},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, tt.prices.sentence())
	}
}

func TestWriteTable_PausedAndUnjudged(t *testing.T) {
	cluster := sampleCluster()
	cluster.Workloads = append(cluster.Workloads,
		discovery.Workload{Namespace: "preview-42", Kind: v1alpha1.TargetKindDeployment, Name: "asleep",
			State: discovery.StatePaused, Reason: "paused 5h ago", Managed: true, Replicas: 3, HourlyCost: 0.3},
		discovery.Workload{Namespace: "kube-tools", Kind: v1alpha1.TargetKindDeployment, Name: "agent",
			State: discovery.StateUnknown, Unmeasured: "no CPU requests"})
	cluster.Totals.Paused, cluster.Totals.PausedHourlyCost = 1, 0.3
	var out bytes.Buffer

	require.NoError(t, writeScan(&out, cluster, scanOptions{output: "table"}))

	got := out.String()
	assert.Contains(t, got, "Napwake has 1 workload paused right now, freeing $0.30 an hour.")
	assert.Contains(t, got, "paused (live)")
	assert.Contains(t, got, "paused 5h ago")
	assert.NotContains(t, got, "deployment/agent", "unjudged workloads leave the table")
	assert.Contains(t, got, "1 workload set no CPU requests, so their use can't be measured: kube-tools/agent")
}

func TestWriteTable_MeasuredInDryRun(t *testing.T) {
	cluster := sampleCluster()
	cluster.Workloads = append(cluster.Workloads, discovery.Workload{
		Namespace: "preview-7", Kind: v1alpha1.TargetKindDeployment, Name: "api", Replicas: 2,
		State: discovery.StateActive, Managed: true, DryRun: true, HourlyCost: 0.13,
		Measured: &discovery.Measured{Since: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC),
			Pauses: 4, Wakes: 4, SleptHours: 96.5, Freed: 12.48, MonthlyFreed: 81},
	})
	cluster.Totals.DryRun = 1
	cluster.Totals.Measured = *cluster.Workloads[2].Measured
	var out bytes.Buffer

	require.NoError(t, writeScan(&out, cluster,
		scanOptions{output: "table"}))

	got := out.String()
	assert.Regexp(t, `preview-7\s+deployment/api\s+active \(dry-run\)\s+measuring since Oct 1\s+\$0\s+\$0.13\s+`+
		`97h\s+4\s+.*\$81`, got, "what Napwake measured is in its columns, with the period")
	assert.Contains(t, got, "1 workload is in dry-run: measured by Napwake since starting, it would have slept "+
		"97 hours, freeing $12.48,\n  about $81 a month.")
	assert.Contains(t, got, "COULD FREE/MO")
	assert.NotContains(t, got, "Measured in dry-run:", "no separate section")
	assert.Contains(t, got, "start pausing:\n       kubectl napwake enable deployment/api -n preview-7",
		"enable names the workload dry-run measured")
	assert.NotContains(t, got, "helm install", "a managed workload shows Napwake is installed")
	assert.Contains(t, got, "kubectl label deployment checkout-api", "and an unmanaged idle one can still be measured")
}

func TestWriteTable_OnlyMeasuring(t *testing.T) {
	report := &discovery.ClusterReport{Mode: discovery.ModeSnapshot, Workloads: []discovery.Workload{{
		Namespace: "preview-7", Kind: v1alpha1.TargetKindStatefulSet, Name: "db", Replicas: 1,
		State: discovery.StateActive, Managed: true, DryRun: true,
		Measured: &discovery.Measured{Since: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC), Pauses: 1, SleptHours: 0.5},
	}}, Totals: discovery.Totals{Workloads: 1}}
	var out bytes.Buffer

	require.NoError(t, writeScan(&out, scanResult{Cluster: "staging", ClusterReport: report},
		scanOptions{output: "table"}))

	got := out.String()
	assert.Regexp(t, `statefulset/db\s+active \(dry-run\)\s+measuring since Oct 1\s+\$0\s+\$0.00\s+30m\s+0`, got,
		"dry-run's measurement shows even without history")
	assert.Contains(t, got, "1. When you're happy with what dry-run measured, start pausing:")
	assert.Contains(t, got, "kubectl napwake enable statefulset/db -n preview-7")
	assert.NotContains(t, got, "kubectl label", "nothing unmanaged is idle")
}

// A replay of a few hours frees cents, which say "<$1" rather than a $0
// that reads as nothing beside the month it projects.
func TestWriteTable_HistoryFreedUnderADollar(t *testing.T) {
	cluster := sampleCluster()
	cluster.Mode = discovery.ModeHistory
	cluster.History = &discovery.HistorySource{Prometheus: "monitoring/prometheus-operated", Hours: 4}
	cluster.Totals.Replayed = discovery.ReplayTotals{Workloads: 12, Sleepers: 12, SleepHours: 36, Freed: 0.03,
		MonthlyFreed: 11.2}
	var out bytes.Buffer

	require.NoError(t, writeScan(&out, cluster, scanOptions{output: "table"}))

	assert.Contains(t, out.String(), "and freed <$1: about $11/month.")
}

func TestWriteTable_History(t *testing.T) {
	cluster := sampleCluster()
	cluster.Mode = discovery.ModeHistory
	cluster.History = &discovery.HistorySource{Prometheus: "monitoring/prometheus-operated", Hours: 168}
	cluster.Workloads[0].History = &discovery.History{Hours: 168, RunningHours: 168, QuietHours: 141,
		SleepHours: 120, Wakes: 9, Freed: 204.3, MonthlyFreed: 887.6}
	cluster.Totals.Replayed = discovery.ReplayTotals{Workloads: 2, Sleepers: 1, SleepHours: 1204, Wakes: 9,
		Freed: 204.3, MonthlyFreed: 887.6}
	var out bytes.Buffer

	require.NoError(t, writeScan(&out, cluster,
		scanOptions{output: "table"}))

	got := out.String()
	assert.Contains(t, got,
		"Replaying the last 7 days, Napwake would have paused 1 of 2 unmanaged workloads for 1,204 hours in all,")
	assert.Contains(t, got, "and freed $204: about $888/month.")
	assert.Regexp(t, `STATE\s+BECAUSE\s+COST/MO\s+COULD SLEEP\s+WAKES\s+COULD FREE/MO`, got)
	assert.NotContains(t, got, "REPLICAS", "what the state's reason and the cost already say")
	assert.Regexp(t,
		`deployment/checkout-api\s+idle \(unmanaged\)\s+CPU 2% of its request; last deployed 23 days ago\s+`+
			`\$1,240\s+120h\s+9\s+\$888`,
		got, "the evidence comes right after the state it explains")
	assert.Regexp(t, `statefulset/postgres\s+active \(unmanaged\)\s+\$96\s+-\s+-\s+-`, got,
		"no history, no numbers")
	assert.NotContains(t, got, "COST/HOUR")
	assert.NotContains(t, got, "QUIET")
	assert.NotContains(t, got, "which one moment can't show", "history shows it")
}

func TestWriteTable_HistoryWithNothingToSleep(t *testing.T) {
	cluster := sampleCluster()
	cluster.Mode = discovery.ModeHistory
	cluster.History = &discovery.HistorySource{Hours: 30}
	cluster.Totals.Replayed = discovery.ReplayTotals{Workloads: 2}
	var out bytes.Buffer

	require.NoError(t, writeScan(&out, cluster,
		scanOptions{output: "table"}))

	assert.Contains(t, out.String(),
		"Replaying the last 30 hours, 0 of 2 unmanaged workloads would have slept.")
}

// With too little history to show a day's pattern, the terminal says why
// nothing would have slept, as the report does.
func TestWriteTable_TooLittleHistory(t *testing.T) {
	cluster := sampleCluster()
	cluster.Mode = discovery.ModeHistory
	cluster.History = &discovery.HistorySource{Hours: 0.5, Partial: true}
	for i := range cluster.Workloads {
		cluster.Workloads[i].History = &discovery.History{Hours: 0.5}
	}
	cluster.Workloads = append(cluster.Workloads, discovery.Workload{Namespace: "reports", Name: "reports",
		Kind: v1alpha1.TargetKindStatefulSet, State: discovery.StateActive, WontPause: "PVCs deleted on scale-down"})
	cluster.Totals.Replayed = discovery.ReplayTotals{Workloads: 2}
	var out bytes.Buffer

	require.NoError(t, writeScan(&out, cluster, scanOptions{output: "table"}))

	assert.Contains(t, out.String(), "  Replaying the last 30 minutes, 0 of 2 unmanaged workloads would have slept.\n"+
		"  That's too little history to show a day's pattern; run the scan again after a few days.\n"+
		"  Not counted: 1 that won't pause (PVCs deleted on scale-down).\n", "why, right after what it's about")

	cluster.History.Partial = false
	out.Reset()
	require.NoError(t, writeScan(&out, cluster, scanOptions{output: "table"}))
	assert.NotContains(t, out.String(), "too little history", "half an hour was all that was asked for")
}

// The replay's count leaves out what it can't count, and says what, so it
// adds up to the workloads the table lists.
func TestWriteTable_SaysWhatTheReplayDoesntCount(t *testing.T) {
	cluster := sampleCluster()
	cluster.Mode = discovery.ModeHistory
	cluster.History = &discovery.HistorySource{Hours: 168}
	for i := range cluster.Workloads {
		cluster.Workloads[i].History = &discovery.History{Hours: 168}
	}
	cluster.Workloads = append(cluster.Workloads, discovery.Workload{Namespace: "reports", Name: "reports",
		Kind: v1alpha1.TargetKindStatefulSet, State: discovery.StateActive, WontPause: "PVCs deleted on scale-down"})
	cluster.Totals.Replayed = discovery.ReplayTotals{Workloads: 2}
	var out bytes.Buffer

	require.NoError(t, writeScan(&out, cluster, scanOptions{output: "table"}))

	assert.Contains(t, out.String(), "  Replaying the last 7 days, 0 of 2 unmanaged workloads would have slept.\n"+
		"  Not counted: 1 that won't pause (PVCs deleted on scale-down).\n")
}

func TestNotReplayed(t *testing.T) {
	replayed := &discovery.History{Hours: 168}
	workloads := []Workload{
		{Name: "counted", State: discovery.StateIdle, History: replayed},
		{Name: "managed", State: discovery.StateIdle, Managed: true},
		{Name: "unjudged", State: discovery.StateUnknown},
		{Name: "reports", State: discovery.StateActive, WontPause: "PVCs deleted on scale-down", History: replayed},
		{Name: "archive", State: discovery.StateIdle, WontPause: "PVCs deleted on scale-down"},
		{Name: "locked", State: discovery.StateIdle, Protected: true, History: replayed},
		{Name: "off", State: discovery.StatePaused, ScaledByHand: true},
		{Name: "new", State: discovery.StateActive},
	}

	assert.Equal(t, "2 that won't pause (PVCs deleted on scale-down), 1 in protected namespaces, 1 scaled to zero "+
		"by hand and 1 without CPU history to replay", notReplayed(workloads))
	assert.Empty(t, notReplayed(workloads[:3]), "managed and unjudged workloads aren't in the replay's count or the "+
		"unmanaged ones the table lists")
}

// The window is the history actually replayed, said as people say it.
func TestReplayedOver(t *testing.T) {
	tests := map[float64]string{0: "", 0.01: "the last minute", 0.5: "the last 30 minutes", 1: "the last hour",
		5: "the last 5 hours", 30: "the last 30 hours", 168: "the last 7 days", 720: "the last 30 days"}
	for hours, want := range tests {
		assert.Equal(t, want, replayedOver(&discovery.HistorySource{Hours: hours}), hours)
	}
	assert.Empty(t, replayedOver(nil))
}

// With half an hour of history the headline says half an hour, not "1
// hours".
func TestWriteTable_HistoryOfMinutes(t *testing.T) {
	cluster := sampleCluster()
	cluster.Mode = discovery.ModeHistory
	cluster.History = &discovery.HistorySource{Hours: 0.5}
	cluster.Totals.Replayed = discovery.ReplayTotals{Workloads: 2}
	var out bytes.Buffer

	require.NoError(t, writeScan(&out, cluster, scanOptions{output: "table"}))

	assert.Contains(t, out.String(), "Replaying the last 30 minutes, ")
	assert.NotContains(t, out.String(), "1 hours")
}

func TestHistoryAccess(t *testing.T) {
	forbidden := &discovery.ProxyForbiddenError{Namespace: "monitoring", Service: "prometheus-operated", Port: "web"}
	tests := []struct {
		name     string
		username string
		reviewOK bool
		want     string
	}{
		{name: "a user", username: "jane@example.com", reviewOK: true, want: "--user=jane@example.com"},
		{name: "a service account", username: "system:serviceaccount:ci:scanner", reviewOK: true,
			want: "--serviceaccount=ci:scanner"},
		{name: "a cluster that can't say who you are", want: "--user=<you>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
				Create: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.CreateOption) error {
					if !tt.reviewOK {
						return errors.New("selfsubjectreviews not served")
					}
					obj.(*authenticationv1.SelfSubjectReview).Status.UserInfo.Username = tt.username
					return nil
				},
			}).Build()

			got := historyAccess(context.Background(), c, forbidden)

			require.Len(t, got, 2)
			assert.Equal(t, "kubectl create role napwake-scan -n monitoring --verb=get --resource=services/proxy "+
				"--resource-name=prometheus-operated:web", got[0])
			assert.Equal(t, "kubectl create rolebinding napwake-scan -n monitoring --role=napwake-scan "+tt.want, got[1])
		})
	}
}

// A Prometheus served over TLS is reached at the proxy's https: name, which
// is the name a Role must grant.
func TestHistoryAccess_HTTPS(t *testing.T) {
	forbidden := &discovery.ProxyForbiddenError{Namespace: "monitoring", Service: "prometheus", Port: "https",
		Scheme: "https"}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	got := historyAccess(context.Background(), c, forbidden)

	assert.Contains(t, got[0], "--resource-name=https:prometheus:https")
}

func TestWriteTable_HistoryAccess(t *testing.T) {
	cluster := sampleCluster()
	cluster.HistoryAccess = []string{"kubectl create role x", "kubectl create rolebinding x"}
	var out bytes.Buffer

	require.NoError(t, writeScan(&out, cluster,
		scanOptions{output: "table"}))

	assert.Contains(t, out.String(), "To replay history, an admin can let you read Prometheus, and nothing else, with:\n"+
		"  kubectl create role x\n  kubectl create rolebinding x\nOr pass --prometheus-url")
}

func TestWriteTable_Dependencies(t *testing.T) {
	cluster := sampleCluster()
	cluster.Workloads[0].Dependencies = []discovery.Dependency{
		{Namespace: "preview-42", Kind: v1alpha1.TargetKindStatefulSet, Name: "postgres", Via: "PGHOST",
			Address: "postgres-0.postgres-hl", Headless: true},
		{Namespace: "messaging", Kind: v1alpha1.TargetKindStatefulSet, Name: "nats", Via: "NATS_URL",
			Address: "nats://nats.messaging:4222", Headless: true},
		{Namespace: "preview-42", Kind: v1alpha1.TargetKindDeployment, Name: "cache", Via: "CACHE",
			Address: "cache:6379"},
	}
	cluster.Workloads[1].Dependencies = []discovery.Dependency{
		{Namespace: "preview-42", Kind: v1alpha1.TargetKindStatefulSet, Name: "backup", Via: "BACKUP",
			Address: "backup-0.backup", Headless: true, Declared: true},
	}
	var out bytes.Buffer

	require.NoError(t, writeScan(&out, cluster,
		scanOptions{output: "table"}))

	got := out.String()
	assert.Contains(t, got, "  4 dependencies found. Once Napwake manages both ends of one, it wakes the "+
		"dependency with the workload that needs it, and keeps it up while that workload runs:\n",
		"with nothing managed, what every status would say is said once")
	assert.Regexp(t, `deployment/checkout-api\s+->\s+statefulset/postgres\s+PGHOST\n`, got, "no status column")
	assert.Regexp(t, `->\s+messaging/statefulset/nats\s+NATS_URL\n`, got, "another namespace's is named with it")
	assert.NotContains(t, got, "connected once", "no jargon")
	assert.NotContains(t, got, "napwake.dev/depends-on=", "Napwake connects dependencies itself")
}

// Once Napwake manages a workload, each of its dependencies says what
// Napwake does with it.
func TestWriteTable_DependenciesOfManagedWorkloads(t *testing.T) {
	cluster := sampleCluster()
	cluster.Workloads[0].Managed = true
	cluster.Workloads[0].Dependencies = []discovery.Dependency{
		{Namespace: "preview-42", Kind: v1alpha1.TargetKindStatefulSet, Name: "postgres", Via: "PGHOST",
			Connected: true},
		{Namespace: "preview-42", Kind: v1alpha1.TargetKindDeployment, Name: "cache", Via: "CACHE"},
	}
	cluster.Workloads[1].Managed = true
	cluster.Workloads = append(cluster.Workloads, discovery.Workload{Namespace: "preview-42", Name: "cache",
		Kind: v1alpha1.TargetKindDeployment, State: discovery.StateActive})
	var out bytes.Buffer

	require.NoError(t, writeScan(&out, cluster, scanOptions{output: "table"}))

	got := out.String()
	assert.Contains(t, got, "  2 dependencies found, and what Napwake does with each:\n")
	assert.Regexp(t, `statefulset/postgres\s+PGHOST\s+woken and held with it\n`, got)
	assert.Regexp(t, `deployment/cache\s+CACHE\s+not managed, so never paused\n`, got)
}

// Many dependencies are summed up, listed most useful first, and cut at
// --limit like the workloads table.
func TestWriteTable_ManyDependencies(t *testing.T) {
	cluster := sampleCluster()
	since := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for i := range 6 {
		wl := discovery.Workload{Namespace: "preview-42", Kind: v1alpha1.TargetKindDeployment,
			Name: fmt.Sprintf("svc-%d", i), State: discovery.StateActive, Managed: true, DryRun: true,
			Measured: &discovery.Measured{Since: since, Pauses: 1, SleptHours: 10, Freed: float64(i)}}
		wl.Dependencies = []discovery.Dependency{{Namespace: "preview-42", Kind: v1alpha1.TargetKindStatefulSet,
			Name: "db", Via: "DB", Address: "db:5432", Headless: i == 4}}
		cluster.Workloads = append(cluster.Workloads, wl)
	}
	var out bytes.Buffer

	require.NoError(t, writeScan(&out, cluster,
		scanOptions{output: "table", limit: 2}))

	got := out.String()
	assert.Contains(t, got, "  ...and 4 more; --limit 0 lists them all")
	assert.Contains(t, got, "6 dependencies")
}

func TestWriteTable_ScaledToZeroByHand(t *testing.T) {
	cluster := sampleCluster()
	cluster.Workloads = append(cluster.Workloads, discovery.Workload{Namespace: "preview-1",
		Kind: v1alpha1.TargetKindDeployment, Name: "demo", State: discovery.StatePaused, ScaledByHand: true,
		Reason: "scaled to zero, not by Napwake"})
	cluster.Totals.ScaledToZero = 1
	var out bytes.Buffer

	require.NoError(t, writeScan(&out, cluster, scanOptions{output: "table"}))

	got := out.String()
	assert.Regexp(t, `deployment/demo\s+paused \(unmanaged\)\s+scaled to zero, not by Napwake`, got)
	assert.Contains(t, got, "1 workload is scaled to zero by hand; Napwake can pause it while idle and wake it")
	assert.NotContains(t, got, "Napwake has 1 workload paused")
}

func TestManagement(t *testing.T) {
	assert.Equal(t, "unmanaged", management(discovery.Workload{}))
	assert.Equal(t, "dry-run", management(discovery.Workload{Managed: true, DryRun: true}))
	assert.Equal(t, "live", management(discovery.Workload{Managed: true}))
}

func TestParseWindow(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{in: "7d", want: 7 * 24 * time.Hour},
		{in: "36h", want: 36 * time.Hour},
		{in: "0", want: 0},
		{in: "1w", wantErr: true},
		{in: "-2d", wantErr: true},
		{in: "soon", wantErr: true},
		{in: "366d", want: 366 * 24 * time.Hour},
		{in: "367d", wantErr: true},
		{in: "200000d", wantErr: true},
		{in: "106752d", wantErr: true},
		{in: "9000h", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseWindow(tt.in)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// A price no cost can be worked out from is refused before anything is
// read, rather than pricing every saving below zero.
func TestCLI_ScanRejectsPrices(t *testing.T) {
	for _, args := range [][]string{
		{"--cpu-price", "-0.01"},
		{"--memory-price", "-1"},
		{"--cpu-price", "NaN"},
		{"--memory-price", "+Inf"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, err := runCLI(t, newStatusClient(t, interceptor.Funcs{}), append([]string{"scan", "--window", "0",
				"-o", "json"}, args...)...)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "must be a number of dollars, zero or more")
		})
	}
}

// The help states the table's real order: idle, then paused, then active.
func TestScanHelp_Limit(t *testing.T) {
	limit := scanCmd(&kubeFlags{}).Flags().Lookup("limit")
	require.NotNil(t, limit)
	assert.Contains(t, limit.Usage, "idle first, then paused, then active")
}

// The price flags say they replace every node's list price, and what's
// used without them, as pricing does (PW-23).
func TestScanHelp_Prices(t *testing.T) {
	flags := scanCmd(&kubeFlags{}).Flags()
	for flag, want := range map[string]string{
		"cpu-price": "Your price per vCPU-hour, in dollars, for every workload. Unset, each node is priced at its " +
			"type's list price, and one with none at AWS on-demand in us-east-1.",
		"memory-price": "Your price per GiB-hour of memory, in dollars, for every workload. Unset, each node is " +
			"priced at its type's list price, and one with none at AWS on-demand in us-east-1.",
	} {
		f := flags.Lookup(flag)
		require.NotNil(t, f, flag)
		assert.Equal(t, want, f.Usage, flag)
	}
}

func TestPricesFor(t *testing.T) {
	tests := []struct {
		name              string
		args              []string
		wantCPU           float64
		wantCPUAssumed    bool
		wantMemoryAssumed bool
	}{
		{name: "no prices given", wantCPU: 0.031, wantCPUAssumed: true, wantMemoryAssumed: true},
		{name: "a CPU price", args: []string{"--cpu-price", "0.05"}, wantCPU: 0.05, wantMemoryAssumed: true},
		{name: "a memory price", args: []string{"--memory-price", "0.006"}, wantCPU: 0.031, wantCPUAssumed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := scanCmd(&kubeFlags{})
			require.NoError(t, cmd.ParseFlags(tt.args))
			cpu, err := cmd.Flags().GetFloat64("cpu-price")
			require.NoError(t, err)
			memory, err := cmd.Flags().GetFloat64("memory-price")
			require.NoError(t, err)

			ownCPU, ownMemory := ownPrices(cmd)

			got := pricesFor(scanOptions{cpuPrice: cpu, memoryPrice: memory, ownCPUPrice: ownCPU, ownMemoryPrice: ownMemory})

			assert.InDelta(t, tt.wantCPU, got.CPUPerHour, 1e-9)
			assert.Equal(t, tt.wantCPUAssumed, got.CPUAssumed)
			assert.Equal(t, tt.wantMemoryAssumed, got.MemoryAssumed)
		})
	}
}

// A dependency is woken and held with the workload that needs it only when
// Napwake manages both, and each status says what it does.
func TestDependencyStatus(t *testing.T) {
	tests := []struct {
		name                string
		managed, depManaged bool
		d                   discovery.Dependency
		want                string
	}{
		{name: "neither managed", want: "woken with it once both are managed"},
		{name: "only the dependency managed", depManaged: true, d: discovery.Dependency{Declared: true},
			want: "woken with it once both are managed"},
		{name: "the dependency not managed", managed: true, d: discovery.Dependency{Connected: true},
			want: "not managed, so never paused"},
		{name: "declared", managed: true, depManaged: true, d: discovery.Dependency{Declared: true, Connected: true},
			want: "declared; woken and held with it"},
		{name: "learned", managed: true, depManaged: true, d: discovery.Dependency{Connected: true},
			want: "woken and held with it"},
		{name: "not learned yet", managed: true, depManaged: true, want: "not learned yet"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := dependencyLine{wl: Workload{Managed: tt.managed}, d: tt.d, managed: tt.depManaged}
			assert.Equal(t, tt.want, dependencyStatus(l))
		})
	}
}

// Whether a dependency is managed is read from the workloads scanned, the
// cluster's infrastructure included.
func TestDependencyLines_KnowWhatsManaged(t *testing.T) {
	db := discovery.Workload{Namespace: "dev", Kind: v1alpha1.TargetKindStatefulSet, Name: "db", Managed: true}
	queue := discovery.Workload{Namespace: "infra", Kind: v1alpha1.TargetKindStatefulSet, Name: "queue",
		Managed: true, Platform: "rabbitmq"}
	api := discovery.Workload{Namespace: "dev", Kind: v1alpha1.TargetKindDeployment, Name: "api", Managed: true,
		Dependencies: []discovery.Dependency{
			{Namespace: "dev", Kind: v1alpha1.TargetKindStatefulSet, Name: "db"},
			{Namespace: "infra", Kind: v1alpha1.TargetKindStatefulSet, Name: "queue"},
			{Namespace: "dev", Kind: v1alpha1.TargetKindStatefulSet, Name: "cache"},
		}}

	lines := dependencyLines([]Workload{api, db}, []Workload{queue})

	require.Len(t, lines, 3)
	assert.True(t, lines[0].managed)
	assert.True(t, lines[1].managed, "infrastructure Napwake manages")
	assert.False(t, lines[2].managed, "one not scanned isn't known to be managed")
}

func TestWriteScan_FreedAndSavedThisMonth(t *testing.T) {
	var out bytes.Buffer

	require.NoError(t, writeScan(&out, historyResult(), scanOptions{output: "table"}))

	got := out.String()
	assert.Contains(t, got, "Napwake has freed $4 this month from the 1 live workload it pauses, and saved $1 of it.")
	assert.Regexp(t, `FREED THIS MONTH\s+SAVED\s+COULD SLEEP`, got)
	assert.Regexp(t, `preview-3\s+deployment/web\s+paused \(live\)\s+paused 3h ago\s+\$48\s+\$4\s+\$1\s+-\s+-\s+-`,
		got, "a live workload's freed and saved are in their columns, with nothing estimated")
	assert.NotContains(t, got, "COULD SAVE", "the scan can't know what pausing would save, only free")
}

// A workload put back in dry-run shows what it freed while live, and the
// headline counts it, though nothing is live now.
func TestWriteScan_FreedBeforeGoingBackToDryRun(t *testing.T) {
	result := scanResult{Cluster: "staging", ClusterReport: &discovery.ClusterReport{Namespaces: 1,
		Workloads: []Workload{{Namespace: "preview-3", Kind: "Deployment", Name: "web", State: discovery.StateIdle,
			Managed: true, DryRun: true, FreedThisMonth: 7, SavedThisMonth: 2, Measured: &discovery.Measured{}}}}}
	result.Totals = discovery.Totals{Workloads: 1, DryRun: 1, FreedThisMonth: 7, SavedThisMonth: 2}
	var out bytes.Buffer

	require.NoError(t, writeScan(&out, result, scanOptions{output: "table"}))

	got := out.String()
	assert.Contains(t, got, "Napwake has freed $7 this month, and saved $2 of it, pausing workloads before they "+
		"went back to dry-run.")
	assert.Regexp(t, `preview-3\s+deployment/web\s+idle \(dry-run\)\s+measuring since Jan 1\s+\$0\s+\$0.00\s+\$7\s+\$2\s`,
		got)
}

// Scan and status show the same workload's money the same way: three cents
// freed is <$1 in both, never $0, which reads as nothing happened. A live
// workload Napwake never pauses isn't counted among those it pauses, and
// the headline says how many there are.
func TestCLI_ScanAndStatusAgreeOnMoney(t *testing.T) {
	now := metav1.Now()
	cost := &v1alpha1.CostStatus{FreedThisMonth: "$0.03", SavedThisMonth: "$0.00", LastAccumulatedAt: &now}
	api := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "orders"},
		Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](0),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api"}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main"}}}}}}
	apiMW := statusWorkloadObj("orders", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhasePaused, time.Hour)
	apiMW.Status.Cost, apiMW.Status.Pause = cost, &v1alpha1.PauseStatus{PreviousReplicas: 2}
	reports := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "reports", Namespace: "orders"},
		Spec: appsv1.StatefulSetSpec{Replicas: ptr.To[int32](1),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "reports"}},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "reports"}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main"}}}},
			PersistentVolumeClaimRetentionPolicy: &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
				WhenScaled: appsv1.DeletePersistentVolumeClaimRetentionPolicyType}}}
	reportsMW := statusWorkloadObj("orders", "reports", v1alpha1.TargetKindStatefulSet, v1alpha1.PhaseRunning,
		time.Hour)
	reportsMW.Status.Conditions = []metav1.Condition{condition("Protected", metav1.ConditionTrue,
		"PVCsDeletedOnScaleDown", "StatefulSet reports deletes its PersistentVolumeClaims when scaled down")}
	objs := []client.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "orders"}}, api, apiMW, reports,
		reportsMW}

	scan, err := runCLI(t, newStatusClient(t, interceptor.Funcs{}, objs...), "scan", "-n", "orders", "--window",
		"0", "--open=false")
	require.NoError(t, err, scan)
	status, err := runCLI(t, newStatusClient(t, interceptor.Funcs{}, objs...), "status", "-n", "orders")
	require.NoError(t, err, status)

	assert.Contains(t, scan, "Napwake has freed <$1 this month from the 1 live workload it pauses, and saved $0 of it.\n"+
		"  1 more live workload is protected, so Napwake never pauses it.\n")
	assert.Regexp(t, `orders\s+deployment/api\s+paused \(live\)\s+.*\s<\$1\s+\$0\s`, scan)
	assert.Contains(t, status, "Freed <$1 this month, and saved $0 of it.")
	assert.Regexp(t, `orders\s+deployment/api\s+paused\s.*\s<\$1\s+\$0\s*\n`, status)
	assert.NotContains(t, scan+status, "$0 this month")
}

// A workload that has freed under a cent, which the operator writes
// <$0.01, is <$1 in the scan and its report, in the headline and its row,
// never $0, which says nothing was freed.
func TestCLI_ScanFreedUnderACent(t *testing.T) {
	api := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "orders"},
		Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](0),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api"}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main"}}}}}}
	apiMW := statusWorkloadObj("orders", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhasePaused, time.Hour)
	apiMW.Status.Cost = &v1alpha1.CostStatus{FreedThisMonth: "<$0.01", SavedThisMonth: "<$0.01"}
	apiMW.Status.Pause = &v1alpha1.PauseStatus{PreviousReplicas: 2}
	objs := []client.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "orders"}}, api, apiMW}
	page := filepath.Join(t.TempDir(), "scan.html")

	out, err := runCLI(t, newStatusClient(t, interceptor.Funcs{}, objs...), "scan", "-n", "orders", "--window", "0",
		"--html", page, "--open=false")
	require.NoError(t, err, out)
	report, err := os.ReadFile(page)
	require.NoError(t, err)

	assert.Contains(t, out, "Napwake has freed <$1 this month from the 1 live workload it pauses, and saved <$1 of it.")
	assert.Regexp(t, `orders\s+deployment/api\s+paused \(live\)\s+.*\s<\$1\s+<\$1\n`, out)
	assert.Contains(t, string(report), `<div class="figure"><b>&lt;$1</b><span>freed by Napwake this month</span>`+
		`<small>from the 1 live workload it pauses; &lt;$1 of it saved as nodes scaled down</small>`)
	assert.Regexp(t, `<td class="n" data-v="0\.00\d+">&lt;\$1<span class="sub">&lt;\$1 saved</span></td>`,
		string(report))
	assert.NotContains(t, string(report), "<b>$0</b>")
}

// What a workload freed before it was protected is said as its own, never
// as freed by workloads put back in dry-run when none were.
func TestWriteScan_FreedBeforeProtected(t *testing.T) {
	result := scanResult{Cluster: "staging", ClusterReport: &discovery.ClusterReport{Namespaces: 1,
		Workloads: []Workload{{Namespace: "reports", Kind: "StatefulSet", Name: "reports", State: discovery.StateIdle,
			Managed: true, WontPause: "PVCs deleted on scale-down", FreedThisMonth: 12, SavedThisMonth: 5}}}}
	result.Totals = discovery.Totals{Workloads: 1, LiveProtected: 1, FreedThisMonth: 12, SavedThisMonth: 5,
		ProtectedFreedThisMonth: 12, ProtectedSavedThisMonth: 5}
	var out bytes.Buffer

	require.NoError(t, writeScan(&out, result, scanOptions{output: "table"}))

	got := out.String()
	assert.NotContains(t, got, "dry-run")
	assert.Contains(t, got, "  1 live workload is protected, so Napwake never pauses it; it freed $12 this month "+
		"before it was, and saved $5 of that.\n")
	assert.NotContains(t, got, "Napwake has freed", "nothing else freed anything")

	h, _ := headlineFor(result)
	require.NotEmpty(t, h.Figures)
	assert.Equal(t, "$12", h.Figures[0].Value)
	assert.Equal(t, "from workloads it paused before they were protected; $5 of it saved as nodes scaled down",
		h.Figures[0].Detail)

	result.Workloads = append(result.Workloads, Workload{Namespace: "orders", Kind: "Deployment", Name: "api",
		State: discovery.StatePaused, Managed: true, FreedThisMonth: 3, SavedThisMonth: 1})
	result.Totals.Live, result.Totals.FreedThisMonth, result.Totals.SavedThisMonth = 1, 15, 6
	out.Reset()
	require.NoError(t, writeScan(&out, result, scanOptions{output: "table"}))
	assert.Contains(t, out.String(), "  Napwake has freed $3 this month from the 1 live workload it pauses, "+
		"and saved $1 of it.\n"+
		"  1 more live workload is protected, so Napwake never pauses it; it freed $12 this month before it was, "+
		"and saved $5 of that.\n")
	h, _ = headlineFor(result)
	assert.Equal(t, "$15", h.Figures[0].Value)
	assert.Equal(t, "from the 1 live workload it pauses and others it paused before they were protected; "+
		"$6 of it saved as "+
		"nodes scaled down", h.Figures[0].Detail)
}

// What protected workloads freed before they were is said of one or of
// several alike.
func TestLiveProtected_Freed(t *testing.T) {
	assert.Equal(t, "1 live workload is protected, so Napwake never pauses it; it freed $12 this month before "+
		"it was, and saved $5 of that.", liveProtected(discovery.Totals{LiveProtected: 1, ProtectedFreedThisMonth: 12,
		ProtectedSavedThisMonth: 5}))
	assert.Equal(t, "2 more live workloads are protected, so Napwake never pauses them; they freed $12 this "+
		"month before they were, and saved $5 of that.", liveProtected(discovery.Totals{Live: 1, LiveProtected: 2,
		ProtectedFreedThisMonth: 12, ProtectedSavedThisMonth: 5}))
}

func TestLiveProtected(t *testing.T) {
	assert.Equal(t, "1 more live workload is protected, so Napwake never pauses it.",
		liveProtected(discovery.Totals{Live: 1, LiveProtected: 1}))
	assert.Equal(t, "2 live workloads are protected, so Napwake never pauses them.",
		liveProtected(discovery.Totals{LiveProtected: 2}))
}

// The node autoscaler decides whether what pausing frees is ever saved, so
// the scan says what the bill rests on.
func TestWriteScan_NodeAutoscaler(t *testing.T) {
	tests := []struct {
		name string
		a    nodescale.Autoscaler
		want string
	}{
		{name: "found", a: nodescale.Autoscaler{Scalers: []nodescale.Scaler{{Kind: nodescale.ClusterAutoscaler,
			Provider: "AKS"}}},
			want: "staging: 1 workload in 1 namespace\nYour bill: drops only as Cluster Autoscaler on AKS removes " +
				"nodes\n\n"},
		{name: "none", want: "staging: 1 workload in 1 namespace\nYour bill: won't drop until something removes " +
			"nodes, and no node autoscaler was found. See https://docs.napwake.dev/guides/cluster-autoscaler/\n\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := scanResult{Cluster: "staging", NodeAutoscaler: tt.a, ClusterReport: &discovery.ClusterReport{
				Namespaces: 1, Workloads: []discovery.Workload{{Namespace: "preview-42", Name: "api",
					State: discovery.StateActive}}, Totals: discovery.Totals{Workloads: 1}}}
			var out bytes.Buffer

			require.NoError(t, writeScan(&out, result, scanOptions{output: "table"}))

			assert.True(t, strings.HasPrefix(out.String(), tt.want), out.String())
		})
	}
}

func TestWriteScan_Protected(t *testing.T) {
	result := historyResult()
	for i := range result.Workloads {
		if result.Workloads[i].Name == "postgres" {
			result.Workloads[i].Protected = true
		}
	}
	var out bytes.Buffer

	require.NoError(t, writeScan(&out, result, scanOptions{output: "table"}))

	got := out.String()
	assert.Regexp(t, `statefulset/postgres\s+idle \(protected\)`, got)
	assert.NotContains(t, got, "kubectl label statefulset postgres", "never suggested")
}

// Next steps never suggest a protected workload, in the table or the
// report, which choose them the same way.
func TestNextSteps_SkipProtected(t *testing.T) {
	protected := Workload{Namespace: "prod", Kind: v1alpha1.TargetKindDeployment, Name: "payments",
		State: discovery.StateIdle, Protected: true}
	open := Workload{Namespace: "dev", Kind: v1alpha1.TargetKindDeployment, Name: "preview", State: discovery.StateIdle}
	measuringProtected := Workload{Namespace: "prod", Kind: v1alpha1.TargetKindDeployment, Name: "ledger",
		Managed: true, DryRun: true, Protected: true, Measured: &discovery.Measured{}}
	tests := []struct {
		name      string
		workloads []Workload
		want      []string
	}{
		{name: "only protected", workloads: []Workload{protected, measuringProtected}},
		{name: "the first that isn't", workloads: []Workload{protected, open}, want: []string{
			"kubectl label deployment preview -n dev napwake.dev/managed=true",
			"kubectl annotate deployment preview -n dev napwake.dev/dry-run=true",
			"kubectl napwake enable deployment/preview -n dev",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := scanResult{Cluster: "c", ClusterReport: &discovery.ClusterReport{Workloads: tt.workloads,
				Totals: discovery.Totals{Workloads: len(tt.workloads), Idle: 2}}}
			var table bytes.Buffer
			writeNextSteps(&printer{w: &table}, result)
			page := renderHTML(t, result)

			if tt.want == nil {
				assert.Empty(t, table.String())
				assert.NotContains(t, page, "Next steps")
				assert.Nil(t, buildReport(result, time.Hour).NextSteps)
				return
			}
			for _, command := range tt.want {
				assert.Contains(t, table.String(), command)
			}
			steps := buildReport(result, time.Hour).NextSteps
			require.NotNil(t, steps)
			require.Len(t, steps.Measure, 2, "annotate, then label")
			require.NotNil(t, steps.Enable, "then go live")
			for _, c := range append(steps.Measure, *steps.Enable) {
				assert.NotContains(t, string(c.Line), "payments")
				assert.Equal(t, "dev", c.Namespace, "each command's target")
				assert.Equal(t, "deployment/preview", c.Workload)
			}
			assert.Contains(t, string(steps.Enable.Line), "napwake enable")
			assert.NotContains(t, table.String(), "payments")
		})
	}
}

// A label put on by hand would be reverted by the GitOps tool that sets a
// workload's replicas, so next steps suggest one Git doesn't own; with
// Napwake's own Deployment found, there's no install step.
func TestNextSteps_SkipGitOpsAndKnowNapwakeIsInstalled(t *testing.T) {
	fromGit := Workload{Namespace: "dev", Kind: v1alpha1.TargetKindDeployment, Name: "synced",
		State: discovery.StateIdle, ReplicasFromGit: "Argo CD"}
	measuringFromGit := Workload{Namespace: "dev", Kind: v1alpha1.TargetKindDeployment, Name: "synced-dry",
		Managed: true, DryRun: true, ReplicasFromGit: "Flux", Measured: &discovery.Measured{}}
	open := Workload{Namespace: "dev", Kind: v1alpha1.TargetKindDeployment, Name: "preview", State: discovery.StateIdle}
	napwake := Workload{Namespace: "ops", Kind: v1alpha1.TargetKindDeployment, Name: "napwake",
		Platform: discovery.PlatformNapwake}

	only := scanResult{Cluster: "c", ClusterReport: &discovery.ClusterReport{
		Workloads: []Workload{fromGit, measuringFromGit}}}
	var table bytes.Buffer
	writeNextSteps(&printer{w: &table}, only)
	assert.Empty(t, table.String())

	mixed := scanResult{Cluster: "c", ClusterReport: &discovery.ClusterReport{
		Workloads: []Workload{fromGit, measuringFromGit, open}, Platform: []Workload{napwake}}}
	table.Reset()
	writeNextSteps(&printer{w: &table}, mixed)
	got := table.String()
	assert.Contains(t, got, "kubectl label deployment preview -n dev napwake.dev/managed=true")
	assert.Contains(t, got, "kubectl napwake enable deployment/preview -n dev")
	assert.NotContains(t, got, "synced")
	assert.NotContains(t, got, "helm install", "Napwake's own Deployment shows it's installed")
}

// releasedAs has the plugin built as a release, as the release workflow
// builds it.
func releasedAs(t *testing.T, tag string) {
	t.Helper()
	built := version
	version = tag
	t.Cleanup(func() { version = built })
}

// With no Napwake found, the next steps start by installing it, as the
// installation guide does, with the values file it asks for; measuring and
// going live follow, numbered after it.
func TestNextSteps_InstallFirstWhenNapwakeIsntFound(t *testing.T) {
	releasedAs(t, "v0.2.0")
	var table bytes.Buffer

	writeNextSteps(&printer{w: &table}, sampleCluster())

	assert.Equal(t, strings.Join([]string{
		"Next steps:",
		"  1. Install Napwake, if the cluster doesn't have it yet. Put your users' time zone in",
		"     napwake-values.yaml first, such as timezone: Europe/London. Then:",
		"       helm install napwake oci://ghcr.io/napwake/charts/napwake --version 0.2.0 --namespace napwake-system " +
			"--create-namespace -f napwake-values.yaml",
		"     See https://docs.napwake.dev/getting-started/installation/",
		"  2. Measure a workload first, in dry-run, which pauses nothing:",
		"       kubectl annotate deployment checkout-api -n preview-42 napwake.dev/dry-run=true",
		"       kubectl label deployment checkout-api -n preview-42 napwake.dev/managed=true",
		"  3. When you're happy with what it measures, start pausing it while idle:",
		"       kubectl napwake enable deployment/checkout-api -n preview-42",
		"", "",
	}, "\n"), table.String())
}

// With Napwake found, by a workload it manages or its own Deployment, the
// next steps start with measuring, as they always have.
func TestNextSteps_NoInstallStepWhenNapwakeIsFound(t *testing.T) {
	releasedAs(t, "v0.2.0")
	managed := sampleCluster()
	managed.Workloads = append(managed.Workloads, Workload{Namespace: "preview-7", Kind: v1alpha1.TargetKindDeployment,
		Name: "web", State: discovery.StateActive, Managed: true})
	own := sampleCluster()
	own.Platform = []Workload{{Namespace: "napwake-system", Kind: v1alpha1.TargetKindDeployment,
		Name: "napwake-controller-manager", Platform: discovery.PlatformNapwake}}
	for name, result := range map[string]scanResult{"a workload it manages": managed, "its own Deployment": own} {
		t.Run(name, func(t *testing.T) {
			var table bytes.Buffer

			writeNextSteps(&printer{w: &table}, result)

			assert.Equal(t, `Next steps:
  1. Measure a workload first, in dry-run, which pauses nothing:
       kubectl annotate deployment checkout-api -n preview-42 napwake.dev/dry-run=true
       kubectl label deployment checkout-api -n preview-42 napwake.dev/managed=true
  2. When you're happy with what it measures, start pausing it while idle:
       kubectl napwake enable deployment/checkout-api -n preview-42

`, table.String())
		})
	}
}

// The install step installs the chart of the plugin's own release, so the
// two match, release candidates included. A build that isn't a release
// leaves the version to Helm.
func TestInstallCommand_PinsThePluginsRelease(t *testing.T) {
	for plugin, want := range map[string]string{
		"v0.2.0": "helm install napwake oci://ghcr.io/napwake/charts/napwake --version 0.2.0 " +
			"--namespace napwake-system --create-namespace -f napwake-values.yaml",
		"v0.2.0-rc.9": "helm install napwake oci://ghcr.io/napwake/charts/napwake --version 0.2.0-rc.9 " +
			"--namespace napwake-system --create-namespace -f napwake-values.yaml",
		"dev": "helm install napwake oci://ghcr.io/napwake/charts/napwake " +
			"--namespace napwake-system --create-namespace -f napwake-values.yaml",
	} {
		t.Run(plugin, func(t *testing.T) {
			assert.Equal(t, want, installCommand(plugin))
		})
	}
}

// A workload in dry-run that someone scaled to zero has nothing for going
// live to free, so next steps take the next one measuring instead.
func TestNextSteps_SkipDryRunScaledByHand(t *testing.T) {
	scaled := Workload{Namespace: "dev", Kind: v1alpha1.TargetKindDeployment, Name: "parked", State: discovery.StatePaused,
		Managed: true, DryRun: true, ScaledByHand: true, Measured: &discovery.Measured{MonthlyFreed: 40}}
	measuring := Workload{Namespace: "dev", Kind: v1alpha1.TargetKindDeployment, Name: "preview",
		State: discovery.StateIdle, Managed: true, DryRun: true, Measured: &discovery.Measured{MonthlyFreed: 10}}

	idle, got := nextStepTargets([]Workload{scaled, measuring})

	assert.Nil(t, idle)
	require.NotNil(t, got)
	assert.Equal(t, "preview", got.Name)
}

// A workload Napwake refuses to pause isn't suggested, and could save
// nothing, however idle it is.
func TestNextSteps_SkipWhatWontPause(t *testing.T) {
	postgres := Workload{Namespace: "dev", Kind: v1alpha1.TargetKindStatefulSet, Name: "postgres",
		State: discovery.StateIdle, WontPause: "PVCs deleted on scale-down",
		History: &discovery.History{MonthlyFreed: 90}}
	api := Workload{Namespace: "dev", Kind: v1alpha1.TargetKindDeployment, Name: "api", State: discovery.StateIdle}

	idle, _ := nextStepTargets([]Workload{postgres, api})

	require.NotNil(t, idle)
	assert.Equal(t, "api", idle.Name)
	assert.Equal(t, "-", couldFreeCell(postgres))
}

// Under an hour is shown in minutes, rounded first, so what rounds to an
// hour says so.
func TestHours(t *testing.T) {
	for h, want := range map[float64]string{0: "0h", 0.004: "0m", 0.5: "30m", 0.99: "59m", 0.995: "1h", 1: "1h",
		1.4: "1h", 1.5: "2h", 167.9: "168h"} {
		assert.Equal(t, want, hours(h), "%v hours", h)
	}
}

func TestParseHeaders(t *testing.T) {
	got, err := parseHeaders([]string{"X-Scope-OrgID: team-a", "x-extra:  two words  "})
	require.NoError(t, err)
	assert.Equal(t, "team-a", got.Get("X-Scope-OrgID"))
	assert.Equal(t, "two words", got.Get("X-Extra"))

	for _, bad := range []string{"no colon", ": no name", "bad name: x", "X-A: one\rtwo"} {
		_, err := parseHeaders([]string{bad})
		assert.Error(t, err, bad)
	}
}

func TestPrometheusFrom(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("s3cret\n"), 0o600))

	setup, err := prometheusFrom(prometheusOptions{url: "https://mimir.example.com", selector: `cluster="prod"`,
		headers: []string{"X-Scope-OrgID: team-a"}, bearerTokenFile: tokenFile})
	require.NoError(t, err)
	assert.Equal(t, discovery.Selector{`cluster="prod"`}, setup.selector)
	assert.Equal(t, "Bearer s3cret", setup.header.Get("Authorization"))
	assert.Equal(t, "team-a", setup.header.Get("X-Scope-OrgID"))
	require.NotNil(t, setup.client)

	tests := []struct {
		name string
		opts prometheusOptions
		want string
	}{
		{"a bad selector", prometheusOptions{selector: `cluster="prod"} or vector(1)`}, "--prometheus-selector"},
		{"auth without a URL", prometheusOptions{headers: []string{"X-A: b"}}, "are for --prometheus-url"},
		{"two Authorizations", prometheusOptions{url: "https://p", headers: []string{"Authorization: Basic x"},
			bearerTokenFile: tokenFile}, "can't both be given"},
		{"a missing token file", prometheusOptions{url: "https://p", bearerTokenFile: tokenFile + "-missing"},
			"--prometheus-bearer-token-file"},
		{"a missing CA file", prometheusOptions{url: "https://p", caFile: tokenFile + "-missing"}, "--prometheus-ca-file"},
		{"a URL and a Service", prometheusOptions{url: "https://p", service: "monitoring/prometheus"}, "pass one"},
		{"a Service without a namespace", prometheusOptions{service: "prometheus-operated"},
			"isn't namespace/name or namespace/name:port"},
		{"a Service with an empty port", prometheusOptions{service: "monitoring/prometheus-operated:"},
			"isn't namespace/name or namespace/name:port"},
		{"a Service that can't be named so", prometheusOptions{service: "monitoring/Prometheus"},
			"isn't namespace/name or namespace/name:port"},
		{"auth with a Service", prometheusOptions{service: "monitoring/prometheus", bearerTokenFile: tokenFile},
			"are for --prometheus-url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := prometheusFrom(tt.opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestPrometheusFrom_Service(t *testing.T) {
	setup, err := prometheusFrom(prometheusOptions{service: "monitoring/prometheus-operated:web"})
	require.NoError(t, err)
	assert.Equal(t, &serviceRef{namespace: "monitoring", name: "prometheus-operated", port: "web"}, setup.service)

	setup, err = prometheusFrom(prometheusOptions{service: "monitoring/prometheus-operated"})
	require.NoError(t, err)
	assert.Empty(t, setup.service.port, "the Service's own API port")

	setup, err = prometheusFrom(prometheusOptions{})
	require.NoError(t, err)
	assert.Nil(t, setup.service, "found in the cluster")
}

// The terminal names the Prometheus the history is from, how much it
// replayed, and the others it could have read.
func TestWriteTable_HistoryLine(t *testing.T) {
	tests := []struct {
		name    string
		history discovery.HistorySource
		want    string
	}{
		{name: "found", history: discovery.HistorySource{Prometheus: "monitoring/prometheus-operated", Hours: 0.5},
			want: "History: the last 30 minutes, from monitoring/prometheus-operated\n"},
		{name: "one of several", history: discovery.HistorySource{Prometheus: "monitoring/prometheus-operated",
			Hours: 168, Others: []string{"istio-system/prometheus"}},
			want: "History: the last 7 days, from monitoring/prometheus-operated; also found istio-system/prometheus, " +
				"which --prometheus-service reads instead\n"},
		{name: "a URL", history: discovery.HistorySource{Prometheus: "https://thanos.example.com", Hours: 720},
			want: "History: the last 30 days, from https://thanos.example.com\n"},
		{name: "none of the namespaces' history",
			history: discovery.HistorySource{Prometheus: "monitoring/prometheus-operated"},
			want:    "History: monitoring/prometheus-operated has no CPU history of the namespaces scanned\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := sampleCluster()
			cluster.Mode = discovery.ModeHistory
			cluster.History = &tt.history
			var out bytes.Buffer

			require.NoError(t, writeScan(&out, cluster, scanOptions{output: "table"}))

			assert.Contains(t, out.String(), tt.want)
		})
	}

	var out bytes.Buffer
	require.NoError(t, writeScan(&out, sampleCluster(), scanOptions{output: "table"}))
	assert.NotContains(t, out.String(), "History:", "no history read, no line")
}

// proxiedPrometheus is an API server whose service proxy reaches one
// Prometheus, at path, and refuses the user any other.
func proxiedPrometheus(t *testing.T, path string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case path + "/api/v1/query":
			_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		case path + "/api/v1/query_range":
			_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden",`+
				`"code":403}`)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// runCLIAt is runCLI against an API server, for what goes through it
// rather than the client, such as the service proxy.
func runCLIAt(t *testing.T, server string, c client.Client, args ...string) (string, error) {
	t.Helper()
	writeKubeconfig(t, server, "preview-42")
	root := newRootCmd(func(*rest.Config) (client.Client, error) { return c, nil })
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// --prometheus-service reads history from the Prometheus Service it names,
// one the scan wouldn't have found, through the API server.
func TestCLI_ScanPrometheusService(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "preview-42"}}
	prometheus := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "frontend", Namespace: "gmp-public"},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "web", Port: 9090}}}}
	server := proxiedPrometheus(t, "/api/v1/namespaces/gmp-public/services/frontend:web/proxy")

	out, err := runCLIAt(t, server.URL, newStatusClient(t, interceptor.Funcs{}, ns, prometheus), "scan",
		"-n", "preview-42", "-o", "json", "--prometheus-service", "gmp-public/frontend")

	require.NoError(t, err, out)
	var result scanResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.NotNil(t, result.History)
	assert.Equal(t, "gmp-public/frontend", result.History.Prometheus)
}

// A Prometheus the user names that the API server won't let them reach
// fails the scan, saying what an admin can run to let them.
func TestCLI_ScanPrometheusServiceForbidden(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "preview-42"}}
	prometheus := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "prometheus-operated", Namespace: "monitoring"},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "web", Port: 9090}}}}
	server := proxiedPrometheus(t, "/nowhere")

	_, err := runCLIAt(t, server.URL, newStatusClient(t, interceptor.Funcs{}, ns, prometheus), "scan",
		"-n", "preview-42", "-o", "json", "--prometheus-service", "monitoring/prometheus-operated")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading history from --prometheus-service monitoring/prometheus-operated: "+
		"can't query monitoring/prometheus-operated through the API server")
	assert.Contains(t, err.Error(), "kubectl create role napwake-scan -n monitoring --verb=get "+
		"--resource=services/proxy --resource-name=prometheus-operated:web")
}

// A scan that couldn't read all it should have exits non-zero, after its
// report is written.
func TestIncompleteError(t *testing.T) {
	result := sampleCluster()
	assert.NoError(t, incompleteError(result))

	result.Incomplete = &discovery.Incomplete{Failed: []string{"orders", "payments"}}
	err := incompleteError(result)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the scan of staging is incomplete: 2 namespaces couldn't be read in full")
	assert.Contains(t, err.Error(), "pass a longer --timeout")

	result.Incomplete = &discovery.Incomplete{Unscanned: []string{"team-z"}}
	err = incompleteError(result)

	require.Error(t, err)
	assert.EqualError(t, err, "the scan of staging is incomplete: it ran out of --timeout before it had scanned "+
		"1 namespace, which the report leaves out; pass a longer --timeout, or scan fewer namespaces with -n")
}

// A namespace named with -n that doesn't exist, or that the user can't
// read, won't be read by trying again or waiting longer, so the advice is
// for what will.
func TestCLI_ScanIncompleteAdvice(t *testing.T) {
	denied := interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList,
		opts ...client.ListOption) error {
		if _, ok := list.(*appsv1.DeploymentList); ok {
			return apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "", nil)
		}
		return c.List(ctx, list, opts...)
	}}
	tests := []struct {
		name string
		c    client.Client
		want string
	}{
		{name: "missing", c: newStatusClient(t, interceptor.Funcs{}),
			want: "the scan of hanging is incomplete: 1 namespace named with -n doesn't exist: nope; check the names"},
		{name: "denied", c: newStatusClient(t, denied, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "nope"}}),
			want: "the scan of hanging is incomplete: your access doesn't allow reading workloads in nope; ask an " +
				"admin for list on Deployments and StatefulSets there, or leave it out of -n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := runCLI(t, tt.c, "scan", "-n", "nope", "--window", "0", "-o", "json")

			require.Error(t, err)
			assert.Equal(t, tt.want, err.Error())
		})
	}
}

// A Prometheus that answers its check but is too slow for the history,
// as a big namespace on a slow Thanos is, costs the replay, not the scan:
// it finishes from CPU right now, says it ran out of history's time, and
// suggests --window 0, rather than failing on the API server.
func TestCLI_ScanSlowPrometheus(t *testing.T) {
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api/v1/query") {
			_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
			return
		}
		<-r.Context().Done()
	}))
	t.Cleanup(prom.Close)
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "preview-42"},
		Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](1),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api"}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main"}}}}}}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "preview-42"}}

	out, err := runCLI(t, newStatusClient(t, interceptor.Funcs{}, d, ns), "scan", "-n", "preview-42", "-o", "json",
		"--timeout", "1s", "--prometheus-url", prom.URL)

	require.NoError(t, err)
	var result scanResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.Len(t, result.Workloads, 1)
	notes := strings.Join(result.Notes, "\n")
	assert.Contains(t, notes, "the scan ran out of the time it gives CPU history")
	assert.Contains(t, notes, "--window 0")
}

// The scan names the node autoscaler in its JSON, and one RBAC hides is
// unknown, not absent.
func TestCLI_ScanNodeAutoscaler(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "preview-42"}}
	status := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cluster-autoscaler-status",
		Namespace: "kube-system"}}
	tests := []struct {
		name   string
		funcs  interceptor.Funcs
		kinds  []string
		unread []string
	}{
		{name: "found", kinds: []string{string(nodescale.ClusterAutoscaler)}},
		{name: "hidden", funcs: forbidReads("NodeList", "ConfigMap"), kinds: []string{},
			unread: []string{"nodes", "the cluster-autoscaler-status ConfigMap in kube-system"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := runCLI(t, newStatusClient(t, tt.funcs, ns, status), "scan", "-n", "preview-42", "-o", "json",
				"--window", "0")

			require.NoError(t, err)
			var result scanResult
			require.NoError(t, json.Unmarshal([]byte(out), &result))
			assert.Equal(t, tt.kinds, scalerKinds(result.NodeAutoscaler))
			assert.Equal(t, tt.unread, result.NodeAutoscaler.Unread)
		})
	}
}

// An empty cluster says so, rather than pointing at notes it doesn't have.
func TestEmptyCluster(t *testing.T) {
	result := scanResult{Cluster: "c", ClusterReport: &discovery.ClusterReport{Namespaces: 3, Workloads: []Workload{}}}
	var out bytes.Buffer

	require.NoError(t, writeScan(&out, result, scanOptions{output: "table"}))

	assert.Contains(t, out.String(), "c: 3 namespaces scanned\n")
	assert.Contains(t, out.String(), "No Deployments or StatefulSets were found.")
	assert.NotContains(t, out.String(), "No running workloads are idle")
	page := renderHTML(t, result)
	assert.Contains(t, page, "No Deployments or StatefulSets were found.")
	assert.NotContains(t, page, "the notes say why")

	result.Notes = []string{"your access doesn't allow reading workloads in 3 namespaces"}
	assert.Contains(t, renderHTML(t, result), "the notes say what it couldn&#39;t")
}

// neverPausedIdle is a cluster whose only idle workload is one Napwake never
// pauses, small and live, with the rest already managed: as the test
// cluster's reports StatefulSet was.
func neverPausedIdle() scanResult {
	reports := discovery.Workload{Namespace: "reports", Kind: v1alpha1.TargetKindStatefulSet, Name: "reports",
		Replicas: 1, State: discovery.StateIdle, Managed: true, WontPause: "PVCs deleted on scale-down",
		PodCPURequestMillis: 20, PodMemoryRequestBytes: 32 << 20, HourlyCost: 0.001, MonthlyCost: 0.73}
	api := discovery.Workload{Namespace: "orders", Kind: v1alpha1.TargetKindDeployment, Name: "api",
		Replicas: 1, State: discovery.StateActive, Managed: true, HourlyCost: 0.002, MonthlyCost: 1.46}
	return scanResult{Context: "napwake", Cluster: "napwake", ClusterReport: &discovery.ClusterReport{
		Mode: discovery.ModeSnapshot, Namespaces: 2, Workloads: []discovery.Workload{reports, api},
		Totals: discovery.Totals{Workloads: 2, Live: 1, LiveProtected: 1, Idle: 1, IdleNeverPaused: 1,
			IdleCPUMillis: 20, IdleMemoryBytes: 32 << 20, IdleHourlyCost: 0.001, IdleMonthlyCost: 0.73,
			IdleNeverPausedHourlyCost: 0.001, IdleNeverPausedMonthlyCost: 0.73, MonthlyCost: 2.19},
	}}
}

// What's idle but never paused frees nothing (P50); a small reservation
// isn't shown as none (P51); and with nothing left to opt in, the scan
// doesn't point at next steps below that aren't there (P53).
func TestWriteTable_IdleNapwakeNeverPauses(t *testing.T) {
	var out bytes.Buffer

	require.NoError(t, writeScan(&out, neverPausedIdle(), scanOptions{output: "table", limit: 25}))

	got := out.String()
	assert.Contains(t, got, "1 workload is idle right now, reserving 0.02 vCPU and 0.03 GiB of memory.")
	assert.Contains(t, got, "Napwake never pauses it, so its idling frees nothing.")
	assert.NotContains(t, got, "each hour asleep frees that")
	assert.NotContains(t, got, "depends on how often they'd be woken", "nothing is left to opt in")
	assert.NotContains(t, got, "below")
}
