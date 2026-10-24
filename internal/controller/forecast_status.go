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

package controller

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
	"github.com/okedeji/hybernate/internal/forecast"
)

// forecastOutlook says why the workload's forecast is or isn't acting, and
// what it waits for, as status.prediction's reason and message.
func forecastOutlook(workload *v1alpha1.ManagedWorkload, o forecast.Outlook) (reason, message string) {
	var b strings.Builder
	switch {
	case o.Acting:
		reason = v1alpha1.ForecastActing
		b.WriteString("acting")
		if p := workload.Status.Prediction; p != nil && p.History != nil && p.History.Reason == HistoryLearned {
			fmt.Fprintf(&b, ", on %s of CPU history and what it has seen since", historySpan(p.History.Hours))
		}
		fmt.Fprintf(&b, ": its forecasts of the last day were %d%% right, and it acts from %d%%, "+
			"so it holds off pauses before hours it expects to be busy", o.Confidence, o.Threshold)
		if autoResumes(workload) {
			b.WriteString(" and wakes the workload 15 minutes ahead of them")
		} else {
			b.WriteString("; turn on autoResume to have it wake the workload ahead of them too")
		}
		return reason, b.String()
	case pausedUnseen(workload) != "":
		return v1alpha1.ForecastNotLearning, "not learning while paused: " + pausedUnseen(workload)
	case o.Waiting == forecast.WaitingToRelearn:
		reason = v1alpha1.ForecastRelearning
		fmt.Fprintf(&b, "relearning: demand stopped matching what it had learned, so it's earning its confidence "+
			"again; its forecasts of the last day were %d%% right, and it acts from %d%%", o.Confidence, o.Threshold)
	case o.Waiting == forecast.WaitingForDay && o.HoursOfDaySeen < forecast.DailySeason:
		reason = v1alpha1.ForecastObserving
		fmt.Fprintf(&b, "learning: it has seen %d of the 24 hours of the day, and can't act until it has seen "+
			"them all", o.HoursOfDaySeen)
	case o.Waiting == forecast.WaitingForDay:
		reason = v1alpha1.ForecastObserving
		fmt.Fprintf(&b, "learning: it has seen %d of the 168 hours of the week, and scores its forecast of an hour "+
			"from the second time it sees it", o.HoursOfWeekSeen)
	default:
		reason = v1alpha1.ForecastEarningConfidence
		fmt.Fprintf(&b, "learning: its forecasts of the last day were %d%% right, and it acts from %d%%",
			o.Confidence, o.Threshold)
		if !o.WeekSeen {
			b.WriteString("; it hasn't seen a whole week yet")
		}
	}
	switch {
	case !o.StartsActing.IsZero():
		fmt.Fprintf(&b, "; expected to start acting around %s, if the coming days repeat the last week",
			o.StartsActing.UTC().Format("Mon 2 Jan 15:04 UTC"))
	case reason != v1alpha1.ForecastObserving:
		b.WriteString("; not expected to start within four weeks if the coming days repeat the last week")
	}
	if seen := pausedSeenBy(workload); seen != "" {
		fmt.Fprintf(&b, "; while it's paused, it learns from %s", seen)
	}
	return reason, b.String()
}

func autoResumes(workload *v1alpha1.ManagedWorkload) bool {
	return workload.Spec.IdlePolicy != nil && workload.Spec.IdlePolicy.AutoResume
}

const (
	wakeOnRequestGuide = "https://okedeji.io/hybernate/concepts/wake-on-request/"
	queriesGuide       = "https://okedeji.io/hybernate/guides/prometheus-signals/"
)

// pausedSeenBy is what shows a paused workload's demand, so the hours it
// spends paused are learned: requests through the doorman, which would
// wake it, people, who alone wake it with wake.mode Ask, through the
// doorman's asleep page, and Prometheus activity queries, which show the
// activity they measure. It's empty for a workload that isn't paused, or
// that nothing watches. Queries that can't be evaluated show nothing, but
// don't blind the doorman, unless it asks: then it sees only people.
func pausedSeenBy(workload *v1alpha1.ManagedWorkload) string {
	if workload.Status.Phase != v1alpha1.PhasePaused {
		return ""
	}
	conditions := workload.Status.Conditions
	queries := hasPrometheusActivity(workload)
	failing := queries && meta.IsStatusConditionFalse(conditions, conditionPrometheusAvailable)
	routed := meta.IsStatusConditionTrue(conditions, conditionWakeOnRequest)
	if workload.Spec.Wake.Asks() {
		const people = "people waking it (a click on its asleep page, a wake request or an activity annotation)"
		switch {
		case failing:
			return ""
		case routed && queries:
			return people + " and its Prometheus activity queries"
		case routed:
			return people
		case queries:
			return "its Prometheus activity queries"
		}
		return ""
	}
	switch {
	case routed && failing:
		return "requests through the doorman, its Prometheus activity queries failing"
	case routed && queries:
		return "requests through the doorman and its Prometheus activity queries"
	case routed:
		return "requests through the doorman"
	case queries && !failing:
		return "its Prometheus activity queries"
	}
	return ""
}

// pausedUnseen says why the forecast can't see a paused workload's demand,
// so the hours it spends paused aren't learned, then what fixes it and
// where the docs say how; it's empty when something watches the workload,
// or it isn't paused. Without activity queries, the cause is why requests
// don't wake it, by its WakeOnRequest condition's reason. The cause comes
// first, up to the first semicolon, for kubectl hybernate's tables.
func pausedUnseen(workload *v1alpha1.ManagedWorkload) string {
	if workload.Status.Phase != v1alpha1.PhasePaused || pausedSeenBy(workload) != "" {
		return ""
	}
	if hasPrometheusActivity(workload) {
		return "its Prometheus activity queries can't be evaluated; they can't show its demand until Prometheus " +
			"answers, and PrometheusAvailable says why: " + queriesGuide + "#when-a-query-cant-be-evaluated"
	}
	var reason string
	if c := meta.FindStatusCondition(workload.Status.Conditions, conditionWakeOnRequest); c != nil {
		reason = c.Reason
	}
	why, fix, section := "requests don't wake it", "see why in its WakeOnRequest condition",
		"#when-a-workload-isnt-routed"
	switch reason {
	case "NoServices":
		why, fix = "no Service routes requests to it", "give it a Service, or, for a workload no request "+
			"reaches, such as a queue worker, a Prometheus activity query that sees its demand"
	case "DoormanDisabled":
		why, fix, section = "the doorman is turned off for the cluster", "set the Helm value doorman.enabled to true",
			"#turning-it-off"
	case "UnsupportedLoadBalancer":
		why, fix, section = "its Services are behind GKE container-native load balancing (NEGs), which the "+
			"doorman can't route without its forwarder", "set the Helm value doorman.negForwarder.enabled to true",
			"#gke-container-native-load-balancing-negs"
	case reasonForwarderNotReady:
		why, fix, section = "the forwarder its GKE NEG Services are routed through isn't Ready",
			"see why in its WakeOnRequest condition", "#gke-container-native-load-balancing-negs"
	case "ServedByOtherPods":
		why, fix, section = "its Services are served by other workloads' Ready pods",
			"give it a Service of its own", "#services-shared-with-other-workloads"
	case "NodeLocalTraffic":
		why, fix = "its Services keep traffic on the caller's node, with internalTrafficPolicy: Local",
			"give it a Service with the default policy, Cluster"
	case "UnsupportedIPFamily":
		why = "no doorman pod has an address in its Services' IP family"
	case "DoormanUnavailable":
		why, fix = "no doorman pod is Ready", "check the doorman's pods in the operator's namespace"
	case "NoFreePort", "TooManyPorts":
		why = "the doorman has no port for it"
	case reasonRoutingFailed:
		why = "routing its Services through the doorman is failing, and being retried"
	}
	unseen := "a request can't wake it, so its demand can't be seen"
	if workload.Spec.Wake.Asks() {
		unseen = "nobody can click Wake it on its asleep page, so an hour nobody woke it in can't be told from one " +
			"they couldn't"
	}
	return fmt.Sprintf("%s; %s, and it learns only the hours it's awake. To learn the hours it's paused too, "+
		"%s: %s%s", why, unseen, fix, wakeOnRequestGuide, section)
}
