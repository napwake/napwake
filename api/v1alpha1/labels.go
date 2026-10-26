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

package v1alpha1

import "path"

// True is the value that turns a Napwake label or boolean annotation on.
// Any other value, "True" and "yes" included, leaves it off.
const True = "true"

// FieldManager is the name Napwake's writes, such as the replica count it
// pauses, are made under, so the API server attributes them to it where it
// records who set a field.
const FieldManager = "napwake"

const (
	// LabelManaged opts a Deployment or StatefulSet, or every one in a
	// namespace, in to Napwake: "true" gets it a ManagedWorkload.
	LabelManaged = "napwake.dev/managed"

	// LabelProtected marks a namespace Napwake must never manage, such as
	// production: nothing in it is opted in or paused, whatever its labels.
	LabelProtected = "napwake.dev/protected"

	// LabelAllowProtected lets Napwake manage a protected namespace, one
	// marked with LabelProtected or matching the operator's protected
	// namespace patterns. It's set on purpose, never by Napwake.
	LabelAllowProtected = "napwake.dev/allow-protected"

	// LabelIgnore excludes a workload from management, even in a managed
	// namespace.
	LabelIgnore = "napwake.dev/ignore"

	// LabelFromLabel marks a ManagedWorkload created because its workload
	// or namespace carries LabelManaged. Its spec follows the annotations
	// and is rewritten from them.
	LabelFromLabel = "napwake.dev/from-label"

	// LabelOperator, on the doorman EndpointSlices an operator writes, is
	// the namespace of its install, like AnnotationOperator on the
	// ManagedWorkloads it takes on. Another install leaves them alone.
	LabelOperator = "napwake.dev/operator"

	// AnnotationLastActivity records activity seen by another tool, such as a
	// developer portal or CI pipeline, as an RFC 3339 time. Set on the ManagedWorkload
	// or its target. A newer value wakes a paused workload.
	AnnotationLastActivity = "napwake.dev/last-activity"

	// AnnotationActiveUntil keeps a workload awake until an RFC 3339 time.
	// Set on the ManagedWorkload or its target.
	AnnotationActiveUntil = "napwake.dev/active-until"

	// AnnotationLastRequest records, as an RFC 3339 time, a request the
	// doorman is holding for a paused workload. It wakes the workload like
	// AnnotationLastActivity, and the clock records the wake as a request.
	AnnotationLastRequest = "napwake.dev/last-request"

	// AnnotationLastRequestFrom is the address the request that woke a
	// paused workload came from, stamped by the doorman with
	// AnnotationLastRequest, so the operator can learn which workload sent
	// it.
	AnnotationLastRequestFrom = "napwake.dev/last-request-from"

	// AnnotationTurnedAway is where the doorman counts the requests it
	// turned away from a paused workload whose wake.mode is Ask, as a JSON
	// TurnedAway. The operator copies it to status.turnedAway. Don't set
	// it.
	AnnotationTurnedAway = "napwake.dev/turned-away"

	// AnnotationPauseRequested asks Napwake to pause a ManagedWorkload's
	// workload now rather than when its idle clock runs out. The value is a
	// token, such as the time of the request, that only needs to differ from
	// the last request's: Napwake acts on each value once, recording it in
	// status.lastPauseRequest. kubectl napwake pause sets it.
	AnnotationPauseRequested = "napwake.dev/pause-requested"

	// AnnotationPauseOverridesForecast, set to "true" with a pause request,
	// pauses the workload even when the forecast expects demand within the
	// hour. Without it, such a request is declined, saying when the demand
	// is expected, so the person asking can decide.
	AnnotationPauseOverridesForecast = "napwake.dev/pause-overrides-forecast"

	// AnnotationWakeRequested asks Napwake to wake a ManagedWorkload's
	// workload now, as a person's tooling does for them: a pull request
	// bot, a portal or a chat command. It wakes a paused workload whatever
	// the wake setting, and restarts an awake one's idle clock. The value is
	// a token, such as the time of the request, that only needs to differ
	// from the last request's: Napwake acts on each value once, recording
	// it in status.lastWakeRequest. kubectl napwake wake sets it.
	AnnotationWakeRequested = "napwake.dev/wake-requested"

	// Settings for a workload opted in with LabelManaged, set on the
	// workload or its namespace; the workload's own wins.
	AnnotationDryRun       = "napwake.dev/dry-run"
	AnnotationIdleAfter    = "napwake.dev/idle-after"
	AnnotationCPUThreshold = "napwake.dev/cpu-threshold"
	AnnotationAutoResume   = "napwake.dev/auto-resume"
	AnnotationPauseEarly   = "napwake.dev/pause-early"
	AnnotationForecast     = "napwake.dev/forecast"
	AnnotationDependsOn    = "napwake.dev/depends-on"
	AnnotationWakeMaxWait  = "napwake.dev/wake-max-wait"
	AnnotationWakePage     = "napwake.dev/wake-page"

	// AnnotationWake is what wakes a paused workload opted in with
	// LabelManaged through the doorman, as wake.mode: "request" or "ask".
	// Any other value is read as "ask", so a typo never lets crawlers wake
	// a workload meant to wake only for people.
	AnnotationWake = "napwake.dev/wake"

	// AnnotationPausedFrom is set by Napwake on a Deployment or
	// StatefulSet it has paused, before scaling it to zero, and removed when
	// it hands it back: a JSON copy of what the pause changed (the replicas,
	// and the KEDA ScaledObject with its own paused-replicas value), so the
	// pause is undone exactly even if the ManagedWorkload's status is lost,
	// as to a backup restored without it. Don't set or edit it.
	AnnotationPausedFrom = "napwake.dev/paused-from"

	// AnnotationIgnoreDependencies lists dependencies Napwake shouldn't
	// learn for a workload, comma-separated, as namespace/name or a name in
	// its own namespace. Set on the workload or its ManagedWorkload.
	AnnotationIgnoreDependencies = "napwake.dev/ignore-dependencies"

	// AnnotationDependentsFrom, on a namespace, lists the other namespaces,
	// comma-separated, whose workloads may depend on its workloads, or "*"
	// for any. A dependency in another namespace, declared or learned, is
	// only held awake and woken for its dependents when its namespace lists
	// theirs: anyone who can write a ManagedWorkload in one namespace could
	// otherwise keep another namespace's workloads awake.
	AnnotationDependentsFrom = "napwake.dev/dependents-from"

	// FinalizerCleanup is the finalizer added to ManagedWorkloads, so deleting
	// one scales its paused workload back up first.
	FinalizerCleanup = "napwake.dev/cleanup"

	// AnnotationOperator is the namespace of the Napwake install whose
	// operator added FinalizerCleanup to a ManagedWorkload. Another install
	// leaves that ManagedWorkload alone, and its uninstall and upgrade
	// guards don't count it.
	AnnotationOperator = "napwake.dev/operator"
)

// ParseWake reads a wake setting as napwake.dev/wake and the Helm default
// spell it, "request" or "ask".
func ParseWake(v string) (WakeMode, bool) {
	switch v {
	case "request":
		return WakeOnRequest, true
	case "ask":
		return WakeOnAsk, true
	default:
		return "", false
	}
}

// SystemNamespacePatterns name the namespaces Kubernetes itself runs in,
// such as kube-system, and those OpenShift, Rancher and GKE run their own
// components in, such as openshift-dns, cattle-system and gmp-system. They
// are always protected: pausing what runs there would take down the
// cluster's DNS, its link to its platform, or the metrics Napwake reads
// activity from. The scan leaves them out by the same list.
var SystemNamespacePatterns = []string{"kube-*", "openshift-*", "cattle-*", "gke-*", "gmp-*"}

// Protected says Napwake must not manage the namespace: it's a system
// namespace, it's labelled protected, or its name matches one of patterns
// (shell globs, such as prod-*), and it isn't labelled to allow it anyway.
func Protected(name string, labels map[string]string, patterns []string) bool {
	if labels[LabelAllowProtected] == True {
		return false
	}
	return labels[LabelProtected] == True || matchesAny(name, SystemNamespacePatterns) || matchesAny(name, patterns)
}

// matchesAny says name matches one of patterns. A pattern that isn't a
// valid glob matches nothing.
func matchesAny(name string, patterns []string) bool {
	for _, pattern := range patterns {
		if ok, err := path.Match(pattern, name); err == nil && ok {
			return true
		}
	}
	return false
}
