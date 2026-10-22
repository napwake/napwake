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
	// now is the replay's clock: the history it reads ends now.
	now func() time.Time
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
	// managed is a workload Hybernate manages, as forecast NAME takes it,
	// such as "api -n orders": its own forecast shows without history.
	managed string
}

type forecastWorkload struct {
	Namespace string              `json:"namespace"`
	Kind      v1alpha1.TargetKind `json:"kind"`
	Name      string              `json:"name"`
	Managed   bool                `json:"managed"`
	// Operator is what the operator's own forecast of a workload it
	// manages is doing, for the workload NAME names.
	Operator *operatorForecast         `json:"operator,omitempty"`
	Forecast *discovery.ForecastReplay `json:"forecast,omitempty"`
	// NoReplay says why the workload NAME names has no replay of its
	// history, and what would give it one.
	NoReplay string `json:"noReplay,omitempty"`
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
		now:          time.Now,
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

NAME, a workload's name or kind/name, shows that workload's forecast: for
one Hybernate manages, what its own forecast is doing, from the
ManagedWorkload, and for any, the replay of its history day by day.

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
			zone := ""
			if cmd.Flags().Changed("timezone") {
				zone = opts.timezone
			}
			if len(args) == 1 {
				return forecastNamed(ctx, cmd.OutOrStdout(), args[0], forecastRun{window: window, prom: prom,
					mode: mode, zone: zone}, opts)
			}
			if zone == "" {
				if zone = operatorTimeZone(ctx, opts.kube); zone != "" {
					if loc, err = time.LoadLocation(zone); err != nil {
						return fmt.Errorf("the operator's time zone, %s: %w", zone, err)
					}
				}
			}
			result, _, err := replayCluster(ctx, forecastRun{window: window, prom: prom, mode: mode}, loc, opts)
			if err != nil {
				return forecastFailed(ctx, err, opts.timeout)
			}
			return writeOutput(cmd.OutOrStdout(), opts.output, result, func() error {
				return writeForecast(cmd.OutOrStdout(), result, loc)
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

// forecastRun is what a forecast replay reads, and how: the history's
// window and Prometheus, the setting for workloads Hybernate doesn't manage,
// and the time zone --timezone names, if it does.
type forecastRun struct {
	window time.Duration
	prom   prometheusSetup
	mode   forecast.Mode
	zone   string
	// historyOptional replays what history there is, and scans on without
	// any, for a workload named on the command line.
	historyOptional bool
}

// replayCluster scans the cluster with the forecast replayed over each
// workload's history, which needs Prometheus unless run.historyOptional
// says to go on without it. The result lists the workloads replayed; the
// scan has every workload read.
func replayCluster(ctx context.Context, run forecastRun, loc *time.Location, opts forecastOptions) (
	forecastResult, scanResult, error) {
	scan, err := scanCluster(ctx, run.window, run.prom, scanOptions{
		kube:            opts.kube,
		namespaces:      opts.namespaces,
		exclude:         opts.exclude,
		cpuThreshold:    opts.cpuThreshold,
		cpuPrice:        cost.DefaultRates.CPUPerHour,
		memoryPrice:     cost.DefaultRates.MemoryPerHour,
		idleAfter:       opts.idleAfter,
		forecast:        &discovery.ForecastOptions{Confidence: run.mode.Threshold(), Location: loc},
		historyOptional: run.historyOptional,
		now:             opts.now,
	})
	if err != nil {
		return forecastResult{}, scanResult{}, err
	}
	if scan.History == nil && scan.historyUnread == nil {
		return forecastResult{}, scanResult{}, &historyError{errors.New("the forecast is replayed over CPU " +
			"history, and there's none to read")}
	}
	result := forecastResult{Context: scan.Context, Cluster: scan.Cluster, Window: opts.window,
		Timezone: loc.String(), Setting: string(run.mode), Workloads: []forecastWorkload{}, Notes: scan.Notes}
	if h := scan.History; h != nil {
		result.Prometheus, result.HistoryHours, result.HistorySince = h.Prometheus, h.Hours, h.Since
	}
	for _, w := range scan.Workloads {
		if w.Managed && result.managed == "" {
			result.managed = kindName(forecastWorkload{Kind: w.Kind, Name: w.Name}) + " -n " + w.Namespace
		}
		if w.Forecast == nil {
			continue
		}
		result.Workloads = append(result.Workloads, forecastWorkload{Namespace: w.Namespace, Kind: w.Kind,
			Name: w.Name, Managed: w.Managed, Forecast: w.Forecast})
	}
	return result, scan, nil
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

// forecastFailed is scanFailed for a replay, which can't do without history
// as the scan can.
func forecastFailed(ctx context.Context, err error, timeout time.Duration) error {
	var history *historyError
	if errors.Is(ctx.Err(), context.DeadlineExceeded) && errors.As(err, &history) {
		return fmt.Errorf("the replay didn't finish within --timeout %s: Prometheus answered too slowly; pass a "+
			"longer --timeout, or a shorter --window", timeout)
	}
	return scanFailed(ctx, err, timeout)
}

func writeForecast(w io.Writer, result forecastResult, loc *time.Location) error {
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
		if result.managed != "" {
			p.line("  To see what Hybernate's own forecast of a workload it manages is doing, which needs no history, "+
				"name it: kubectl hybernate forecast %s", result.managed)
		}
		p.line("")
	} else if len(result.Workloads) == 0 {
		p.line("  No workload has CPU history to replay the forecast over.")
	} else {
		writeForecastTable(p, result.Workloads, loc)
	}
	writeForecastNotes(p, result.Notes)
	return p.err
}

func writeForecastNotes(p *printer, notes []string) {
	if len(notes) == 0 {
		return
	}
	p.line("Notes:")
	for _, n := range notes {
		p.line("  - %s", oneLine(n))
	}
	p.line("")
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
	p.line("it day by day, and for one Hybernate manages, what its own forecast is doing now.")
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
			return forecastActing
		}
	} else if o.Acting {
		return "acts from day one"
	}
	if o.Waiting == v1alpha1.ForecastNotLearning {
		return "not learning: paused without wake on request"
	}
	if o.StartsActing != nil {
		return "acts from " + o.StartsActing.In(loc).Format("Mon 2 Jan")
	}
	if o.Waiting == string(forecast.WaitingForDay) || o.Waiting == "Observing" {
		return forecastLearning
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
