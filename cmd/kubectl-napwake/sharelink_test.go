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
	"html"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/napwake/napwake/internal/discovery"
)

// lessResult is couldResult with could of what pausing could free a month,
// of basis, what the workloads cost on the same basis, from hours of
// Prometheus history.
func lessResult(basis, could, hours float64) scanResult {
	result := couldResult(karpenterFound)
	result.Totals.FreeableBasis = basis
	result.Totals.Replayed.MonthlyFreed, result.Totals.Measured.MonthlyFreed = could, 0
	result.History.Hours = hours
	return result
}

// snapshotLess is lessResult measured in dry-run, without history.
func snapshotLess() scanResult {
	result := lessResult(446, 0, 0)
	result.Mode, result.History = discovery.ModeSnapshot, nil
	result.Totals.Replayed = discovery.ReplayTotals{}
	result.Totals.Measured.MonthlyFreed = 179
	return result
}

// noneJudged is couldResult with none of its workloads judged, so the page
// shows no card whatever the totals say.
func noneJudged() scanResult {
	result := couldResult(karpenterFound)
	var unjudged []discovery.Workload
	for _, wl := range result.Workloads {
		if wl.State == discovery.StateUnknown {
			unjudged = append(unjudged, wl)
		}
	}
	result.Workloads = unjudged
	return result
}

// statusPageAt is the status page's data for objs, read at monthNow.
func statusPageAt(t *testing.T, objs ...client.Object) statusPage {
	t.Helper()
	opts := statusOpts()
	opts.now = func() time.Time { return monthNow }
	result, err := clusterStatus(context.Background(), newStatusClient(t, interceptor.Funcs{}, objs...), "preview-42",
		opts)
	require.NoError(t, err)
	result.Cluster = testCluster
	return buildStatusPage(result, 24*time.Hour, monthNow)
}

func renderStatusPageOf(t *testing.T, page statusPage) string {
	t.Helper()
	var out bytes.Buffer
	require.NoError(t, executeWithPolicy(&out, statusPageTemplate, page))
	return out.String()
}

// The share link is napwake.dev's address for the card's percentage, then
// the card's own amounts as plain numbers, rounded as the card writes them
// (cmo-075): on the scan, whole dollars, and the days of history to three
// places, 0 for a snapshot; on the status page, to the cent under $100 and
// whole dollars from $100, and the days counted. Amounts under $100, from
// $100 and over $1,000, and the whole of what they cost, 100%.
func TestShareLink_Format(t *testing.T) {
	scan := map[string]struct {
		result scanResult
		want   string
	}{
		"under $100": {result: lessResult(80, 59, 168),
			want: "https://napwake.dev/s/74/?v=1&with=21&without=80&src=7"},
		"from $100": {result: lessResult(446, 179, 168),
			want: "https://napwake.dev/s/40/?v=1&with=267&without=446&src=7"},
		"over $1,000": {result: lessResult(12345.6, 9000, 168),
			want: "https://napwake.dev/s/73/?v=1&with=3346&without=12346&src=7"},
		"30 hours": {result: lessResult(446, 179, 30),
			want: "https://napwake.dev/s/40/?v=1&with=267&without=446&src=1.25"},
		"36 and a half hours, the card's 37": {result: lessResult(446, 179, 36.5),
			want: "https://napwake.dev/s/40/?v=1&with=267&without=446&src=1.542"},
		"30.4995 hours, the card's 30": {result: lessResult(446, 179, 30.4995),
			want: "https://napwake.dev/s/40/?v=1&with=267&without=446&src=1.25"},
		"a week and a minute and a half, the card's 7 days": {result: lessResult(446, 179, 168.024),
			want: "https://napwake.dev/s/40/?v=1&with=267&without=446&src=7"},
		"dry-run, with 30 minutes of history": {result: shortHistoryWithDryRunOnly(),
			want: "https://napwake.dev/s/9/?v=1&with=406&without=446&src=0.0208"},
		"a snapshot, measured in dry-run": {result: snapshotLess(),
			want: "https://napwake.dev/s/40/?v=1&with=267&without=446&src=0"},
		"100%": {result: lessResult(446, 446, 168),
			want: "https://napwake.dev/s/100/?v=1&with=0&without=446&src=7"},
		"rounded up to 100%": {result: lessResult(1000, 996, 168),
			want: "https://napwake.dev/s/100/?v=1&with=4&without=1000&src=7"},
		"what scales unknown": {result: couldResult(scalerUnknown),
			want: "https://napwake.dev/s/40/?v=1&with=267&without=446&src=7"},
		"pods billed, Autopilot": {result: couldResult(autopilotFound),
			want: "https://napwake.dev/s/40/?v=1&with=267&without=446&src=7"},
	}
	for name, tt := range scan {
		t.Run("scan, "+name, func(t *testing.T) {
			page := buildReport(tt.result, time.Hour)

			assert.Equal(t, tt.want, page.ShareLink)
			c := page.Could
			require.NotNil(t, c)
			got := renderHTML(t, tt.result)
			pageHas(t, got, "<b>"+strconv.Itoa(c.Percent)+"% less</b><span>Paused when idle, these "+
				"workloads could cost about "+c.With+" a month instead of "+c.Without+".</span>",
				"the card the link is of")
			if span := tt.result.History.Span(); page.History && span != "" {
				src := regexp.MustCompile(`&src=([0-9.]+)$`).FindStringSubmatch(page.ShareLink)
				require.Len(t, src, 2)
				assert.Equal(t, span, spanOfDays(t, src[1]), "the link names the card's window")
				pageHas(t, got, `data-source="`+replayedOver(tt.result.History)+` of Prometheus history`)
			}
		})
	}

	status := map[string]struct {
		objs []client.Object
		want string
	}{
		"under $100": {objs: []client.Object{earlyMonth()},
			want: "https://napwake.dev/s/less-73/?v=1&bill=8.60&without=31.94&saved=23.34&days=2"},
		"from $100": {objs: []client.Object{monthWorkload("api", "$40.00", "$110.00", "$100.00", 8*24*time.Hour,
			"$30.00", "$28.00")},
			want: "https://napwake.dev/s/less-67/?v=1&bill=50.00&without=150&saved=100&days=8"},
		"over $1,000": {objs: []client.Object{monthWorkload("api", "$300.00", "$1200.00", "$900.00", 8*24*time.Hour,
			"$300.00", "$250.00")},
			want: "https://napwake.dev/s/less-60/?v=1&bill=600&without=1500&saved=900&days=8"},
		"100%": {objs: []client.Object{monthWorkload("api", "$0.00", "$24.62", "$24.62", 53*time.Hour, "$1.74",
			"$1.74")},
			want: "https://napwake.dev/s/less-100/?v=1&bill=0&without=24.62&saved=24.62&days=2"},
	}
	for name, tt := range status {
		t.Run("status, "+name, func(t *testing.T) {
			page := statusPageAt(t, tt.objs...)

			assert.Equal(t, tt.want, page.ShareLink)
			m := page.Month
			require.NotNil(t, m)
			got := statusPartsOf(t, renderStatusPageOf(t, page))
			assert.Contains(t, got.lead, "<b>"+strconv.Itoa(m.SavedPercent)+"% less</b><span>"+m.Bill+" a month instead of "+
				m.Without+", at this month&#39;s pace.</span>", "the card the link is of")
		})
	}
}

// There's a link only where the page shows a "less" card, and only when
// every amount on it is a number (cmo-075): none for "Not on the bill
// yet", once Napwake is live, for the most a snapshot could free, with
// nothing to free, for an estimate from too little history, under 1% or
// over 100%, with an amount that reads <$1 or <$0.01, before a day is
// counted, where saved can't be measured, and with no workloads.
func TestShareLink_NoneWithoutAPercentage(t *testing.T) {
	scan := map[string]scanResult{
		"not on the bill yet": couldResult(noScaler),
		"live":                historyResult(),
		"at most, a snapshot": sampleCluster(),
		"nothing to free":     goodNewsResult(),
		"too little history":  shortHistoryResult(),
		"under 1%":            lessResult(446, 1, 168),
		"0%":                  lessResult(446, 0, 168),
		"over 100%":           lessResult(446, 500, 168),
		"with under a dollar": lessResult(10, 9.9, 168),
		"none judged":         noneJudged(),
		"no workloads":        {Cluster: "c", ClusterReport: &discovery.ClusterReport{Workloads: []Workload{}}},
	}
	for name, result := range scan {
		t.Run("scan, "+name, func(t *testing.T) {
			page := buildReport(result, time.Hour)

			assert.Empty(t, page.ShareLink)
			got := renderHTML(t, result)
			pageLacks(t, got, copyLinkButton)
			pageLacks(t, got, "napwake.dev/s/")
			var out bytes.Buffer
			require.NoError(t, writeScan(&out, result, scanOptions{output: outputTable}))
			assert.NotContains(t, out.String(), "Share these numbers")
		})
	}

	unmeasured := monthWorkload("web", "$1.00", "$3.00", "$0.00", 53*time.Hour, "$0.20", "$0.00")
	unmeasured.Status.Cost.SavingsUnknown = cantListNodes
	status := map[string][]client.Object{
		"saved can't be measured": {earlyMonth(), unmeasured},
		"not on the bill yet":     {monthWorkload("api", "$7.32", "$24.62", "$0.00", 53*time.Hour, "$0.80", "$0.00")},
		"under 1% saved":          {monthWorkload("api", "$7.32", "$24.62", "$0.10", 53*time.Hour, "$0.80", "<$0.01")},
		"saved under a cent":      {monthWorkload("api", "$0.00", "$0.20", "<$0.01", 53*time.Hour, "$0.10", "<$0.01")},
		"pending":                 {monthWorkload("api", "pending", "pending", "pending", 20*time.Hour, "$0.40", "$0.30")},
		"none":                    {monthWorkload("api", "$12.00", "$0.00", "$0.00", 3*24*time.Hour, "$0.00", "$0.00")},
		"no workloads":            nil,
	}
	for name, objs := range status {
		t.Run("status, "+name, func(t *testing.T) {
			page := statusPageAt(t, objs...)

			assert.Empty(t, page.ShareLink)
			got := renderStatusPageOf(t, page)
			pageLacks(t, got, copyLinkButton)
			pageLacks(t, got, "napwake.dev/s/")
		})
	}

	t.Run("status, saved under a cent shows its card", func(t *testing.T) {
		page := statusPageAt(t, status["saved under a cent"]...)
		require.NotNil(t, page.Month)
		assert.Equal(t, "<$0.01", page.Month.Saved)
		assert.True(t, page.Month.OnTheBill, "the card the link would be of is there")
	})
}

// shortHistoryWithDryRunOnly is too little history with nothing replayed
// that would sleep, so what dry-run measured leads, which the page shows a
// card of, without the caveat an estimate from the history would have.
func shortHistoryWithDryRunOnly() scanResult {
	result := shortHistoryResult()
	result.Totals.Replayed.Sleepers, result.Totals.Replayed.MonthlyFreed = 0, 0
	return result
}

// copyLinkButton is how Copy link starts in a page's markup. The script
// names data-copy-link too, on every page.
const copyLinkButton = `<button type="button" class="btn" data-copy-link data-link="`

// shareNumbers is every share link a page can make: its path's percentage,
// then each field, in order, a plain number, and nothing else.
var shareNumbers = regexp.MustCompile(`^https://napwake\.dev/s/(?:\d+|less-\d+)/\?v=1` +
	`(?:&with=\d+(?:\.\d{2})?&without=\d+(?:\.\d{2})?&src=\d+(?:\.\d{1,3})?` +
	`|&bill=\d+(?:\.\d{2})?&without=\d+(?:\.\d{2})?&saved=\d+(?:\.\d{2})?&days=\d+)$`)

// copyLinkHandler is what the page's script does with Copy link.
var copyLinkHandler = regexp.MustCompile(`(?s)each\(\$\$\("\[data-copy-link\]"\), function \(b\) \{.*?\n  \}\);`)

// A link never holds the cluster's name, the Prometheus source or any text,
// with the box ticked or not (cmo-075): it's numbers only, the same whatever
// the cluster is called, and it's in the page only as Copy link's own
// data, which the script copies as it is, without reading the box.
func TestShareLink_NumbersOnly(t *testing.T) {
	named := func(result scanResult, cluster string) scanResult {
		result.Cluster, result.Context = cluster, cluster
		return result
	}
	scanA := named(couldResult(karpenterFound), "staging-7 (EKS us-east-1)")
	scanB := named(couldResult(karpenterFound), "billing-prod-2")
	statusA := statusPageAt(t, earlyMonth())
	statusB := statusPageAt(t, earlyMonth())
	statusB.Cluster = "billing-prod-2"

	pages := map[string]struct {
		html, other, link, cluster string
	}{
		"scan": {html: renderHTML(t, scanA), other: renderHTML(t, scanB),
			link: buildReport(scanA, time.Hour).ShareLink, cluster: scanA.Cluster},
		"status": {html: renderStatusPageOf(t, statusA), other: renderStatusPageOf(t, statusB),
			link: statusA.ShareLink, cluster: statusA.Cluster},
	}
	for name, p := range pages {
		t.Run(name, func(t *testing.T) {
			assert.Regexp(t, shareNumbers, p.link)
			for _, never := range []string{p.cluster, "staging", "EKS", "monitoring", "prometheus", "Prometheus"} {
				assert.NotContains(t, p.link, never)
			}
			pageHas(t, p.html, html.EscapeString(p.cluster), "the page names the cluster")

			got := regexp.MustCompile(`data-link="([^"]*)"`).FindAllStringSubmatch(p.html, -1)
			require.Len(t, got, 2, "Copy link in the panel and in the share dialog")
			for _, m := range got {
				assert.Equal(t, p.link, html.UnescapeString(m[1]))
			}
			assert.Equal(t, 3, strings.Count(p.html, html.EscapeString(p.link)),
				"the link is only Copy link's, and the dialog's text of it")
			otherLinks := regexp.MustCompile(`data-link="([^"]*)"`).FindAllStringSubmatch(p.other, -1)
			require.NotEmpty(t, otherLinks)
			assert.Equal(t, p.link, html.UnescapeString(otherLinks[0][1]), "the same whatever the cluster is called")

			handler := copyLinkHandler.FindString(p.html)
			require.NotEmpty(t, handler, "the script copies the link")
			assert.Contains(t, handler, "copyText(b.dataset.link,", "it copies Copy link's own data")
			for _, never := range []string{"named()", "summary()", "cluster:", "optin"} {
				assert.NotContains(t, handler, never, "it never reads the box or the summary")
			}
		})
	}
}

// The scan's table ends with the link, as the page has it, for whoever
// never opens the report (cmo-075). JSON and YAML, which programs read,
// never have it, nor does a table with no link.
func TestWriteScan_ShareLinkLast(t *testing.T) {
	result := couldResult(karpenterFound)
	want := buildReport(result, time.Hour).ShareLink
	require.NotEmpty(t, want)

	var out bytes.Buffer
	require.NoError(t, writeScan(&out, result, scanOptions{output: outputTable, idleAfter: time.Hour}))
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	assert.Equal(t, "Share these numbers: "+want, lines[len(lines)-1], "the last line")
	assert.Equal(t, 1, strings.Count(out.String(), "napwake.dev/s/"))

	for _, output := range []string{outputJSON, outputYAML} {
		t.Run(output, func(t *testing.T) {
			var out bytes.Buffer
			require.NoError(t, writeScan(&out, result, scanOptions{output: output}))
			assert.NotContains(t, out.String(), "napwake.dev/s/")
			assert.NotContains(t, out.String(), "Share these numbers")
		})
	}

	t.Run("no link", func(t *testing.T) {
		var out bytes.Buffer
		require.NoError(t, writeScan(&out, couldResult(noScaler), scanOptions{output: outputTable}))
		assert.NotContains(t, out.String(), "Share these numbers")
		assert.NotContains(t, out.String(), "napwake.dev/s/")
	})
}

// Copy link sits beside Copy summary, in the panel and in the share
// dialog, only on a page with a link, and the dialog's line on what the
// page sends says what the link holds and when it goes to napwake.dev.
func TestPages_CopyLinkOnlyWithALink(t *testing.T) {
	const withLink = "Your browser drew the card and wrote the summary here, on this computer. The link holds the " +
		"card&#39;s numbers only, never a name. They reach napwake.dev only once someone opens it, or a chat previews it."
	const noLink = "That&#39;s why there&#39;s no link to share: send the image, the summary or the file."
	statusLink, statusNone := statusPageAt(t, earlyMonth()), statusPageAt(t)
	pages := map[string]struct {
		html, link string
	}{
		"scan with a link": {html: renderHTML(t, couldResult(karpenterFound)),
			link: buildReport(couldResult(karpenterFound), time.Hour).ShareLink},
		"scan without":       {html: renderHTML(t, couldResult(noScaler))},
		"scan, live":         {html: renderHTML(t, historyResult())},
		"status with a link": {html: renderStatusPageOf(t, statusLink), link: statusLink.ShareLink},
		"status without":     {html: renderStatusPageOf(t, statusNone)},
	}
	for name, p := range pages {
		t.Run(name, func(t *testing.T) {
			pageHas(t, p.html, `"btn" data-copy-summary>Copy summary</button>`)
			pageHas(t, p.html, "This page sends nothing anywhere.")
			if p.link == "" {
				pageLacks(t, p.html, ">Copy link</button>")
				pageLacks(t, p.html, copyLinkButton)
				pageHas(t, p.html, noLink)
				return
			}
			button := `<button type="button" class="btn" data-copy-link data-link="` + html.EscapeString(p.link) +
				`">Copy link</button>`
			pageHas(t, p.html, `<button type="button" class="btn" data-copy-summary>Copy summary</button>
          `+button, "in the panel, beside Copy summary")
			pageHas(t, p.html, `<div class="share-btns" style="margin-top:8px"><button type="button" class="btn" `+
				`data-copy-summary>Copy summary</button>`+button+`<p class="link-box" data-link-box>`+
				html.EscapeString(p.link)+`</p></div>`, "in the dialog, beside Copy summary")
			assert.Equal(t, 2, strings.Count(p.html, "data-copy-link data-link"))
			pageHas(t, p.html, withLink)
			pageLacks(t, p.html, noLink)
		})
	}
}

// The link is the page's data, not its code: a page with one runs the same
// script as a page without, so the policy's hash of it holds whatever the
// numbers are.
func TestPages_ShareLinkIsDataNotScript(t *testing.T) {
	scripts := func(page string) []string {
		var out []string
		for _, m := range inlineBlock.FindAllStringSubmatch(page, -1) {
			if m[1] == "script" {
				out = append(out, m[2])
			}
		}
		return out
	}
	scanWith, scanWithout := renderHTML(t, couldResult(karpenterFound)), renderHTML(t, couldResult(noScaler))
	statusWith := renderStatusPageOf(t, statusPageAt(t, earlyMonth()))
	statusWithout := renderStatusPageOf(t, statusPageAt(t))
	require.Contains(t, scanWith, copyLinkButton)
	require.Contains(t, statusWith, copyLinkButton)

	assert.Equal(t, scripts(scanWithout), scripts(scanWith))
	assert.Equal(t, scripts(statusWithout), scripts(statusWith))
	for _, page := range []string{scanWith, statusWith} {
		for _, script := range scripts(page) {
			assert.NotContains(t, script, "napwake.dev/s/", "the script holds no link")
		}
	}
}

// A link carries an amount only as the plain number the card writes, and
// none when one isn't a number, such as <$1 or <$0.01, or when what the
// workloads would cost without Napwake is $0, which no percentage is of.
func TestShareAmounts(t *testing.T) {
	for _, tt := range []struct {
		cards []string
		want  []string
	}{
		{cards: []string{"$267", "$446"}, want: []string{"267", "446"}},
		{cards: []string{"$1,234", "$12,345,678"}, want: []string{"1234", "12345678"}},
		{cards: []string{"$8.60", "$31.94", "$23.34"}, want: []string{"8.60", "31.94", "23.34"}},
		{cards: []string{"$0", "$446"}, want: []string{"0", "446"}},
		{cards: []string{"$267", "$0"}},
		{cards: []string{"<$1", "$446"}},
		{cards: []string{"$8.60", "<$0.01"}},
		{cards: []string{"$8.60", "$31.94", "<$0.01"}},
		{cards: []string{"-$5", "$10"}},
		{cards: []string{"$1234", "$10"}},
		{cards: []string{"$7.5", "$10"}},
		{cards: []string{"7.59", "$10"}},
		{cards: []string{"$7.59 ", "$10"}},
	} {
		got, ok := shareAmounts(tt.cards[0], tt.cards[1], tt.cards[2:]...)
		assert.Equal(t, tt.want != nil, ok, "%q", tt.cards)
		assert.Equal(t, tt.want, got, "%q", tt.cards)
	}
}

// spanOfDays spells a link's src as napwake.dev's page does (share.mjs's
// history(), cmo-075): minutes under an hour, hours under two days, then
// days, each rounded half up.
func spanOfDays(t *testing.T, src string) string {
	t.Helper()
	days, err := strconv.ParseFloat(src, 64)
	require.NoError(t, err, "src %q", src)
	spell := func(n float64, one string) string {
		if n == 1 {
			return "1 " + one
		}
		return fmt.Sprintf("%d %ss", int(n), one)
	}
	hours := days * 24
	switch minutes := math.Floor(hours*60 + .5); {
	case minutes < 60:
		return spell(math.Max(minutes, 1), "minute")
	case math.Floor(hours+.5) < 48:
		return spell(math.Floor(hours+.5), "hour")
	}
	return spell(math.Floor(hours/24+.5), "day")
}

// The link's src names the window the card does (brief 03): read back as
// napwake.dev's page reads it, it's the span the card's words say, at every
// edge where they change, a minute's, an hour's, a day's and a unit's, from
// either side, and every few seconds or minutes between.
func TestShareDays_NamesTheCardsWindow(t *testing.T) {
	var spans []time.Duration
	edge := func(at time.Duration) {
		for _, e := range []time.Duration{0, time.Nanosecond, time.Millisecond, time.Second} {
			spans = append(spans, at-e, at+e)
		}
	}
	for m := time.Duration(1); m <= 60; m++ {
		edge(m*time.Minute - 30*time.Second)
	}
	for h := time.Duration(1); h <= 48; h++ {
		edge(h*time.Hour - 30*time.Minute)
	}
	for d := time.Duration(2); d <= 400; d++ {
		edge(d*24*time.Hour - 12*time.Hour)
	}
	for d := time.Second; d < 3*24*time.Hour; d += 7 * time.Second {
		spans = append(spans, d)
	}
	for d := 3 * 24 * time.Hour; d < 400*24*time.Hour; d += 13 * time.Minute {
		spans = append(spans, d)
	}
	page := reportPage{History: true, Workloads: []workloadRow{{}}, NodeScale: nodeScale{State: scaleFound},
		Could: &could{Without: "$446", With: "$267", Percent: 40}}
	link := regexp.MustCompile(`&src=([0-9.]+)$`)
	wrong := 0
	for _, d := range spans {
		if d <= 0 {
			continue
		}
		history := &discovery.HistorySource{Hours: d.Hours()}
		m := link.FindStringSubmatch(scanShareLink(page, history))
		require.Len(t, m, 2, "%s: a link", d)
		card, src := history.Span(), m[1]
		if got := spanOfDays(t, src); got != card {
			wrong++
			if wrong <= 10 {
				t.Errorf("%s of history: the card says %q, src=%s says %q", d, card, src, got)
			}
		}
		if d < 59*time.Minute {
			assert.Regexp(t, `^0(\.\d{1,4})?$`, src, "%s: four places at most under an hour", d)
		} else if d > 61*time.Minute {
			assert.Regexp(t, `^\d+(\.\d{1,3})?$`, src, "%s: three places at most from an hour", d)
		}
	}
	assert.Zero(t, wrong, "of %d spans", len(spans))
	assert.Greater(t, len(spans), 50000, "the sweep is fine-grained")
	assert.Equal(t, "0", shareDays(0), "a snapshot")
}

// sayHandler is the page's script's confirmation, and linkFailure what
// Copy link does when the browser won't copy.
var (
	sayHandler  = regexp.MustCompile(`(?s)  function say\(text, change\) \{.*?\n  \}\n`)
	linkFailure = regexp.MustCompile(`(?s)if \(!ok\) \{\s*if \(!b\.closest\("dialog"\)\) openShare\(\);\s*` +
		`selectLink\(\);\s*` +
		`say\("Your browser didn\\u2019t allow copying; copy the link below instead"\);\s*\}`)
)

// When the browser won't copy the link, the reader can still have it
// (brief 03): the share dialog has it as text to select, with no address
// to follow, and Copy link opens the dialog with it selected, saying so.
// The confirmation shows inside the dialog while it's open, not under it.
func TestPages_LinkToSelectWhenCopyingFails(t *testing.T) {
	statusLink := statusPageAt(t, earlyMonth())
	pages := map[string]struct{ html, link string }{
		"scan": {html: renderHTML(t, couldResult(karpenterFound)),
			link: buildReport(couldResult(karpenterFound), time.Hour).ShareLink},
		"status": {html: renderStatusPageOf(t, statusLink), link: statusLink.ShareLink},
	}
	for name, p := range pages {
		t.Run(name, func(t *testing.T) {
			dialog := regexp.MustCompile(`(?s)<dialog class="share".*?</dialog>`).FindString(p.html)
			require.NotEmpty(t, dialog)
			pageHas(t, dialog, `<p class="link-box" data-link-box>`+html.EscapeString(p.link)+`</p>`,
				"the link as text in the dialog")
			assert.NotRegexp(t, `href="https://napwake\.dev/s/`, p.html, "never an address to follow")
			pageHas(t, p.html, ".link-box {", "styled to read and select")
			assert.Regexp(t, linkFailure, p.html, "opened with the link selected, saying so")
			pageHas(t, p.html, "function selectLink() {")
			say := sayHandler.FindString(p.html)
			require.NotEmpty(t, say, "the page's confirmation")
			assert.Contains(t, say, `var host = doc.querySelector("dialog[open]") || body;`)
			assert.Contains(t, say, "if (toast.parentNode !== host) host.appendChild(toast);",
				"inside the open dialog")
		})
	}

	t.Run("no link", func(t *testing.T) {
		for _, page := range []string{renderHTML(t, couldResult(noScaler)), renderStatusPageOf(t, statusPageAt(t))} {
			pageLacks(t, page, `data-link-box>`)
		}
	})
}
