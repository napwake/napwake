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
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"time"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/doorman"
)

//go:embed statuspage.html
var statusPageHTML string

var statusPageTemplate = template.Must(template.New("status").Parse(statusPageHTML))

// statusPagePattern names a temporary status page, and is what --html
// given without a file stands for.
const statusPagePattern = "napwake-status-*.html"

// statusPage is what the status web page shows, worded from the same
// status and helpers as the terminal table, so the two never disagree.
type statusPage struct {
	Cluster   string
	At        string
	Summary   string
	NodeScale nodeScale
	TimeZone  string
	Spot      string
	Figures   []figure
	// FreedThisMonth and SavedThisMonth are what the first two figures
	// say, as numbers, for the page to lead with and draw the meter from.
	// SavedMeasurable says what's saved can be measured for every
	// workload; without it, SavedThisMonth is of those it can.
	FreedThisMonth, SavedThisMonth float64
	SavedMeasurable                bool
	// Month is the workloads' month at this month's pace, the page's
	// before and after, or nil with no workloads.
	Month *pageMonth
	// FreedUnderACent and SavedUnderACent say the first two figures read
	// <$0.01, which the meter puts in words.
	FreedUnderACent, SavedUnderACent bool
	Notes                            []string
	Attention                        []attentionRow
	Workloads                        []statusPageRow
	DryRun                           bool
	Recent                           []recentRow
	// NoRecent says why no pauses and wakes are listed, when none are.
	NoRecent string
	Since    string
	Mark     string
	// PausingOff is the line status leads with while pausing is off for
	// some or all of the workloads, or empty.
	PausingOff string
}

// attentionRow is a problem as the page lists it. Workload is the
// target's name, as the table shows it; Target is its kind and name, as
// the Workloads table names it, for a page to match the two by, and
// ManagedWorkload what to kubectl describe.
type attentionRow struct {
	Workload, Namespace, Type, Message string
	Target, ManagedWorkload            string
}

type statusPageRow struct {
	Workload, Namespace, ManagedWorkload string
	State, StateClass, For               string
	LastActivity, Next, Forecast         string
	Freed, SavedNote, WouldFree          string
	ForSort, FreedSort, WouldFreeSort    float64
	// WokenBy is what woke it, as status -o json gives it.
	WokenBy string
}

// recentRow is a pause or wake, with its Kind and, for a wake, what woke
// it, as status -o json gives them.
type recentRow struct {
	At, Ago, Workload, Namespace, Message string
	Kind, WokenBy                         string
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
	if err := executeWithPolicy(w, statusPageTemplate, buildStatusPage(result, since, now)); err != nil {
		return fmt.Errorf("writing the status page: %w", err)
	}
	return nil
}

func buildStatusPage(result statusResult, since time.Duration, now time.Time) statusPage {
	page := statusPage{
		Cluster:   result.Cluster,
		At:        now.UTC().Format("2 January 2006, 15:04 MST"),
		NodeScale: nodeScaleFor(result.NodeAutoscaler),
		TimeZone:  timeZoneLine(result.ForecastTimeZones),
		Spot:      spotLine(result.Workloads),
		Since:     span(since),
		Mark:      doorman.Mark,
		Figures:   statusFigures(result),

		FreedThisMonth:  result.FreedThisMonth,
		SavedThisMonth:  result.SavedThisMonth,
		SavedMeasurable: len(result.SavingsUnknown) == 0,
		Month:           pageMonthOf(result.MonthAtPace, result.NodesNotScalingDown),

		FreedUnderACent: underACent(result.FreedThisMonth),
		SavedUnderACent: underACent(result.SavedThisMonth),
	}
	page.PausingOff = pausingOffLine(result)
	if len(result.Workloads) == 0 {
		page.Summary = "Napwake manages no workloads here. kubectl napwake scan finds idle ones; label one " +
			v1alpha1.LabelManaged + "=true for Napwake to manage it."
	} else {
		page.Summary = fmt.Sprintf("Napwake manages %s: %s.", countOf(len(result.Workloads), "workload"),
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
				"kubectl napwake nodes shows why.", dollars(result.FreedThisMonth), dollars(result.SavedThisMonth))})
	}
	for _, pr := range result.Problems {
		page.Attention = append(page.Attention, attentionRow{Workload: pr.Workload, Namespace: pr.Namespace,
			Type: pr.Type, Message: oneLine(pr.Message), Target: pr.Target, ManagedWorkload: pr.ManagedWorkload})
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
			Freed:         statusFreedCell(wl),
			FreedSort:     wl.FreedThisMonth,
			SavedNote:     savedNote(wl),
			WouldFree:     wouldFreeCell(wl),
			WouldFreeSort: -1,
			WokenBy:       wl.WokenBy,
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
			Workload: h.Workload, Namespace: h.Namespace, Message: oneLine(h.Message), Kind: h.Kind,
			WokenBy: h.WokenBy, AtSort: h.At.Unix()})
	}
	return page
}

// pageMonth is the month at this month's pace as the status page writes
// it: monthAtPace's amounts as money, and which of its leads it takes.
type pageMonth struct {
	Without, Saved, Bill, Freed, With string
	SavedPercent, FreedPercent        int
	SavedMeasurable                   bool
	Days                              int
	State                             string
	// OnTheBill says what comes off the bill leads, as a share of Without:
	// it's measured for every workload, it's at least 1%, and nodes are
	// scaling down. Without it, the page leads with what pausing frees, as
	// what's possible, with no big figure, since a share of the bill that
	// isn't coming off it would claim more than it shows.
	OnTheBill bool
	// NothingSaved says none of what pausing frees comes off the bill, as
	// measured, which the page says apart from a little.
	NothingSaved bool
}

// pageMonthOf is m as the page writes it, or nil for none.
// nodesNotScalingDown is status's judgement on this month so far, which
// keeps what comes off the bill from leading while it's far below what
// pausing frees.
func pageMonthOf(m *monthAtPace, nodesNotScalingDown bool) *pageMonth {
	if m == nil {
		return nil
	}
	return &pageMonth{
		Without: money(m.Without), Saved: money(m.Saved), Bill: money(m.Bill), Freed: money(m.Freed),
		With: money(m.With), SavedPercent: m.SavedPercent, FreedPercent: m.FreedPercent,
		SavedMeasurable: m.SavedMeasurable, Days: m.Days, State: m.State,
		OnTheBill:    m.SavedMeasurable && m.SavedPercent >= 1 && !nodesNotScalingDown,
		NothingSaved: m.Saved == 0,
	}
}

// statusFigures leads with what pausing has freed so far this month and
// what of that was saved, to the cent under $100, so they add up under the
// month at this pace, then, when any workload is in dry-run, what dry-run
// measured pausing would have freed.
func statusFigures(result statusResult) []figure {
	saved := figure{Value: money(result.SavedThisMonth), Label: "saved of it",
		Detail: "what came off the bill as nodes scaled down", Kind: figureSaved, Amount: result.SavedThisMonth}
	if len(result.SavingsUnknown) > 0 {
		saved.Detail = "for the workloads whose savings can be measured"
	}
	figures := []figure{
		{Value: money(result.FreedThisMonth), Label: "freed by pausing so far this month",
			Detail: "what the paused replicas request, at list price", Kind: figureFreed,
			Amount: result.FreedThisMonth},
		saved,
	}
	if n, since := dryRunMeasured(result.Workloads); n > 0 && since != nil {
		figures = append(figures, figure{Value: dollars(result.WouldFree), Label: "pausing would have freed",
			Detail: fmt.Sprintf("measured in dry-run since %s, for %s", since.Format("Jan 2"),
				countOf(n, "workload")), Kind: figureWouldHaveFreed, Amount: result.WouldFree})
	}
	return figures
}

// savedNote says, under what pausing a workload freed, what of it was
// saved.
func savedNote(wl statusWorkload) string {
	if wl.FreedThisMonth == 0 {
		return ""
	}
	switch saved := statusSavedCell(wl); {
	case saved == "unknown":
		return "saved can't be measured"
	case wl.SavedThisMonth == 0:
		return "none of it saved yet"
	default:
		return saved + " saved"
	}
}

// stateClass is the door a phase gets on the page: paused, idle and
// running as the scan report draws them, as paused, idle and active, and
// pausing, resuming and creating, Creating for a workload with no phase yet.
func stateClass(phase v1alpha1.WorkloadPhase) string {
	switch phase {
	case v1alpha1.PhasePaused:
		return "paused"
	case v1alpha1.PhaseIdle:
		return "idle"
	case v1alpha1.PhaseRunning:
		return "active"
	case v1alpha1.PhasePausing:
		return "pausing"
	case v1alpha1.PhaseResuming:
		return "resuming"
	}
	return "creating"
}
