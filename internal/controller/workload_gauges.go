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
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/api/meta"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/metrics"
)

// recordConditions publishes the conditions alerts watch, as the status
// says them. A source counts as unreadable only where Napwake reads it:
// CPU while the workload is awake, since a paused one has none and keeps
// the condition from before, and Prometheus while it has queries, since
// removing them leaves the condition behind.
func recordConditions(workload *v1alpha1.ManagedWorkload) {
	ns, name := workload.Namespace, workload.Name
	conditions := workload.Status.Conditions
	awake := workload.Status.Phase == v1alpha1.PhaseRunning || workload.Status.Phase == v1alpha1.PhaseIdle

	setWhile(awake && meta.IsStatusConditionFalse(conditions, conditionMetricsAvailable),
		metrics.ActivityUnreadable, ns, name, "cpu")
	setWhile(hasPrometheusActivity(workload) && meta.IsStatusConditionFalse(conditions, conditionPrometheusAvailable),
		metrics.ActivityUnreadable, ns, name, "prometheus")
	setWhile(meta.IsStatusConditionTrue(conditions, conditionResumeIncomplete),
		metrics.ResumeIncomplete, ns, name)
}

// setWhile has the gauge's series at 1 while on, and drops it otherwise.
func setWhile(on bool, gauge *prometheus.GaugeVec, labels ...string) {
	if on {
		gauge.WithLabelValues(labels...).Set(1)
		return
	}
	gauge.DeleteLabelValues(labels...)
}
