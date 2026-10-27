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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

func renderStatusPage(t *testing.T, objs ...client.Object) string {
	t.Helper()
	return renderStatusPageAt(t, statusNow, objs...)
}

// renderStatusPageAt is renderStatusPage with status run at now.
func renderStatusPageAt(t *testing.T, now time.Time, objs ...client.Object) string {
	t.Helper()
	opts := statusOpts()
	opts.now = func() time.Time { return now }
	result, err := clusterStatus(context.Background(), newStatusClient(t, interceptor.Funcs{}, objs...), "preview-42",
		opts)
	require.NoError(t, err)
	result.Cluster = testCluster
	var out bytes.Buffer
	require.NoError(t, writeStatusHTML(&out, result, 24*time.Hour, now))
	return out.String()
}

// The page says what the terminal does, from the same status: the
// overview, freed and saved, dry-run's estimate, what needs attention, each
// workload, and recent pauses and wakes.
func TestWriteStatusHTML(t *testing.T) {
	got := renderStatusPage(t, statusCluster()...)

	assert.Contains(t, got, "<title>Napwake status: staging (EKS us-east-1)</title>")
	assert.Contains(t, got, "<span>staging (EKS us-east-1) &middot; 5 October 2026, 12:00 UTC</span>")
	assert.Contains(t, got, `<p class="lede">Napwake manages 4 workloads: 1 paused, 2 running, 1 resuming; 1 in `+
		`dry-run, never paused: pausing it would have freed $7 since Oct 1.</p>`)
	assert.Contains(t, got, `<p class="prices">won&#39;t drop until something removes nodes`)
	assert.Contains(t, got, `<p class="prices">Forecast time zone: Europe/London</p>`)
	assert.Contains(t, got, `<div class="figure"><b>$42.45</b><span>freed by pausing so far this month</span>`)
	assert.Contains(t, got, `<div class="figure"><b>$35.00</b><span>saved of it</span>`)
	assert.Contains(t, got, `<div class="figure"><b>$7</b><span>pausing would have freed</span>`+
		`<small>measured in dry-run since Oct 1, for 1 workload</small></div>`)
	assert.Contains(t, got, "<h2>Needs attention</h2>")
	assert.Contains(t, got, `<td class="ref">worker<span class="sub">preview-7</span></td>
        <td class="ref">GitOpsConflict</td>
        <td class="message">Argo CD set the replicas from Git</td>`)
	assert.Contains(t, got, `<td class="message">resuming for 25m: waiting for preview-42/postgres</td>`)
	assert.Contains(t, got, `<td class="ref">deployment/api<span class="sub">preview-42</span></td>`)
	assert.Contains(t, got,
		`<span class="state paused"><span class="dot"></span>paused</span><span class="sub">for 3h</span>`)
	assert.Contains(t, got, `<td>held awake while preview-7/worker uses it</td>`)
	assert.Contains(t, got, `$12<span class="sub">$10 saved</span>`)
	assert.Contains(t, got, `<span class="state active"><span class="dot"></span>running (dry-run)</span>`)
	assert.Contains(t, got, `<button type="button">Would free</button>`)
	assert.Contains(t, got, `<td class="n" data-v="7.4">$7</td>`)
	assert.Contains(t, got, `<td class="ref" data-v="1791190800">Oct 5, 09:00 UTC<span class="sub">3h ago</span></td>
        <td class="ref">api<span class="sub">preview-42</span></td>
        <td class="message">paused</td>`)
	assert.Contains(t, got, "over the last 24h")
	assertLoadsNothingFromElsewhere(t, got)
	assert.Contains(t, got, `<link rel="icon" type="image/svg+xml" href="data:image/svg+xml;base64,`,
		"its tab shows the doorman, drawn inline")
}

// Messages come from conditions and events that anyone who can write a
// ManagedWorkload or an event can set, so they're escaped, never markup.
// While pausing is off for some or all of the workloads, the page says so
// at its top, as the terminal does.
func TestWriteStatusHTML_SaysFirstThatPausingIsOff(t *testing.T) {
	for off, want := range map[int]string{
		4: "Pausing is off (pausing.enabled: false): nothing here pauses, and everything wakes.",
		1: "Pausing is off for 1 of these workloads (pausing.enabled: false on the install that manages them): " +
			"they pause nothing, and still wake.",
	} {
		got := renderStatusPage(t, withPausingOff(t, off)...)

		line := `<p class="callout" data-pausing-off><span><b>` + want + `</b></span></p>`
		require.Contains(t, got, line)
		assert.Less(t, strings.Index(got, line), strings.Index(got, `<div class="money`), "at the top, before the money")
	}
	assert.NotContains(t, renderStatusPage(t, statusCluster()...), "data-pausing-off")
}

func TestWriteStatusHTML_Escapes(t *testing.T) {
	w := statusWorkloadObj("shop", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning, time.Hour)
	w.Status.Conditions = []metav1.Condition{condition("GitOpsConflict", metav1.ConditionTrue, "PauseUndone",
		`<script>alert("x")</script>`)}

	got := renderStatusPage(t, w, statusEvent("shop", "api", "Paused", `api: <img src=x onerror=alert(1)>`, time.Minute))

	assert.NotContains(t, got, `<script>alert`)
	assert.NotContains(t, got, `<img src=x`)
	assert.Contains(t, got, `&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;`)
	assert.Contains(t, got, `&lt;img src=x onerror=alert(1)&gt;`)
}

// Under a cent freed, saved or measured in dry-run, which the operator
// writes <$0.01, is never $0 on the page, which says there was none: <$0.01
// in what's freed and saved so far this month, which the page writes to the
// cent, and <$1 in the rows and in what dry-run measured, which it writes in
// whole dollars, as the terminal does.
func TestWriteStatusHTML_UnderACent(t *testing.T) {
	api := statusWorkloadObj("preview-42", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhasePaused, time.Hour)
	api.Status.Cost = &v1alpha1.CostStatus{FreedThisMonth: "<$0.01", SavedThisMonth: "<$0.01"}
	web := statusWorkloadObj("preview-7", "web", v1alpha1.TargetKindDeployment, v1alpha1.PhaseIdle, time.Hour)
	web.Spec.DryRun = true
	web.Status.DryRun = &v1alpha1.DryRunStatus{Since: *before(3 * time.Hour), Pauses: 1, EstimatedFreed: "<$0.01"}

	got := renderStatusPage(t, api, web)

	assert.Contains(t, got, `<div class="figure"><b>&lt;$0.01</b><span>freed by pausing so far this month</span>`)
	assert.Contains(t, got, `<div class="figure"><b>&lt;$0.01</b><span>saved of it</span>`)
	assert.Contains(t, got, `<div class="figure"><b>&lt;$1</b><span>pausing would have freed</span>`)
	assert.Contains(t, got, "pausing it would have freed &lt;$1 since Oct 5.")
	assert.Regexp(t, `<td class="n" data-v="0\.00\d+">&lt;\$1<span class="sub">&lt;\$1 saved</span></td>`, got)
	assert.Regexp(t, `<td class="n" data-v="0\.00\d+">&lt;\$1</td>`, got, "what dry-run measured")
	assert.NotContains(t, got, "<b>$0</b>")
}

// With what was freed, or what came off the bill, under a cent, which the
// page shows as <$0.01, the meter says so in words (cmo-043, cmo-070), and
// the share card leaves out what it's of when freed is under a cent, as the
// page leaves out the bar. Every other case keeps its line, to the cent
// under $100, as the figures above it (cmo-069).
func TestWriteStatusHTML_MeterUnderACent(t *testing.T) {
	const unknown = cantListNodes
	tests := []struct {
		name       string
		costs      []v1alpha1.CostStatus
		want       string
		meterLabel string
	}{
		{name: "freed under a cent, some saved",
			costs: []v1alpha1.CostStatus{{FreedThisMonth: "<$0.01", SavedThisMonth: "<$0.01"}},
			want:  "Under a cent freed so far this month. Some of it came off the bill."},
		{name: "freed under a cent, at least some saved",
			costs: []v1alpha1.CostStatus{{FreedThisMonth: "<$0.01", SavedThisMonth: "<$0.01"},
				{FreedThisMonth: "<$0.01", SavingsUnknown: unknown}},
			want: "Under a cent freed so far this month. Some of it came off the bill."},
		{name: "freed under a cent, measured, none saved yet",
			costs: []v1alpha1.CostStatus{{FreedThisMonth: "<$0.01", SavedThisMonth: "$0.00"}},
			want:  "Under a cent freed so far this month. None of it has come off the bill yet."},
		{name: "freed under a cent, can't measure",
			costs: []v1alpha1.CostStatus{{FreedThisMonth: "<$0.01", SavingsUnknown: unknown}},
			want:  "Under a cent freed so far this month. Napwake can&#39;t tell here how much came off the bill."},
		{name: "freed $12, saved under a cent, measured",
			costs:      []v1alpha1.CostStatus{{FreedThisMonth: "$12.00", SavedThisMonth: "<$0.01"}},
			want:       "Under a cent of the $12.00 freed came off the bill.",
			meterLabel: "of the $12.00 freed by pausing so far this month"},
		{name: "freed $12, saved under a cent, at least",
			costs: []v1alpha1.CostStatus{{FreedThisMonth: "$11.99", SavedThisMonth: "<$0.01"},
				{FreedThisMonth: "$0.01", SavingsUnknown: unknown}},
			want:       "Some of the $12.00 freed came off the bill.",
			meterLabel: "of the $12.00 freed by pausing so far this month"},
		{name: "freed $12, saved $5",
			costs:      []v1alpha1.CostStatus{{FreedThisMonth: "$12.00", SavedThisMonth: "$5.00"}},
			want:       "$5.00 of the $12.00 freed came off the bill.",
			meterLabel: "of the $12.00 freed by pausing so far this month"},
		{name: "freed and saved in cents",
			costs:      []v1alpha1.CostStatus{{FreedThisMonth: "$1.74", SavedThisMonth: "$1.65"}},
			want:       "$1.65 of the $1.74 freed came off the bill.",
			meterLabel: "of the $1.74 freed by pausing so far this month"},
		{name: "freed in cents, none saved yet",
			costs: []v1alpha1.CostStatus{{FreedThisMonth: "$0.60", SavedThisMonth: "$0.00"}},
			want:  "None of the $0.60 freed has come off the bill yet."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := make([]client.Object, 0, len(tt.costs))
			for i, cost := range tt.costs {
				w := statusWorkloadObj("preview-42", fmt.Sprintf("w%d", i), v1alpha1.TargetKindDeployment,
					v1alpha1.PhasePaused, time.Hour)
				w.Status.Cost = &cost
				objs = append(objs, w)
			}

			got := renderStatusPage(t, objs...)

			pageHas(t, got, `<p class="meter-t">`+tt.want+`</p>`)
			if tt.meterLabel != "" {
				pageHas(t, got, ` data-meter-label="`+tt.meterLabel+`"`, "the card says what it's of")
			} else {
				pageLacks(t, got, `data-meter-label`, "the card has no bar to say what it's of")
			}
		})
	}
}

func TestWriteStatusHTML_NothingManaged(t *testing.T) {
	got := renderStatusPage(t)

	assert.Contains(t, got, "Napwake manages no workloads here.")
	assert.NotContains(t, got, "<h2>Workloads</h2>")
	assert.NotContains(t, got, "Would free")
	assert.Contains(t, got, `<p class="empty">No pauses or wakes in this period.</p>`)
}

// --html writes the page as well as the table: given a file, there, with
// or without "=", and given none, to a new temporary file. In a terminal it
// opens it, unless --open=false; elsewhere it says where it is. Without
// --html, status takes no arguments.
func TestStatusCLI_HTML(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		terminal   bool
		wantFile   string
		wantOpened bool
		wantErr    string
	}{
		{name: "in a terminal, opened from a temporary file", args: []string{"--html"}, terminal: true,
			wantOpened: true},
		{name: "a temporary file, piped", args: []string{"--html"}},
		{name: "a file after a space", args: []string{"--html", "status.html"}, terminal: true,
			wantFile: "status.html", wantOpened: true},
		{name: "a file after =, not opened", args: []string{"--html=status.html", "--open=false"}, terminal: true,
			wantFile: "status.html"},
		{name: "an argument without --html", args: []string{"status.html"}, wantErr: `unknown command "status.html"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("TMPDIR", dir)
			t.Chdir(dir)
			var opened string
			restoreBrowse, restoreInteractive := browse, interactive
			t.Cleanup(func() { browse, interactive = restoreBrowse, restoreInteractive })
			interactive = func(io.Writer) bool { return tt.terminal }
			browse = func(path string) error { opened = path; return nil }

			out, err := runCLI(t, newStatusClient(t, interceptor.Funcs{}, statusCluster()...),
				append([]string{"status"}, tt.args...)...)

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Contains(t, out, "Napwake manages 4 workloads.", "the table is shown as well")
			path := tt.wantFile
			if tt.wantFile == "" {
				files, err := filepath.Glob(filepath.Join(dir, "napwake-status-*.html"))
				require.NoError(t, err)
				require.Len(t, files, 1)
				path = files[0]
				info, err := os.Stat(path)
				require.NoError(t, err)
				assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the page names the cluster's workloads")
			}
			page, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Contains(t, string(page), "<h1>Napwake status</h1>")
			if tt.wantOpened {
				assert.Equal(t, path, opened)
				assert.Contains(t, out, "Status page: "+path+" (opened in your browser; --open=false to skip)\n")
				return
			}
			assert.Empty(t, opened)
			assert.Contains(t, out, "Status page: ")
			assert.NotContains(t, out, "opened in your browser")
		})
	}

	t.Run("without --html, no page", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("TMPDIR", dir)
		restoreInteractive := interactive
		t.Cleanup(func() { interactive = restoreInteractive })
		interactive = func(io.Writer) bool { return true }

		out, err := runCLI(t, newStatusClient(t, interceptor.Funcs{}, statusCluster()...), "status")

		require.NoError(t, err)
		assert.NotContains(t, out, "Status page")
		files, err := filepath.Glob(filepath.Join(dir, "*.html"))
		require.NoError(t, err)
		assert.Empty(t, files)
	})
}

// cantListNodes is why what's saved can't be measured, as the operator
// says it.
const cantListNodes = "the operator can't list nodes"

// monthNow is late enough in the month for a week to be counted.
var monthNow = statusNow.AddDate(0, 0, 22).Add(2*time.Hour + 20*time.Minute)

// monthWorkload is a paused workload whose cost record projects a month at
// this pace, as the operator writes one: what it costs a month with
// Napwake, what pausing frees, and the part of that saved, over the time
// tracked, with what it freed and saved so far this month. "pending" is
// what the operator writes before a day is tracked.
func monthWorkload(name, cost, freed, saved string, tracked time.Duration, soFarFreed,
	soFarSaved string) *v1alpha1.ManagedWorkload {
	w := statusWorkloadObj("preview-42", name, v1alpha1.TargetKindDeployment, v1alpha1.PhasePaused, time.Hour)
	w.Status.Cost = &v1alpha1.CostStatus{Tracked: metav1.Duration{Duration: tracked},
		ProjectedMonthlyCost: cost, ProjectedMonthlyFreed: freed, ProjectedMonthlySavings: saved,
		FreedThisMonth: soFarFreed, SavedThisMonth: soFarSaved}
	return w
}

// The test cluster's month, 2 days in (cmo-069): $31.94 without Napwake,
// $23.34 of it off the bill, $8.60 left, 73%; $24.62 freed, 77%.
func earlyMonth() *v1alpha1.ManagedWorkload {
	return monthWorkload("api", "$7.32", "$24.62", "$23.34", 53*time.Hour, "$1.74", "$1.65")
}

// statusPageParts are the parts of the page each state of the month
// changes: the lead's block, empty without one, the share card's data, and
// the Copy summary's first line.
type statusPageParts struct {
	lead, card, summary string
}

func statusPartsOf(t *testing.T, page string) statusPageParts {
	t.Helper()
	var parts statusPageParts
	if m := regexp.MustCompile(`(?s)<div class="pace[ "].*?</div>`).FindString(page); m != "" {
		parts.lead = m
	}
	parts.card = regexp.MustCompile(`<div hidden id="card-data"[^>]*></div>`).FindString(page)
	require.NotEmpty(t, parts.card, "the share card's data")
	summary := regexp.MustCompile(`(?s)<div hidden id="summary-lines">\s*<p>(.*?)</p>`).FindStringSubmatch(page)
	require.Len(t, summary, 2, "the Copy summary")
	parts.summary = summary[1]
	return parts
}

// Each state of the month at this pace leads the page as the notes'
// states table has it (cmo-069, cmo-070): what comes off the bill as a
// share, with both amounts, once it's measured and 1% or more, nodes
// scaling down or not; what pausing frees, as what's possible, where it
// isn't; and, before a
// day is counted, with nothing freed, or with no workloads, the page as it
// was, freed and saved so far this month leading. Each checks the lead's
// words and figures, the share card's data, and the Copy summary's first
// line.
func TestStatusPage_MonthStates(t *testing.T) {
	const at = "Napwake status<span data-private> of staging (EKS us-east-1)</span>, 27 October 2026, 14:20 UTC: "
	unmeasured := monthWorkload("web", "$1.00", "$3.00", "$0.00", 53*time.Hour, "$0.20", "$0.00")
	unmeasured.Status.Cost.SavingsUnknown = cantListNodes
	tests := []struct {
		name string
		objs []client.Object
		// lead is what the lead's block holds, in order, or nil for none.
		lead    []string
		card    []string
		summary string
		// also is what else the page holds, and lacks what it doesn't.
		also, lacks []string
	}{
		{name: "steady",
			objs: []client.Object{monthWorkload("api", "$8.35", "$23.59", "$22.36", 8*24*time.Hour, "$8.07", "$7.65")},
			lead: []string{`<div class="pace">`,
				`<p class="pace-fig"><b>70% less</b><span>$9.58 a month instead of $31.94, at this month&#39;s pace.</span></p>`,
				`<p class="pace-note">Nodes scaling down take at least $22.36 a month off the bill.</p>`},
			card: []string{`data-eyebrow="Status"`, `data-pace="yes"`, `data-value="70% less"`,
				`data-label="$9.58 a month instead of $31.94, at this month&#39;s pace"`,
				`data-detail="Nodes scaling down take at least $22.36 a month off the bill."`},
			summary: at + "at this month&#39;s pace, the workloads Napwake manages cost $9.58 a month instead of $31.94, " +
				"70% less, as nodes scale down. $8.07 freed by pausing so far this month, $7.65 saved of it (what came off " +
				"the bill as nodes scaled down).",
			also:  []string{`<div class="money paced">`, `<b>$8.07</b><span>freed by pausing so far this month</span>`},
			lacks: []string{`class="pace-early"`, `data-detail-lead`}},
		{name: "early",
			objs: []client.Object{earlyMonth()},
			lead: []string{`<div class="pace">`,
				`<p class="pace-fig"><b>73% less</b><span>$8.60 a month instead of $31.94, at this month&#39;s pace.</span></p>`,
				`<p class="pace-early"><b>Early:</b> 2 days counted. It settles after a week.</p>`,
				`<p class="pace-note">Nodes scaling down take at least $23.34 a month off the bill.</p>`},
			card: []string{`data-pace="yes"`, `data-value="73% less"`,
				`data-label="$8.60 a month instead of $31.94, at this month&#39;s pace"`, `data-detail-lead="Early:"`,
				`data-detail="Early: 2 days counted. It settles after a week."`},
			summary: at + "at this month&#39;s pace, the workloads Napwake manages cost $8.60 a month instead of $31.94, " +
				"73% less, as nodes scale down. Early: 2 days counted. It settles after a week. $1.74 freed by pausing so " +
				"far this month, $1.65 saved of it (what came off the bill as nodes scaled down).",
			also: []string{`<p class="meter-t">$1.65 of the $1.74 freed came off the bill.</p>`}},
		{name: "saved unknown",
			objs: []client.Object{earlyMonth(), unmeasured},
			lead: []string{`<div class="pace possible">`, `<p class="pace-k label">Saved can&#39;t be measured</p>`,
				`<p class="pace-fig"><span>Pausing frees 77% of what these workloads would cost, $27.62 a month at this ` +
					`pace.</span></p>`,
				`<p class="pace-early"><b>Early:</b> 2 days counted. It settles after a week.</p>`,
				`<p class="pace-note">It comes off the bill as nodes scale down. Napwake can&#39;t tell here how much ` +
					`does.</p>`},
			card: []string{`data-value="$1.65"`, `data-label="came off the bill this month"`,
				`data-detail="for the workloads whose savings can be measured"`,
				`data-meter-label="of the $1.94 freed by pausing so far this month"`},
			summary: at + "at this month&#39;s pace, pausing frees 77% of what the workloads Napwake manages would cost, " +
				"$27.62 a month. It comes off the bill as nodes scale down. Napwake can&#39;t tell here how much does. " +
				"Early: 2 days counted. It settles after a week. $1.94 freed by pausing so far this month, $1.65 saved of " +
				"it (for the workloads whose savings can be measured)."},
		{name: "off the bill",
			objs: []client.Object{monthWorkload("api", "$7.32", "$24.62", "$0.00", 53*time.Hour, "$0.80", "$0.00")},
			lead: []string{`<div class="pace possible">`, `<p class="pace-k label">Not on the bill yet</p>`,
				`<p class="pace-fig"><span>Pausing frees 77% of what these workloads would cost, $24.62 a month at this ` +
					`pace.</span></p>`,
				`<p class="pace-note">None of it comes off the bill until nodes scale down, and they haven&#39;t yet.</p>`},
			card: []string{`data-value="$0.80"`, `data-label="freed by pausing so far this month"`,
				`data-detail="what the paused replicas request, at list price; none of it off the bill yet"`},
			summary: at + "at this month&#39;s pace, pausing frees 77% of what the workloads Napwake manages would cost, " +
				"$24.62 a month. None of it comes off the bill until nodes scale down, and they haven&#39;t yet. Early: 2 " +
				"days counted. It settles after a week. $0.80 freed by pausing so far this month, $0 saved of it (what " +
				"came off the bill as nodes scaled down)."},
		{name: "off the bill, under 1% of it saved",
			objs: []client.Object{monthWorkload("api", "$7.32", "$24.62", "$0.10", 53*time.Hour, "$0.80", "<$0.01")},
			lead: []string{`<div class="pace possible">`, `<p class="pace-k label">Not on the bill yet</p>`,
				`<p class="pace-note">Under 1% comes off the bill, as nodes have hardly scaled down.</p>`},
			card: []string{`data-value="&lt;$0.01"`, `data-label="came off the bill this month"`},
			summary: at + "at this month&#39;s pace, pausing frees 77% of what the workloads Napwake manages would cost, " +
				"$24.62 a month. Under 1% comes off the bill, as nodes have hardly scaled down. Early: 2 days counted. It " +
				"settles after a week. $0.80 freed by pausing so far this month, &lt;$0.01 saved of it (what came off the " +
				"bill as nodes scaled down)."},
		{name: "nodes not scaling down, 1% or more saved",
			objs: []client.Object{monthWorkload("api", "$7.32", "$24.62", "$6.00", 53*time.Hour, "$12.40", "$1.00")},
			lead: []string{`<div class="pace">`,
				`<p class="pace-fig"><b>19% less</b><span>$25.94 a month instead of $31.94, at this month&#39;s ` +
					`pace.</span></p>`,
				`<p class="pace-early"><b>Early:</b> 2 days counted. It settles after a week.</p>`,
				`<p class="pace-note">Nodes scaling down take at least $6.00 a month off the bill.</p>`},
			card: []string{`data-pace="yes"`, `data-value="19% less"`,
				`data-label="$25.94 a month instead of $31.94, at this month&#39;s pace"`, `data-detail-lead="Early:"`},
			summary: at + "at this month&#39;s pace, the workloads Napwake manages cost $25.94 a month instead of $31.94, " +
				"19% less, as nodes scale down. Early: 2 days counted. It settles after a week. $12.40 freed by pausing " +
				"so far this month, $1.00 saved of it (what came off the bill as nodes scaled down).",
			also: []string{`<b>Needs you:</b> 1 thing to look at.`,
				`<td class="ref">Nodes not scaling down</td>
        <td class="message">Pausing freed $12 this month, but only $1 of it came off the bill. kubectl napwake ` +
					`nodes shows why.</td>`},
			lacks: []string{"Not on the bill yet", "comes off the bill, as nodes have hardly scaled down"}},
		{name: "pending",
			objs: []client.Object{monthWorkload("api", "pending", "pending", "pending", 20*time.Hour, "$0.40", "$0.30")},
			card: []string{`data-value="$0.30"`, `data-label="came off the bill this month"`,
				`data-meter-label="of the $0.40 freed by pausing so far this month"`},
			summary: at + "$0.40 freed by pausing so far this month, $0.30 saved of it (what came off the bill as nodes " +
				"scaled down).",
			also: []string{`<div class="money">`, `<div class="slot s1 lead"><div class="figure"><b>$0.30</b>`,
				`<p class="pace-wait">A month at this pace, with and without Napwake, shows once a full day is counted. ` +
					`Each month&#39;s count starts on the 1st, at 00:00 UTC.</p>`}},
		{name: "none",
			objs: []client.Object{monthWorkload("api", "$12.00", "$0.00", "$0.00", 3*24*time.Hour, "$0.00", "$0.00")},
			card: []string{`data-value="$0"`, `data-label="freed by pausing so far this month"`,
				`data-detail="what the paused replicas request, at list price"`},
			summary: at + "$0 freed by pausing so far this month, $0 saved of it (what came off the bill as nodes scaled " +
				"down).",
			also:  []string{`<div class="money">`, `<div class="slot s0 lead"><div class="figure zero"><b>$0</b>`},
			lacks: []string{`class="pace-wait"`}},
		{name: "nil, with no workloads",
			card: []string{`data-value="$0"`, `data-label="freed by pausing so far this month"`},
			summary: at + "$0 freed by pausing so far this month, $0 saved of it (what came off the bill as nodes scaled " +
				"down).",
			also:  []string{`<div class="money quiet">`, "Napwake manages no workloads here."},
			lacks: []string{`class="pace-wait"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page := renderStatusPageAt(t, monthNow, tt.objs...)
			got := statusPartsOf(t, page)

			if tt.lead == nil {
				assert.Empty(t, got.lead, "the month doesn't lead")
			}
			at := 0
			for _, passage := range tt.lead {
				i := strings.Index(got.lead[at:], passage)
				if !assert.GreaterOrEqual(t, i, 0, "the lead lacks, in its place, %q\nlead: %s", passage, got.lead) {
					continue
				}
				at += i + len(passage)
			}
			for _, passage := range tt.card {
				assert.Contains(t, got.card, passage, "the share card")
			}
			assert.Equal(t, tt.summary, got.summary, "the Copy summary's first line")
			for _, passage := range tt.also {
				pageHas(t, page, passage)
			}
			for _, passage := range tt.lacks {
				pageLacks(t, page, passage)
			}
		})
	}
}

// Where what comes off the bill isn't something to headline, the status
// page says what pausing frees as what's possible, with no big percentage,
// on the page or its card (cmo-069, cmo-070): "Saved can't be measured"
// for some workloads, or "Not on the bill yet" while it's under 1% of what
// they'd cost. Each alone does it, from a month that would lead with "73%
// less". Nodes not scaling down don't: with 1% or more saved, that leads.
func TestStatusPage_NotOnTheBillYetHasNoBigFigure(t *testing.T) {
	unmeasured := monthWorkload("web", "$1.00", "$3.00", "$0.00", 53*time.Hour, "$0.20", "$0.00")
	unmeasured.Status.Cost.SavingsUnknown = cantListNodes
	underAPercent := earlyMonth()
	underAPercent.Status.Cost.ProjectedMonthlySavings = "$0.15"
	for name, tt := range map[string]struct {
		objs   []client.Object
		kicker string
	}{
		"saved can't be measured": {objs: []client.Object{earlyMonth(), unmeasured},
			kicker: "Saved can&#39;t be measured"},
		"under 1% saved": {objs: []client.Object{underAPercent}, kicker: "Not on the bill yet"},
	} {
		t.Run(name, func(t *testing.T) {
			got := statusPartsOf(t, renderStatusPageAt(t, monthNow, tt.objs...))

			assert.Contains(t, got.lead, `<p class="pace-k label">`+tt.kicker+`</p>`)
			assert.Contains(t, got.lead, "Pausing frees 77% of what these workloads would cost")
			for part, text := range map[string]string{"lead": got.lead, "card": got.card, "summary": got.summary} {
				assert.NotContains(t, text, "% less", "no big percentage in the %s", part)
			}
			assert.NotContains(t, got.lead, `<p class="pace-fig"><b>`, "no big figure")
			assert.NotContains(t, got.card, "data-pace", "the card is the one it was")
		})
	}

	got := statusPartsOf(t, renderStatusPageAt(t, monthNow, earlyMonth()))
	assert.Contains(t, got.lead, "<b>73% less</b>", "the month they start from leads with what comes off the bill")

	notScalingDown := earlyMonth()
	notScalingDown.Status.Cost.SavedThisMonth = "$0.80"
	page := renderStatusPageAt(t, monthNow, notScalingDown)
	assert.Contains(t, statusPartsOf(t, page).lead, "<b>73% less</b>", "nodes not scaling down, 73% saved")
	pageHas(t, page, `<td class="ref">Nodes not scaling down</td>`, "Needs attention says why the rest isn't off")
}
