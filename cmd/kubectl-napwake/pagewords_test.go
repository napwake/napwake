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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// pageHas and pageLacks check a page for a passage, and say only the
// passage when they fail: the page is hundreds of kilobytes.
func pageHas(t *testing.T, page, passage string, why ...string) {
	t.Helper()
	assert.True(t, strings.Contains(page, passage), "the page lacks %q %s", passage, strings.Join(why, ": "))
}

func pageLacks(t *testing.T, page, passage string, why ...string) {
	t.Helper()
	assert.False(t, strings.Contains(page, passage), "the page has %q %s", passage, strings.Join(why, ": "))
}

// The Workloads section says only what its columns show: what pausing has
// freed and saved once something is live, and what it could free once
// there's history or dry-run to say.
func TestWriteHTML_WorkloadsIntroSaysWhatTheColumnsShow(t *testing.T) {
	got := renderHTML(t, sampleCluster())

	pageHas(t, got, "<p>Every Deployment and StatefulSet scanned: what each is doing right now.</p>",
		"a snapshot of a cluster with nothing live")
	pageLacks(t, got, "has freed and saved")

	result := historyResult()
	result.Workloads[2].Managed, result.Totals.Live = false, 0
	result.Workloads[2].FreedThisMonth, result.Workloads[2].SavedThisMonth = 0, 0
	result.Totals.FreedThisMonth, result.Totals.SavedThisMonth = 0, 0
	got = renderHTML(t, result)
	pageHas(t, got, "notes list: what each is doing right now, and what pausing them could free.</p>")
}

// The pages say only what Napwake can see, signs of use, never people:
// an idle room shows no sign of use, not that nobody is there.
func TestReportAndStatusPage_SayOnlyWhatNapwakeSees(t *testing.T) {
	for name, page := range map[string]string{"scan": reportHTML, "status": statusPageHTML} {
		pageLacks(t, page, "nobody", name)
		pageHas(t, page, `" workloads") + " with no sign of use would be paused until someone uses "`, name)
	}
	pageHas(t, reportHTML, "Every running workload shows signs of use.")
}

// The pages say an idle workload shows no sign of use, in plain words, the
// picture beside them, not in its place: in the legend, and in the word
// the room cards and the picture's summary for screen readers use. The
// scan report's switch for its picture says what it shows (cmo-053).
func TestReportAndStatusPage_PlainWordsForThePicture(t *testing.T) {
	for name, page := range map[string]string{"scan": renderHTML(t, historyResult()),
		"status": renderStatusPage(t, statusCluster()...)} {
		pageHas(t, page, `<em>no sign of use</em> &middot; idle`, name, "the legend")
		pageHas(t, page, `idle: "no sign of use"`, name, "the room cards' and the summary's word")
		pageLacks(t, page, "lit, no one seen", name, "anywhere the page shows")
		// What a reader sees, screen readers and the share card included, says
		// what happens: workloads and doors, not hotel rooms and lights.
		for _, old := range []string{"Every room is", "Every lit room", "with no one seen", "The same hotel",
			"A hotel at night", "one room per workload", "one room each", `" rooms"`, "are shuttered",
			"Hover a room", "No room is asleep"} {
			pageLacks(t, page, old, name, "the picture's old words")
		}
		pageHas(t, page, "Every workload is asleep.", name, "the calm statement")
		pageHas(t, page, " running with no sign of use.", name, "the scan's statement")
		pageHas(t, page, `"The same cluster with Napwake: the workloads with no sign of use paused, `+
			`the rest as they were."`, name, "what Napwake would do, for screen readers")
	}
	got := renderHTML(t, historyResult())
	pageHas(t, got, `role="group" aria-label="Show the cluster as a picture"`, "the picture's switch")
	pageLacks(t, got, "Show the building")
}

// A running workload is awake in the pages' legends, as on the docs' home
// page ("Awake · running"), beside each page's own word for it, which stays
// (P64). The room cards and the picture's summary for screen readers use
// the legend's word.
func TestReportAndStatusPage_AwakeForRunning(t *testing.T) {
	for name, tt := range map[string]struct{ page, legend string }{
		"scan":   {page: renderHTML(t, historyResult()), legend: `<em>awake</em> &middot; active`},
		"status": {page: renderStatusPage(t, statusCluster()...), legend: `<em>awake</em> &middot; running`},
	} {
		pageHas(t, tt.page, tt.legend, name, "the legend")
		pageHas(t, tt.page, `active: "awake"`, name, "the room cards' and the summary's word")
		pageLacks(t, tt.page, `<em>in use</em>`, name, "the legend's old word")
	}
}

// A dependency Napwake manages stays up while what depends on it is awake,
// as the dependencies concept says, and the pages say so plainly.
func TestReportAndStatusPage_Words(t *testing.T) {
	pageHas(t, reportHTML, "and keeps it up while they&#39;re awake; the status says which it does for each.")
	pageLacks(t, reportHTML, "while they are;")

	status := renderStatusPage(t, statusCluster()...)
	pageHas(t, status, "over the last 24h, or as far back as the cluster keeps events if that&#39;s less: "+
		"an hour, unless its API server keeps more.")

	for name, page := range map[string]string{"scan": reportHTML, "status": statusPageHTML} {
		pageHas(t, page, `paused: "Paused by Napwake. It wakes when someone needs it."`, name,
			"a workload that asks wakes only for a click, and one paused on request may have been in use")
		pageHas(t, page, `idle: "Running with no activity. The table says what Napwake does next."`, name,
			"dry-run, a forecast or a dependency can keep it up past its idle-after")
		pageHas(t, page, `idle: "Running, with no activity right now."`, name,
			"the scan judges activity, not whether anyone uses it")
		pageHas(t, page, "The scan paused nothing: this is what Napwake would do.", name,
			"on a live scan Napwake has paused things, but not the scan")
		pageLacks(t, page, "Nothing has been paused", name)
	}
}

// The status page's callout says how many things need a look, not that they
// stop Napwake working: a protected workload or a back-off doesn't (P55). The
// scan page's bar names where "What the numbers mean" goes (P56). Kede's
// words (cmo-036).
func TestReportAndStatusPage_CalloutAndBar(t *testing.T) {
	pageHas(t, statusPageHTML, `<b>Needs you:</b> {{len .Attention}} `+
		`{{if eq (len .Attention) 1}}thing{{else}}things{{end}} to look at. See what, and why.`)
	pageLacks(t, statusPageHTML, "keeps</span>")
	pageHas(t, reportHTML, `<a href="#meaning">What the numbers mean</a>`)
	pageLacks(t, reportHTML, `>The numbers<`)
}
