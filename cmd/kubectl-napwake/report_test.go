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
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/discovery"
	"github.com/napwake/napwake/internal/nodescale"
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
	assert.Contains(t, got, `<div class="figure"><b>$4</b><span>freed by Napwake this month</span>`+
		`<small>from the 1 live workload it pauses; $1 of it saved as nodes scaled down</small></div>`)
	assert.Contains(t, got, `<p class="prices">won&#39;t drop until something removes nodes, and no node autoscaler `+
		`was found. <a href="https://docs.napwake.dev/guides/cluster-autoscaler/">How to fix it</a></p>`)
	assert.Contains(t, got, `<div class="figure"><b>$179</b><span>could be freed a month</span>`+
		`<small>40% of what these workloads cost over the same time</small></div>`,
		"what history shows for unmanaged workloads, plus what dry-run measured, against what they cost")
	assert.NotContains(t, got, "Had Napwake been pausing them", "the figures and facts say it")
	assert.Contains(t, got, "It&#39;s idle once it has had no activity for 1h")
	assert.NotContains(t, got, "Napwake Hub", "it has no page to send anyone to yet")
	tip := "Hours Napwake would have had it paused, measured since dry-run started for a dry-run workload, or " +
		"from history over the last 7 days for an unmanaged one. Under it, how many times activity would have " +
		"woken it, each a wait for someone."
	assert.Contains(t, got, `class="tip" title="`+tip+`">Could sleep</button>`, "the definition is on the header")
	assert.Contains(t, got, "<dt>Could sleep</dt><dd>"+tip+"</dd>", "and kept in the method section for print")
	assert.NotContains(t, got, "Quiet")
	assert.Contains(t, got, "<div>$446<span>a month, what these workloads cost while running</span></div>")
	assert.Contains(t, got, "<div>1 of 2<span>unmanaged workloads would have slept over the last 7 days</span></div>")
	assert.NotContains(t, got, "wakes over", "wakes are per workload, in the table")
	assert.Contains(t, got, "<div>2 of 5<span>managed by Napwake: 1 live, 1 in dry-run</span></div>")
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
		"a dry-run workload's dependency Napwake doesn't manage")
	assert.Contains(t, got, "<th><button type=\"button\">Status</button></th>", "a managed workload's need a status")
	assert.NotContains(t, got, "napwake.dev/depends-on=", "Napwake connects dependencies itself")
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
		`<span class="sh-flag">-n</span> preview-42 <span class="sh-key">napwake.dev/managed</span>=`+
		`<span class="sh-val">true</span></span>`, "coloured like a shell")
	assert.Contains(t, got, `napwake enable deployment/api <span class="sh-flag">-n</span> preview-7`,
		"enable the one dry-run measured")
	assertLoadsNothingFromElsewhere(t, got)
	assert.Contains(t, got, `<link rel="icon" type="image/svg+xml" href="data:image/svg+xml;base64,`,
		"its tab shows the doorman, drawn inline")
}

// assertLoadsNothingFromElsewhere checks a page is one file: no script
// from a URL, and anything it links, such as its tab icon, inline.
func assertLoadsNothingFromElsewhere(t *testing.T, page string) {
	t.Helper()
	assert.NotContains(t, page, "<script src", "nothing loads from elsewhere")
	for _, link := range regexp.MustCompile(`<link[^>]*>`).FindAllString(page, -1) {
		assert.Contains(t, link, `href="data:`, "a link is drawn inline, not loaded from elsewhere: %s", link)
	}
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

// While Napwake manages none of the workloads, every dependency would say
// the same, so the report says it once, without a status column.
func TestWriteHTML_DependenciesBeforeAnythingIsManaged(t *testing.T) {
	result := sampleCluster()
	result.Workloads[0].Dependencies = []discovery.Dependency{{Namespace: "preview-42",
		Kind: v1alpha1.TargetKindStatefulSet, Name: "postgres", Via: "PGHOST"}}

	got := renderHTML(t, result)

	assert.Contains(t, got, "What each workload connects to. Once Napwake manages both ends of one, it wakes the "+
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

// Before Napwake has paused anything, the headline is the opportunity and
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
		Detail: "40% of what these workloads cost over the same time", Kind: figureCouldFree, Amount: 179}, h.Figures[0],
		"the opportunity first")
	assert.Equal(t, figure{Value: "$446", Label: "a month, what these workloads cost while running",
		Kind: figureCost, Amount: 446}, h.Figures[1], "the cost it comes from beside it")
	assert.Equal(t, fact{Value: "2", Label: "workloads in 2 namespaces"}, facts[0],
		"the cost moves to the headline; the scale stays")
	for _, f := range h.Figures {
		assert.NotEqual(t, "freed by Napwake this month", f.Label)
	}
}

// With too little history, could-free is zero because of it, and says so
// rather than being a bare $0; with what's idle now, that's the most
// pausing could free.
func TestHeadlineFor_TooLittleHistory(t *testing.T) {
	short := &discovery.HistorySource{Prometheus: "monitoring/prometheus-operated", Hours: 0.5, Partial: true}
	tests := []struct {
		name      string
		totals    discovery.Totals
		want      figure
		wantShort string
	}{
		{name: "nothing idle", totals: discovery.Totals{Workloads: 14, MonthlyCost: 30,
			Replayed: discovery.ReplayTotals{Workloads: 13}},
			want: figure{Value: "$0", Label: "could be freed a month", Detail: "too little history yet: Prometheus " +
				"has only 30 minutes of it; run the scan again after a few days", Kind: figureCouldFree},
			wantShort: "Prometheus has only 30 minutes of it; run the scan again after a few days."},
		{name: "something idle now", totals: discovery.Totals{Workloads: 14, MonthlyCost: 30, Idle: 2,
			IdleMonthlyCost: 8, IdleHourlyCost: 0.011, Replayed: discovery.ReplayTotals{Workloads: 13}},
			want: figure{Value: "$8", Label: "could be freed a month, at most", Detail: "what the 2 workloads idle " +
				"right now cost while running, $0.01 an hour; too little history yet: Prometheus has only 30 " +
				"minutes of it; run the scan again after a few days", Kind: figureCouldFree, Amount: 8},
			wantShort: "Prometheus has only 30 minutes of it; run the scan again after a few days."},
		{name: "some would have slept", totals: discovery.Totals{Workloads: 14, MonthlyCost: 30, FreeableBasis: 30,
			Replayed: discovery.ReplayTotals{Workloads: 13, Sleepers: 1, MonthlyFreed: 3}},
			want: figure{Value: "$3", Label: "could be freed a month", Detail: "10% of what these workloads cost " +
				"over the same time; from only 30 minutes of history, so run the scan again after a few days for a " +
				"steadier figure", Kind: figureCouldFree, Amount: 3},
			wantShort: "From only 30 minutes of history, so run the scan again after a few days for a steadier " +
				"figure."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := headlineFor(scanResult{ClusterReport: &discovery.ClusterReport{Mode: discovery.ModeHistory,
				History: short, Totals: tt.totals}})

			require.NotEmpty(t, h.Figures)
			assert.Equal(t, tt.want, h.Figures[0])
			assert.Equal(t, tt.wantShort, h.ShortHistory, "the caveat, as a sentence of its own")

			live := tt.totals
			live.Live, live.FreedThisMonth = 1, 5
			h, _ = headlineFor(scanResult{ClusterReport: &discovery.ClusterReport{Mode: discovery.ModeHistory,
				History: short, Totals: live}})
			assert.Empty(t, h.ShortHistory, "what was freed leads, and it's measured")
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
		Detail: "no unmanaged workload would have slept over the last 7 days", Kind: figureCouldFree}, h.Figures[0],
		"a week of history says more than what's idle this moment")
	h, _ = headlineFor(snapshot)
	assert.Equal(t, "$0", h.Figures[0].Value)
	assert.Contains(t, h.Figures[0].Detail, "nothing is idle right now")
}

// Once Napwake has paused something, what it freed leads, and the
// opportunity follows only when there's one.
func TestHeadlineFor_Live(t *testing.T) {
	result := scanResult{ClusterReport: &discovery.ClusterReport{Mode: discovery.ModeSnapshot,
		Totals: discovery.Totals{Workloads: 3, MonthlyCost: 30, Live: 1, FreedThisMonth: 12, SavedThisMonth: 5}}}

	h, facts := headlineFor(result)

	require.Len(t, h.Figures, 1, "no zero opportunity beside what was freed")
	assert.Equal(t, "freed by Napwake this month", h.Figures[0].Label)
	assert.Equal(t, figureFreed, h.Figures[0].Kind, "a page tells it's live by the kind")
	assert.InDelta(t, 12, h.Figures[0].Amount, 0.001)
	assert.Equal(t, "a month, what these workloads cost while running", facts[0].Label)
}

// A live workload Napwake never pauses is managed, but not counted among
// those it pauses, in the report as in the terminal.
func TestHeadlineFor_LiveProtected(t *testing.T) {
	result := scanResult{ClusterReport: &discovery.ClusterReport{Mode: discovery.ModeSnapshot,
		Totals: discovery.Totals{Workloads: 3, Live: 1, LiveProtected: 1, FreedThisMonth: 0.03}}}

	h, facts := headlineFor(result)

	assert.Equal(t, "<$1", h.Figures[0].Value)
	assert.Equal(t, "from the 1 live workload it pauses; $0 of it saved as nodes scaled down", h.Figures[0].Detail)
	assert.Equal(t, fact{Value: "2 of 3", Label: "managed by Napwake: 1 live, 0 in dry-run, 1 live but protected, " +
		"so never paused"}, facts[len(facts)-1])
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
			assert.Equal(t, "$40", ns.CouldFree, "dry-run's could-free is what Napwake measured")
		}
	}
}

// A workload Napwake already pauses isn't counted in what pausing would
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

// Each money cell has one source: what Napwake freed and saved, for a
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
	assert.Contains(t, got, `<p class="prices">yours, $0.045 per vCPU-hour and $0.006 per GiB-hour</p>`)
	assert.NotContains(t, got, "freed by Napwake this month", "nothing is live, so no zero leads")
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

// With no Napwake found, the report's next steps start by installing it, as
// the terminal's do: the strip and the section say so, the install command
// comes first, marked apart, and the page gives it a block of its own,
// numbered before measuring.
func TestWriteHTML_InstallFirstWhenNapwakeIsntFound(t *testing.T) {
	releasedAs(t, "v0.2.0")
	result := sampleCluster()

	steps := buildReport(result, time.Hour).NextSteps

	require.NotNil(t, steps)
	require.NotNil(t, steps.Install)
	assert.Equal(t, command{Line: shellHTML("helm install napwake oci://ghcr.io/napwake/charts/napwake --version " +
		"0.2.0 --namespace napwake-system --create-namespace -f napwake-values.yaml")}, *steps.Install,
		"the terminal's command, for no one workload")

	got := renderHTML(t, result)
	const lead = "Install Napwake if the cluster doesn&#39;t have it yet, then opt a workload in. Measure it first, " +
		"which pauses nothing, then let Napwake pause it once the measurements look right."
	pageHas(t, got, "<span>"+lead+`</span><a href="#next">See the commands</a>`, "the strip")
	pageHas(t, got, "<p>"+lead+"</p>", "the section")
	pageLacks(t, got, "Opt a workload in, measuring first")
	pageHas(t, got, `<pre class="shell"><span data-step="install"><span class="line"><span class="sh-cmd">helm</span> `+
		`install napwake oci://ghcr.io/napwake/charts/napwake <span class="sh-flag">--version</span> 0.2.0 `,
		"the install command first, marked apart")
	install, measure := strings.Index(got, `<span data-step="install">`), strings.Index(got, "</span> annotate ")
	enable := strings.Index(got, `<span data-step="enable">`)
	assert.True(t, install < measure && measure < enable, "install, then measure, then go live")

	pageHas(t, reportHTML, `if (install.length) block(++n + ". Install Napwake, if the cluster doesn't have it yet", `+
		`["Put your users' time zone in ", ["code", "napwake-values.yaml"], " first, such as ", `+
		`["code", "timezone: Europe/London"], ". The ", `+
		`["a", "installation guide", "https://docs.napwake.dev/getting-started/installation/"], " has the rest."], `+
		`install);`, "the install block, its code as code and the guide linked")
	pageHas(t, reportHTML, `block(++n + ". Measure first", "In dry-run, Napwake only measures. Nothing is paused.", `+
		`measure);`)
	pageHas(t, reportHTML, `block(++n + ". When the measurements look right, let Napwake pause it", `+
		`"Run this only after step " + (n - 1) + " has measured for a while.", live);`,
		"numbered after the install block, when there is one")
}

// With Napwake found, the report's next steps start with measuring, and the
// strip and the section say what they always have.
func TestWriteHTML_NoInstallStepWhenNapwakeIsFound(t *testing.T) {
	result := historyResult()

	steps := buildReport(result, time.Hour).NextSteps

	require.NotNil(t, steps)
	assert.Nil(t, steps.Install, "a workload Napwake manages shows it's installed")
	got := renderHTML(t, result)
	const lead = "Opt a workload in, measuring first so nothing is paused, then let Napwake pause it once the " +
		"measurements look right."
	pageHas(t, got, "<span>"+lead+`</span><a href="#next">See the commands</a>`, "the strip")
	pageHas(t, got, "<p>"+lead+"</p>", "the section")
	pageLacks(t, got, `<span data-step="install">`)
	pageLacks(t, got, "Install Napwake if the cluster")
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
	predictable := filepath.Join(dir, "napwake-scan-20261003-090000.html")
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
	assert.Regexp(t, `^napwake-scan-\d+\.html$`, filepath.Base(first))
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
	browser := exec.Command("sleep", "120")
	t.Cleanup(func() {
		if browser.Process != nil {
			_ = browser.Process.Kill() // it's left running by design, and only the test needs it gone
		}
	})
	start := time.Now()
	assert.NoError(t, startOpener(browser, 50*time.Millisecond), "the browser, still open")
	assert.Less(t, time.Since(start), promptly, "not waited for, the two minutes it stays open")
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
				assert.Regexp(t, `^napwake-scan-\d+\.html$`, filepath.Base(files[0]))
			} else {
				assert.Empty(t, opened)
			}
		})
	}
}

// Napwake doesn't record how long it has had live workloads paused yet,
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

// The page doesn't offer what's idle but never paused as something still to
// free, and says why it's idle anyway (P50, P51).
func TestBuildReport_IdleNapwakeNeverPauses(t *testing.T) {
	page := buildReport(neverPausedIdle(), time.Hour)

	for _, f := range page.Headline.Figures {
		assert.NotEqual(t, figureCouldFree, f.Kind, "pausing can't free what Napwake never pauses: %+v", f)
	}
	var idle string
	for _, f := range page.Facts {
		if strings.HasPrefix(f.Label, "idle right now") {
			idle = f.Value + " " + f.Label
		}
	}
	assert.Equal(t, "1 idle right now, reserving 0.02 vCPU and 0.03 GiB of memory; Napwake never pauses it", idle)
}

// What scales a cluster's nodes, as the scan finds it.
var (
	karpenterFound = nodescale.Autoscaler{Scalers: []nodescale.Scaler{{Kind: nodescale.Karpenter}}}
	autopilotFound = nodescale.Autoscaler{Scalers: []nodescale.Scaler{{Kind: nodescale.GKEAutopilot}}}
	scalerUnknown  = nodescale.Autoscaler{Unread: []string{"Karpenter's NodePools"}}
	noScaler       = nodescale.Autoscaler{}
)

// couldResult is historyResult before anything is live, with what scales
// its nodes: dry-run measured and history shows $179 a month pausing could
// free, of the $446 the workloads cost on the same basis, 40%.
func couldResult(scales nodescale.Autoscaler) scanResult {
	result := historyResult()
	result.NodeAutoscaler = scales
	result.Workloads = slices.Delete(result.Workloads, 2, 3)
	result.Totals.Workloads, result.Totals.Paused, result.Totals.PausedHourlyCost = 4, 0, 0
	result.Totals.Live, result.Totals.FreedThisMonth, result.Totals.SavedThisMonth = 0, 0, 0
	return result
}

// shortHistoryResult is couldResult replayed over too little history for
// a steady figure.
func shortHistoryResult() scanResult {
	result := couldResult(karpenterFound)
	result.History.Hours, result.History.Partial = 0.5, true
	return result
}

// goodNewsResult is a scan with nothing idle and nothing to free.
func goodNewsResult() scanResult {
	return scanResult{Cluster: "c", ScannedAt: statusNow, ClusterReport: &discovery.ClusterReport{
		Mode: discovery.ModeSnapshot,
		Workloads: []discovery.Workload{{Namespace: "shop", Kind: v1alpha1.TargetKindDeployment, Name: "api",
			State: discovery.StateActive, Reason: "CPU 64% of its request", MonthlyCost: 30}},
		Totals: discovery.Totals{Workloads: 1, MonthlyCost: 30}}}
}

// reportPageParts are the parts of the scan page each lead changes: the
// before and after's block, empty without one, the share card's data,
// and the Copy summary's first line.
type reportPageParts struct {
	lead, card, summary string
}

func reportPartsOf(t *testing.T, page string) reportPageParts {
	t.Helper()
	var parts reportPageParts
	parts.lead = regexp.MustCompile(`(?s)<div class="pace[ "].*?</div>`).FindString(page)
	parts.card = regexp.MustCompile(`<div hidden id="card-data"[^>]*></div>`).FindString(page)
	require.NotEmpty(t, parts.card, "the share card's data")
	summary := regexp.MustCompile(`(?s)<div hidden id="summary-lines">\s*<p>(.*?)</p>`).FindStringSubmatch(page)
	require.Len(t, summary, 2, "the Copy summary")
	parts.summary = summary[1]
	return parts
}

// Each lead the scan page can take, as the notes' states table has them
// (cmo-069): what pausing could free as a month's before and after, "40%
// less" where nodes scale down, with a note where the scan can't see what
// scales them, and "Not on the bill yet" where nothing does; and, as
// headlineFor has them, unchanged, what Napwake freed once it's live, the
// most pausing could free from a snapshot, nothing to free, and an
// estimate from too little history. Each checks the lead's words and
// figures, the share card's data, and the Copy summary's first line.
func TestReportPage_ScanStates(t *testing.T) {
	const at = "Workload scan<span data-private> of staging (EKS us-east-1)</span>, 3 October 2026, 09:00 UTC: "
	const couldFigure = "$179 could be freed a month (40% of what these workloads cost over the same time)."
	atMost := sampleCluster()
	atMost.ScannedAt = historyResult().ScannedAt
	tests := []struct {
		name   string
		result scanResult
		could  *could
		// lead is the before and after's block, or "" for none.
		lead    string
		card    []string
		summary string
		also    []string
	}{
		{name: "could, with a node autoscaler found", result: couldResult(karpenterFound),
			could: &could{Without: "$446", With: "$267", Percent: 40},
			lead: `<div class="pace"><p class="pace-fig"><b>40% less</b><span>Paused when idle, these workloads could ` +
				`cost about $267 a month instead of $446.</span></p></div>`,
			card: []string{`data-pace="yes"`, `data-value="40% less"`,
				`data-label="Paused when idle, these workloads could cost about $267 a month instead of $446"`,
				`data-detail=""`},
			summary: at + "paused when idle, these workloads could cost about $267 a month instead of $446, 40% less. " +
				couldFigure,
			also: []string{`<div class="hero-figs paced">`, `<b>$179</b><span>could be freed a month</span>`,
				`<b>$446</b><span>a month, what these workloads cost while running</span>`}},
		{name: "could, with what scales nodes unknown", result: couldResult(scalerUnknown),
			could: &could{Without: "$446", With: "$267", Percent: 40},
			lead: `<div class="pace"><p class="pace-fig"><b>40% less</b><span>Paused when idle, these workloads could ` +
				`cost about $267 a month instead of $446.</span></p><p class="pace-note">The bill drops by that much ` +
				`only as nodes scale down, and this scan can&#39;t see what scales them.</p></div>`,
			card: []string{`data-pace="yes"`, `data-value="40% less"`,
				`data-label="Paused when idle, these workloads could cost about $267 a month instead of $446"`},
			summary: at + "paused when idle, these workloads could cost about $267 a month instead of $446, 40% less. " +
				couldFigure},
		{name: "could, with no node autoscaler", result: couldResult(noScaler),
			could: &could{Without: "$446", With: "$267", Percent: 40},
			lead: `<div class="pace possible"><p class="pace-k label">Not on the bill yet</p><p class="pace-fig"><span>` +
				`Paused when idle, these workloads could free 40% of what they cost.</span></p><p class="pace-note">` +
				`Nothing removes nodes on this cluster yet, so the bill wouldn&#39;t drop until something does.</p></div>`,
			card: []string{`data-value="$179"`, `data-label="could be freed a month"`,
				`data-detail="40% of what these workloads cost over the same time"`},
			summary: at + "paused when idle, these workloads could free 40% of what they cost, once nodes scale down. " +
				couldFigure},
		{name: "live", result: historyResult(),
			card: []string{`data-value="$4"`, `data-label="freed by Napwake this month"`,
				`data-more="$179 more could be freed a month"`},
			summary: at + "$4 freed by Napwake this month (from the 1 live workload it pauses; $1 of it saved as " +
				"nodes scaled down).",
			also: []string{`<div class="hero-figs live">`}},
		{name: "at most", result: atMost,
			card: []string{`data-value="$1,240"`, `data-label="could be freed a month, at most"`},
			summary: "Workload scan<span data-private> of staging</span>, 3 October 2026, 09:00 UTC: $1,240 could be " +
				"freed a month, at most (what the 1 workload idle right now costs while running, $1.70 an hour).",
			also: []string{`<div class="hero-figs">`}},
		{name: "nothing to free", result: goodNewsResult(),
			card: []string{`data-statement="Every workload is in use."`, `data-value="$0"`,
				`data-label="could be freed a month"`},
			summary: "Workload scan<span data-private> of c</span>, 5 October 2026, 12:00 UTC: $0 could be freed a " +
				"month (nothing is idle right now; Prometheus history, or measuring in dry-run, shows how often " +
				"workloads are).",
			also: []string{`<div class="hero-figs quiet">`}},
		{name: "short history", result: shortHistoryResult(),
			card: []string{`data-value="$179"`, `data-label="could be freed a month"`},
			summary: at + "$179 could be freed a month (40% of what these workloads cost over the same time; from only " +
				"30 minutes of history, so run the scan again after a few days for a steadier figure).",
			also: []string{`data-short-history="From only 30 minutes of history, so run the scan again after a few ` +
				`days for a steadier figure."`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.could, buildReport(tt.result, time.Hour).Could)

			page := renderHTML(t, tt.result)
			got := reportPartsOf(t, page)

			assert.Equal(t, tt.lead, got.lead, "the before and after")
			for _, passage := range tt.card {
				assert.Contains(t, got.card, passage, "the share card")
			}
			if tt.lead == "" || strings.Contains(tt.lead, "possible") {
				assert.NotContains(t, got.card, "data-pace", "the card is the one it was")
			}
			assert.Equal(t, tt.summary, got.summary, "the Copy summary's first line")
			for _, passage := range tt.also {
				pageHas(t, page, passage)
			}
		})
	}
}

// The scan says "less" only where the bill can drop by it (cmo-069): where
// something scales nodes, or pods are billed, and where it can't see what
// scales them, with a note that says so. With nothing to remove nodes, the
// same share is what pausing could free, "Not on the bill yet", and no
// "less" anywhere it leads: the page, its card or its summary.
func TestReportPage_LessOnlyWhereTheBillCanDrop(t *testing.T) {
	const unseen = "this scan can&#39;t see what scales them"
	for name, scales := range map[string]nodescale.Autoscaler{"Karpenter": karpenterFound,
		"GKE Autopilot": autopilotFound, "what scales nodes unknown": scalerUnknown} {
		t.Run(name, func(t *testing.T) {
			got := reportPartsOf(t, renderHTML(t, couldResult(scales)))

			assert.Contains(t, got.lead, "<b>40% less</b>")
			assert.Contains(t, got.card, `data-value="40% less"`)
			assert.Contains(t, got.summary, ", 40% less.")
			if scales.Known() {
				assert.NotContains(t, got.lead, unseen)
			} else {
				assert.Contains(t, got.lead, unseen, "a note that the scan can't see what scales them")
			}
		})
	}

	t.Run("nothing removes nodes", func(t *testing.T) {
		got := reportPartsOf(t, renderHTML(t, couldResult(noScaler)))

		assert.Contains(t, got.lead, `<p class="pace-k label">Not on the bill yet</p>`)
		assert.Contains(t, got.lead, "could free 40% of what they cost")
		for part, text := range map[string]string{"lead": got.lead, "card": got.card, "summary": got.summary} {
			assert.NotContains(t, text, " less", "no less in the %s", part)
		}
	})
}

// dollarsRange is the amounts an amount dollars wrote stands for, from lo
// up to hi: $0 is none, <$1 above it but under 50 cents, and whole dollars
// the half dollar either side.
func dollarsRange(t *testing.T, written string) (lo, hi float64) {
	t.Helper()
	switch written {
	case "$0":
		return 0, 0
	case "<$1":
		return 0, 0.5
	}
	n, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimPrefix(written, "$"), ",", ""), 64)
	require.NoError(t, err, "dollars wrote %q", written)
	return n - 0.5, n + 0.5
}

// The scan's before and after always agrees with itself, and with the
// figure under it (cmo-069): for every basis and every amount pausing could
// free, from a cent to a million dollars a month and from none to more
// than the basis, Percent is the share the could-free figure's detail
// gives, Without is the share's own basis, not what the workloads cost now,
// With is never below zero, and read back as written, With and Without give
// Percent, within what writing them in whole dollars rounds away. Where
// that can't hold, a share under 1% or over 100%, there's none.
func TestCouldFor_PercentWithAndWithoutAgree(t *testing.T) {
	share := regexp.MustCompile(`^(\d+)% of `)
	checked := 0
	for e := -2.0; e <= 6; e += 0.25 {
		for _, k := range []float64{1, 1.37, 2.9, 7.3} {
			basis := float64(math.Pow(10, e) * k)
			for _, part := range []float64{0, 0.001, 0.0049, 0.005, 0.0051, 0.01, 0.1, 1.0 / 3, 0.4, 0.5, 0.73, 0.994,
				0.995, 0.999, 1, 1.004, 1.0049, 1.005, 1.006, 1.2, 4} {
				could := basis * part
				// Converted, each amount is rounded as it's stored, so a sum
				// below can't fuse with a product, as Go may, and differ from
				// the same sum of the stored amounts.
				replayed, measured := float64(could*0.7), float64(could*0.3)
				result := scanResult{ClusterReport: &discovery.ClusterReport{Mode: discovery.ModeHistory,
					History: &discovery.HistorySource{Hours: 168}, Totals: discovery.Totals{Workloads: 3,
						MonthlyCost: basis*0.6 + 17, FreeableBasis: basis, DryRun: 1,
						Replayed: discovery.ReplayTotals{Workloads: 2, Sleepers: 1, MonthlyFreed: replayed},
						Measured: discovery.Measured{MonthlyFreed: measured}}}}
				name := fmt.Sprintf("%v of %v", could, basis)
				h, _ := headlineFor(result)
				m := share.FindStringSubmatch(h.Figures[0].Detail)
				require.Len(t, m, 2, "%s: the figure gives its share: %q", name, h.Figures[0].Detail)
				detail, err := strconv.Atoi(m[1])
				require.NoError(t, err)

				got := couldFor(result, h)

				if detail < 1 || detail > 100 {
					assert.Nil(t, got, "%s: a share of %d%%", name, detail)
					continue
				}
				if !assert.NotNil(t, got, "%s: a share of %d%%", name, detail) {
					continue
				}
				checked++
				assert.Equal(t, detail, got.Percent, "%s: the figure's share", name)
				assert.Equal(t, dollars(basis), got.Without, "%s: on the share's own basis", name)
				if left := basis - (replayed + measured); left < basis*floatNoise {
					assert.Equal(t, "$0", got.With, "%s: none left", name)
				} else {
					assert.Equal(t, dollars(left), got.With, "%s", name)
				}
				assert.False(t, strings.HasPrefix(got.With, "-"), "%s: With below zero: %s", name, got.With)
				withLo, withHi := dollarsRange(t, got.With)
				withoutLo, withoutHi := dollarsRange(t, got.Without)
				most := 100 * (1 - withLo/withoutHi)
				least := math.Inf(-1)
				if withoutLo > 0 {
					least = 100 * (1 - withHi/withoutLo)
				}
				assert.True(t, float64(got.Percent) >= least-0.5-1e-9 && float64(got.Percent) <= most+0.5+1e-9,
					"%s: %d%% less, %s instead of %s, which give %.2f%% to %.2f%%", name, got.Percent, got.With,
					got.Without, least, most)
			}
		}
	}
	assert.Greater(t, checked, 1000, "the range is covered")
}
