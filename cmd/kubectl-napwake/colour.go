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
	"cmp"
	"strings"
	"unicode/utf8"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

// palette paints status for a person at a terminal: paused, resuming and
// running in their colours, each beside a door, shut, ajar or open, so the
// states read at a glance without relying on colour alone; what needs
// attention in red; and the table's header and dry-run in grey. Its zero
// value paints nothing, as for output that isn't a terminal's, which
// scripts read.
type palette struct {
	colour bool
	doors  bool
}

// SGR codes for the palette, chosen for contrast on the common terminals'
// default palettes, light and dark: bright blue, since plain blue is too
// dark on black, and yellow made bold, since it's faint on white.
const (
	sgrPaused    = "94"
	sgrResuming  = "1;33"
	sgrRunning   = "32"
	sgrAttention = "1;31"
	sgrCondition = "31"
	sgrQuiet     = "90"
	sgrHeading   = "1"
)

// paletteFor is how to paint status's table for output that's a terminal
// or not, on goos, with getenv: nothing unless it's a terminal. There,
// colour unless NO_COLOR is set, as no-color.org asks, or TERM is dumb,
// and on Windows only in Windows Terminal, since the older console shows
// the codes as text. Doors only where the locale is UTF-8, and not Chinese,
// Japanese or Korean, where terminals may draw them two cells wide.
func paletteFor(terminal bool, goos string, getenv func(string) string) palette {
	if !terminal {
		return palette{}
	}
	colour := getenv("NO_COLOR") == "" && getenv("TERM") != "dumb" && (goos != goosWindows || getenv("WT_SESSION") != "")
	locale := strings.ToLower(cmp.Or(getenv("LC_ALL"), getenv("LC_CTYPE"), getenv("LANG")))
	utf8Locale := strings.Contains(locale, "utf-8") || strings.Contains(locale, "utf8")
	wide := strings.HasPrefix(locale, "zh") || strings.HasPrefix(locale, "ja") || strings.HasPrefix(locale, "ko")
	return palette{colour: colour, doors: utf8Locale && !wide}
}

func (pl palette) paint(sgr, s string) string {
	if !pl.colour || s == "" {
		return s
	}
	return "\x1b[" + sgr + "m" + s + "\x1b[0m"
}

// phaseColour is a phase's colour and door, for the phases that have one.
func phaseColour(phase v1alpha1.WorkloadPhase) (sgr, door string, ok bool) {
	switch phase {
	case v1alpha1.PhasePaused:
		return sgrPaused, "▮", true
	case v1alpha1.PhaseResuming:
		return sgrResuming, "◧", true
	case v1alpha1.PhaseRunning:
		return sgrRunning, "▯", true
	}
	return "", "", false
}

// stateCell is a workload's STATE, with its door in front when the palette
// has doors, and two spaces for a state without one, so the words line up.
// It's laid out unpainted; paintState colours it once the table is.
func (pl palette) stateCell(wl statusWorkload) string {
	cell := stateCell(wl)
	if !pl.doors {
		return cell
	}
	if _, door, ok := phaseColour(cmp.Or(wl.Phase, v1alpha1.PhaseCreating)); ok {
		return door + " " + cell
	}
	return "  " + cell
}

// paintTable colours a table tabwriter has laid out: its header in grey,
// and each row's STATE, found at the header's STATE column, in its phase's
// colour, with its dry-run in grey. The codes go in after the layout, so
// they never count towards a column's width.
func (pl palette) paintTable(table string, workloads []statusWorkload) string {
	if !pl.colour {
		return table
	}
	lines := strings.SplitAfter(table, "\n")
	column := utf8.RuneCountInString(lines[0][:strings.Index(lines[0], "STATE")])
	header, end := strings.CutSuffix(lines[0], "\n")
	lines[0] = pl.paint(sgrQuiet, header)
	if end {
		lines[0] += "\n"
	}
	for i, wl := range workloads {
		if i+1 < len(lines) {
			lines[i+1] = pl.paintState(lines[i+1], column, wl)
		}
	}
	return strings.Join(lines, "")
}

// paintState colours the STATE cell starting column runes into line.
func (pl palette) paintState(line string, column int, wl statusWorkload) string {
	at := 0
	for range column {
		_, size := utf8.DecodeRuneInString(line[at:])
		at += size
	}
	cell := pl.stateCell(wl)
	rest, ok := strings.CutPrefix(line[at:], cell)
	if !ok {
		return line
	}
	sgr, _, coloured := phaseColour(cmp.Or(wl.Phase, v1alpha1.PhaseCreating))
	state, dryRun, _ := strings.Cut(cell, " (dry-run)")
	if coloured {
		state = pl.paint(sgr, state)
	}
	if wl.DryRun {
		state += " " + pl.paint(sgrQuiet, "(dry-run)") + dryRun
	}
	return line[:at] + state + rest
}

// phaseCounts is the summary's count of workloads in each phase, each
// count in its phase's colour.
func (pl palette) phaseCounts(workloads []statusWorkload) string {
	return countPhases(workloads, func(phase v1alpha1.WorkloadPhase, count string) string {
		if sgr, _, ok := phaseColour(phase); ok {
			return pl.paint(sgr, count)
		}
		return count
	})
}

// paintProblems colours each problem's type in a table of them tabwriter
// has laid out, a line each, after the workload it's of.
func (pl palette) paintProblems(table string, problems []problem) string {
	if !pl.colour {
		return table
	}
	lines := strings.SplitAfter(table, "\n")
	for i, pr := range problems {
		if i >= len(lines) {
			break
		}
		after := len("  " + pr.Namespace + "/" + pr.Workload)
		if at := strings.Index(lines[i][after:], pr.Type); at >= 0 {
			at += after
			lines[i] = lines[i][:at] + pl.paint(sgrCondition, pr.Type) + lines[i][at+len(pr.Type):]
		}
	}
	return strings.Join(lines, "")
}
