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
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/napwake/napwake/internal/discovery"
	"github.com/napwake/napwake/internal/doorman"
	"github.com/napwake/napwake/internal/nodescale"
)

//go:embed report.html
var reportHTML string

var reportTemplate = template.Must(template.New("report").Funcs(template.FuncMap{
	"hours": hoursTotal,
	"cents": cents,
}).Parse(reportHTML))

// reportPage is what the HTML report shows, worded from the same report
// data and helpers as the terminal table, so the two never disagree.
type reportPage struct {
	Cluster   string
	ScannedAt string
	// Empty says why there are no workloads, when there are none.
	Empty  string
	Basis  string
	Prices string
	// NodeAutoscaler names what would turn freed capacity into savings.
	NodeAutoscaler string
	Headline       headline
	Facts          []fact
	History        bool
	Sleep          bool
	Slept          bool
	Freeing        bool
	CouldFree      bool
	Namespaces     []namespaceRow
	Workloads      []workloadRow
	Dependencies   []dependencyRow
	// DependencyStatus says each dependency shows its status; without it,
	// DependenciesLead says what they'd all say.
	DependencyStatus bool
	DependenciesLead string
	Notes            []string
	Method           []string
	NextSteps        []template.HTML
	Threshold        int
	IdleAfter        string
	Over             string
	Tips             tips
	Terms            []term
	Mark             string
	// Unjudged is how many workloads the notes list instead of Workloads.
	Unjudged int
}

type headline struct {
	Figures []figure
}

// tips explain the money and sleep columns, on their headers and in the
// method section, where printing keeps them.
type tips struct {
	CouldSleep, Slept, Freed, CouldFree string
}

type term struct {
	Name, Meaning string
}

func tipsFor(history bool, over string) tips {
	couldSleep := "Hours Napwake would have had it paused, measured since dry-run started for a dry-run workload"
	if history {
		couldSleep += ", or from history over " + over + " for an unmanaged one"
	}
	return tips{
		CouldSleep: couldSleep + ". Under it, how many times activity would have woken it, each a wait for someone.",
		Slept: "For live workloads: hours Napwake has had it paused this month, and how many times activity " +
			"woke it.",
		Freed: "For live workloads: what Napwake has freed pausing it this month, priced at the rates it uses " +
			"for the workload. Under it, how much of that came off the bill as nodes scaled down.",
		CouldFree: "What pausing would free a month: measured by Napwake for a dry-run workload, estimated from " +
			"history for an unmanaged one.",
	}
}

// termsFor lists the column definitions for the columns the page shows.
func termsFor(page reportPage) []term {
	var terms []term
	if page.Slept {
		terms = append(terms, term{"Slept", page.Tips.Slept})
	}
	if page.Freeing {
		terms = append(terms, term{"Freed", page.Tips.Freed})
	}
	if page.Sleep {
		terms = append(terms, term{"Could sleep", page.Tips.CouldSleep})
	}
	if page.CouldFree {
		terms = append(terms, term{"Could free", page.Tips.CouldFree})
	}
	return terms
}

type figure struct {
	Value, Label, Detail string
}

type fact struct {
	Value, Label string
}

type namespaceRow struct {
	Namespace                          string
	Workloads, Idle                    int
	Cost, Freed, Saved, CouldFree      string
	CostSort, FreedSort, CouldFreeSort float64
	savedSort                          float64
}

type workloadRow struct {
	Namespace, Workload                      string
	State, StateClass                        string
	Cost                                     string
	CostSort                                 float64
	CouldSleep, Wakes, Since, CouldFree      string
	Freed, Saved                             string
	Slept, SleptWakes, SleptSince            string
	SleptSort                                float64
	CouldSleepSort, FreedSort, CouldFreeSort float64
	Because                                  string
}

type dependencyRow struct {
	Workload, Target, Found, Status string
}

// browse opens a file in the user's browser, and interactive says whether
// output goes to a terminal with a desktop to open one on. They're
// variables so tests can stand in for a terminal and a browser.
var (
	browse      = openInBrowser
	interactive = func(w io.Writer) bool { return isTerminal(w) && canBrowse() }
)

// writeReport writes the HTML report where asked, or, when the table is
// shown in a terminal, to a temporary file it opens in the browser. Output
// for scripts, or a session with nowhere to show a browser, gets no report
// unless one is asked for.
func writeReport(stdout, stderr io.Writer, result scanResult, opts scanOptions) error {
	open := opts.open && opts.output == outputTable && interactive(stdout)
	if opts.html == "" && !open {
		return nil
	}
	path, err := writeHTMLFile(opts.html, result, opts.idleAfter)
	if err != nil {
		return err
	}
	showPage(stderr, "Report", path, open)
	return nil
}

// showPage says on stderr where a page was written, as label, and opens it
// in the browser first if asked to.
func showPage(stderr io.Writer, label, path string, open bool) {
	if !open {
		_, _ = fmt.Fprintf(stderr, "%s: %s\n", label, path)
		return
	}
	if err := browse(path); err != nil {
		_, _ = fmt.Fprintf(stderr, "%s: %s (couldn't open a browser: %v)\n", label, path, err)
		return
	}
	_, _ = fmt.Fprintf(stderr, "%s: %s (opened in your browser; --open=false to skip)\n", label, path)
}

// isTerminal reports whether stream, such as stdout or stdin, is a terminal.
func isTerminal(stream any) bool {
	f, ok := stream.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// canBrowse reports whether there's a desktop to open a browser on, which
// an SSH session or a container on Linux doesn't have.
func canBrowse() bool {
	return canBrowseOn(runtime.GOOS, os.Getenv)
}

// canBrowseOn is canBrowse on an OS with an environment. Over SSH to a Mac
// or Windows machine, its desktop is there but the user isn't, so a browser
// would open where nobody sees it. On Linux, a display set over SSH is X
// forwarding, which shows the browser on the user's own screen.
func canBrowseOn(goos string, getenv func(string) string) bool {
	if goos == "linux" {
		return getenv("DISPLAY") != "" || getenv("WAYLAND_DISPLAY") != ""
	}
	if getenv("SSH_CONNECTION") != "" || getenv("SSH_TTY") != "" {
		return false
	}
	return goos == "darwin" || goos == goosWindows
}

const goosWindows = "windows"

// openerWait is how long the opener has to say it failed. Most hand the
// file to the browser and exit at once, but xdg-open without a desktop
// environment runs the browser itself, in the foreground, until it's closed.
const openerWait = 3 * time.Second

func openInBrowser(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case goosWindows:
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return startOpener(cmd, openerWait)
}

// startOpener starts the opener, and returns its error if it exits within
// wait. One still running then is the browser itself, which is left
// running: its exit is waited for in the background, and the plugin exits
// without it.
func startOpener(cmd *exec.Cmd, wait time.Duration) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case err := <-exited:
		return err
	case <-timer.C:
		return nil
	}
}

// writeHTMLFile writes the report to path, or, without one, to a new
// temporary file. It returns where it wrote.
func writeHTMLFile(path string, result scanResult, idleAfter time.Duration) (string, error) {
	return writePage(path, "napwake-scan-*.html", func(w io.Writer) error { return writeHTML(w, result, idleAfter) })
}

// writePage writes a page to path, or, without one, to a new temporary file
// named after pattern, as os.CreateTemp takes it. Either is readable only by
// the user, since a page names the cluster's workloads. It returns where it
// wrote.
func writePage(path, pattern string, write func(io.Writer) error) (string, error) {
	var f *os.File
	var err error
	if path == "" {
		f, err = os.CreateTemp("", pattern)
	} else {
		f, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		// The mode applies only to a file it creates: one written over keeps
		// its own, which may let others read it.
		if err == nil {
			if err = f.Chmod(0o600); err != nil {
				_ = f.Close() // the chmod error is the one to report
			}
		}
	}
	if err != nil {
		return "", fmt.Errorf("creating the page: %w", err)
	}
	if err := write(f); err != nil {
		_ = f.Close() // the write error is the one to report
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("writing %s: %w", f.Name(), err)
	}
	return f.Name(), nil
}

func writeHTML(w io.Writer, result scanResult, idleAfter time.Duration) error {
	if err := reportTemplate.Execute(w, buildReport(result, idleAfter)); err != nil {
		return fmt.Errorf("writing the report: %w", err)
	}
	return nil
}

func buildReport(result scanResult, idleAfter time.Duration) reportPage {
	history := result.Mode == discovery.ModeHistory
	page := reportPage{
		Cluster:        result.Cluster,
		ScannedAt:      result.ScannedAt.Format("2 January 2006, 15:04 MST"),
		Basis:          basisSentence(result),
		Prices:         result.costsSentence(),
		NodeAutoscaler: reportAutoscaler(result.NodeAutoscaler),
		History:        history,
		Threshold:      result.Settings.CPUThreshold,
		IdleAfter:      roundedDuration(idleAfter),
		Over:           replayedOver(result.History),
		Mark:           doorman.Mark,
	}
	if result.Totals.Workloads == 0 {
		page.Empty = emptySentence(result)
	}
	page.Headline, page.Facts = headlineFor(result)
	page.Freeing, page.CouldFree = moneyColumns(result.Workloads)
	page.Sleep = sleepColumns(history, result.Workloads)
	page.Slept = sleptColumn(result.Workloads)
	page.Tips = tipsFor(history, page.Over)
	page.Terms = termsFor(page)

	byNamespace := map[string]*namespaceRow{}
	for _, wl := range result.Workloads {
		if wl.State != discovery.StateUnknown {
			page.Workloads = append(page.Workloads, workloadRowFor(wl))
		}
		ns := byNamespace[wl.Namespace]
		if ns == nil {
			ns = &namespaceRow{Namespace: wl.Namespace}
			byNamespace[wl.Namespace] = ns
		}
		ns.Workloads++
		if wl.State == discovery.StateIdle {
			ns.Idle++
		}
		if wl.State != discovery.StatePaused {
			ns.CostSort += wl.MonthlyCost
		}
		ns.FreedSort += wl.FreedThisMonth
		ns.savedSort += wl.SavedThisMonth
		ns.CouldFreeSort += discovery.CouldFree(wl)
	}
	for _, ns := range byNamespace {
		ns.Cost, ns.Freed, ns.Saved = dollars(ns.CostSort), dollars(ns.FreedSort), dollars(ns.savedSort)
		ns.CouldFree = dollars(ns.CouldFreeSort)
		page.Namespaces = append(page.Namespaces, *ns)
	}
	slices.SortFunc(page.Namespaces, func(a, b namespaceRow) int {
		if c := cmp.Compare(b.CouldFreeSort, a.CouldFreeSort); c != 0 {
			return c
		}
		if c := cmp.Compare(b.CostSort, a.CostSort); c != 0 {
			return c
		}
		return strings.Compare(a.Namespace, b.Namespace)
	})

	lines := dependencyLines(result.Workloads, result.Platform)
	for _, l := range lines {
		page.Dependencies = append(page.Dependencies, dependencyRow{Workload: workloadRef(l.wl),
			Target: dependencyRef(l.wl.Namespace, l.d), Found: foundVia(l.d), Status: dependencyStatus(l)})
	}
	page.DependencyStatus = statusColumn(lines)
	if !page.DependencyStatus {
		page.DependenciesLead = dependenciesLead + "."
	}

	_, unjudged := splitJudged(result.Workloads)
	page.Unjudged = len(unjudged)
	page.Notes = append(unjudgedNotes(unjudged), result.Notes...)
	if n := result.Totals.ScaledToZero; n > 0 {
		page.Notes = append([]string{plural(n, "workload is", "workloads are") + " scaled to zero by hand; Napwake " +
			"can pause them while idle and wake them on the next request instead"}, page.Notes...)
	}
	page.Method = methodFor(result, history, len(page.Dependencies) > 0, idleAfter)
	measure, enable := nextStepCommands(nextStepTargets(result.Workloads))
	for _, step := range measure {
		page.NextSteps = append(page.NextSteps, shellHTML(step))
	}
	if enable != "" {
		page.NextSteps = append(page.NextSteps, shellHTML(enable))
	}
	return page
}

func reportAutoscaler(a nodescale.Autoscaler) string {
	line := nodeAutoscalerLine(a)
	if s := freedBecomesSaved(a); s != "" {
		line += ". " + s
	}
	return line
}

func workloadRef(wl Workload) string {
	return fmt.Sprintf("%s/%s/%s", wl.Namespace, strings.ToLower(string(wl.Kind)), wl.Name)
}

func basisSentence(result scanResult) string {
	if result.Mode != discovery.ModeHistory || result.History == nil {
		return "A snapshot of CPU at the time of the scan, without history."
	}
	if over := replayedOver(result.History); over != "" {
		return fmt.Sprintf("Based on %s of Prometheus history, from %s, and CPU at the time of the scan.", over,
			result.History.Prometheus)
	}
	return fmt.Sprintf("A snapshot of CPU at the time of the scan: %s has no CPU history of the namespaces scanned.",
		result.History.Prometheus)
}

// headlineFor leads with two figures. Until Napwake has paused anything,
// they're the opportunity, what pausing could free a month, and what the
// workloads it comes from cost; a zero that's down to too little history
// says so. Once it has, they're what it has freed this month, with how much
// of it was saved, and the opportunity, while there's one. Four facts
// follow: the scale, the waste right now, how many workloads there are to
// act on, and how far Napwake's rollout has got.
func headlineFor(result scanResult) (headline, []fact) {
	t := result.Totals
	live := t.Live > 0 || t.FreedThisMonth > 0
	var h headline
	if live {
		h.Figures = append(h.Figures, figure{Value: dollars(t.FreedThisMonth), Label: "freed by Napwake this month",
			Detail: fmt.Sprintf("so far, pausing %s; %s of it saved as nodes scaled down", freedBy(t),
				dollars(t.SavedThisMonth))})
	}
	if f, ok := couldFree(result, live); ok {
		h.Figures = append(h.Figures, f)
	}

	idle := fact{Value: strconv.Itoa(t.Idle), Label: "idle right now"}
	if t.Idle > 0 {
		idle.Label = fmt.Sprintf("idle right now, reserving %s and %s of memory",
			vcpu(t.IdleCPUMillis), gib(t.IdleMemoryBytes))
	}
	cost := fact{Value: dollars(t.MonthlyCost), Label: "a month, what these workloads cost while running"}
	facts := []fact{cost, idle}
	if !live {
		h.Figures = append(h.Figures, figure{Value: cost.Value, Label: cost.Label})
		workloads := "workloads"
		if t.Workloads == 1 {
			workloads = "workload"
		}
		facts[0] = fact{Value: strconv.Itoa(t.Workloads),
			Label: workloads + " in " + countOf(namespacesOf(result.Workloads), "namespace")}
	}
	over, notCounted := replayedOver(result.History), notReplayed(result.Workloads)
	if r := t.Replayed; r.Workloads > 0 {
		replayed := fact{Value: fmt.Sprintf("%d of %d", r.Sleepers, r.Workloads),
			Label: fmt.Sprintf("unmanaged workloads would have slept over %s", over)}
		if notCounted != "" {
			replayed.Label += "; not counted: " + notCounted
		}
		facts = append(facts, replayed)
	} else {
		facts = append(facts, fact{Value: cents(t.IdleHourlyCost), Label: "an hour, what idle workloads cost while running"})
	}
	managed := fact{Value: fmt.Sprintf("%d of %d", t.Live+t.LiveProtected+t.DryRun, t.Workloads),
		Label: fmt.Sprintf("managed by Napwake: %d live, %d in dry-run", t.Live, t.DryRun)}
	if t.LiveProtected > 0 {
		managed.Label += fmt.Sprintf(", %d live but protected, so never paused", t.LiveProtected)
	}
	facts = append(facts, managed)
	return h, facts
}

// freedBy says which workloads what Napwake freed this month came from:
// those live now, or with none live, those put back in dry-run, and those
// since protected.
func freedBy(t discovery.Totals) string {
	freed, _ := unprotectedFreed(t)
	var by []string
	switch {
	case t.Live > 0:
		by = append(by, countOf(t.Live, "live workload"))
	case anyMoney(freed) && t.DryRun > 0:
		by = append(by, "workloads since put back in dry-run")
	case anyMoney(freed):
		by = append(by, "workloads")
	}
	if anyMoney(t.ProtectedFreedThisMonth) {
		by = append(by, "workloads since protected")
	}
	if len(by) == 0 {
		return "workloads"
	}
	return strings.Join(by, " and ")
}

// couldFree is the headline's figure for what pausing could free a month:
// what dry-run measured and history shows, or, without either, what's idle
// right now costs, as the most pausing it could free. Before anything is
// live it's always shown, a zero with why; after, only when there's
// something to free.
func couldFree(result scanResult, live bool) (figure, bool) {
	t, r := result.Totals, result.Totals.Replayed
	short := tooLittleHistory(result.History)
	var tooEarly string
	if short {
		tooEarly = "too little history yet: Prometheus has only " + result.History.Span() + " of it; run the scan " +
			"again after a few days"
	}
	switch {
	case r.Sleepers > 0 || t.DryRun > 0:
		could := r.MonthlyFreed + t.Measured.MonthlyFreed
		f := figure{Value: dollars(could), Label: "could be freed a month"}
		// The share is of what the workloads cost on the estimate's own
		// basis. A workload that ran more pods in its history than it runs
		// now would otherwise be shown freeing more than it costs.
		if t.FreeableBasis > 0 {
			of := "what these workloads cost"
			if r.Workloads > 0 {
				of += " over the same time"
			}
			f.Detail = fmt.Sprintf("%d%% of %s", int(could/t.FreeableBasis*100+0.5), of)
		}
		if short && r.Sleepers > 0 {
			steadier := "from only " + result.History.Span() + " of history, so run the scan again after a few " +
				"days for a steadier figure"
			if f.Detail != "" {
				steadier = f.Detail + "; " + steadier
			}
			f.Detail = steadier
		}
		return f, true
	case t.Idle > 0 && (r.Workloads == 0 || short):
		// A whole window replayed in which nothing would have slept says
		// more than one moment's idleness, so this is only for without one.
		costs := "cost"
		if t.Idle == 1 {
			costs = "costs"
		}
		f := figure{Value: dollars(t.IdleMonthlyCost), Label: "could be freed a month, at most",
			Detail: fmt.Sprintf("what the %s idle right now %s while running, %s an hour",
				countOf(t.Idle, "workload"), costs, cents(t.IdleHourlyCost))}
		if short {
			f.Detail += "; " + tooEarly
		}
		return f, true
	case live:
		return figure{}, false
	}
	f := figure{Value: dollars(0), Label: "could be freed a month",
		Detail: "nothing is idle right now; Prometheus history, or measuring in dry-run, shows how often workloads are"}
	switch {
	case short:
		f.Detail = tooEarly
	case r.Workloads > 0:
		f.Detail = "no unmanaged workload would have slept over " + replayedOver(result.History)
	}
	return f, true
}

// tooLittleHistory says the replay had less than a day of history, short
// of the window asked for: a workload's pattern is daily, so a day is the
// least that can show how much of it would be idle.
func tooLittleHistory(h *discovery.HistorySource) bool {
	return h != nil && h.Partial && h.Hours > 0 && h.Hours < 24
}

func workloadRowFor(wl Workload) workloadRow {
	row := workloadRow{
		Namespace:  wl.Namespace,
		Workload:   strings.ToLower(string(wl.Kind)) + "/" + wl.Name,
		State:      string(wl.State),
		StateClass: string(wl.State),
		Cost:       dollars(wl.MonthlyCost),
		CostSort:   wl.MonthlyCost,
		Freed:      freedCell(wl), FreedSort: -1, Saved: scanSavedCell(wl),
		CouldFree: couldFreeCell(wl), CouldFreeSort: -1,
		Because: because(wl),
	}
	row.State += " (" + management(wl) + ")"
	row.CouldSleep, row.Wakes, row.Since, row.CouldSleepSort = sleepCells(wl)
	row.Slept, row.SleptWakes, row.SleptSince, row.SleptSort = sleptCells(wl)
	if row.Freed != "-" {
		row.FreedSort = wl.FreedThisMonth
	}
	if row.CouldFree != "-" {
		row.CouldFreeSort = discovery.CouldFree(wl)
	}
	return row
}

func methodFor(result scanResult, history, dependencies bool, idleAfter time.Duration) []string {
	method := []string{
		"Costs are what each workload's pods request, sidecars included, at the prices above. They're list " +
			"prices for the capacity reserved, not your bill.",
		"An hour asleep frees that capacity. It becomes money only when a node autoscaler removes the nodes " +
			"it no longer needs, so the scan reports what pausing could free, not what it would save.",
		fmt.Sprintf("A workload is active when its CPU is at least %d%% of what it requests (the scan's "+
			"--cpu-threshold), it was deployed, or an activity annotation says so. It's idle once it has had no "+
			"activity for %s (the scan's --idle-after), which is when Napwake would pause it. Workloads Napwake "+
			"manages are judged by the activity clock it keeps for them, with their own CPU threshold and "+
			"idle-after.",
			result.Settings.CPUThreshold, roundedDuration(idleAfter)),
	}
	if history {
		method = append(method, fmt.Sprintf("The history replay applies Napwake's rules to recorded CPU and "+
			"rollouts: after its idle-after with no activity (%s, or a managed workload's own), a workload would "+
			"be paused until the next activity, which wakes it. "+
			"Requests and activity annotations aren't recorded in Prometheus, so a workload used with little CPU "+
			"can look like it would sleep more than it would. Requests and prices are today's. The total counts "+
			"only workloads Napwake doesn't pause yet.", roundedDuration(idleAfter)))
	} else {
		method = append(method, "Without history, the scan sees CPU at one moment, which can't show how often a "+
			"workload would be woken, so this report doesn't estimate what pausing would free. Scanning a cluster with "+
			"Prometheus, or measuring with dry-run, does.")
	}
	if dependencies {
		method = append(method, "Dependencies are found in workloads' environment variables, literal and from "+
			"ConfigMaps, matched to the cluster's Services. Secrets aren't read, and addresses are left out of "+
			"this report.")
	}
	return method
}

// shellHTML colours a command the way a terminal would: the program and
// its subcommands, flags, and the key and value of each key=value.
func shellHTML(command string) template.HTML {
	var b strings.Builder
	b.WriteString(`<span class="line">`)
	for i, word := range strings.Fields(command) {
		if i > 0 {
			b.WriteByte(' ')
		}
		esc := template.HTMLEscapeString(word)
		key, value, isPair := strings.Cut(word, "=")
		switch {
		case i == 0:
			fmt.Fprintf(&b, `<span class="sh-cmd">%s</span>`, esc)
		case strings.HasPrefix(word, "-"):
			fmt.Fprintf(&b, `<span class="sh-flag">%s</span>`, esc)
		case isPair:
			fmt.Fprintf(&b, `<span class="sh-key">%s</span>=<span class="sh-val">%s</span>`,
				template.HTMLEscapeString(key), template.HTMLEscapeString(value))
		default:
			b.WriteString(esc)
		}
	}
	b.WriteString(`</span>`)
	return template.HTML(b.String()) //nolint:gosec // every part is escaped above
}

// roundedDuration writes d to the second without zero units: "1h", "90m"
// as "1h30m", "10s", "2h0m5s" as "2h5s".
func roundedDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d == 0 {
		return "0s"
	}
	sign := ""
	if d < 0 {
		sign, d = "-", -d
	}
	var b strings.Builder
	b.WriteString(sign)
	for _, unit := range []struct {
		size time.Duration
		name string
	}{{time.Hour, "h"}, {time.Minute, "m"}, {time.Second, "s"}} {
		if n := d / unit.size; n > 0 {
			fmt.Fprintf(&b, "%d%s", n, unit.name)
			d -= n * unit.size
		}
	}
	return b.String()
}
