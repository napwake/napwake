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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/okedeji/hybernate/internal/forecast/sim"
)

func defaults() options {
	return options{scenario: "all", setting: "all", weeks: 2, cpuThreshold: 10, idleAfter: time.Hour,
		timezone: "UTC", autoResume: true, doorman: true, output: "table"}
}

func TestEveryScenarioRuns(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, run(defaults(), &out))
	for _, name := range scenarioNames {
		for _, setting := range []string{"eager", "balanced", "cautious"} {
			assert.Contains(t, out.String(), name+" "+setting)
		}
	}
}

func TestOneScenarioDayByDay(t *testing.T) {
	o := defaults()
	o.scenario, o.setting, o.days = "office", "balanced", true
	var out bytes.Buffer
	require.NoError(t, run(o, &out))
	assert.Contains(t, out.String(), "Mon 16 Mar  09:10      cold")
	assert.Equal(t, 1+1+1+1+14, strings.Count(out.String(), "\n")-1, "the summary, then a line a day")
}

// office hours recorded every five minutes for four weeks, as a CSV and as
// a Prometheus range query's response.
func recordings(t *testing.T) (csvPath, promPath string) {
	t.Helper()
	monday := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	traffic := sim.OfficeHours(time.UTC)
	var csv strings.Builder
	csv.WriteString("time,percent\n")
	var values [][2]any
	for at := monday; at.Before(monday.AddDate(0, 0, 28)); at = at.Add(5 * time.Minute) {
		p := traffic(at).Percent
		fmt.Fprintf(&csv, "%s,%g\n", at.Format(time.RFC3339), p)
		values = append(values, [2]any{at.Unix(), fmt.Sprint(p)})
	}
	prom, err := json.Marshal(map[string]any{"status": "success", "data": map[string]any{
		"resultType": "matrix", "result": []any{map[string]any{"metric": map[string]string{}, "values": values}}}})
	require.NoError(t, err)

	dir := t.TempDir()
	csvPath, promPath = filepath.Join(dir, "cpu.csv"), filepath.Join(dir, "cpu.json")
	require.NoError(t, os.WriteFile(csvPath, []byte(csv.String()), 0o600))
	require.NoError(t, os.WriteFile(promPath, prom, 0o600))
	return csvPath, promPath
}

func TestReplaysRecordedHistory(t *testing.T) {
	csvPath, promPath := recordings(t)
	for _, path := range []string{csvPath, promPath} {
		t.Run(filepath.Ext(path), func(t *testing.T) {
			o := defaults()
			o.history, o.setting, o.output = path, "balanced", outputJSON
			var out bytes.Buffer
			require.NoError(t, run(o, &out))
			var reports []sim.Report
			require.NoError(t, json.Unmarshal(out.Bytes(), &reports))
			require.Len(t, reports, 1)
			s := reports[0].Summarize(reports[0].Start.AddDate(0, 0, 21))
			assert.Equal(t, 5, s.UsedDays)
			assert.Equal(t, 5, s.Ready, "every morning of the fourth week")
		})
	}
}

func TestRejectsBadFlags(t *testing.T) {
	for name, mutate := range map[string]func(*options){
		"setting":    func(o *options) { o.setting = "keen" },
		"confidence": func(o *options) { o.confidence = 30 },
		"scenario":   func(o *options) { o.scenario = "nope" },
		"timezone":   func(o *options) { o.timezone = "Mars/Olympus" },
		"output":     func(o *options) { o.output = "xml" },
		"history":    func(o *options) { o.history = filepath.Join(t.TempDir(), "missing.csv") },
	} {
		t.Run(name, func(t *testing.T) {
			o := defaults()
			mutate(&o)
			require.Error(t, run(o, &bytes.Buffer{}))
		})
	}
}
