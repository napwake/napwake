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
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
	"github.com/okedeji/hybernate/internal/cost"
	"github.com/okedeji/hybernate/internal/discovery"
	"github.com/okedeji/hybernate/internal/forecast"
)

// maxForecastWindow keeps the history a forecast replay reads at
// five-minute steps, which Prometheus returns up to about 34 days of in one
// query and the replay needs to see each hour whole.
const maxForecastWindow = 34 * 24 * time.Hour

type forecastOptions struct {
	kube         *kubeFlags
	namespaces   namespaceFlags
	exclude      []string
	output       string
	window       string
	setting      string
	timezone     string
	cpuThreshold int
	idleAfter    time.Duration
	timeout      time.Duration
	prometheus   prometheusOptions
}

// forecastResult is the forecast replayed over a cluster's history.
type forecastResult struct {
	Context    string `json:"context"`
	Cluster    string `json:"cluster"`
	Prometheus string `json:"prometheus,omitempty"`
	// HistoryHours is how much history Prometheus had, which is less than
	// the window asked for when it keeps less, from HistorySince.
	HistoryHours float64   `json:"historyHours"`
	HistorySince time.Time `json:"historySince,omitzero"`
	Window       string    `json:"window"`
	Timezone     string    `json:"timezone"`
	// Setting is what workloads Hybernate doesn't manage were replayed
	// with; managed ones use their own.
	Setting   string             `json:"setting"`
	Workloads []forecastWorkload `json:"workloads"`
	Notes     []string           `json:"notes,omitempty"`
}

type forecastWorkload struct {
	Namespace string                    `json:"namespace"`
	Kind      v1alpha1.TargetKind       `json:"kind"`
	Name      string                    `json:"name"`
	Managed   bool                      `json:"managed"`
	Forecast  *discovery.ForecastReplay `json:"forecast"`
}

func forecastCmd(kube *kubeFlags) *cobra.Command {
	opts := forecastOptions{
		kube:         kube,
		window:       "28d",
		setting:      string(forecast.Balanced),
		timezone:     "UTC",
		cpuThreshold: defaultCPUThreshold,
		idleAfter:    time.Hour,
		timeout:      5 * time.Minute,
	}
	cmd := &cobra.Command{
		Use:   "forecast [NAME]",
		Short: "Replay Hybernate's forecast over your workloads' own CPU history",
		Long: `Forecast shows what Hybernate's forecast would make of your workloads, from
the CPU history in your Prometheus, before you trust it with them: whether,
opted in today, it would act at once, having learned that history, as the
operator does for a new workload; and how it would have done over the last
half of the history, having learned the first, with autoResume waking each
workload ahead of the first use of the day. It reports the mornings it would
have had each workload ready, those whose first use would have found it
paused and woken it, and the wakes it would have made for nothing.

A workload counts as used when its CPU is above --cpu-threshold, as the
activity clock counts it. It runs the operator's own forecast and decision
rules, and reads Prometheus as scan does: found in the cluster and queried
through the API server, or at --prometheus-url. It only reads.

NAME, a workload's name or kind/name, shows that workload day by day.

Examples:
  # Every workload in a namespace
  kubectl hybernate forecast -n preview-42

  # One workload's mornings, in the operator's time zone
  kubectl hybernate forecast api -n preview-42 --timezone Europe/London

  # As the eager setting would act
  kubectl hybernate forecast -n preview-42 --forecast eager`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkOutput(opts.output); err != nil {
				return err
			}
			window, err := forecastWindow(opts.window)
			if err != nil {
				return err
			}
			mode, ok := forecast.ParseMode(opts.setting)
			if !ok {
				return fmt.Errorf("--forecast %q isn't eager, balanced or cautious", opts.setting)
			}
			loc, err := time.LoadLocation(opts.timezone)
			if err != nil {
				return fmt.Errorf("--timezone: %w", err)
			}
			if opts.cpuThreshold < 1 || opts.cpuThreshold > 100 {
				return errors.New("--cpu-threshold must be a percentage from 1 to 100")
			}
			if err := checkPositive("idle-after", opts.idleAfter); err != nil {
				return err
			}
			if err := checkPositive("timeout", opts.timeout); err != nil {
				return err
			}
			prom, err := prometheusFrom(opts.prometheus)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), opts.timeout)
			defer cancel()
			if !cmd.Flags().Changed("timezone") {
				if zone := operatorTimeZone(ctx, opts.kube); zone != "" {
					if loc, err = time.LoadLocation(zone); err != nil {
						return fmt.Errorf("the operator's time zone, %s: %w", zone, err)
					}
				}
			}
			result, err := replayCluster(ctx, window, prom, mode, loc, opts)
			if err != nil {
				return scanFailed(ctx, err, opts.timeout)
			}
			name := ""
			if len(args) == 1 {
				name = args[0]
				if result.Workloads, err = pickWorkload(result.Workloads, name); err != nil {
					return err
				}
			}
			return writeOutput(cmd.OutOrStdout(), opts.output, result, func() error {
				return writeForecast(cmd.OutOrStdout(), result, name != "", loc)
			})
		},
	}
	addNamespaceFlags(cmd, &opts.namespaces,
		"Namespace to replay; repeat for several (defaults to all you can read)")
	cmd.Flags().StringSliceVar(&opts.exclude, "exclude-namespaces", discovery.SystemNamespaces, "Namespaces to skip")
	addOutputFlag(cmd, &opts.output)
	cmd.Flags().StringVar(&opts.window, "window", opts.window,
		"How much Prometheus history to read, such as 28d or 14d, from 2d to 34d")
	cmd.Flags().StringVar(&opts.setting, "forecast", opts.setting,
		"How soon the forecast acts, eager, balanced or cautious, for workloads Hybernate doesn't manage; "+
			"managed workloads use their own")
	cmd.Flags().StringVar(&opts.timezone, "timezone", opts.timezone,
		"The time zone whose hours and weekdays the forecast learns and times are shown in; by default the "+
			"operator's, as its ManagedWorkloads record it, or UTC")
	cmd.Flags().IntVar(&opts.cpuThreshold, "cpu-threshold", opts.cpuThreshold,
		"CPU use, as a percentage of requests from 1 to 100, above which a workload is in use; managed workloads "+
			"use their own")
	cmd.Flags().DurationVar(&opts.idleAfter, "idle-after", opts.idleAfter,
		"How long without use before a pause, as Hybernate's idleAfter; managed workloads use their own")
	cmd.Flags().DurationVar(&opts.timeout, "timeout", opts.timeout, "How long the replay may take before it gives up")
	addPrometheusFlags(cmd, &opts.prometheus)
	return cmd
}

func forecastWindow(s string) (time.Duration, error) {
	window, err := parseWindow(s)
	if err != nil {
		return 0, err
	}
	if window < 2*24*time.Hour || window > maxForecastWindow {
		return 0, fmt.Errorf("--window %q must be from 2d to 34d", s)
	}
	return window, nil
}

// replayCluster scans the cluster with the forecast replayed over each
// workload's history, which needs Prometheus.
func replayCluster(ctx context.Context, window time.Duration, prom prometheusSetup, mode forecast.Mode,
	loc *time.Location, opts forecastOptions) (forecastResult, error) {
	scan, err := scanCluster(ctx, window, prom, scanOptions{
		kube:         opts.kube,
		namespaces:   opts.namespaces,
		exclude:      opts.exclude,
		cpuThreshold: opts.cpuThreshold,
		cpuPrice:     cost.DefaultRates.CPUPerHour,
		memoryPrice:  cost.DefaultRates.MemoryPerHour,
		idleAfter:    opts.idleAfter,
		forecast:     &discovery.ForecastOptions{Confidence: mode.Threshold(), Location: loc},
	})
	if err != nil {
		return forecastResult{}, err
	}
	if scan.History == nil {
		return forecastResult{}, &historyError{errors.New(strings.Join(append([]string{
			"the forecast is replayed over CPU history, and there's none to read"}, scan.Notes...), "; "))}
	}
	result := forecastResult{Context: scan.Context, Cluster: scan.Cluster, Prometheus: scan.History.Prometheus,
		HistoryHours: scan.History.Hours, HistorySince: scan.History.Since, Window: opts.window, Timezone: loc.String(), Setting: string(mode),
		Workloads: []forecastWorkload{}}
	for _, w := range scan.Workloads {
		if w.Forecast == nil {
			continue
		}
		result.Workloads = append(result.Workloads, forecastWorkload{Namespace: w.Namespace, Kind: w.Kind,
			Name: w.Name, Managed: w.Managed, Forecast: w.Forecast})
	}
	result.Notes = scan.Notes
	return result, nil
}

// operatorTimeZone is the time zone the operator's forecasts count hours
// in, as most of the ManagedWorkloads the user can read record it, or ""
// when none can be read.
func operatorTimeZone(ctx context.Context, kube *kubeFlags) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c, _, err := kube.client(ctx)
	if err != nil || c == nil {
		return ""
	}
	var list v1alpha1.ManagedWorkloadList
	if err := c.List(ctx, &list); err != nil {
		return ""
	}
	counts := map[string]int{}
	for _, w := range list.Items {
		if p := w.Status.Prediction; p != nil && p.TimeZone != "" {
			counts[p.TimeZone]++
		}
	}
	zones := slices.Sorted(maps.Keys(counts))
	if len(zones) == 0 {
		return ""
	}
	return slices.MaxFunc(zones, func(a, b string) int { return cmp.Compare(counts[a], counts[b]) })
}

// pickWorkload is the one workload NAME names, as name or kind/name.
func pickWorkload(workloads []forecastWorkload, name string) ([]forecastWorkload, error) {
	kind, bare, qualified := strings.Cut(name, "/")
	if !qualified {
		bare = name
	}
	var picked []forecastWorkload
	for _, w := range workloads {
		if w.Name == bare && (!qualified || strings.EqualFold(string(w.Kind), kind)) {
			picked = append(picked, w)
		}
	}
	switch len(picked) {
	case 0:
		return nil, fmt.Errorf("no workload %s with CPU history to replay; leave NAME out to see those there are", name)
	case 1:
		return picked, nil
	}
	var names []string
	for _, w := range picked {
		names = append(names, w.Namespace+"/"+strings.ToLower(string(w.Kind))+"/"+w.Name)
	}
	return nil, fmt.Errorf("%s is %s; name one with -n or kind/name", name, strings.Join(names, " and "))
}

func writeForecast(w io.Writer, result forecastResult, detail bool, loc *time.Location) error {
	p := &printer{w: w}
	p.line("%s: the forecast replayed over %s of CPU history from %s, times in %s", result.Cluster,
		spanOf(result.HistoryHours), result.Prometheus, result.Timezone)
	p.line("")
	least := discovery.MinForecastHistory
	if len(result.Workloads) == 0 && result.HistoryHours < least.Hours() {
		p.line("  %s of CPU history is too little to replay the forecast over: it needs at least %s.",
			spanOf(result.HistoryHours), spanOf(least.Hours()))
		if !result.HistorySince.IsZero() {
			p.line("  Run it again after %s.", result.HistorySince.Add(least).In(loc).Format("Mon 2 Jan 15:04 MST"))
		}
		p.line("")
	} else if len(result.Workloads) == 0 {
		p.line("  No workload has CPU history to replay the forecast over.")
	} else if detail {
		writeForecastDetail(p, result.Workloads[0], loc)
	} else {
		writeForecastTable(p, result.Workloads, loc)
	}
	if len(result.Notes) > 0 {
		p.line("Notes:")
		for _, n := range result.Notes {
			p.line("  - %s", oneLine(n))
		}
		p.line("")
	}
	return p.err
}

func writeForecastTable(p *printer, workloads []forecastWorkload, loc *time.Location) {
	tw := tabwriter.NewWriter(p, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAMESPACE\tNAME\tFORECAST\tREADY\tCOLD\tWOKEN FOR NOTHING\tWOKEN")
	for _, w := range workloads {
		f := w.Forecast
		ready, cold, wasted, woken := "-", "-", "-", "-"
		if r := f.Replay; r != nil {
			wasted = wastedWakes(r)
			if r.UsedDays > 0 {
				ready, cold = fmt.Sprintf("%d/%d", r.Ready, r.UsedDays), fmt.Sprint(r.Cold)
			}
			woken = typicalWake(r, loc)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", w.Namespace, displayName(w), forecastNow(w, loc),
			ready, cold, wasted, woken)
	}
	_ = tw.Flush()
	p.line("")
	p.line("FORECAST is when the forecast would act if the workload were opted in today, having learned all of")
	p.line("its history, or a managed workload's own. READY and COLD replay the last half of the history with")
	p.line("autoResume, from a forecast that learned only the first half: the mornings it had the workload up")
	p.line("before its first use, and those the first use found it paused and woke it. Name a workload to see")
	p.line("it day by day.")
	p.line("")
}

func writeForecastDetail(p *printer, w forecastWorkload, loc *time.Location) {
	f := w.Forecast
	p.line("%s/%s: %s, at a confidence of %d%%.", w.Namespace, displayName(w), forecastNow(w, loc), f.Threshold)
	if f.Live != nil && f.Live.Message != "" {
		p.line("  Its forecast now: %s.", oneLine(f.Live.Message))
	}
	r := f.Replay
	if r == nil {
		p.line("")
		return
	}
	p.line("")
	p.line("From %s to %s, with autoResume, from a forecast that learned only the history before:",
		r.From.In(loc).Format("Mon 2 Jan"), r.To.In(loc).Format("Mon 2 Jan"))
	tw := tabwriter.NewWriter(p, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "DAY\tFIRST USE\tFOUND IT\tWOKEN AT\tFORECAST\tWOKEN FOR NOTHING")
	for _, d := range r.Days {
		first, found, woke := "-", "-", "-"
		if d.FirstUse != nil {
			first, found = d.FirstUse.In(loc).Format("15:04"), d.Found
		}
		if d.WokeAt != nil {
			woke = d.WokeAt.In(loc).Format("15:04")
		}
		acting := "learning"
		if d.Acting {
			acting = "acting"
		}
		wasted := "-"
		if d.WastedWakes > 0 {
			wasted = fmt.Sprint(d.WastedWakes)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", d.Date.Format("Mon 2 Jan"), first, found, woke, acting, wasted)
	}
	_ = tw.Flush()
	p.line("")
	if r.UsedDays > 0 {
		p.line("Ready on %d of %d mornings%s; %s.", r.Ready, r.UsedDays, leadClause(r), wastedSentence(r))
	} else {
		p.line("Nobody used it; %s.", wastedSentence(r))
	}
	p.line("")
}

// spanOf is a number of hours of history in days, or in hours under two
// days.
func spanOf(hours float64) string {
	if hours < 47.5 {
		return plural(int(hours+0.5), "hour", "hours")
	}
	return plural(int(hours/24+0.5), "day", "days")
}

func displayName(w forecastWorkload) string {
	name := w.Name
	if w.Kind == v1alpha1.TargetKindStatefulSet {
		name = "statefulset/" + name
	}
	if w.Managed {
		name += " (managed)"
	}
	return name
}

// forecastNow is when the workload's forecast acts: a managed workload's
// own, or what it would do opted in today.
func forecastNow(w forecastWorkload, loc *time.Location) string {
	o := w.Forecast.IfOptedIn
	if l := w.Forecast.Live; l != nil {
		o = *l
		if o.Acting {
			return "acting"
		}
	} else if o.Acting {
		return "acts from day one"
	}
	if o.StartsActing != nil {
		return "acts from " + o.StartsActing.In(loc).Format("Mon 2 Jan")
	}
	if o.Waiting == string(forecast.WaitingForDay) || o.Waiting == "Observing" {
		return "learning"
	}
	return "not within 4 weeks"
}

func wastedWakes(r *discovery.ForecastRun) string {
	if r.WastedWakes == 0 {
		return "0"
	}
	return fmt.Sprintf("%d (%.1fh)", r.WastedWakes, r.WastedHours)
}

func wastedSentence(r *discovery.ForecastRun) string {
	switch r.WastedWakes {
	case 0:
		return "it never woke it for nothing"
	case 1:
		return fmt.Sprintf("it woke it for nothing once, keeping it up %.1f hours", r.WastedHours)
	default:
		return fmt.Sprintf("it woke it for nothing %d times, keeping it up %.1f hours in all", r.WastedWakes,
			r.WastedHours)
	}
}

func leadClause(r *discovery.ForecastRun) string {
	if r.Ready == 0 {
		return ""
	}
	return fmt.Sprintf(", woken %s ahead", roundedDuration(r.MedianLead))
}

// typicalWake is when autoResume most often woke the workload, and how far
// ahead of its first use.
func typicalWake(r *discovery.ForecastRun, loc *time.Location) string {
	if r.UsedDays > 0 && r.Awake == r.UsedDays {
		return "never paused"
	}
	counts := map[string]int{}
	best := ""
	for _, d := range r.Days {
		if d.WokeAt == nil {
			continue
		}
		at := d.WokeAt.In(loc).Format("15:04")
		counts[at]++
		if counts[at] > counts[best] || counts[at] == counts[best] && at < best {
			best = at
		}
	}
	if best == "" {
		return "-"
	}
	return fmt.Sprintf("%s, %s ahead", best, roundedDuration(r.MedianLead))
}
