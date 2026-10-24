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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/discovery"
)

// The tests here reword a sentence the product writes, which a page once
// read to decide what to show, and check the page still shows it: each
// decision is read from a field, so the words can change freely.

func renderReportPage(t *testing.T, page reportPage) string {
	t.Helper()
	var out bytes.Buffer
	require.NoError(t, reportTemplate.Execute(&out, page))
	return out.String()
}

// A live scan leads with what Napwake freed, and the page knows it's live
// from the figure's kind, not from its label starting "freed by".
func TestReportPage_LiveWhateverTheLabelSays(t *testing.T) {
	page := buildReport(historyResult(), time.Hour)
	page.Headline.Figures[0].Label = "Napwake has freed this month"

	got := renderReportPage(t, page)

	pageHas(t, got, `<div class="hero-figs live">`, "a live scan's figures")
	pageHas(t, got, `<span class="kicker label">Still to free</span>`, "the opportunity follows what was freed")
	pageHas(t, got, `data-more="$179 more could be freed a month"`, "the card adds what's still to free")
}

// A scan with nothing to free and nothing idle is good news, which the page
// knows from the figure's amount, not from its reading "$0".
func TestReportPage_GoodNewsWhateverTheAmountReads(t *testing.T) {
	result := scanResult{Cluster: "c", ScannedAt: statusNow, ClusterReport: &discovery.ClusterReport{
		Mode: discovery.ModeSnapshot,
		Workloads: []discovery.Workload{{Namespace: "shop", Kind: v1alpha1.TargetKindDeployment, Name: "api",
			State: discovery.StateActive, Reason: "CPU 64% of its request", MonthlyCost: 30}},
		Totals: discovery.Totals{Workloads: 1, MonthlyCost: 30}}}
	page := buildReport(result, time.Hour)
	page.Headline.Figures[0].Value = "US$0"

	got := renderReportPage(t, page)

	pageHas(t, got, `<p class="statement big">Every room is in use.</p>`, "nothing idle, nothing to free")
	pageHas(t, got, `<div class="figure zero"><b>US$0</b>`, "a zero is drawn as one")
}

// With too little history, the page says so before the figure, from the
// caveat the product gives as its own field, whatever the figure's detail
// says around it.
func TestReportPage_TooLittleHistoryFromItsOwnField(t *testing.T) {
	result := historyResult()
	result.History.Hours, result.History.Partial = 0.5, true
	result.Workloads[2].Managed, result.Totals.Live = false, 0
	result.Workloads[2].FreedThisMonth, result.Workloads[2].SavedThisMonth = 0, 0
	result.Totals.FreedThisMonth, result.Totals.SavedThisMonth = 0, 0
	page := buildReport(result, time.Hour)
	page.Headline.Figures[0].Detail = strings.ReplaceAll(page.Headline.Figures[0].Detail, "from only", "with just")

	got := renderReportPage(t, page)

	pageHas(t, got, `data-short-history="From only 30 minutes of history, so run the scan again after a few days `+
		`for a steadier figure."`, "the caveat the page shows, in full")
	pageLacks(t, reportHTML, "too little history yet|", "the script reads the field, not the detail")
}

// What the page shares says what the history covers, but not the Prometheus
// it was read from unless the reader ticks the box.
func TestReportPage_BasisPublicLeavesOutTheSource(t *testing.T) {
	got := renderHTML(t, historyResult())

	pageHas(t, got, `<span data-private>Based on the last 7 days of Prometheus history, from `+
		`monitoring/prometheus-operated, and CPU at the time of the scan.</span>`)
	pageHas(t, got, `<span data-public>Based on the last 7 days of Prometheus history, and CPU at the time of the `+
		`scan.</span>`)

	snapshot := sampleCluster()
	assert.Equal(t, basisSentence(snapshot), buildReport(snapshot, time.Hour).BasisPublic,
		"a snapshot names no source")
}

// The next steps' go-live command is marked as such, so the page splits it
// from the measuring ones whatever the command says.
func TestReportPage_EnableMarkedApart(t *testing.T) {
	got := renderHTML(t, historyResult())

	pageHas(t, got, `<span data-step="enable"><span class="line"><span class="sh-cmd">kubectl</span> napwake enable `+
		`deployment/api`, "the go-live command, marked")
	for name, page := range map[string]string{"scan": reportHTML, "status": statusPageHTML} {
		pageLacks(t, page, `enable\b/.test(`, name, "the script reads the mark, not the command")
	}
}

// The measuring commands are for the idle workload, and going live for the
// one dry-run already measures, each named with its command.
func TestReportPage_NextStepsWithTheirTargets(t *testing.T) {
	steps := buildReport(historyResult(), time.Hour).NextSteps

	require.NotNil(t, steps)
	require.Len(t, steps.Measure, 2)
	for _, c := range steps.Measure {
		assert.Equal(t, command{Line: c.Line, Namespace: "preview-42", Workload: "statefulset/postgres"}, c)
	}
	require.NotNil(t, steps.Enable)
	assert.Equal(t, "preview-7", steps.Enable.Namespace)
	assert.Equal(t, "deployment/api", steps.Enable.Workload)
	assert.Contains(t, string(steps.Enable.Line), "napwake enable deployment/api")
}
