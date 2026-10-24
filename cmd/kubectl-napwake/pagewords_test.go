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
