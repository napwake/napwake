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
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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

func renderStatusPageData(t *testing.T, page statusPage) string {
	t.Helper()
	var out bytes.Buffer
	require.NoError(t, statusPageTemplate.Execute(&out, page))
	return out.String()
}

func statusPageOf(t *testing.T, objs ...client.Object) statusPage {
	t.Helper()
	result, err := clusterStatus(context.Background(), newStatusClient(t, interceptor.Funcs{}, objs...), "preview-42",
		statusOpts())
	require.NoError(t, err)
	result.Cluster = testCluster
	return buildStatusPage(result, 24*time.Hour, statusNow)
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

// An idle workload Napwake won't pause keeps its light in the With Napwake
// picture, which the page knows from the row's own field, wherever its
// reason sits in the line under the state.
func TestReportPage_WontPauseWhateverTheReasonSays(t *testing.T) {
	result := historyResult()
	postgres := &result.Workloads[0]
	postgres.WontPause = "PVCs deleted on scale-down"
	postgres.Clues = []string{"won't pause: PVCs deleted on scale-down"}

	got := renderHTML(t, result)
	pageHas(t, got, `<tr id="wl-0" data-stays="">`, "its reason after the evidence for its state")

	page := buildReport(result, time.Hour)
	page.Workloads[0].Because = "Napwake never pauses it: PVCs deleted on scale-down"
	got = renderReportPage(t, page)
	pageHas(t, got, `<tr id="wl-0" data-stays="">`, "reworded")

	result.Workloads[3].WontPause = "PVCs deleted on scale-down"
	got = renderHTML(t, result)
	pageHas(t, got, `<tr id="wl-3">`, "one in use stays on anyway, and isn't counted as kept lit")
	assert.Equal(t, "PVCs deleted on scale-down", buildReport(result, time.Hour).Workloads[3].WontPause,
		"the reason, as its own field")
}

// The status page leads with what came off the bill, under what was freed,
// and draws the meter between them, from the amounts as numbers, not from
// the digits of "$35".
func TestStatusPage_MoneyWhateverTheFiguresRead(t *testing.T) {
	page := statusPageOf(t, statusCluster()...)
	page.Figures[0].Value, page.Figures[1].Value = "42 dollars", "35 dollars"

	got := renderStatusPageData(t, page)

	pageHas(t, got, `<div class="slot s1 lead">`, "what was saved leads")
	pageHas(t, got, `data-of="42.45" data-part="35"`, "the meter's amounts")

	objs := statusCluster()
	for _, o := range objs {
		if w, ok := o.(*v1alpha1.ManagedWorkload); ok && w.Status.Cost != nil {
			w.Status.Cost.SavedThisMonth = "$0.00"
		}
	}
	page = statusPageOf(t, objs...)
	page.Figures[0].Value, page.Figures[1].Value = "42 dollars", "0 dollars"
	got = renderStatusPageData(t, page)
	pageHas(t, got, `<div class="slot s0 lead">`, "with nothing saved, what was freed leads")
	pageHas(t, got, `None of the 42 dollars freed has come off the bill yet.`)
}

// The status page's amounts are the status's own totals, and say whether
// what's saved is measured for every workload.
func TestStatusPage_Amounts(t *testing.T) {
	page := statusPageOf(t, statusCluster()...)

	assert.InDelta(t, 42.45, page.FreedThisMonth, 0.001)
	assert.InDelta(t, 35, page.SavedThisMonth, 0.001)
	assert.True(t, page.SavedMeasurable)
	assert.Equal(t, []string{figureFreed, figureSaved, figureWouldHaveFreed},
		[]string{page.Figures[0].Kind, page.Figures[1].Kind, page.Figures[2].Kind})
	assert.InDelta(t, 7.4, page.Figures[2].Amount, 0.001, "what dry-run measured")

	objs := statusCluster()
	objs[0].(*v1alpha1.ManagedWorkload).Status.Cost.SavingsUnknown = "pods outside watchNamespaces aren't visible"
	assert.False(t, statusPageOf(t, objs...).SavedMeasurable)
}

// A workload waking is told by its state's class, whatever its state says.
func TestStatusPage_WakingWhateverItsStateSays(t *testing.T) {
	web := statusWorkloadObj("preview-7", "web", v1alpha1.TargetKindDeployment, v1alpha1.PhaseResuming,
		30*time.Second)
	page := statusPageOf(t, web)
	page.Workloads[0].State = "waking up"

	got := renderStatusPageData(t, page)

	pageHas(t, got, `<p class="bldg-say">Napwake is waking deployment/web.</p>`)
	pageHas(t, got, `<span class="state resuming"><span class="dot"></span>waking up</span>`, "its door ajar")
}

// Every phase has its own class, which draws its door: a workload with no
// phase yet is being created.
func TestStateClass(t *testing.T) {
	for phase, want := range map[v1alpha1.WorkloadPhase]string{
		v1alpha1.PhasePaused: "paused", v1alpha1.PhaseIdle: "idle", v1alpha1.PhaseRunning: "active",
		v1alpha1.PhasePausing: "pausing", v1alpha1.PhaseResuming: "resuming", v1alpha1.PhaseCreating: "creating",
		"": "creating",
	} {
		assert.Equal(t, want, stateClass(phase), phase)
	}
}

// A waking workload, and one stuck waking, are told apart by their state's
// class and by the problem's target, whatever the state's words and however
// the problem names the workload.
func TestStatusPage_WakingAndStuckWhateverTheyreCalled(t *testing.T) {
	page := statusPageOf(t, statusCluster()...)
	for i := range page.Workloads {
		page.Workloads[i].State = strings.ReplaceAll(page.Workloads[i].State, "resuming", "waking up")
	}
	for i := range page.Attention {
		if page.Attention[i].Workload == "worker" {
			page.Attention[i].Workload = "deployment/worker"
		}
	}

	got := renderStatusPageData(t, page)

	pageHas(t, got, `deployment/worker is stuck: <span class="sub-say">resuming for 25m: waiting for `+
		`preview-42/postgres</span>`, "the stuck wake's caption")
	pageHas(t, got, `<tr id="mw-3" data-needs="">`, "a note pinned on its room")
	pageHas(t, got, `<span class="state resuming"><span class="dot"></span>waking up</span>`, "its door ajar")
}

// A problem names its workload's target as the Workloads table does, and
// the ManagedWorkload to kubectl describe.
func TestStatusPage_AttentionKeyedByTarget(t *testing.T) {
	page := statusPageOf(t, statusCluster()...)

	require.NotEmpty(t, page.Attention)
	for _, a := range page.Attention {
		assert.Equal(t, "deployment/worker", a.Target, a.Type)
		assert.Equal(t, "worker", a.ManagedWorkload, a.Type)
	}

	w := statusWorkloadObj("shop", "db", v1alpha1.TargetKindStatefulSet, v1alpha1.PhasePausing, time.Hour)
	w.Name = "shop-db"
	problems := problemsOf(w, statusNow)
	require.Len(t, problems, 1)
	assert.Equal(t, problem{Namespace: "shop", Workload: "db", Target: "statefulset/db", ManagedWorkload: "shop-db",
		Type: "Stuck", Message: "pausing for 1h"}, problems[0])
}

// The doorman holds the door only for a workload a request woke, which the
// page knows from what woke it, not from the event's words.
func TestStatusPage_DoormanWhateverTheEventSays(t *testing.T) {
	web := statusWorkloadObj("preview-7", "web", v1alpha1.TargetKindDeployment, v1alpha1.PhaseResuming,
		30*time.Second)
	objs := []client.Object{web,
		statusEvent("preview-7", "web", "Paused", "web: paused", 2*time.Hour),
		statusEvent("preview-7", "web", "WokenByRequest", "Service web got a request; waking", 40*time.Second),
		statusEvent("preview-7", "web", "WokeByActivity", "web: a request is waiting, waking", 39*time.Second),
	}

	got := renderStatusPageData(t, statusPageOf(t, objs...))

	pageHas(t, got, `<p class="bldg-say">The doorman holds the door while deployment/web wakes.</p>`)
}

// A namespace shows what came off the bill under what pausing freed in it,
// whenever it freed anything, whatever the amount reads.
func TestReportPage_NamespaceSavedWhateverTheAmountReads(t *testing.T) {
	page := buildReport(historyResult(), time.Hour)
	for i := range page.Namespaces {
		if page.Namespaces[i].FreedSort > 0 {
			page.Namespaces[i].Freed = "$0"
		}
	}

	got := renderReportPage(t, page)

	pageHas(t, got, `$0<span class="sub">$1 saved</span>`, "preview-3's $3.50 freed, shown as reworded")
}

// Each row says when Napwake would pause it, by its own idle-after.
func TestReportPage_IdleAfterPerRow(t *testing.T) {
	result := historyResult()
	result.Workloads[0].IdleAfter = metav1.Duration{Duration: time.Hour}
	result.Workloads[1].IdleAfter = metav1.Duration{Duration: 90 * time.Minute}

	page := buildReport(result, time.Hour)

	assert.Equal(t, "1h", page.Workloads[0].IdleAfter)
	assert.Equal(t, "1h30m", page.Workloads[1].IdleAfter, "a managed workload's own")
}
