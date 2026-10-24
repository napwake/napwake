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

package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// --- Tier 1: Cluster Health ---

var (
	// BuildInfo is 1, labelled with the running binary's release, so a
	// dashboard can tell which version each operator and doorman pod runs.
	BuildInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "napwake_build_info",
		Help: "Always 1, labelled with the version of the running operator or doorman.",
	}, []string{"version"})

	// WorkloadPhase is 1 for each workload's current phase, in the style of
	// kube-state-metrics, so alerts can see which workload is in which phase
	// and for how long.
	WorkloadPhase = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "napwake_workload_phase",
		Help: "Current lifecycle phase of each managed workload (1 for the current phase).",
	}, []string{"namespace", "workload", "phase"})

	ReconcileErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "napwake_reconcile_errors_total",
		Help: "Total reconciliation errors by controller.",
	}, []string{"controller"})

	LifecycleTransitions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "napwake_lifecycle_transitions_total",
		Help: "Total lifecycle phase transitions.",
	}, []string{"from", "to"})

	LifecycleActionDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "napwake_lifecycle_action_duration_seconds",
		Help:    "Duration of lifecycle actions (pause, resume).",
		Buckets: prometheus.ExponentialBuckets(0.1, 2, 10),
	}, []string{"action"})
)

// --- Tier 2: Operational Insight ---

var (
	PredictionConfidence = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "napwake_prediction_confidence_percent",
		Help: "Prediction engine confidence percentage by season.",
	}, []string{"season", "namespace", "workload"})

	PredictionPhase = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "napwake_prediction_phase",
		Help: "Prediction engine phase (0=Observing, 1=DailySuggesting, 2=DailyActive, 3=WeeklySuggesting, 4=FullyActive).",
	}, []string{"namespace", "workload"})

	PredictionDataPoints = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "napwake_prediction_data_points",
		Help: "Total data points collected by the prediction engine.",
	}, []string{"namespace", "workload"})

	PredictionAnomalies = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "napwake_prediction_anomalies_total",
		Help: "Total anomalies detected by the prediction engine.",
	}, []string{"namespace", "workload"})

	IdleDetections = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "napwake_idle_detections_total",
		Help: "Times each workload's idle clock ran out.",
	}, []string{"namespace", "workload"})

	ExternalScaleUps = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "napwake_external_scale_ups_total",
		Help: "Paused workloads scaled up outside Napwake, by what did it: argo-cd, flux, or other.",
	}, []string{"by"})
)

// --- Tier 3: Debugging ---

var (
	IdleSeconds = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "napwake_idle_seconds",
		Help: "Seconds since the workload's last activity from any source.",
	}, []string{"namespace", "workload"})

	PredictionRegimeChanges = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "napwake_prediction_regime_changes_total",
		Help: "Total regime changes detected by the prediction engine.",
	}, []string{"namespace", "workload"})

	DryrunActions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "napwake_dryrun_actions_total",
		Help: "Total actions that would have been taken in dry-run mode.",
	}, []string{"action"})

	TargetUnavailable = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "napwake_target_unavailable_total",
		Help: "Total times the target workload was not found.",
	}, []string{"namespace", "workload"})
)

// --- Savings ---

// These are labelled by node pool, the first of a node's
// karpenter.sh/nodepool, cloud.google.com/gke-nodepool,
// eks.amazonaws.com/nodegroup and kubernetes.azure.com/agentpool labels, ""
// for nodes with none, and "*" for paused replicas whose pool wasn't
// recorded, which are set against what every pool has left over.
var (
	CapacityPausedCPUCores = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "napwake_capacity_paused_cpu_cores",
		Help: "vCPUs the replicas Napwake holds paused requested, by the node pool they ran in.",
	}, []string{"pool"})

	CapacityPausedMemoryBytes = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "napwake_capacity_paused_memory_bytes",
		Help: "Memory the replicas Napwake holds paused requested, by the node pool they ran in.",
	}, []string{"pool"})

	CapacitySpareCPUCores = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "napwake_capacity_spare_cpu_cores",
		Help: "Allocatable vCPUs no pod requests on each node pool's schedulable, Ready nodes.",
	}, []string{"pool"})

	CapacitySpareMemoryBytes = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "napwake_capacity_spare_memory_bytes",
		Help: "Allocatable memory no pod requests on each node pool's schedulable, Ready nodes.",
	}, []string{"pool"})

	SavingsRealisedDollarsPerHour = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "napwake_savings_realised_dollars_per_hour",
		Help: "What the paused replicas a node pool's spare capacity can't hold would cost, at its nodes' " +
			"list rates: what pausing takes off the bill each hour, at least.",
	}, []string{"pool"})
)

// NodeAutoscaler is 1 for each node autoscaler the operator found, by kind:
// "none" when it could read everywhere it looks and found none, "unknown"
// when it couldn't. Freed capacity reaches the bill only as nodes scale down.
var NodeAutoscaler = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Name: "napwake_node_autoscaler",
	Help: "1 for each node autoscaler found, by kind; none or unknown when there's none or it couldn't tell.",
}, []string{"kind"})

// perPool are the vectors labelled by node pool.
var perPool = []*prometheus.GaugeVec{
	CapacityPausedCPUCores,
	CapacityPausedMemoryBytes,
	CapacitySpareCPUCores,
	CapacitySpareMemoryBytes,
	SavingsRealisedDollarsPerHour,
}

// DeletePool drops a node pool's series, once it has no Ready nodes and
// nothing paused.
func DeletePool(pool string) {
	for _, v := range perPool {
		v.DeleteLabelValues(pool)
	}
}

// --- Doorman ---

var (
	// DoormanWakes counts connections the doorman accepted for a paused
	// workload, by how each ended: success (passed to a Ready pod), page (a
	// browser got the waking-up page), timeout (no Ready pod within maxWait),
	// error (Ready pods couldn't be reached, or the wake couldn't be
	// stamped), canceled (the caller left, or the doorman shut down, while
	// it was held), ignored (a health check, a scrape, or a connection that
	// sent nothing), limited (refused by a cap on held connections or
	// wakes), refused (the route is draining and nothing is Ready), and, for
	// a workload whose wake.mode is Ask, asked (a person's browser got the
	// asleep page) and turned_away (anything else was told it's asleep, and
	// nothing woke).
	DoormanWakes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "napwake_doorman_wakes_total",
		Help: "Connections the doorman accepted for a paused workload, by result " +
			"(success, page, timeout, error, canceled, ignored, limited, refused, asked, turned_away).",
	}, []string{"namespace", "workload", "result"})

	DoormanWaitSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "napwake_doorman_wait_seconds",
		Help:    "How long the doorman held a connection before passing it through or closing it.",
		Buckets: prometheus.ExponentialBuckets(0.25, 2, 10),
	}, []string{"result"})

	DoormanHeldConnections = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "napwake_doorman_held_connections",
		Help: "Connections the doorman is holding while their workload wakes.",
	})

	DoormanProxiedConnections = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "napwake_doorman_proxied_connections",
		Help: "Connections the doorman has passed to a woken workload and is still carrying.",
	})

	DoormanPortConflicts = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "napwake_doorman_port_conflicts",
		Help: "Doorman ports claimed by more than one route, which the doorman refuses to serve.",
	})

	DoormanListenFailures = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "napwake_doorman_listen_failures",
		Help: "Routed doorman ports this doorman pod can't listen on, which refuse every connection.",
	})
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		// Tier 1
		BuildInfo,
		WorkloadPhase,
		ReconcileErrors,
		LifecycleTransitions,
		LifecycleActionDuration,

		// Tier 2
		PredictionConfidence,
		PredictionPhase,
		PredictionDataPoints,
		PredictionAnomalies,
		IdleDetections,
		ExternalScaleUps,

		// Tier 3
		IdleSeconds,
		PredictionRegimeChanges,
		DryrunActions,
		TargetUnavailable,

		// Savings
		CapacityPausedCPUCores,
		CapacityPausedMemoryBytes,
		CapacitySpareCPUCores,
		CapacitySpareMemoryBytes,
		SavingsRealisedDollarsPerHour,
		NodeAutoscaler,

		// Doorman
		DoormanWakes,
		DoormanWaitSeconds,
		DoormanHeldConnections,
		DoormanProxiedConnections,
		DoormanPortConflicts,
		DoormanListenFailures,
	)
}

// perWorkload are the vectors with series for each ManagedWorkload, labelled
// by its namespace and name.
var perWorkload = []interface {
	DeletePartialMatch(labels prometheus.Labels) int
}{
	WorkloadPhase,
	PredictionConfidence,
	PredictionPhase,
	PredictionDataPoints,
	PredictionAnomalies,
	IdleDetections,
	IdleSeconds,
	PredictionRegimeChanges,
	TargetUnavailable,
	DoormanWakes,
}

// DeleteWorkload drops every series of a deleted ManagedWorkload, so its
// gauges stop reporting it and one recreated under the same name starts
// afresh.
func DeleteWorkload(namespace, workload string) {
	labels := prometheus.Labels{"namespace": namespace, "workload": workload}
	for _, v := range perWorkload {
		v.DeletePartialMatch(labels)
	}
}
