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
	"regexp"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
	"github.com/okedeji/hybernate/internal/discovery"
	"github.com/okedeji/hybernate/internal/gitops"
	"github.com/okedeji/hybernate/internal/nodescale"
)

// stuckAfter is how long a pause or wake may take before status reports it.
// Both normally finish in seconds; a wake waiting on a slow dependency can
// take a few minutes.
const stuckAfter = 10 * time.Minute

// clockLag is how far the activity clock in status can trail the operator's
// own: it checks every minute, but writes a clock that only moved every five
// minutes. A pause time this far past on a workload still Running isn't
// late yet; there may be activity status doesn't show.
const clockLag = 7 * time.Minute

// errNotInstalled means the cluster has no ManagedWorkload CRD.
var errNotInstalled = errors.New("no ManagedWorkload CRD, so Hybernate isn't installed in this cluster; " +
	"kubectl hybernate scan works without it, and shows what it would pause")

// errAmbiguous means a name given to a command matches more than one
// ManagedWorkload's target.
var errAmbiguous = errors.New("matches more than one ManagedWorkload")

// savedShareExpected is the share of what pausing frees that status expects
// to come off the bill once nodes scale down. Less than this, over at
// least a dollar, and it asks why nodes aren't scaling down.
const savedShareExpected = 0.5

// recentLimit caps the pauses and wakes listed, so a busy cluster still fits
// one screen; -o json has them all.
const recentLimit = 10

type statusOptions struct {
	namespaces namespaceFlags
	output     string
	since      time.Duration
	timeout    time.Duration
	now        func() time.Time
}

func statusCmd(kube *kubeFlags) *cobra.Command {
	opts := statusOptions{since: 24 * time.Hour, timeout: time.Minute, now: time.Now}
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show what Hybernate is doing in a cluster right now",
		Long: `Status shows every workload Hybernate manages on one screen: what each is
doing, its last activity, when it pauses next or what holds it awake, what
pausing it has freed this month, and how much of that has come off the bill.
Freed capacity becomes savings only as a node autoscaler removes nodes, so
status names the one it found. Above the table it lists what needs
attention, and below it recent pauses and wakes, with what caused each, as
far back as the cluster keeps events: an hour, unless its API server is set
to keep more.

It reads every namespace you can, or only the context's namespace when your
access doesn't allow listing them all. It only reads ManagedWorkloads, their
events, their workloads' annotations, and what tells which node autoscaler
runs, and checks that a namespace named with -n that has no ManagedWorkloads
exists. kubectl hybernate nodes shows why nodes aren't scaling down.

Examples:
  kubectl hybernate status
  kubectl hybernate status --context staging -n preview-42
  kubectl hybernate status --since 1h -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkOutput(opts.output); err != nil {
				return err
			}
			if err := checkPositive("since", opts.since); err != nil {
				return err
			}
			if err := checkPositive("timeout", opts.timeout); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), opts.timeout)
			defer cancel()
			k8s, at, err := kube.client(ctx)
			if err != nil {
				return fmt.Errorf("building kubernetes client: %w", err)
			}
			result, err := clusterStatus(ctx, k8s, at.namespace, opts)
			if err != nil {
				return timedOut(ctx, err, opts.timeout)
			}
			result.Context = at.context
			result.Cluster = clusterName(at.context)
			return writeStatus(cmd.OutOrStdout(), result, opts.output, opts.now())
		},
	}
	addNamespaceFlags(cmd, &opts.namespaces, "Namespace to show; repeat for several (defaults to all you can read)")
	addOutputFlag(cmd, &opts.output)
	cmd.Flags().DurationVar(&opts.since, "since", opts.since, "How far back to list pauses and wakes")
	addTimeoutFlag(cmd, &opts.timeout, "How long to wait for the cluster before giving up")
	return cmd
}

type statusResult struct {
	Context        string               `json:"context"`
	Cluster        string               `json:"cluster"`
	NodeAutoscaler nodescale.Autoscaler `json:"nodeAutoscaler"`
	FreedThisMonth float64              `json:"freedThisMonth"`
	SavedThisMonth float64              `json:"savedThisMonth"`
	// SavingsUnknown are why what pausing saved can't be measured, for
	// the workloads it can't, each once.
	SavingsUnknown []string `json:"savingsUnknown,omitempty"`
	// ForecastTimeZones are the time zones forecasts count hours in, by
	// what the operator recorded: one, unless two installs differ.
	ForecastTimeZones []string `json:"forecastTimeZones,omitempty"`
	// NodesNotScalingDown is set when pausing freed far more than came off
	// the bill this month, for the workloads whose savings are measured.
	NodesNotScalingDown bool             `json:"nodesNotScalingDown,omitempty"`
	Workloads           []statusWorkload `json:"workloads"`
	Problems            []problem        `json:"problems"`
	Recent              []happening      `json:"recent"`
	// Note says when fewer namespaces were read than were asked for.
	Note string `json:"note,omitempty"`
	// EventsHidden is set when the user can't read events, so Recent is
	// empty for that reason and not because nothing happened.
	EventsHidden bool `json:"eventsHidden,omitempty"`
}

type statusWorkload struct {
	Namespace      string                 `json:"namespace"`
	Name           string                 `json:"managedWorkload"`
	Target         string                 `json:"target"`
	Phase          v1alpha1.WorkloadPhase `json:"phase"`
	DryRun         bool                   `json:"dryRun"`
	Since          *time.Time             `json:"since,omitempty"`
	LastActivity   *time.Time             `json:"lastActivity,omitempty"`
	ActivitySource string                 `json:"activitySource,omitempty"`
	Next           string                 `json:"next"`
	FreedThisMonth float64                `json:"freedThisMonth"`
	SavedThisMonth float64                `json:"savedThisMonth"`
	SavingsUnknown string                 `json:"savingsUnknown,omitempty"`
	// namedApart is set for a ManagedWorkload written by hand under a name
	// other than its target's, which the table then shows beside it.
	namedApart bool
}

// problem is something about a workload that keeps Hybernate from doing
// its job, and needs a person. Workload is the target's name, which is how
// status names workloads; ManagedWorkload is what to kubectl describe.
type problem struct {
	Namespace       string `json:"namespace"`
	Workload        string `json:"workload"`
	ManagedWorkload string `json:"managedWorkload"`
	Type            string `json:"type"`
	Message         string `json:"message"`
}

// happening is one pause or wake.
type happening struct {
	At              time.Time `json:"at"`
	Namespace       string    `json:"namespace"`
	Workload        string    `json:"workload"`
	ManagedWorkload string    `json:"managedWorkload"`
	Reason          string    `json:"reason"`
	Message         string    `json:"message"`
}

// troubles are the conditions in the state that's a problem: most are
// raised as True, and the ones that report something working are a
// problem when False.
var troubles = map[string]metav1.ConditionStatus{
	"GitOpsConflict":       metav1.ConditionTrue,
	"DependencyCycle":      metav1.ConditionTrue,
	"DependencyNotFound":   metav1.ConditionTrue,
	"DependencyNotAllowed": metav1.ConditionTrue,
	"DuplicateTarget":      metav1.ConditionTrue,
	"Protected":            metav1.ConditionTrue,
	"TargetAvailable":      metav1.ConditionFalse,
	"MetricsAvailable":     metav1.ConditionFalse,
	"PrometheusAvailable":  metav1.ConditionFalse,
	"PrometheusQueryEmpty": metav1.ConditionTrue,
	"WakeOnRequest":        metav1.ConditionFalse,
	"ResumeIncomplete":     metav1.ConditionTrue,
	"PauseIncomplete":      metav1.ConditionTrue,
}

// untroubled are the reasons a trouble condition carries when it needs
// nobody's attention: it reports what the user set up on purpose, or the
// normal state of a workload that's awake.
var untroubled = map[string]bool{
	"NoServices":         true,
	"TargetIgnored":      true,
	"HybernateComponent": true,
	"DoormanDisabled":    true,
	"NotPaused":          true,
}

// wakeCauses are the event reasons that say what woke a workload. The
// Resumed that follows one adds nothing, so it's left out; a Resumed with no
// cause before it, such as a hand-back on protecting the namespace, is the
// only record of that wake, so it's kept.
var wakeCauses = map[string]bool{
	"WokeByActivity": true,
	"WokenByRequest": true,
	"AutoResume":     true,
	"ScaledUp":       true,
}

// happenings are the event reasons that say a workload paused or woke.
var happenings = map[string]bool{
	"Paused":           true,
	"Resumed":          true,
	"WokeByActivity":   true,
	"WokenByRequest":   true,
	"AutoResume":       true,
	"ScaledUp":         true,
	"RequestNotServed": true,
}

// A wake by a request the doorman held is reported by the doorman's
// WokenByRequest, which names the Service, so the operator's own
// WokeByActivity for it is left out.
const requestWakeMessage = "a request is waiting, waking"

// targetKey is a Deployment or StatefulSet.
type targetKey struct {
	namespace string
	kind      v1alpha1.TargetKind
	name      string
}

func clusterStatus(ctx context.Context, c client.Client, home string, opts statusOptions) (statusResult, error) {
	now := opts.now()
	workloads, scope, err := listManaged(ctx, c, opts.namespaces, home)
	if err != nil {
		return statusResult{}, err
	}
	result := statusResult{Workloads: []statusWorkload{}, Problems: []problem{}, Recent: []happening{},
		Note: scope.note}

	events, err := listEvents(ctx, c, scope.namespaces)
	if apierrors.IsForbidden(err) {
		result.EventsHidden = true
	} else if err != nil {
		return statusResult{}, err
	}
	activeUntil, err := targetActiveUntil(ctx, c, scope.namespaces, workloads)
	if err != nil {
		return statusResult{}, err
	}
	result.NodeAutoscaler, err = nodescale.Detect(ctx, c)
	if err != nil {
		return statusResult{}, fmt.Errorf("finding the node autoscaler: %w", err)
	}

	targets := map[client.ObjectKey]string{}
	for i := range workloads {
		w := &workloads[i]
		targets[client.ObjectKeyFromObject(w)] = w.Spec.Target.Name
		row := workloadStatus(w, activeUntil[targetOfManaged(w)], now)
		result.Workloads = append(result.Workloads, row)
		result.Problems = append(result.Problems, problemsOf(w, now)...)
		if p := w.Status.Prediction; p != nil && p.TimeZone != "" && !slices.Contains(result.ForecastTimeZones, p.TimeZone) {
			result.ForecastTimeZones = append(result.ForecastTimeZones, p.TimeZone)
		}
	}
	slices.Sort(result.ForecastTimeZones)
	sumFreedAndSaved(&result)
	result.Recent = append(result.Recent, recentHappenings(events, targets, now.Add(-opts.since))...)
	return result, nil
}

// sumFreedAndSaved totals what the workloads freed and saved this month,
// and judges whether nodes are scaling down from those whose savings are
// measured.
func sumFreedAndSaved(result *statusResult) {
	var measuredFreed, measuredSaved float64
	for _, row := range result.Workloads {
		result.FreedThisMonth += row.FreedThisMonth
		result.SavedThisMonth += row.SavedThisMonth
		if row.SavingsUnknown != "" {
			if !slices.Contains(result.SavingsUnknown, row.SavingsUnknown) {
				result.SavingsUnknown = append(result.SavingsUnknown, row.SavingsUnknown)
			}
			continue
		}
		measuredFreed += row.FreedThisMonth
		measuredSaved += row.SavedThisMonth
	}
	result.NodesNotScalingDown = measuredFreed >= 1 && measuredSaved < measuredFreed*savedShareExpected
}

// managedScope is the namespaces status read, and why they're fewer than
// asked for, if they are.
type managedScope struct {
	namespaces []string
	note       string
}

// listManaged lists the ManagedWorkloads in the namespaces asked for, or in
// every namespace. Without -n or -A, a user who can't list every namespace
// gets the context's.
func listManaged(ctx context.Context, c client.Client, ns namespaceFlags, home string) (
	[]v1alpha1.ManagedWorkload, managedScope, error) {
	scope := managedScope{namespaces: ns.list()}
	if len(scope.namespaces) == 0 {
		scope.namespaces = []string{metav1.NamespaceAll}
	}
	var all []v1alpha1.ManagedWorkload
	for _, n := range scope.namespaces {
		var list v1alpha1.ManagedWorkloadList
		err := c.List(ctx, &list, client.InNamespace(n))
		if n == metav1.NamespaceAll && !ns.all && apierrors.IsForbidden(err) {
			scope.note = fmt.Sprintf("Only %s, the context's namespace, is shown: your access doesn't allow "+
				"listing ManagedWorkloads in every namespace. Name others with -n.", home)
			scope.namespaces = []string{home}
			err = c.List(ctx, &list, client.InNamespace(home))
			n = home
		}
		switch {
		case meta.IsNoMatchError(err):
			return nil, scope, errNotInstalled
		case n == metav1.NamespaceAll && apierrors.IsForbidden(err):
			return nil, scope, fmt.Errorf("listing ManagedWorkloads in every namespace: %w; "+
				"pass -n for the namespaces you can read", err)
		case n == metav1.NamespaceAll && err != nil:
			return nil, scope, fmt.Errorf("listing ManagedWorkloads in every namespace: %w", err)
		case err != nil:
			return nil, scope, fmt.Errorf("listing ManagedWorkloads in %s: %w", n, err)
		}
		if len(list.Items) == 0 && n != metav1.NamespaceAll {
			if err := checkNamespaceExists(ctx, c, n); err != nil {
				return nil, scope, err
			}
		}
		all = append(all, list.Items...)
	}
	slices.SortFunc(all, func(a, b v1alpha1.ManagedWorkload) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})
	return all, scope, nil
}

// checkNamespaceExists fails for a namespace that doesn't exist, which
// would otherwise read as one Hybernate manages nothing in, as for a typo
// in -n. Without access to namespaces, it can't tell, and lets it be.
func checkNamespaceExists(ctx context.Context, c client.Client, name string) error {
	var ns corev1.Namespace
	err := c.Get(ctx, client.ObjectKey{Name: name}, &ns)
	switch {
	case apierrors.IsNotFound(err):
		return fmt.Errorf("namespace %s doesn't exist; check the name", name)
	case err != nil && !apierrors.IsForbidden(err):
		return fmt.Errorf("getting namespace %s: %w", name, err)
	}
	return nil
}

func listEvents(ctx context.Context, c client.Client, namespaces []string) ([]corev1.Event, error) {
	var all []corev1.Event
	for _, ns := range namespaces {
		var events corev1.EventList
		if err := c.List(ctx, &events, client.InNamespace(ns),
			client.MatchingFields{"involvedObject.kind": "ManagedWorkload"}); err != nil {
			return nil, fmt.Errorf("listing events: %w", err)
		}
		all = append(all, events.Items...)
	}
	slices.SortStableFunc(all, func(a, b corev1.Event) int { return eventTime(&a).Compare(eventTime(&b)) })
	return all, nil
}

// targetActiveUntil reads the active-until annotation on the workloads that
// could pause next, which the operator honours on the workload as well as on
// its ManagedWorkload. Only metadata is read. Without access to the
// workloads, status goes without it.
func targetActiveUntil(ctx context.Context, c client.Client, namespaces []string,
	workloads []v1alpha1.ManagedWorkload) (map[targetKey]time.Time, error) {
	out := map[targetKey]time.Time{}
	if !slices.ContainsFunc(workloads, func(w v1alpha1.ManagedWorkload) bool { return mayPause(&w) }) {
		return out, nil
	}
	kinds := map[v1alpha1.TargetKind]string{
		v1alpha1.TargetKindDeployment:  "DeploymentList",
		v1alpha1.TargetKindStatefulSet: "StatefulSetList",
	}
	for kind, listKind := range kinds {
		for _, ns := range namespaces {
			var list metav1.PartialObjectMetadataList
			list.SetGroupVersionKind(schema.GroupVersionKind{Group: appsv1.GroupName, Version: "v1", Kind: listKind})
			err := c.List(ctx, &list, client.InNamespace(ns))
			if apierrors.IsForbidden(err) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("listing %s workloads: %w", kind, err)
			}
			for _, obj := range list.Items {
				if until := activeUntil(obj.Annotations); !until.IsZero() {
					out[targetKey{obj.Namespace, kind, obj.Name}] = until
				}
			}
		}
	}
	return out, nil
}

func mayPause(w *v1alpha1.ManagedWorkload) bool {
	return w.Status.Phase == v1alpha1.PhaseRunning || w.Status.Phase == v1alpha1.PhaseIdle
}

func activeUntil(annotations map[string]string) time.Time {
	until, err := time.Parse(time.RFC3339, annotations[v1alpha1.AnnotationActiveUntil])
	if err != nil {
		return time.Time{}
	}
	return until
}

func targetOfManaged(w *v1alpha1.ManagedWorkload) targetKey {
	return targetKey{w.Namespace, w.Spec.Target.Kind, w.Spec.Target.Name}
}

// workloadStatus is a workload's row. targetActiveUntil is the active-until
// on the workload itself, which its ManagedWorkload doesn't record.
func workloadStatus(w *v1alpha1.ManagedWorkload, targetActiveUntil, now time.Time) statusWorkload {
	row := statusWorkload{
		Namespace:      w.Namespace,
		Name:           w.Name,
		Target:         strings.ToLower(string(w.Spec.Target.Kind)) + "/" + w.Spec.Target.Name,
		Phase:          w.Status.Phase,
		DryRun:         w.Spec.DryRun,
		Next:           nextFor(w, targetActiveUntil, now),
		FreedThisMonth: discovery.FreedThisMonth(w, now),
		SavedThisMonth: discovery.SavedThisMonth(w, now),
		namedApart:     w.Name != w.Spec.Target.Name && w.Labels[v1alpha1.LabelFromLabel] != v1alpha1.True,
	}
	if c := w.Status.Cost; c != nil {
		row.SavingsUnknown = c.SavingsUnknown
	}
	if t := w.Status.LastTransitionTime; t != nil {
		since := t.UTC()
		row.Since = &since
	}
	if a := w.Status.Activity; a != nil && !a.LastActivityTime.IsZero() {
		last := a.LastActivityTime.UTC()
		row.LastActivity = &last
		row.ActivitySource = string(a.LastActivitySource)
	}
	return row
}

// nextFor says what happens to a workload next, or what's stopping it.
func nextFor(w *v1alpha1.ManagedWorkload, targetActiveUntil, now time.Time) string {
	conditions := w.Status.Conditions
	if c := meta.FindStatusCondition(conditions, "TargetAvailable"); c != nil && c.Status == metav1.ConditionFalse {
		if c.Reason == "TargetIgnored" {
			return "nothing: labelled " + v1alpha1.LabelIgnore
		}
		return "nothing: workload not found"
	}
	switch {
	case meta.IsStatusConditionTrue(conditions, "DuplicateTarget"):
		return "nothing: another ManagedWorkload has it"
	case meta.IsStatusConditionTrue(conditions, "Protected"):
		switch meta.FindStatusCondition(conditions, "Protected").Reason {
		case "HybernateComponent":
			return "never pauses: part of Hybernate"
		case "PVCsDeletedOnScaleDown":
			return "never pauses: PVCs deleted on scale-down"
		}
		return "never pauses: namespace protected"
	}
	switch w.Status.Phase {
	case v1alpha1.PhasePaused:
		if len(w.Status.Doorman) > 0 {
			return "wakes on a request or activity"
		}
		return "wakes on activity"
	case v1alpha1.PhaseRunning, v1alpha1.PhaseIdle:
	default:
		return "-"
	}
	if w.Spec.IdlePolicy == nil {
		return "never pauses: no idlePolicy"
	}
	if c := meta.FindStatusCondition(conditions, "HeldByDependents"); c != nil && c.Status == metav1.ConditionTrue {
		return heldByDependents(c.Message)
	}
	if until := later(activeUntil(w.Annotations), targetActiveUntil); until.After(now) {
		return "held awake for " + span(until.Sub(now)) + " (active-until)"
	}
	switch {
	case meta.IsStatusConditionTrue(conditions, "DependencyCycle"):
		return "won't pause: dependsOn cycle"
	case meta.IsStatusConditionFalse(conditions, "MetricsAvailable"):
		return "won't pause: no CPU metrics"
	case meta.IsStatusConditionFalse(conditions, "PrometheusAvailable"):
		return "won't pause: Prometheus failing"
	}
	a := w.Status.Activity
	if a == nil || a.PauseAt == nil {
		return "-"
	}
	verb := "pauses"
	if w.Spec.DryRun {
		verb = "would pause"
	}
	pauseAt := a.PauseAt.Time
	retryAt, heldByGitOps := gitOpsHold(w, now)
	afterGitOps := "pauses at " + clockTime(retryAt) + " (after GitOps conflict)"
	switch {
	case pauseAt.After(now) && heldByGitOps && retryAt.After(pauseAt):
		return afterGitOps
	case pauseAt.After(now):
		return verb + " in " + span(pauseAt.Sub(now))
	case meta.IsStatusConditionTrue(conditions, "IdleVetoed"):
		return heldByForecast(meta.FindStatusCondition(conditions, "IdleVetoed").Message)
	case heldByGitOps:
		return afterGitOps
	case w.Status.Phase == v1alpha1.PhaseIdle:
		return verb + " now"
	case now.Sub(pauseAt) < clockLag:
		return verb + " soon"
	}
	return "overdue to pause"
}

// gitOpsHold is when the operator pauses a workload again whose last pause
// a GitOps tool undid, and true until then. Dry-run never pauses, so it
// has none.
func gitOpsHold(w *v1alpha1.ManagedWorkload, now time.Time) (time.Time, bool) {
	s := w.Status.LastScaledUp
	if w.Spec.DryRun || s == nil || !meta.IsStatusConditionTrue(w.Status.Conditions, "GitOpsConflict") {
		return time.Time{}, false
	}
	until := s.At.Add(gitops.RetryAfter)
	return until, now.Before(until)
}

// dependentsAwake starts a HeldByDependents message, before the dependents
// it names.
const dependentsAwake = "kept awake for "

// heldByDependents names the dependents that hold a workload awake, until
// they pause, from its HeldByDependents message, which lists them.
func heldByDependents(message string) string {
	names, ok := strings.CutPrefix(oneLine(message), dependentsAwake)
	switch {
	case !ok || names == "":
		return "held awake by its dependents"
	case strings.Contains(names, ", ") || strings.HasSuffix(names, " more"):
		return "held awake while " + names + " use it"
	}
	return "held awake while " + names + " uses it"
}

// forecastHour is the hour the forecast expects demand in, in an IdleVetoed
// message.
var forecastHour = regexp.MustCompile(`in the hour from (\d{2}:\d{2} UTC)`)

// heldByForecast says when the forecast expects the demand that holds a
// workload awake, from its IdleVetoed message.
func heldByForecast(message string) string {
	if m := forecastHour.FindStringSubmatch(message); m != nil {
		return "held awake by the forecast: busy from " + m[1]
	}
	return "held awake by the forecast"
}

// clockTime is a time of day in UTC, as the operator's messages give one.
func clockTime(t time.Time) string {
	return t.UTC().Format("15:04 UTC")
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func problemsOf(w *v1alpha1.ManagedWorkload, now time.Time) []problem {
	var out []problem
	for _, c := range w.Status.Conditions {
		bad, ok := troubles[c.Type]
		if !ok || c.Status != bad || untroubled[c.Reason] {
			continue
		}
		out = append(out, problem{Namespace: w.Namespace, Workload: w.Spec.Target.Name, ManagedWorkload: w.Name,
			Type: c.Type, Message: cmp.Or(c.Message, c.Reason)})
	}
	phase := w.Status.Phase
	if t := w.Status.LastTransitionTime; t != nil && (phase == v1alpha1.PhasePausing || phase == v1alpha1.PhaseResuming) {
		if stuck := now.Sub(t.Time); stuck >= stuckAfter {
			msg := fmt.Sprintf("%s for %s", strings.ToLower(string(phase)), span(stuck))
			if c := meta.FindStatusCondition(w.Status.Conditions, "WaitingForDependencies"); c != nil &&
				c.Status == metav1.ConditionTrue {
				msg += ": " + c.Message
			}
			out = append(out, problem{Namespace: w.Namespace, Workload: w.Spec.Target.Name, ManagedWorkload: w.Name,
				Type: "Stuck", Message: msg})
		}
	}
	return out
}

// recentHappenings lists the pauses and wakes since a time, newest first,
// from events sorted oldest first. Dry-run events are left out: nothing
// paused or woke. The operator starts its messages with the target's name,
// which is dropped, since status shows the workload beside it.
func recentHappenings(events []corev1.Event, targets map[client.ObjectKey]string, since time.Time) []happening {
	caused := map[client.ObjectKey]bool{}
	var out []happening
	for i := range events {
		ev := &events[i]
		if !happenings[ev.Reason] || strings.HasPrefix(ev.Message, "[dry-run]") {
			continue
		}
		key := client.ObjectKey{Namespace: ev.InvolvedObject.Namespace, Name: ev.InvolvedObject.Name}
		switch {
		case ev.Reason == "Paused":
			caused[key] = false
		case ev.Reason == "Resumed" && caused[key]:
			caused[key] = false
			continue
		case wakeCauses[ev.Reason]:
			caused[key] = true
		}
		at := eventTime(ev).UTC()
		if at.Before(since) {
			continue
		}
		// Events outlive the ManagedWorkload they're for; one that's gone
		// is named as it was.
		target := cmp.Or(targets[key], key.Name)
		message := strings.TrimPrefix(ev.Message, target+": ")
		if ev.Reason == "WokeByActivity" && message == requestWakeMessage {
			continue
		}
		out = append(out, happening{At: at, Namespace: key.Namespace, Workload: target, ManagedWorkload: key.Name,
			Reason: ev.Reason, Message: message})
	}
	slices.SortStableFunc(out, func(a, b happening) int { return b.At.Compare(a.At) })
	return out
}

// eventTime is when an event last happened. Events recorded through
// events.k8s.io set eventTime, and series.lastObservedTime once repeated;
// older recorders set lastTimestamp.
func eventTime(ev *corev1.Event) time.Time {
	switch {
	case ev.Series != nil && !ev.Series.LastObservedTime.IsZero():
		return ev.Series.LastObservedTime.Time
	case !ev.EventTime.IsZero():
		return ev.EventTime.Time
	case !ev.LastTimestamp.IsZero():
		return ev.LastTimestamp.Time
	}
	return ev.FirstTimestamp.Time
}

// findManaged gets the ManagedWorkload a user named in a namespace: by its
// own name, or by its workload's as status shows it, "[kind/]name". The two
// differ for a ManagedWorkload written by hand under another name, and for
// one the operator names after its kind because a Deployment and a
// StatefulSet share a name. A bare name more than one ManagedWorkload
// answers to, by either name, is ambiguous; "kind/name" names a workload,
// which settles it.
func findManaged(ctx context.Context, c client.Client, namespace, arg string) (*v1alpha1.ManagedWorkload, error) {
	kind, name, err := workloadArg(arg)
	if err != nil {
		return nil, err
	}
	var named *v1alpha1.ManagedWorkload
	var notFound error
	if kind == "" {
		var w v1alpha1.ManagedWorkload
		err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &w)
		switch {
		case err == nil:
			named = &w
		case meta.IsNoMatchError(err):
			return nil, errNotInstalled
		case !apierrors.IsNotFound(err):
			return nil, fmt.Errorf("getting ManagedWorkload %s/%s: %w", namespace, name, err)
		default:
			notFound = err
		}
	}

	var list v1alpha1.ManagedWorkloadList
	if err := c.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		switch {
		case meta.IsNoMatchError(err):
			return nil, errNotInstalled
		case named != nil && apierrors.IsForbidden(err):
			return named, nil
		case notFound != nil && apierrors.IsForbidden(err):
			return nil, fmt.Errorf("getting ManagedWorkload %s/%s: %w", namespace, name, notFound)
		}
		return nil, fmt.Errorf("listing ManagedWorkloads in %s: %w", namespace, err)
	}
	var matches []*v1alpha1.ManagedWorkload
	for i := range list.Items {
		w := &list.Items[i]
		ownName := kind == "" && w.Name == name
		if ownName || w.Spec.Target.Name == name && (kind == "" || strings.EqualFold(string(w.Spec.Target.Kind), kind)) {
			matches = append(matches, w)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		if notFound == nil {
			notFound = apierrors.NewNotFound(
				schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "managedworkloads"}, arg)
		}
		return nil, fmt.Errorf("no ManagedWorkload in %s is named %s or manages a workload of that name: %w",
			namespace, arg, notFound)
	}
	names := make([]string, 0, len(matches))
	for _, w := range matches {
		names = append(names, fmt.Sprintf("%s (%s/%s)", w.Name, strings.ToLower(string(w.Spec.Target.Kind)),
			w.Spec.Target.Name))
	}
	return nil, fmt.Errorf("%s in %s %w: %s; name one by its workload's kind and name, such as %s/%s", arg,
		namespace, errAmbiguous, strings.Join(names, ", "), strings.ToLower(string(matches[0].Spec.Target.Kind)),
		matches[0].Spec.Target.Name)
}

func writeStatus(w io.Writer, result statusResult, output string, now time.Time) error {
	return writeOutput(w, output, result, func() error { return writeStatusTable(w, result, now) })
}

func writeStatusTable(w io.Writer, result statusResult, now time.Time) error {
	p := &printer{w: w}
	if len(result.Workloads) == 0 {
		p.line("%s: Hybernate manages no workloads here.", result.Cluster)
		p.line("%s", nodeAutoscalerLine(result.NodeAutoscaler))
		if result.Note != "" {
			p.line("%s", result.Note)
		}
		p.line("kubectl hybernate scan finds idle ones; label one hybernate.io/managed=true for Hybernate to manage it.")
		return p.err
	}
	p.line("%s: Hybernate manages %s.", result.Cluster, countOf(len(result.Workloads), "workload"))
	p.line("  %s", phaseCounts(result.Workloads))
	p.line("  %s", nodeAutoscalerLine(result.NodeAutoscaler))
	if zone := timeZoneLine(result.ForecastTimeZones); zone != "" {
		p.line("  %s", zone)
	}
	if result.FreedThisMonth > 0 || result.SavedThisMonth > 0 {
		p.line("  Freed %s this month, and saved %s of it.", savedMoney(result.FreedThisMonth),
			savedMoney(result.SavedThisMonth))
	}
	for _, why := range result.SavingsUnknown {
		p.line("  What's saved can't be measured for some workloads: %s", oneLine(why))
	}
	if result.Note != "" {
		p.line("  %s", result.Note)
	}

	if len(result.Problems) > 0 || result.NodesNotScalingDown {
		p.line("")
		p.line("Needs attention:")
		if result.NodesNotScalingDown {
			p.line("  Pausing freed %s this month, but only %s of it came off the bill: nodes aren't scaling down.",
				savedMoney(result.FreedThisMonth), savedMoney(result.SavedThisMonth))
			p.line("  kubectl hybernate nodes shows why.")
		}
		tw := tabwriter.NewWriter(p, 0, 0, 3, ' ', 0)
		for _, pr := range result.Problems {
			_, _ = fmt.Fprintf(tw, "  %s/%s\t%s\t%s\n", pr.Namespace, pr.Workload, pr.Type, oneLine(pr.Message))
		}
		_ = tw.Flush() // writes through p, which keeps the first error
	}

	p.line("")
	tw := tabwriter.NewWriter(p, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(tw, "  NAMESPACE\tWORKLOAD\tSTATE\tFOR\tLAST ACTIVITY\tNEXT\tFREED THIS MONTH\tSAVED")
	for _, wl := range result.Workloads {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", wl.Namespace, workloadCell(wl), stateCell(wl),
			sinceCell(wl.Since, now), activityCell(wl, now), wl.Next, moneyCell(wl.FreedThisMonth), statusSavedCell(wl))
	}
	_ = tw.Flush() // writes through p, which keeps the first error

	p.line("")
	switch {
	case result.EventsHidden:
		p.line("Recent pauses and wakes aren't shown: your access doesn't allow reading events.")
	case len(result.Recent) == 0:
		p.line("No pauses or wakes in this period.")
	default:
		p.line("Recent pauses and wakes:")
		tw := tabwriter.NewWriter(p, 0, 0, 3, ' ', 0)
		for i, h := range result.Recent {
			if i == recentLimit {
				_, _ = fmt.Fprintf(tw, "  and %d more; -o json lists them all\n", len(result.Recent)-recentLimit)
				break
			}
			_, _ = fmt.Fprintf(tw, "  %s\t%s/%s\t%s\n", ago(h.At, now), h.Namespace, h.Workload, oneLine(h.Message))
		}
		_ = tw.Flush() // writes through p, which keeps the first error
	}
	return p.err
}

// workloadCell names the workload, with its ManagedWorkload when that's
// named differently, since wake and deps take either.
func workloadCell(wl statusWorkload) string {
	if wl.namedApart {
		return fmt.Sprintf("%s (%s)", wl.Target, wl.Name)
	}
	return wl.Target
}

// phaseCounts is how many workloads are in each phase, busiest phases first.
func phaseCounts(workloads []statusWorkload) string {
	order := []v1alpha1.WorkloadPhase{v1alpha1.PhasePaused, v1alpha1.PhaseRunning, v1alpha1.PhaseIdle,
		v1alpha1.PhasePausing, v1alpha1.PhaseResuming, v1alpha1.PhaseCreating}
	counts := map[v1alpha1.WorkloadPhase]int{}
	dryRun := 0
	for _, wl := range workloads {
		counts[cmp.Or(wl.Phase, v1alpha1.PhaseCreating)]++
		if wl.DryRun {
			dryRun++
		}
	}
	var parts []string
	for _, phase := range order {
		if n := counts[phase]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, strings.ToLower(string(phase))))
		}
	}
	s := strings.Join(parts, ", ")
	if dryRun > 0 {
		s += fmt.Sprintf("; %d in dry-run, measured but never paused", dryRun)
	}
	return s
}

// timeZoneLine says which time zone forecasts count hours in, by what the
// operator recorded, and how to change it from the default, which is
// rarely where people are.
func timeZoneLine(zones []string) string {
	switch {
	case len(zones) == 0:
		return ""
	case len(zones) > 1:
		return "Forecast time zones: " + strings.Join(zones, ", ") + ", as the installs differ"
	case zones[0] == "UTC":
		return "Forecast time zone: UTC, the default; if your users are elsewhere, set the Helm value timezone " +
			"to theirs, such as Europe/London, so wakes follow their clock"
	}
	return "Forecast time zone: " + zones[0]
}

func stateCell(wl statusWorkload) string {
	state := strings.ToLower(string(cmp.Or(wl.Phase, v1alpha1.PhaseCreating)))
	if wl.DryRun {
		state += " (dry-run)"
	}
	return state
}

func sinceCell(t *time.Time, now time.Time) string {
	if t == nil {
		return "-"
	}
	return span(now.Sub(*t))
}

func activityCell(wl statusWorkload, now time.Time) string {
	if wl.LastActivity == nil {
		return "-"
	}
	return wl.ActivitySource + ", " + ago(*wl.LastActivity, now)
}

// statusSavedCell is what pausing a workload took off the bill this month,
// or "unknown" when the operator can't measure it.
func statusSavedCell(wl statusWorkload) string {
	if wl.SavingsUnknown != "" && wl.SavedThisMonth == 0 {
		return "unknown"
	}
	return moneyCell(wl.SavedThisMonth)
}

func moneyCell(amount float64) string {
	if amount == 0 {
		return "-"
	}
	return savedMoney(amount)
}

// savedMoney is an amount in whole dollars, as scan shows one. An amount
// under half a dollar isn't nothing, so it isn't shown as $0.
func savedMoney(amount float64) string {
	if amount > 0 && amount < 0.5 {
		return "<$1"
	}
	return dollars(amount)
}

func ago(t, now time.Time) string {
	d := now.Sub(t)
	if d < time.Minute {
		return "just now"
	}
	return span(d) + " ago"
}

// span is a duration to its largest unit, as kubectl shows ages: 45s, 12m,
// 3h, 6d.
func span(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(int(d.Seconds()), 0))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
