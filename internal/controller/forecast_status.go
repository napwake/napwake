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
	case pausedUnseen(workload):
		return v1alpha1.ForecastNotLearning, "not learning: the workload is paused without wake on request, so a request " +
			"can't wake it and its demand can't be seen; the forecast learns while it's awake, and from every hour " +
			"with wake on request"
	case o.Waiting == forecast.WaitingToRelearn:
		reason = v1alpha1.ForecastRelearning
		fmt.Fprintf(&b, "relearning: demand stopped matching what it had learned, so it's earning its confidence "+
			"again; its forecasts of the last day were %d%% right, and it acts from %d%%", o.Confidence, o.Threshold)
	case o.Waiting == forecast.WaitingForDay:
		reason = v1alpha1.ForecastObserving
		fmt.Fprintf(&b, "learning: it has seen %d of the 24 hours of the day, and starts scoring its forecasts "+
			"once it has seen them all", o.HoursOfDaySeen)
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
	return reason, b.String()
}

func autoResumes(workload *v1alpha1.ManagedWorkload) bool {
	return workload.Spec.IdlePolicy != nil && workload.Spec.IdlePolicy.AutoResume
}

// pausedUnseen reports whether the workload is paused where a request
// wouldn't wake it, so the hours it spends paused aren't learned.
func pausedUnseen(workload *v1alpha1.ManagedWorkload) bool {
	return workload.Status.Phase == v1alpha1.PhasePaused &&
		!meta.IsStatusConditionTrue(workload.Status.Conditions, conditionWakeOnRequest)
}
