# Metrics Reference

All metrics are prefixed with `napwake_` and registered with the controller-runtime metrics registry. They are served on the metrics endpoint, which the chart and the Kustomize manifests serve on `:8443` over HTTPS; a scraper needs a token allowed to `get` `/metrics` (see [Monitoring](../operations/monitoring.md#prometheus)). The operator drops a workload's series when its ManagedWorkload is deleted; the doorman drops its `napwake_doorman_wakes_total` series then too, on its next sync of routes, not when the workload merely stops being routed.

## Tier 1: Cluster Health

These metrics show whether the operator is healthy and what phase each workload is in.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `napwake_build_info` | Gauge | `version` | Always 1, labelled with the release the pod runs, such as `v0.2.0`. Served by the doorman too |
| `napwake_workload_phase` | Gauge | `namespace`, `workload`, `phase` | 1 for each workload's current lifecycle phase |
| `napwake_activity_unreadable` | Gauge | `namespace`, `workload`, `source` | 1 while Napwake can't read one of the workload's activity sources: `cpu` from metrics-server while it's awake (`MetricsAvailable` is `False`), or `prometheus`, its activity queries (`PrometheusAvailable` is `False`). No series otherwise |
| `napwake_resume_incomplete` | Gauge | `namespace`, `workload` | 1 while the workload's last resume finished with replicas not Ready (`ResumeIncomplete` is `True`). No series otherwise |
| `napwake_reconcile_errors_total` | Counter | `controller` | Reconciles that returned an error, by `controller`: `managedworkload`, or `optin` for the controller that creates ManagedWorkloads from the `napwake.dev/managed` label |
| `napwake_lifecycle_transitions_total` | Counter | `from`, `to` | Phase transitions |
| `napwake_lifecycle_action_duration_seconds` | Histogram | `action` | Duration of lifecycle actions (pause, resume) |

## Tier 2: Operational Insight

These metrics help you understand what the operator is doing and why.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `napwake_prediction_confidence_percent` | Gauge | `season`, `namespace`, `workload` | Forecast confidence, 1 − WAPE, as a percentage, for `season` `daily` or `weekly` |
| `napwake_prediction_phase` | Gauge | `namespace`, `workload` | Engine phase (0-4) |
| `napwake_prediction_data_points` | Gauge | `namespace`, `workload` | Data points collected |
| `napwake_prediction_anomalies_total` | Counter | `namespace`, `workload` | Anomalies detected |
| `napwake_idle_detections_total` | Counter | `namespace`, `workload` | Times each workload's idle clock ran out |
| `napwake_wrong_pauses_total` | Counter | `namespace`, `workload` | Pauses made while the workload was in use: undone soon after by a request, a click, a wake request or an activity annotation. See [Paused while in use](../concepts/idle-detection.md#paused-while-in-use) |
| `napwake_external_scale_ups_total` | Counter | `namespace`, `workload`, `by` | Times each paused workload was scaled up outside Napwake, by the GitOps tool that did it (`argo-cd`, `flux`, `fleet`, `kapp`, `kluctl`), or `other` |

## Tier 3: Debugging

These metrics help troubleshoot specific workload behavior.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `napwake_idle_seconds` | Gauge | `namespace`, `workload` | Seconds since the workload's last activity. It keeps growing while the workload is paused, updated each time Napwake checks it: every minute awake, every few minutes paused |
| `napwake_prediction_regime_changes_total` | Counter | `namespace`, `workload` | Regime changes detected |
| `napwake_dryrun_actions_total` | Counter | `action` | Actions that would have been taken in dry-run; `action` is `idle_pause`, `early_pause` for one the forecast brought forward (`pauseEarly`), or `requested_pause` for a pause asked for with `kubectl napwake pause` |
| `napwake_target_unavailable_total` | Counter | `namespace`, `workload` | Target workload not found |

## Savings

Each node pool's figures from the operator's [measure of what pausing saved](../concepts/cost-tracking.md#how-saved-is-measured), every 5 minutes, by the leader. `pool` is the first of a node's `karpenter.sh/nodepool`, `cloud.google.com/gke-nodepool`, `eks.amazonaws.com/nodegroup` and `kubernetes.azure.com/agentpool` labels, `""` for nodes with none, and `*` for paused replicas whose pool wasn't recorded, which are set against what every pool has left over. There's a series for each pool with Ready nodes or paused replicas, so they number as the cluster's pools do. An operator limited by `watchNamespaces` can't measure them, and doesn't export them.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `napwake_capacity_paused_cpu_cores` | Gauge | `pool` | vCPUs the replicas Napwake holds paused requested, by the pool they ran in |
| `napwake_capacity_paused_memory_bytes` | Gauge | `pool` | Memory the replicas Napwake holds paused requested |
| `napwake_capacity_spare_cpu_cores` | Gauge | `pool` | Allocatable vCPUs on the pool's schedulable, Ready nodes that no bound, unfinished pod requests |
| `napwake_capacity_spare_memory_bytes` | Gauge | `pool` | Allocatable memory on those nodes that no pod requests |
| `napwake_savings_realised_dollars_per_hour` | Gauge | `pool` | What the paused requests the spare capacity can't hold would cost an hour at the pool's nodes' list rates, averaged over them (the defaults for nodes with no list price): what pausing takes off the bill each hour, at least |
| `napwake_node_autoscaler` | Gauge | `kind` | 1 for each node autoscaler the operator found (`Karpenter`, `Cluster Autoscaler`, `EKS Auto Mode`, `GKE Autopilot`), or for `none` when it read everywhere it looks and found none, or `unknown` when it wasn't allowed to look, as in a `watchNamespaces` install. Looked at every 10 minutes. With `none`, what pausing frees doesn't come off the bill; see the [Cluster Autoscaler Guide](../guides/cluster-autoscaler.md#what-napwake-detects) |

## Doorman

Served by the doorman pods, on the same port and path as the operator's metrics.

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `napwake_doorman_wakes_total` | Counter | `namespace`, `workload`, `result` | Connections the doorman accepted for a paused workload, by `result` (below) |
| `napwake_doorman_wait_seconds` | Histogram | `result` | How long the doorman held a connection before passing it through or closing it |
| `napwake_doorman_held_connections` | Gauge | | Connections held right now, waiting for their workload to wake |
| `napwake_doorman_proxied_connections` | Gauge | | Connections passed to a woken workload and still being carried |
| `napwake_doorman_port_conflicts` | Gauge | | Doorman ports claimed by more than one route, which the doorman refuses to serve. Should be 0 |
| `napwake_doorman_listen_failures` | Gauge | | Routed doorman ports this pod can't listen on, which refuse every connection. Should be 0; a pod that can't listen on one for 30 seconds reports itself not Ready |

| `result` | Meaning |
|----------|---------|
| `success` | Passed to a Ready pod |
| `page` | A browser was shown the waking-up page |
| `timeout` | No pod was Ready within `wake.maxWait` |
| `error` | The woken pods couldn't be reached, or the wake couldn't be recorded, by `wake.maxWait` |
| `canceled` | The caller left, or the doorman shut down, while the connection was held |
| `ignored` | A health check or scraper, recognised by its `User-Agent` and answered 503, or Google's load balancer health check, answered 200 (see [Health checks the doorman can't recognise](../concepts/wake-on-request.md#health-checks-the-doorman-cant-recognise)); a request for a hidden file such as `/.env`, answered 404; or a connection that closed without sending anything, as each forwarder pod's check that it reaches the doorman does every 5 seconds |
| `limited` | Refused by a cap on held connections or a wake rate limit; see [Limits](../concepts/wake-on-request.md#limits) |
| `refused` | The workload's route is draining and none of its pods is Ready |
| `asked` | With `wake.mode: Ask`, a person's browser was shown the page that asks before waking the workload. See [Asking before waking](../concepts/wake-on-request.md#asking-before-waking) |
| `turned_away` | With `wake.mode: Ask`, anything else, such as a crawler, a scanner or an API client, was told the workload is asleep, and nothing woke. Many mean something automated calls it; `status.turnedAway` counts them for each pause |

## Prediction Phase Values

The `napwake_prediction_phase` gauge uses numeric values:

| Value | Phase |
|-------|-------|
| 0 | Observing |
| 1 | DailySuggesting |
| 2 | DailyActive |
| 3 | WeeklySuggesting |
| 4 | FullyActive |

## Useful PromQL Queries

```promql
# Workloads with low prediction confidence
napwake_prediction_confidence_percent{season="daily"} < 70

# Workloads that will pause within 10 minutes (with the default 1h idleAfter)
napwake_idle_seconds > 3000 and on (namespace, workload) napwake_workload_phase{phase="Running"} == 1

# How long a request to a paused workload waits for it to wake (p95)
histogram_quantile(0.95, sum by (le) (rate(napwake_doorman_wait_seconds_bucket{result="success"}[1h])))

# What pausing takes off the bill an hour, across the cluster
sum(napwake_savings_realised_dollars_per_hour)

# Pools where paused CPU still fits in spare capacity: freed, but not saved
(napwake_capacity_paused_cpu_cores > 0) <= on (pool) napwake_capacity_spare_cpu_cores
```
