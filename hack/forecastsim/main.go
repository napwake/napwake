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

// Command forecastsim runs the forecast engine and the operator's decision
// rules through simulated traffic, or through a workload's recorded CPU,
// and reports what Napwake would have done: how long the forecast took
// to act, the mornings autoResume had the workload ready, the wakes it
// made for nothing, and with -pause-early, the pauses it brought forward
// and the uses that then found the workload paused.
//
//	go run ./hack/forecastsim                     # every scenario, every setting
//	go run ./hack/forecastsim -scenario holiday -setting eager -days
//	go run ./hack/forecastsim -scenario coming-back -pause-early
//	go run ./hack/forecastsim -history cpu.json -timezone Europe/London -days
//
// -history reads a Prometheus range query's JSON response, or CSV of
// time,percent rows (RFC 3339 or Unix seconds), of one workload's CPU as a
// percentage of what its own containers request, a step of at most seven
// minutes apart, such as:
//
//	curl -sG "$PROM/api/v1/query_range" --data-urlencode 'query=
//	  sum(rate(container_cpu_usage_seconds_total{namespace="shop",pod=~"api-.*",container="api"}[5m]))
//	  / sum(kube_pod_container_resource_requests{namespace="shop",pod=~"api-.*",container="api",resource="cpu"})
//	  * 100' --data-urlencode start=-28d ... --data-urlencode step=300 > cpu.json
package main

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/napwake/napwake/internal/forecast"
	"github.com/napwake/napwake/internal/forecast/sim"
)

const (
	outputTable = "table"
	outputJSON  = "json"
)

type options struct {
	scenario     string
	setting      string
	confidence   int
	weeks        int
	history      string
	cpuThreshold float64
	idleAfter    time.Duration
	timezone     string
	autoResume   bool
	pauseEarly   bool
	doorman      bool
	query        bool
	ask          bool
	days         bool
	output       string
}

func main() {
	var o options
	flag.StringVar(&o.scenario, "scenario", "all", "Scenario to run: "+strings.Join(scenarioNames, ", ")+", or all")
	flag.StringVar(&o.setting, "setting", "all", "Forecast setting: eager, balanced, cautious, or all")
	flag.IntVar(&o.confidence, "confidence", 0, "Confidence threshold to act at, from 50 to 100, in place of -setting")
	flag.IntVar(&o.weeks, "weeks", 8, "Weeks to simulate a scenario for")
	flag.StringVar(&o.history, "history", "", "Replay this recorded CPU history instead of a scenario")
	flag.Float64Var(&o.cpuThreshold, "cpu-threshold", 10, "CPU threshold, as a percentage of requests")
	flag.DurationVar(&o.idleAfter, "idle-after", time.Hour, "How long without activity before a pause")
	flag.StringVar(&o.timezone, "timezone", "UTC", "The operator's --timezone")
	flag.BoolVar(&o.autoResume, "auto-resume", true, "Wake ahead of forecast demand")
	flag.BoolVar(&o.pauseEarly, "pause-early", false,
		"Pause before idle-after runs out when the forecast expects quiet hours")
	flag.BoolVar(&o.doorman, "wake-on-request", true, "Route the workload's Services to the doorman while paused")
	flag.BoolVar(&o.query, "wake-on-query", false,
		"Give the workload a Prometheus activity query that sees its use, as a queue's length does a worker's")
	flag.BoolVar(&o.ask, "wake-on-ask", false,
		"Have only people wake the workload while it's paused, as wake: ask does, in place of -wake-on-request")
	flag.BoolVar(&o.days, "days", false, "List every day: when people first used the workload, and how it found it")
	flag.StringVar(&o.output, "o", outputTable, "Output: table or json")
	flag.Parse()
	if err := run(o, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(o options, out io.Writer) error {
	if o.output != outputTable && o.output != outputJSON {
		return fmt.Errorf("-o %q isn't table or json", o.output)
	}
	loc, err := time.LoadLocation(o.timezone)
	if err != nil {
		return fmt.Errorf("-timezone: %w", err)
	}
	thresholds, err := thresholdsFor(o)
	if err != nil {
		return err
	}
	var reports []sim.Report
	for _, th := range thresholds {
		set := sim.Settings{Confidence: th.confidence, CPUThreshold: o.cpuThreshold, IdleAfter: o.idleAfter,
			AutoResume: o.autoResume, PauseEarly: o.pauseEarly, WakeOnRequest: o.doorman && !o.ask, WakeOnQuery: o.query,
			WakeOnAsk: o.ask, Location: loc}
		if o.history != "" {
			r, err := replay(o.history, th.name, set)
			if err != nil {
				return err
			}
			reports = append(reports, r)
			continue
		}
		scs, err := scenarios(o.scenario, loc, time.Duration(o.weeks)*7*24*time.Hour)
		if err != nil {
			return err
		}
		for _, sc := range scs {
			sc.Settings = set
			sc.Name += " " + th.name
			r, err := sim.Run(sc)
			if err != nil {
				return fmt.Errorf("simulating %s: %w", sc.Name, err)
			}
			reports = append(reports, r)
		}
	}
	if o.output == outputJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(reports)
	}
	return writeTable(out, reports, o.days)
}

type threshold struct {
	name       string
	confidence int
}

func thresholdsFor(o options) ([]threshold, error) {
	if o.confidence != 0 {
		if o.confidence < 50 || o.confidence > 100 {
			return nil, errors.New("-confidence must be from 50 to 100")
		}
		return []threshold{{name: strconv.Itoa(o.confidence), confidence: o.confidence}}, nil
	}
	if o.setting == "all" {
		var all []threshold
		for _, m := range forecast.Modes {
			all = append(all, threshold{name: string(m), confidence: m.Threshold()})
		}
		return all, nil
	}
	m, ok := forecast.ParseMode(o.setting)
	if !ok {
		return nil, fmt.Errorf("-setting %q isn't eager, balanced, cautious or all", o.setting)
	}
	return []threshold{{name: string(m), confidence: m.Threshold()}}, nil
}

// printer writes to w until a write fails, and keeps that error.
type printer struct {
	w   io.Writer
	err error
}

func (p *printer) printf(format string, args ...any) {
	if p.err == nil {
		_, p.err = fmt.Fprintf(p.w, format, args...)
	}
}

func writeTable(out io.Writer, reports []sim.Report, days bool) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	p := &printer{w: tw}
	p.printf("SCENARIO\tACTS AFTER\tFULLY ACTIVE\tREADY\tAWAKE\tCOLD\tMISSED\tWASTED WAKES\tON IDLE DAYS\t" +
		"HOURS WASTED\tVETOES\tPAUSED EARLY\tHOURS SAVED\tFOUND PAUSED\n")
	for _, r := range reports {
		s := r.Summarize(r.Start)
		p.printf("%s\t%s\t%s\t%d/%d\t%d\t%d\t%d\t%d\t%d\t%.1f\t%d\t%d\t%.1f\t%d\n", r.Name,
			reached(r, forecast.DailyActive), reached(r, forecast.FullyActive), s.Ready, s.UsedDays, s.Awake, s.Cold,
			s.Missed, s.WastedWakes, s.IdleDayWakes, s.WastedHours, s.Vetoes, s.EarlyPauses, s.SavedHours, s.ColdEarly)
	}
	if !days {
		return errors.Join(p.err, tw.Flush())
	}
	for _, r := range reports {
		p.printf("\n%s\n", r.Name)
		p.printf("DAY\tFIRST USE\tFOUND IT\tLEAD\tFORECAST\tWASTED WAKES\n")
		for _, d := range r.Days {
			first, lead := "-", "-"
			if !d.Arrival.IsZero() {
				first = d.Arrival.In(d.Date.Location()).Format("15:04")
			}
			if d.Outcome == sim.Ready {
				lead = d.Lead.String()
			}
			acting := "learning"
			if d.Acting {
				acting = "acting"
			}
			p.printf("%s\t%s\t%s\t%s\t%s\t%d\n", d.Date.Format("Mon 02 Jan"), first, orDash(string(d.Outcome)),
				lead, acting, d.WastedWakes)
		}
	}
	return errors.Join(p.err, tw.Flush())
}

func reached(r sim.Report, phase forecast.Phase) string {
	d, ok := r.Reached(phase)
	if !ok {
		return "never"
	}
	return fmt.Sprintf("%.1f days", d.Hours()/24)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// replay replays a recorded history file.
func replay(path, name string, set sim.Settings) (sim.Report, error) {
	f, err := os.Open(path)
	if err != nil {
		return sim.Report{}, fmt.Errorf("opening -history: %w", err)
	}
	defer func() { _ = f.Close() }() // read-only; nothing to do if closing fails
	samples, err := readSamples(bufio.NewReader(f))
	if err != nil {
		return sim.Report{}, fmt.Errorf("reading %s: %w", path, err)
	}
	rec, err := sim.NewRecorded(samples)
	if err != nil {
		return sim.Report{}, fmt.Errorf("reading %s: %w", path, err)
	}
	return rec.Replay(path+" "+name, set)
}

// readSamples reads a Prometheus range query's JSON response, or CSV.
func readSamples(r *bufio.Reader) ([]sim.Sample, error) {
	first, err := r.Peek(1)
	if err != nil {
		return nil, fmt.Errorf("empty history: %w", err)
	}
	if first[0] == '{' {
		return readPrometheus(r)
	}
	return readCSV(r)
}

func readPrometheus(r io.Reader) ([]sim.Sample, error) {
	var resp struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Values [][2]json.RawMessage `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(r).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decoding a Prometheus response: %w", err)
	}
	if resp.Status != "success" || len(resp.Data.Result) != 1 {
		return nil, fmt.Errorf("want a successful range query with one series, got status %q with %d",
			resp.Status, len(resp.Data.Result))
	}
	var samples []sim.Sample
	for _, v := range resp.Data.Result[0].Values {
		var at float64
		var value string
		if err := json.Unmarshal(v[0], &at); err != nil {
			return nil, fmt.Errorf("reading a sample's time: %w", err)
		}
		if err := json.Unmarshal(v[1], &value); err != nil {
			return nil, fmt.Errorf("reading a sample's value: %w", err)
		}
		percent, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, fmt.Errorf("reading a sample's value: %w", err)
		}
		samples = append(samples, sim.Sample{At: time.Unix(int64(at), 0).UTC(), Percent: percent})
	}
	return samples, nil
}

func readCSV(r io.Reader) ([]sim.Sample, error) {
	rows, err := csv.NewReader(r).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("reading CSV: %w", err)
	}
	var samples []sim.Sample
	for i, row := range rows {
		if len(row) != 2 {
			return nil, fmt.Errorf("row %d: want time,percent", i+1)
		}
		at, err := parseTime(strings.TrimSpace(row[0]))
		if err != nil {
			if i == 0 {
				continue
			}
			return nil, fmt.Errorf("row %d: %w", i+1, err)
		}
		percent, err := strconv.ParseFloat(strings.TrimSpace(row[1]), 64)
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", i+1, err)
		}
		samples = append(samples, sim.Sample{At: at, Percent: percent})
	}
	return samples, nil
}

func parseTime(s string) (time.Time, error) {
	if unix, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(unix, 0).UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q isn't RFC 3339 or Unix seconds", s)
	}
	return t, nil
}

// scenarios are the named scenarios, in the operator's zone loc, each
// starting on the Monday of a week without a clock change in it, except
// those about the clocks changing.
func scenarios(name string, loc *time.Location, length time.Duration) ([]sim.Scenario, error) {
	monday := time.Date(2026, 3, 16, 0, 0, 0, 0, loc)
	startup := sim.Startup{Percent: 80, For: 3 * time.Minute}
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		return nil, fmt.Errorf("loading America/New_York: %w", err)
	}
	var restarts []sim.Restart
	for d := range int(length / (24 * time.Hour)) {
		restarts = append(restarts, sim.Restart{At: monday.AddDate(0, 0, d).Add(time.Duration(d*97%1440) * time.Minute),
			Down: 2 * time.Minute})
	}
	office := sim.OfficeHours(loc)
	saturday := monday.AddDate(0, 0, 40)
	atWork := sim.Working(loc, func(day time.Time) bool { return day.Equal(saturday) }, 9*time.Hour+10*time.Minute,
		17*time.Hour+30*time.Minute, 40)
	all := []sim.Scenario{
		{Name: "office", Traffic: office},
		{Name: "every-day", Traffic: sim.Working(loc, sim.EveryDay, 9*time.Hour+10*time.Minute,
			17*time.Hour+30*time.Minute, 40)},
		{Name: "always-busy", Traffic: sim.Always(true, 40)},
		{Name: "always-idle", Traffic: sim.Always(false, 0)},
		{Name: "noisy", Traffic: sim.Noisy(loc, 1, 20*time.Minute, 10)},
		{Name: "irregular", Traffic: sim.Noisy(loc, 1, 60*time.Minute, 4)},
		{Name: "holiday", Traffic: sim.Except(office, loc, monday.AddDate(0, 0, 44))},
		{Name: "new-hours", Traffic: sim.Switch(monday.AddDate(0, 0, 42), office,
			sim.Working(loc, sim.Weekdays, 13*time.Hour, 21*time.Hour, 40))},
		{Name: "retired", Traffic: sim.Switch(monday.AddDate(0, 0, 42), office, sim.Always(false, 0))},
		{Name: "weekend-work", Traffic: func(t time.Time) sim.Use {
			if u := atWork(t); u.Active {
				return u
			}
			return office(t)
		}},
		{Name: "light-use", Traffic: sim.SeenElsewhere(sim.Working(loc, sim.Weekdays, 9*time.Hour+10*time.Minute,
			17*time.Hour+30*time.Minute, 3))},
		{Name: "coming-back", Traffic: sim.ComingBack(loc, 1, 3)},
		{Name: "evening-work", Traffic: sim.EveningWork(loc, 1, 3)},
		{Name: "restarts", Traffic: office, Restarts: restarts},
		{Name: "new-york-users", Traffic: sim.OfficeHours(newYork), Start: time.Date(2026, 2, 2, 0, 0, 0, 0, newYork)},
	}
	var picked []sim.Scenario
	for _, sc := range all {
		if name != "all" && sc.Name != name {
			continue
		}
		if sc.Start.IsZero() {
			sc.Start = monday
		}
		sc.Duration, sc.Startup, sc.HandWake = length, startup, 5*time.Minute
		picked = append(picked, sc)
	}
	if len(picked) == 0 {
		return nil, fmt.Errorf("no scenario %q; there are %s", name, strings.Join(scenarioNames, ", "))
	}
	return picked, nil
}

// scenarioNames are the scenarios' names, for -scenario's help. The
// new-york-users scenario's people keep New York's hours, through its
// clock change, so it shows what -timezone does. In coming-back and
// evening-work, people use the workload in hours the forecast learns as
// quiet, which is what -pause-early costs.
var scenarioNames = []string{"office", "every-day", "always-busy", "always-idle", "noisy", "irregular", "holiday",
	"new-hours", "retired", "weekend-work", "light-use", "coming-back", "evening-work", "restarts",
	"new-york-users"}
