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
	"testing"
	"time"

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
