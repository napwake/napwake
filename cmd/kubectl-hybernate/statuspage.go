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
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"time"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
	"github.com/okedeji/hybernate/internal/doorman"
)

//go:embed statuspage.html
var statusPageHTML string

var statusPageTemplate = template.Must(template.New("status").Parse(statusPageHTML))

// statusPagePattern names a temporary status page, and is what --html
// given without a file stands for.
const statusPagePattern = "hybernate-status-*.html"

// statusPage is what the status web page shows, worded from the same
// status and helpers as the terminal table, so the two never disagree.
type statusPage struct {
	Cluster        string
	At             string
	Summary        string
	NodeAutoscaler string
	TimeZone       string
	Spot           string
	Figures        []figure
	Notes          []string
	Attention      []attentionRow
	Workloads      []statusPageRow
	DryRun         bool
	Recent         []recentRow
	// NoRecent says why no pauses and wakes are listed, when none are.
	NoRecent string
	Since    string
	Mark     string
}

type attentionRow struct {
	Workload, Namespace, Type, Message string
}

type statusPageRow struct {
	Workload, Namespace, ManagedWorkload string
	State, StateClass, For               string
	LastActivity, Next, Forecast         string
	Freed, SavedNote, WouldFree          string
	ForSort, FreedSort, WouldFreeSort    float64
}

type recentRow struct {
	At, Ago, Workload, Namespace, Message string
	AtSort                                int64
}

// writeStatusPage writes the status as a web page to path, or to a new
// temporary file when path is statusPagePattern, and says where on stderr,
// opening it in the browser first when open.
func writeStatusPage(stderr io.Writer, path string, result statusResult, since time.Duration, now time.Time,
	open bool) error {
	if path == statusPagePattern {
		path = ""
	}
	written, err := writePage(path, statusPagePattern, func(w io.Writer) error {
		return writeStatusHTML(w, result, since, now)
	})
	if err != nil {
		return err
	}
	showPage(stderr, "Status page", written, open)
	return nil
}

func writeStatusHTML(w io.Writer, result statusResult, since time.Duration, now time.Time) error {
	if err := statusPageTemplate.Execute(w, buildStatusPage(result, since, now)); err != nil {
		return fmt.Errorf("writing the status page: %w", err)
	}
	return nil
}

func buildStatusPage(result statusResult, since time.Duration, now time.Time) statusPage {
	page := statusPage{
		Cluster:        result.Cluster,
		At:             now.UTC().Format("2 January 2006, 15:04 MST"),
		NodeAutoscaler: reportAutoscaler(result.NodeAutoscaler),
		TimeZone:       timeZoneLine(result.ForecastTimeZones),
		Spot:           spotLine(result.Workloads),
		Since:          span(since),
		Mark:           doorman.Mark,
		Figures:        statusFigures(result),
	}
	if len(result.Workloads) == 0 {
		page.Summary = "Hybernate manages no workloads here. kubectl hybernate scan finds idle ones; label one " +
			v1alpha1.LabelManaged + "=true for Hybernate to manage it."
	} else {
		page.Summary = fmt.Sprintf("Hybernate manages %s: %s.", countOf(len(result.Workloads), "workload"),
			phaseCounts(result.Workloads))
	}
	for _, why := range result.SavingsUnknown {
		page.Notes = append(page.Notes, "What's saved can't be measured for some workloads: "+oneLine(why))
	}
	if result.Note != "" {
		page.Notes = append(page.Notes, result.Note)
	}

	if result.NodesNotScalingDown {
		page.Attention = append(page.Attention, attentionRow{Type: "Nodes not scaling down",
			Message: fmt.Sprintf("Pausing freed %s this month, but only %s of it came off the bill. "+
				"kubectl hybernate nodes shows why.", savedMoney(result.FreedThisMonth), savedMoney(result.SavedThisMonth))})
	}
	for _, pr := range result.Problems {
		page.Attention = append(page.Attention, attentionRow{Workload: pr.Workload, Namespace: pr.Namespace,
			Type: pr.Type, Message: oneLine(pr.Message)})
	}

	for _, wl := range result.Workloads {
		page.DryRun = page.DryRun || wl.DryRun
		row := statusPageRow{
			Workload:      wl.Target,
			Namespace:     wl.Namespace,
			State:         stateCell(wl),
			StateClass:    stateClass(wl.Phase),
			For:           sinceCell(wl.Since, now),
			LastActivity:  activityCell(wl, now),
			Next:          wl.Next,
			Forecast:      forecastCell(wl),
			Freed:         moneyCell(wl.FreedThisMonth),
			FreedSort:     wl.FreedThisMonth,
			SavedNote:     savedNote(wl),
			WouldFree:     wouldFreeCell(wl),
			WouldFreeSort: -1,
		}
		if wl.namedApart {
			row.ManagedWorkload = wl.Name
		}
		if wl.Since != nil {
			row.ForSort = now.Sub(*wl.Since).Seconds()
		}
		if wl.WouldFree != nil {
			row.WouldFreeSort = *wl.WouldFree
		}
		page.Workloads = append(page.Workloads, row)
	}

	switch {
	case result.EventsHidden:
		page.NoRecent = "Recent pauses and wakes aren't shown: your access doesn't allow reading events."
	case len(result.Recent) == 0:
		page.NoRecent = "No pauses or wakes in this period."
	}
	for _, h := range result.Recent {
		page.Recent = append(page.Recent, recentRow{At: h.At.UTC().Format("Jan 2, 15:04 MST"), Ago: ago(h.At, now),
			Workload: h.Workload, Namespace: h.Namespace, Message: oneLine(h.Message), AtSort: h.At.Unix()})
	}
	return page
}

// statusFigures leads with what pausing has freed this month and what of
// that was saved, then, when any workload is in dry-run, what dry-run
// measured pausing would have freed.
func statusFigures(result statusResult) []figure {
	saved := figure{Value: savedMoney(result.SavedThisMonth), Label: "saved of it",
		Detail: "what came off the bill as nodes scaled down"}
	if len(result.SavingsUnknown) > 0 {
		saved.Detail = "for the workloads whose savings can be measured"
	}
	figures := []figure{
		{Value: savedMoney(result.FreedThisMonth), Label: "freed by pausing this month",
			Detail: "what the paused replicas request, at list price"},
		saved,
	}
	if n, since := dryRunMeasured(result.Workloads); n > 0 && since != nil {
		figures = append(figures, figure{Value: savedMoney(result.WouldFree), Label: "pausing would have freed",
			Detail: fmt.Sprintf("measured in dry-run since %s, for %s", since.Format("Jan 2"),
				countOf(n, "workload"))})
	}
	return figures
}

// savedNote says, under what pausing a workload freed, what of it was
// saved.
func savedNote(wl statusWorkload) string {
	if wl.FreedThisMonth == 0 {
		return ""
	}
	switch saved := statusSavedCell(wl); saved {
	case "unknown":
		return "saved can't be measured"
	case "-":
		return "none of it saved yet"
	default:
		return saved + " saved"
	}
}

// stateClass is the dot a phase gets on the page, coloured as the scan
// report colours its states: paused, idle, or running.
func stateClass(phase v1alpha1.WorkloadPhase) string {
	switch phase {
	case v1alpha1.PhasePaused:
		return "paused"
	case v1alpha1.PhaseIdle:
		return "idle"
	case v1alpha1.PhaseRunning:
		return "active"
	}
	return ""
}
