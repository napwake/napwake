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

package sim

import (
	"cmp"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/okedeji/hybernate/internal/forecast"
)

// docsTable is a markdown table from the forecasting concepts page: the
// thresholds its columns are headed with, and each row's label and the
// numbers in its cells.
type docsTable struct {
	thresholds []int
	rows       map[string][][]float64
}

// number is a number in a cell, or "never", which is infinitely many days.
var number = regexp.MustCompile(`\d+(\.\d+)?|never`)

// readDocsTable reads the table after heading on the forecasting page whose
// header row has header in it.
func readDocsTable(t *testing.T, heading, header string) docsTable {
	t.Helper()
	page, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "concepts", "forecasting.md"))
	require.NoError(t, err)
	_, section, ok := strings.Cut(string(page), "\n"+heading+"\n")
	require.True(t, ok, "the page has %q", heading)
	var lines []string
	for line := range strings.SplitSeq(section, "\n") {
		if !strings.HasPrefix(line, "|") {
			if len(lines) > 0 {
				break
			}
			continue
		}
		if len(lines) > 0 || strings.Contains(line, header) {
			lines = append(lines, line)
		}
	}
	require.Greater(t, len(lines), 2, "a table under %q headed with %q", heading, header)
	cells := func(line string) []string {
		parts := strings.Split(strings.Trim(line, "|"), "|")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		return parts
	}
	table := docsTable{rows: map[string][][]float64{}}
	for _, column := range cells(lines[0])[1:] {
		if found := number.FindAllString(column, -1); len(found) > 0 {
			th, err := strconv.Atoi(found[len(found)-1])
			require.NoError(t, err)
			table.thresholds = append(table.thresholds, th)
		}
	}
	for _, line := range lines[2:] {
		row := cells(line)
		values := make([][]float64, 0, len(row)-1)
		for _, cell := range row[1:] {
			found := number.FindAllString(cell, -1)
			nums := make([]float64, 0, len(found))
			for _, s := range found {
				if s == "never" {
					nums = append(nums, math.Inf(1))
					continue
				}
				v, err := strconv.ParseFloat(s, 64)
				require.NoError(t, err)
				nums = append(nums, v)
			}
			values = append(values, nums)
		}
		table.rows[row[0]] = values
	}
	return table
}

// reachedDays is how many days the forecast took to reach phase, or
// infinitely many if it didn't within the run, which a median over seeds
// that mostly do reach it tolerates.
func reachedDays(r Report, phase forecast.Phase) float64 {
	d, ok := r.Reached(phase)
	if !ok {
		return math.Inf(1)
	}
	return d.Hours() / 24
}

func median(vs []float64) float64 {
	slices.Sort(vs)
	if len(vs)%2 == 1 {
		return vs[len(vs)/2]
	}
	return (vs[len(vs)/2-1] + vs[len(vs)/2]) / 2
}

// The forecasting page's "How Long It Takes" table is what the simulator
// measures: each cell, days to act and days to be fully active, rounded,
// the median over seeds for traffic drawn from one.
func TestDocs_HowLongItTakes(t *testing.T) {
	t.Parallel()
	office := func(uint64) Traffic { return OfficeHours(time.UTC) }
	scenarios := []struct {
		label   string
		traffic func(seed uint64) Traffic
		seeds   int
	}{
		{"Busy on weekdays", office, 1},
		{"The same, with people arriving and leaving up to 20 minutes", func(seed uint64) Traffic {
			return Noisy(time.UTC, seed, 20*time.Minute, 10)
		}, 20},
		{"The same, with people arriving and leaving up to an hour", func(seed uint64) Traffic {
			return Noisy(time.UTC, seed, 60*time.Minute, 4)
		}, 20},
		{"The same hours every day", func(uint64) Traffic {
			return Working(time.UTC, EveryDay, 9*time.Hour+10*time.Minute, 17*time.Hour+30*time.Minute, 40)
		}, 1},
		{"Always busy", func(uint64) Traffic { return Always(true, 40) }, 1},
		{"Always idle", func(uint64) Traffic { return Always(false, 0) }, 1},
	}
	table := readDocsTable(t, "### How Long It Takes", "`balanced` (75)")
	require.Len(t, table.rows, len(scenarios), "a row for each scenario the test measures")
	for _, sc := range scenarios {
		var row [][]float64
		for label, values := range table.rows {
			if strings.HasPrefix(label, sc.label) {
				row = values
			}
		}
		require.Len(t, row, len(table.thresholds), "the page has a row starting %q", sc.label)
		for i, th := range table.thresholds {
			var runs []Scenario
			for seed := uint64(1); seed <= uint64(sc.seeds); seed++ {
				set := DefaultSettings()
				set.Confidence = th
				runs = append(runs, Scenario{Duration: 10 * week, Traffic: sc.traffic(seed), Settings: set})
			}
			actsDays, fullDays := make([]float64, 0, sc.seeds), make([]float64, 0, sc.seeds)
			for _, r := range simulateAll(t, runs) {
				actsDays = append(actsDays, reachedDays(r, forecast.DailyActive))
				fullDays = append(fullDays, reachedDays(r, forecast.FullyActive))
			}
			acts, full := median(actsDays), median(fullDays)
			require.Len(t, row[i], 2, "%s at %d: days to act and to be fully active", sc.label, th)
			assertDays(t, acts, row[i][0], "%s at %d: acts after %.1f days", sc.label, th, acts)
			assertDays(t, full, row[i][1], "%s at %d: fully active after %.1f days", sc.label, th, full)
		}
	}
}

// assertDays checks a number of days measured against the page's, rounded,
// where "never" is a run that didn't get there.
func assertDays(t *testing.T, measured, page float64, msgAndArgs ...any) {
	t.Helper()
	if math.IsInf(page, 1) || math.IsInf(measured, 1) {
		assert.Equal(t, math.IsInf(page, 1), math.IsInf(measured, 1), msgAndArgs...)
		return
	}
	assert.InDelta(t, measured, page, 0.75, msgAndArgs...)
}

// The forecasting page's table of what each setting trades is what the
// simulator measures on loosely kept office hours, averaged over twenty
// seeds: a more eager setting has the workload ready on more mornings, and
// wakes it for nothing for longer.
func TestDocs_WhatEachSettingTrades(t *testing.T) {
	t.Parallel()
	table := readDocsTable(t, "## Forecast Setting", "Mornings ready")
	require.Len(t, table.rows, len(forecast.Modes))
	const seeds = 20
	ready, wasted := make([]float64, 0, len(forecast.Modes)), make([]float64, 0, len(forecast.Modes))
	for _, mode := range forecast.Modes {
		label := "`" + string(mode) + "`"
		row, ok := table.rows[label]
		require.True(t, ok, "a row for %s", label)
		var runs []Scenario
		for seed := uint64(1); seed <= seeds; seed++ {
			set := DefaultSettings()
			set.Confidence = mode.Threshold()
			runs = append(runs, Scenario{Duration: 8 * week, Traffic: Noisy(time.UTC, seed, 60*time.Minute, 4),
				Settings: set})
		}
		var r, w, used float64
		for _, report := range simulateAll(t, runs) {
			s := report.Summarize(monday)
			r += float64(s.Ready)
			w += s.WastedHours
			used += float64(s.UsedDays)
		}
		require.Len(t, row, 2)
		require.Len(t, row[0], 1)
		assert.InDelta(t, r/seeds, row[0][0], 0.75, "%s: mornings ready", label)
		assert.InDelta(t, w/seeds, row[1][0], 0.75, "%s: hours woken for nothing", label)
		assert.InDelta(t, 30, used/seeds, 0.75, "mornings someone came, as the header says")
		ready, wasted = append(ready, r), append(wasted, w)
	}
	assert.True(t, slices.IsSortedFunc(ready, func(a, b float64) int { return cmp.Compare(b, a) }),
		"ready mornings, eager to cautious: %v", ready)
	assert.True(t, slices.IsSortedFunc(wasted, func(a, b float64) int { return cmp.Compare(b, a) }),
		"hours woken for nothing, eager to cautious: %v", wasted)
}
