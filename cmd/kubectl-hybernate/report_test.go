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
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
	"github.com/okedeji/hybernate/internal/discovery"
)

// historyResult is a cluster replayed over a week of history, with dry-run
// and dependencies to show.
func historyResult() scanResult {
	return scanResult{
		ScannedAt: time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC),
		Context:   "arn:aws:eks:us-east-1:1:cluster/staging",
		Cluster:   "staging (EKS us-east-1)",
		Settings:  settings{CPUThreshold: 10, IdleAfter: "1h0m0s", Window: "7d"},
		Prices:    prices{CPUPerHour: 0.031, MemoryPerHour: 0.004, CPUAssumed: true, MemoryAssumed: true},
		ClusterReport: &discovery.ClusterReport{
			Mode:    discovery.ModeHistory,
			History: &discovery.HistorySource{Prometheus: "monitoring/prometheus-operated", Hours: 168},
			Workloads: []discovery.Workload{
				{Namespace: "preview-42", Kind: v1alpha1.TargetKindStatefulSet, Name: "postgres", Replicas: 1,
					State: discovery.StateIdle, CPUPercent: ptr.To(0), Reason: "CPU under 1% of its request",
					Clues:       []string{"last deployed 23 days ago"},
					MonthlyCost: 140, HourlyCost: 0.19,
					History: &discovery.History{Hours: 168, RunningHours: 168, QuietHours: 168, SleepHours: 167,
						Freed: 32, MonthlyFreed: 139}},
				{Namespace: "preview-7", Kind: v1alpha1.TargetKindDeployment, Name: "api", Replicas: 2,
					State: discovery.StateIdle, CPUPercent: ptr.To(1), Reason: "no activity for 5h", Managed: true, DryRun: true,
					MonthlyCost: 96, HourlyCost: 0.13,
					History: &discovery.History{Hours: 168, RunningHours: 168, QuietHours: 131, SleepHours: 112,
						Wakes: 9, Freed: 14.6, MonthlyFreed: 64},
					Measured: &discovery.Measured{Since: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC),
						Pauses: 4, Wakes: 4, SleptHours: 41, Freed: 5.33, MonthlyFreed: 40},
					Dependencies: []discovery.Dependency{
						{Namespace: "preview-7", Kind: v1alpha1.TargetKindStatefulSet, Name: "postgres", Via: "PGHOST",
							Address: "postgres-0.postgres-hl", Headless: true},
						{Namespace: "messaging", Kind: v1alpha1.TargetKindStatefulSet, Name: "nats", Via: "NATS_URL",
							Address: "nats://nats.messaging:4222"},
					}},
				{Namespace: "preview-3", Kind: v1alpha1.TargetKindDeployment, Name: "web", Replicas: 2,
					State: discovery.StatePaused, Reason: "paused 3h ago", Managed: true, MonthlyCost: 48, HourlyCost: 0.07,
					FreedThisMonth: 3.5, SavedThisMonth: 1.2},
				{Namespace: "payments", Kind: v1alpha1.TargetKindDeployment, Name: "ledger", Replicas: 3,
					State: discovery.StateActive, CPUPercent: ptr.To(64), Reason: "CPU 64% of its request",
					MonthlyCost: 210, HourlyCost: 0.29,
					History: &discovery.History{Hours: 168, RunningHours: 168, QuietHours: 12}},
				{Namespace: "kube-tools", Kind: v1alpha1.TargetKindDeployment, Name: "agent", State: discovery.StateUnknown,
					Unmeasured: "no CPU requests"},
			},
			Notes: []string{"the history replay sees CPU and rollouts only"},
			Totals: discovery.Totals{Workloads: 5, MonthlyCost: 446, FreeableBasis: 446, Paused: 1, PausedHourlyCost: 0.07,
				Idle: 2, IdleCPUMillis: 2500,
				IdleMemoryBytes: 6 << 30, IdleHourlyCost: 0.32, IdleMonthlyCost: 236,
				Replayed: discovery.ReplayTotals{Workloads: 2, Sleepers: 1, SleepHours: 167, Freed: 32, MonthlyFreed: 139},
				Measured: discovery.Measured{Pauses: 4, SleptHours: 41, Freed: 5.33, MonthlyFreed: 40}, DryRun: 1,
				FreedThisMonth: 3.5, SavedThisMonth: 1.2, Live: 1},
		},
	}
}

func renderHTML(t *testing.T, result scanResult) string {
	t.Helper()
	var out bytes.Buffer
	require.NoError(t, writeHTML(&out, result, time.Hour))
	return out.String()
}

func TestWriteHTML_History(t *testing.T) {
	got := renderHTML(t, historyResult())

	assert.Contains(t, got, "<title>Workload scan: staging (EKS us-east-1)</title>")
	assert.Contains(t, got, "3 October 2026, 09:00 UTC")
	assert.Contains(t, got, "Based on the last 7 days of Prometheus history, from monitoring/prometheus-operated, "+
		"and CPU at the time of the scan.", "which Prometheus the history is from")
	assert.Contains(t, got, "Costs use assumed list prices: $0.031 per vCPU-hour and $0.004 per GiB-hour of memory")
	assert.Contains(t, got, `<div class="figure"><b>$4</b><span>freed by Hybernate this month</span>`+
		`<small>so far, pausing 1 live workload; $1 of it saved as nodes scaled down</small></div>`)
	assert.Contains(t, got, `<p class="prices">Node autoscaler: none found — freed capacity won&#39;t lower the `+
		`bill until nodes scale down; see https://okedeji.io/hybernate/guides/cluster-autoscaler/</p>`)
	assert.Contains(t, got, `<div class="figure"><b>$179</b><span>could be freed a month</span>`+
		`<small>40% of what these workloads cost over the same time</small></div>`,
		"what history shows for unmanaged workloads, plus what dry-run measured, against what they cost")
	assert.NotContains(t, got, "Had Hybernate been pausing them", "the figures and facts say it")
	assert.Contains(t, got, "It&#39;s idle once it has had no activity for 1h")
	assert.NotContains(t, got, "Hybernate Hub", "it has no page to send anyone to yet")
	tip := "Hours Hybernate would have had it paused, measured since dry-run started for a dry-run workload, or " +
		"from history over the last 7 days for an unmanaged one. Under it, how many times activity would have " +
		"woken it, each a wait for someone."
	assert.Contains(t, got, `class="tip" title="`+tip+`">Could sleep</button>`, "the definition is on the header")
	assert.Contains(t, got, "<dt>Could sleep</dt><dd>"+tip+"</dd>", "and kept in the method section for print")
	assert.NotContains(t, got, "Quiet")
	assert.Contains(t, got, "<div>$446<span>a month, what these workloads cost while running</span></div>")
	assert.Contains(t, got, "<div>1 of 2<span>unmanaged workloads would have slept over the last 7 days</span></div>")
	assert.NotContains(t, got, "wakes over", "wakes are per workload, in the table")
	assert.Contains(t, got, "<div>2 of 5<span>managed by Hybernate: 1 live, 1 in dry-run</span></div>")
	assert.Contains(t, got, `data-filter="namespaces"`, "namespaces can be filtered like workloads")
	assert.Contains(t, got, `<td class="ref">statefulset/postgres<span class="sub">preview-42</span></td>`,
		"the namespace under the workload, not a column of its own")
	assert.Contains(t, got, `167h<span class="sub">0 wakes</span>`, "wakes under asleep")
	assert.NotContains(t, got, ">Replicas<")
	assert.NotContains(t, got, ">CPU<")
	assert.Contains(t, got, `<table data-sortable id="namespaces">`)
	assert.Contains(t, got, "Freed this month")
	assert.Contains(t, got, `$4<span class="sub">$1 saved</span>`, "what came off the bill under what was freed")
	assert.Contains(t, got, "Could free / month")
	assert.NotContains(t, got, "Could save", "the scan can't know what pausing would save, only free")
	assert.Contains(t, got, `<span class="state active"><span class="dot"></span>active (unmanaged)</span>`+
		`<span class="because">CPU 64% of its request</span>`, "the evidence sits under the state it explains")
	assert.NotContains(t, got, "deployment/agent", "workloads that couldn't be judged go in the notes")
	assert.Contains(t, got, "1 workload set no CPU requests")
	assert.Contains(t, got, `41h<span class="sub">4 wakes<br>since Oct 1</span>`,
		"what dry-run measured is in its columns, with the period it covers")
	assert.Contains(t, got, `<span class="because">no activity for 5h</span>`,
		"the line under a dry-run workload's state explains the state again")
	assert.NotContains(t, got, "Measured in dry-run", "no separate section")
	assert.Contains(t, got, "2 found; select a column to sort")
	assert.Contains(t, got, "<td>PGHOST</td>\n        <td>not managed, so never paused</td>",
		"a dry-run workload's dependency Hybernate doesn't manage")
	assert.Contains(t, got, "<th><button type=\"button\">Status</button></th>", "a managed workload's need a status")
	assert.NotContains(t, got, "hybernate.io/depends-on=", "Hybernate connects dependencies itself")
	assert.NotContains(t, got, "headless")
	assert.Contains(t, got, "messaging/statefulset/nats")
	assert.NotContains(t, got, "postgres-0.postgres-hl", "addresses stay out of a report that gets forwarded")
	assert.NotContains(t, got, "nats://", "addresses stay out of a report that gets forwarded")
	assert.Contains(t, got, "after its idle-after with no activity (1h, or a managed workload&#39;s own)")
	assert.Contains(t, got, "<p>Every Deployment and StatefulSet scanned, except 1 it couldn't judge, which the "+
		"notes list: what each is doing right now, what pausing the live ones has freed and saved, and what pausing "+
		"the rest could free.</p>", "the table leaves out what the scan couldn't judge, and -n limits it")
	assert.Contains(t, got, "with their own CPU threshold and idle-after", "the details are in the method section")
	assert.Contains(t, got, `<span class="line"><span class="sh-cmd">kubectl</span> label statefulset postgres `+
		`<span class="sh-flag">-n</span> preview-42 <span class="sh-key">hybernate.io/managed</span>=`+
		`<span class="sh-val">true</span></span>`, "coloured like a shell")
	assert.Contains(t, got, `hybernate enable deployment/api <span class="sh-flag">-n</span> preview-7`,
		"enable the one dry-run measured")
	assert.NotContains(t, got, "<script src", "nothing loads from elsewhere")
	assert.NotContains(t, got, "<link", "nothing loads from elsewhere")
}

// The summary's replay count says what it doesn't count, so it adds up to
// the workloads listed.
func TestWriteHTML_SaysWhatTheReplayDoesntCount(t *testing.T) {
	result := historyResult()
	result.Workloads = append(result.Workloads, discovery.Workload{Namespace: "reports", Name: "reports",
		Kind: v1alpha1.TargetKindStatefulSet, State: discovery.StateActive, WontPause: "PVCs deleted on scale-down"})

	got := renderHTML(t, result)

	assert.Contains(t, got, "<div>1 of 2<span>unmanaged workloads would have slept over the last 7 days; not "+
		"counted: 1 that won&#39;t pause (PVCs deleted on scale-down)</span></div>")
}

// Every place the report names the window says what was replayed, in the
// same words: the subtitle, the summary, and the glossary.
func TestWriteHTML_WindowReplayed(t *testing.T) {
	result := historyResult()
	result.History.Hours = 0.5

	got := renderHTML(t, result)

	assert.Contains(t, got, "Based on the last 30 minutes of Prometheus history")
	assert.Contains(t, got, "would have slept over the last 30 minutes")
	assert.Contains(t, got, "or from history over the last 30 minutes for an unmanaged one")
	assert.NotContains(t, got, "1 hours")
}

// While Hybernate manages none of the workloads, every dependency would say
// the same, so the report says it once, without a status column.
func TestWriteHTML_DependenciesBeforeAnythingIsManaged(t *testing.T) {
	result := sampleCluster()
	result.Workloads[0].Dependencies = []discovery.Dependency{{Namespace: "preview-42",
		Kind: v1alpha1.TargetKindStatefulSet, Name: "postgres", Via: "PGHOST"}}

	got := renderHTML(t, result)

	assert.Contains(t, got, "What each workload connects to. Once Hybernate manages both ends of one, it wakes the "+
		"dependency with the workload that needs it, and keeps it up while that workload runs.")
	assert.NotContains(t, got, ">Status</button>")
	assert.NotContains(t, got, "connected once")
}

// A workload that ran four pods all week and runs one now could free more
// a month than it costs now. The share is of what it cost over the same
// history, so it's never over 100%.
func TestHeadlineFor_FreeableShareIsOfTheirOwnCost(t *testing.T) {
	totals := discovery.Totals{Workloads: 1, MonthlyCost: 36.5, FreeableBasis: 146,
		Replayed: discovery.ReplayTotals{Workloads: 1, Sleepers: 1, MonthlyFreed: 145}}

	h, _ := headlineFor(scanResult{ClusterReport: &discovery.ClusterReport{Mode: discovery.ModeHistory,
		History: &discovery.HistorySource{Hours: 168}, Totals: totals}})

	require.Len(t, h.Figures, 2)
	assert.Equal(t, "$145", h.Figures[0].Value)
	assert.Equal(t, "99% of what these workloads cost over the same time", h.Figures[0].Detail)
}

// Before Hybernate has paused anything, the headline is the opportunity and
// the cost it comes from, not a zero for something nobody has done yet.
func TestHeadlineFor_BeforeAnythingIsLive(t *testing.T) {
	result := historyResult()
	result.Workloads = result.Workloads[:2]
	result.Workloads[1].Managed, result.Workloads[1].DryRun = false, false
	result.Totals.Live, result.Totals.FreedThisMonth, result.Totals.SavedThisMonth = 0, 0, 0
	result.Totals.Workloads = 2

	h, facts := headlineFor(result)

	require.Len(t, h.Figures, 2)
	assert.Equal(t, figure{Value: "$179", Label: "could be freed a month",
		Detail: "40% of what these workloads cost over the same time"}, h.Figures[0], "the opportunity first")
	assert.Equal(t, figure{Value: "$446", Label: "a month, what these workloads cost while running"}, h.Figures[1],
		"the cost it comes from beside it")
	assert.Equal(t, fact{Value: "2", Label: "workloads in 2 namespaces"}, facts[0],
		"the cost moves to the headline; the scale stays")
	for _, f := range h.Figures {
		assert.NotEqual(t, "freed by Hybernate this month", f.Label)
	}
}

// With too little history, could-free is zero because of it, and says so
// rather than being a bare $0; with what's idle now, that's the most
// pausing could free.
func TestHeadlineFor_TooLittleHistory(t *testing.T) {
	short := &discovery.HistorySource{Prometheus: "monitoring/prometheus-operated", Hours: 0.5, Partial: true}
	tests := []struct {
		name   string
		totals discovery.Totals
		want   figure
	}{
		{name: "nothing idle", totals: discovery.Totals{Workloads: 14, MonthlyCost: 30,
			Replayed: discovery.ReplayTotals{Workloads: 13}},
			want: figure{Value: "$0", Label: "could be freed a month", Detail: "too little history yet: Prometheus " +
				"has only 30 minutes of it; run the scan again after a few days"}},
		{name: "something idle now", totals: discovery.Totals{Workloads: 14, MonthlyCost: 30, Idle: 2,
			IdleMonthlyCost: 8, IdleHourlyCost: 0.011, Replayed: discovery.ReplayTotals{Workloads: 13}},
			want: figure{Value: "$8", Label: "could be freed a month, at most", Detail: "what the 2 workloads idle " +
				"right now cost while running, $0.01 an hour; too little history yet: Prometheus has only 30 " +
				"minutes of it; run the scan again after a few days"}},
		{name: "some would have slept", totals: discovery.Totals{Workloads: 14, MonthlyCost: 30, FreeableBasis: 30,
			Replayed: discovery.ReplayTotals{Workloads: 13, Sleepers: 1, MonthlyFreed: 3}},
			want: figure{Value: "$3", Label: "could be freed a month", Detail: "10% of what these workloads cost " +
				"over the same time; from only 30 minutes of history, so run the scan again after a few days for a " +
				"steadier figure"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := headlineFor(scanResult{ClusterReport: &discovery.ClusterReport{Mode: discovery.ModeHistory,
				History: short, Totals: tt.totals}})

			require.NotEmpty(t, h.Figures)
			assert.Equal(t, tt.want, h.Figures[0])
		})
	}
}

// A whole window in which nothing would have slept is a real zero, and the
// headline says what it's from; so is one moment with nothing idle.
func TestHeadlineFor_NothingToFree(t *testing.T) {
	replayed := scanResult{ClusterReport: &discovery.ClusterReport{Mode: discovery.ModeHistory,
		History: &discovery.HistorySource{Hours: 168}, Totals: discovery.Totals{Workloads: 3, MonthlyCost: 30,
			Replayed: discovery.ReplayTotals{Workloads: 3}}}}
	snapshot := scanResult{ClusterReport: &discovery.ClusterReport{Mode: discovery.ModeSnapshot,
		Totals: discovery.Totals{Workloads: 3, MonthlyCost: 30}}}

	h, _ := headlineFor(replayed)
	assert.Equal(t, "no unmanaged workload would have slept over the last 7 days", h.Figures[0].Detail)
	replayed.Totals.Idle, replayed.Totals.IdleMonthlyCost = 1, 120
	h, _ = headlineFor(replayed)
	assert.Equal(t, figure{Value: "$0", Label: "could be freed a month",
		Detail: "no unmanaged workload would have slept over the last 7 days"}, h.Figures[0],
		"a week of history says more than what's idle this moment")
	h, _ = headlineFor(snapshot)
	assert.Equal(t, "$0", h.Figures[0].Value)
	assert.Contains(t, h.Figures[0].Detail, "nothing is idle right now")
}

// Once Hybernate has paused something, what it freed leads, and the
// opportunity follows only when there's one.
func TestHeadlineFor_Live(t *testing.T) {
	result := scanResult{ClusterReport: &discovery.ClusterReport{Mode: discovery.ModeSnapshot,
		Totals: discovery.Totals{Workloads: 3, MonthlyCost: 30, Live: 1, FreedThisMonth: 12, SavedThisMonth: 5}}}

	h, facts := headlineFor(result)

	require.Len(t, h.Figures, 1, "no zero opportunity beside what was freed")
	assert.Equal(t, "freed by Hybernate this month", h.Figures[0].Label)
	assert.Equal(t, "a month, what these workloads cost while running", facts[0].Label)
}

func TestWriteHTML_ByNamespace(t *testing.T) {
	page := buildReport(historyResult(), time.Hour)

	require.NotEmpty(t, page.Namespaces)
	assert.Equal(t, "preview-42", page.Namespaces[0].Namespace, "most freed first")
	assert.Equal(t, "$139", page.Namespaces[0].CouldFree)
	for _, ns := range page.Namespaces {
		if ns.Namespace == "preview-3" {
			assert.Equal(t, "$0", ns.Cost, "a paused workload isn't costing anything")
		}
		if ns.Namespace == "preview-7" {
			assert.Equal(t, "$40", ns.CouldFree, "dry-run's could-free is what Hybernate measured")
		}
	}
}

// A workload Hybernate already pauses isn't counted in what pausing would
// free, by namespace as in the headline.
func TestWriteHTML_ByNamespaceLeavesOutManaged(t *testing.T) {
	result := historyResult()
	web := &result.Workloads[2]
	require.Equal(t, "web", web.Name)
	web.History = &discovery.History{Hours: 168, RunningHours: 40, SleepHours: 30, MonthlyFreed: 99}

	page := buildReport(result, time.Hour)

	for _, ns := range page.Namespaces {
		if ns.Namespace == "preview-3" {
			assert.Equal(t, "$0", ns.CouldFree)
		}
	}
}

// Each money cell has one source: what Hybernate freed and saved, for a
// live workload; what dry-run measured, for one in dry-run; what history shows,
// for an unmanaged one.
func TestWriteHTML_OneSourcePerCell(t *testing.T) {
	page := buildReport(historyResult(), time.Hour)

	byName := map[string]workloadRow{}
	for _, row := range page.Workloads {
		byName[row.Workload] = row
	}
	postgres, api, web := byName["statefulset/postgres"], byName["deployment/api"], byName["deployment/web"]
	assert.Equal(t, []string{"167h", "0", "-", "$139"}, []string{postgres.CouldSleep, postgres.Wakes, postgres.Freed,
		postgres.CouldFree}, "unmanaged: history")
	assert.Equal(t, []string{"41h", "4", "-", "$40"}, []string{api.CouldSleep, api.Wakes, api.Freed, api.CouldFree},
		"dry-run: measured, not history")
	assert.Equal(t, []string{"-", "-", "$4", "$1", "-"}, []string{web.CouldSleep, web.Wakes, web.Freed, web.Saved,
		web.CouldFree}, "live: what it freed, and saved")
	assert.Less(t, web.CouldSleepSort, byName["deployment/ledger"].CouldSleepSort, "a dash sorts below 0h")
}

func TestWriteHTML_Snapshot(t *testing.T) {
	result := sampleCluster()
	result.ScannedAt = time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	result.Settings = settings{CPUThreshold: 10}
	result.Prices = prices{CPUPerHour: 0.045, MemoryPerHour: 0.006}

	got := renderHTML(t, result)

	assert.Contains(t, got, "A snapshot of CPU at the time of the scan, without history.")
	assert.Contains(t, got, "Costs use your prices: $0.045 per vCPU-hour and $0.006 per GiB-hour of memory.")
	assert.NotContains(t, got, "freed by Hybernate this month", "nothing is live, so no zero leads")
	assert.Contains(t, got, `<div class="figure"><b>$1,240</b><span>could be freed a month, at most</span>`+
		`<small>what the 1 workload idle right now costs while running, $1.70 an hour</small></div>`)
	assert.Contains(t, got, `<div class="figure"><b>$1,336</b><span>a month, what these workloads cost while `+
		`running</span></div>`, "beside the cost it comes from")
	assert.Contains(t, got, "<div>$1.70<span>an hour, what idle workloads cost while running</span></div>",
		"no history, so no wakes: the hourly cost of idle in its place")
	assert.Contains(t, got, "this report doesn&#39;t estimate what pausing would free",
		"one moment can't show what pausing would free")
	assert.NotContains(t, got, "Could free / month", "no history, no dry-run: nothing to say it could free")
	assert.NotContains(t, got, "Freed this month", "nothing live")
}

func TestWriteHTMLFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.html")

	written, err := writeHTMLFile(path, historyResult(), time.Hour)

	require.NoError(t, err)
	assert.Equal(t, path, written)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "Workload scan")
	_, err = writeHTMLFile(filepath.Join(t.TempDir(), "missing", "report.html"), historyResult(), time.Hour)
	assert.Error(t, err)
}

// The report names the cluster's workloads, so a temporary one is a new
// file only the user can read, never one someone else made or linked.
func TestWriteHTMLFile_Temporary(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	predictable := filepath.Join(dir, "hybernate-scan-20261003-090000.html")
	target := filepath.Join(dir, "elsewhere")
	require.NoError(t, os.Symlink(target, predictable))

	first, err := writeHTMLFile("", historyResult(), time.Hour)
	require.NoError(t, err)
	second, err := writeHTMLFile("", historyResult(), time.Hour)
	require.NoError(t, err)

	assert.NotEqual(t, first, second, "each scan its own file")
	assert.NotEqual(t, predictable, first)
	assert.NoFileExists(t, target, "a planted link isn't followed")
	info, err := os.Stat(first)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	assert.Regexp(t, `^hybernate-scan-\d+\.html$`, filepath.Base(first))
}

// A page written over a file the user can share is made theirs alone, as a
// new one is: it names the cluster's workloads.
func TestWriteHTMLFile_OverAnExistingFile(t *testing.T) {
	if runtime.GOOS == goosWindows {
		t.Skip("Windows files have no mode bits for others")
	}
	path := filepath.Join(t.TempDir(), "report.html")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o644))

	_, err := writeHTMLFile(path, historyResult(), time.Hour)

	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// The report opens where the user sits: not on a Mac or Windows machine
// they've reached over SSH, nor on Linux without a display, which X
// forwarding gives an SSH session.
// An opener that runs the browser in the foreground, as xdg-open does
// without a desktop environment, has opened the report, not failed: it's
// left running rather than killed and reported as a failure. One that
// exits with an error has failed.
func TestStartOpener(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("uses sleep, true and false")
	}
	start := time.Now()
	assert.NoError(t, startOpener(exec.Command("sleep", "10"), 50*time.Millisecond), "the browser, still open")
	assert.Less(t, time.Since(start), 5*time.Second, "not waited for")
	assert.NoError(t, startOpener(exec.Command("true"), 5*time.Second))
	assert.Error(t, startOpener(exec.Command("false"), 5*time.Second))
	assert.Error(t, startOpener(exec.Command("no-such-opener-anywhere"), 5*time.Second))
}

func TestCanBrowseOn(t *testing.T) {
	tests := []struct {
		name string
		goos string
		env  map[string]string
		want bool
	}{
		{name: "a Mac", goos: "darwin", want: true},
		{name: "a Mac over SSH", goos: "darwin", env: map[string]string{"SSH_CONNECTION": "10.0.0.2 51234 10.0.0.9 22"}},
		{name: "Windows over SSH", goos: "windows", env: map[string]string{"SSH_TTY": "/dev/pts/0"}},
		{name: "Windows", goos: "windows", want: true},
		{name: "Linux without a display", goos: "linux"},
		{name: "a Linux desktop", goos: "linux", env: map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, want: true},
		{name: "Linux over SSH with X forwarding", goos: "linux",
			env: map[string]string{"SSH_TTY": "/dev/pts/0", "DISPLAY": "localhost:10.0"}, want: true},
		{name: "another OS", goos: "plan9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, canBrowseOn(tt.goos, func(k string) string { return tt.env[k] }))
		})
	}
}

func TestWriteReport(t *testing.T) {
	result := historyResult()
	tests := []struct {
		name        string
		opts        scanOptions
		terminal    bool
		openFails   bool
		wantFile    bool
		wantOpened  bool
		wantMessage string
	}{
		{name: "a terminal opens it from a temporary file", opts: scanOptions{output: "table", open: true},
			terminal: true, wantFile: true, wantOpened: true, wantMessage: "opened in your browser; --open=false to skip"},
		{name: "--open=false doesn't", opts: scanOptions{output: "table"}, terminal: true},
		{name: "piped output doesn't", opts: scanOptions{output: "table", open: true}},
		{name: "JSON doesn't", opts: scanOptions{output: "json", open: true}, terminal: true},
		{name: "--html saves it anyway", opts: scanOptions{output: "json", html: "report.html"},
			wantFile: true, wantMessage: "Report: "},
		{name: "a browser that won't open is reported", opts: scanOptions{output: "table", open: true},
			terminal: true, openFails: true, wantFile: true, wantOpened: true, wantMessage: "couldn't open a browser"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("TMPDIR", dir)
			if tt.opts.html != "" {
				tt.opts.html = filepath.Join(dir, tt.opts.html)
			}
			var opened string
			restoreBrowse, restoreInteractive := browse, interactive
			t.Cleanup(func() { browse, interactive = restoreBrowse, restoreInteractive })
			interactive = func(io.Writer) bool { return tt.terminal }
			browse = func(path string) error {
				opened = path
				if tt.openFails {
					return errors.New("no browser")
				}
				return nil
			}
			var stderr bytes.Buffer

			require.NoError(t, writeReport(&bytes.Buffer{}, &stderr, result, tt.opts))

			files, err := filepath.Glob(filepath.Join(dir, "*.html"))
			require.NoError(t, err)
			if !tt.wantFile {
				assert.Empty(t, files)
				assert.Empty(t, stderr.String())
				return
			}
			require.Len(t, files, 1)
			assert.Contains(t, stderr.String(), tt.wantMessage)
			assert.Contains(t, stderr.String(), files[0])
			if tt.wantOpened {
				assert.Equal(t, files[0], opened)
				assert.Regexp(t, `^hybernate-scan-\d+\.html$`, filepath.Base(files[0]))
			} else {
				assert.Empty(t, opened)
			}
		})
	}
}

// Hybernate doesn't record how long it has had live workloads paused yet,
// so the column only appears once it does.
func TestWriteHTML_SleptThisMonth(t *testing.T) {
	assert.NotContains(t, renderHTML(t, historyResult()), "Slept this month", "no data, no column")

	result := historyResult()
	result.Workloads[2].Slept = &discovery.Slept{Since: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		Hours: 61, Wakes: 3}

	got := renderHTML(t, result)

	assert.Contains(t, got, "Slept this month")
	assert.Contains(t, got, `61h<span class="sub">3 wakes<br>since Oct 1</span>`)
	assert.Contains(t, got, "<dt>Slept</dt>")
}

func TestRoundedDuration(t *testing.T) {
	tests := map[time.Duration]string{
		0:                                     "0s",
		10 * time.Second:                      "10s",
		30 * time.Second:                      "30s",
		time.Minute + 10*time.Second:          "1m10s",
		time.Hour:                             "1h",
		90 * time.Minute:                      "1h30m",
		2*time.Hour + 5*time.Second:           "2h5s",
		20 * time.Minute:                      "20m",
		48 * time.Hour:                        "48h",
		1500 * time.Millisecond:               "2s",
		-time.Hour:                            "-1h",
		time.Hour + time.Minute + time.Second: "1h1m1s",
	}
	for in, want := range tests {
		assert.Equal(t, want, roundedDuration(in), in.String())
	}
}
