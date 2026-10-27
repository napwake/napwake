# Metrics reference

> Every metric the operator and the doorman serve, with its type, labels and meaning.

Source: https://docs.napwake.dev/next/reference/metrics/

How the metrics are served.

| What           | How                                                                                                                                                                                                                       |
| -------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Names          | All start `napwake_`, registered with the controller-runtime metrics registry                                                                                                                                             |
| Endpoint       | `:8443`, over HTTPS, as the chart and the Kustomize manifests serve it                                                                                                                                                    |
| Scraping       | Needs a token allowed to `get` `/metrics` ([Monitoring](https://docs.napwake.dev/next/operations/monitoring/#prometheus))                                                                                                                         |
| Series dropped | The operator’s, when the workload’s ManagedWorkload is deleted. The doorman drops its `napwake_doorman_wakes_total` series then too, at its next sync of routes. It keeps them while a workload merely stops being routed |

## Health

Whether the operator is healthy, and each workload’s phase.

| Metric                                      | Type      | Labels                            | Description                                                                                                                                                                                                                                                                                                                                        |
| ------------------------------------------- | --------- | --------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `napwake_build_info`                        | Gauge     | `version`                         | Always 1, labelled with the release the pod runs, such as `v0.2.0`. The doorman serves it too                                                                                                                                                                                                                                                      |
| `napwake_workload_phase`                    | Gauge     | `namespace`, `workload`, `phase`  | 1 for each workload’s current lifecycle phase                                                                                                                                                                                                                                                                                                      |
| `napwake_activity_unreadable`               | Gauge     | `namespace`, `workload`, `source` | 1 while Napwake can’t read one of the workload’s activity sources. `source` is `cpu`, from metrics-server while it’s awake (`MetricsAvailable` is `False`), or `prometheus`, its activity queries (`PrometheusAvailable` is `False`). No series otherwise, nor for a workload Napwake doesn’t read, as ignored, protected, or whose target is gone |
| `napwake_resume_incomplete`                 | Gauge     | `namespace`, `workload`           | 1 while the workload runs after a resume that finished with replicas not Ready (`ResumeIncomplete` is `True`). A pause clears it. No series otherwise                                                                                                                                                                                              |
| `napwake_reconcile_errors_total`            | Counter   | `controller`                      | Reconciles that returned an error, by `controller`, either `managedworkload`, or `optin`, which creates ManagedWorkloads from the `napwake.dev/managed` label                                                                                                                                                                                      |
| `napwake_lifecycle_transitions_total`       | Counter   | `from`, `to`                      | Phase transitions                                                                                                                                                                                                                                                                                                                                  |
| `napwake_lifecycle_action_duration_seconds` | Histogram | `action`                          | Duration of lifecycle actions (pause, resume)                                                                                                                                                                                                                                                                                                      |

## What the operator does

| Metric                                  | Type    | Labels                            | Description                                                                                                                                                                                               |
| --------------------------------------- | ------- | --------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `napwake_prediction_confidence_percent` | Gauge   | `season`, `namespace`, `workload` | Forecast confidence, 1 − WAPE, as a percentage, for `season` `daily` or `weekly`                                                                                                                          |
| `napwake_prediction_phase`              | Gauge   | `namespace`, `workload`           | Forecast phase, 0 to 4 ([values](https://docs.napwake.dev/next/reference/metrics/#forecast-phases))                                                                                                                                                       |
| `napwake_prediction_data_points`        | Gauge   | `namespace`, `workload`           | Data points collected                                                                                                                                                                                     |
| `napwake_prediction_anomalies_total`    | Counter | `namespace`, `workload`           | Anomalies detected                                                                                                                                                                                        |
| `napwake_idle_detections_total`         | Counter | `namespace`, `workload`           | Times each workload’s idle clock ran out                                                                                                                                                                  |
| `napwake_wrong_pauses_total`            | Counter | `namespace`, `workload`           | Pauses made while the workload was in use. A request, a click, a wake request or an activity annotation undid each soon after ([Paused while in use](https://docs.napwake.dev/next/concepts/idle-detection/#paused-while-in-use)) |
| `napwake_external_scale_ups_total`      | Counter | `namespace`, `workload`, `by`     | Times each paused workload was scaled up outside Napwake. `by` is the GitOps tool that did it (`argo-cd`, `flux`, `fleet`, `kapp`, `kluctl`), or `other`                                                  |

## Debugging

| Metric                                    | Type    | Labels                  | Description                                                                                                                                                                     |
| ----------------------------------------- | ------- | ----------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `napwake_idle_seconds`                    | Gauge   | `namespace`, `workload` | Seconds since the last activity. Keeps growing while paused, updated at each check, every minute awake and every few minutes paused                                             |
| `napwake_prediction_regime_changes_total` | Counter | `namespace`, `workload` | Regime changes detected                                                                                                                                                         |
| `napwake_dryrun_actions_total`            | Counter | `action`                | Actions dry-run would have taken. `action` is `idle_pause`, `early_pause` (one the forecast brought forward, `pauseEarly`), or `requested_pause` (from `kubectl napwake pause`) |
| `napwake_target_unavailable_total`        | Counter | `namespace`, `workload` | Target workload not found                                                                                                                                                       |

## Savings

Each node pool’s figures from the operator’s [measure of what pausing saved](https://docs.napwake.dev/next/concepts/cost-tracking/#how-saved-is-measured), every 5 minutes, by the leader. Not exported under `watchNamespaces`, which can’t measure them.

| Metric                                      | Type  | Labels | Description                                                                                                                                                                                                                                                                                                                                                                                    |
| ------------------------------------------- | ----- | ------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `napwake_capacity_paused_cpu_cores`         | Gauge | `pool` | vCPUs the replicas Napwake holds paused requested, by the pool they ran in                                                                                                                                                                                                                                                                                                                     |
| `napwake_capacity_paused_memory_bytes`      | Gauge | `pool` | Memory the replicas Napwake holds paused requested                                                                                                                                                                                                                                                                                                                                             |
| `napwake_capacity_spare_cpu_cores`          | Gauge | `pool` | Allocatable vCPUs on the pool’s schedulable, Ready nodes that no bound, unfinished pod requests                                                                                                                                                                                                                                                                                                |
| `napwake_capacity_spare_memory_bytes`       | Gauge | `pool` | Allocatable memory on those nodes that no pod requests                                                                                                                                                                                                                                                                                                                                         |
| `napwake_savings_realised_dollars_per_hour` | Gauge | `pool` | What the paused requests the spare capacity can’t hold would cost an hour. Priced at the pool’s nodes’ list rates, averaged over them, or the defaults for nodes with no list price. That’s what pausing takes off the bill each hour, at least                                                                                                                                                |
| `napwake_node_autoscaler`                   | Gauge | `kind` | 1 for each node autoscaler found, of `Karpenter`, `Cluster Autoscaler`, `EKS Auto Mode` and `GKE Autopilot`. Or `none`, when it read everywhere it looks and found none, so what pausing frees [doesn’t come off the bill](https://docs.napwake.dev/next/guides/cluster-autoscaler/#what-napwake-detects). Or `unknown`, when it wasn’t allowed to look, as in a `watchNamespaces` install. Looked at every 10 minutes |

One series for each pool with Ready nodes or paused replicas, by `pool`:

| `pool`                                                                                                                                                 | For                                                                                   |
| ------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------- |
| The node’s first label of `karpenter.sh/nodepool`, `cloud.google.com/gke-nodepool`, `eks.amazonaws.com/nodegroup` and `kubernetes.azure.com/agentpool` | Nodes with one of those labels                                                        |
| `""`                                                                                                                                                   | Nodes with none                                                                       |
| `*`                                                                                                                                                    | Paused replicas whose pool wasn’t recorded, set against what every pool has left over |

## Doorman

Served by the doorman pods, on the same port and path as the operator’s.

| Metric                                | Type      | Labels                            | Description                                                                                                                                                       |
| ------------------------------------- | --------- | --------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `napwake_doorman_wakes_total`         | Counter   | `namespace`, `workload`, `result` | Connections the doorman accepted for a paused workload, by `result` (below)                                                                                       |
| `napwake_doorman_wait_seconds`        | Histogram | `result`                          | How long the doorman held a connection before passing it through or closing it                                                                                    |
| `napwake_doorman_held_connections`    | Gauge     |                                   | Connections held right now, waiting for their workload to wake                                                                                                    |
| `napwake_doorman_proxied_connections` | Gauge     |                                   | Connections passed to a woken workload and still being carried                                                                                                    |
| `napwake_doorman_port_conflicts`      | Gauge     |                                   | Doorman ports claimed by more than one route, which the doorman refuses to serve. Should be 0                                                                     |
| `napwake_doorman_listen_failures`     | Gauge     |                                   | Routed doorman ports this pod can’t listen on, which refuse every connection. Should be 0. A pod that can’t listen on one for 30 seconds reports itself not Ready |

The values of `result`.

| `result`      | Meaning                                                                                                                                                                                                                                                                                                                                                                                                   |
| ------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `success`     | Passed to a Ready pod                                                                                                                                                                                                                                                                                                                                                                                     |
| `page`        | A browser was shown the waking-up page                                                                                                                                                                                                                                                                                                                                                                    |
| `asked`       | With `wake.mode: Ask`, a person’s browser was shown the page that asks before waking ([Asking before waking](https://docs.napwake.dev/next/concepts/wake-on-request/#asking-before-waking))                                                                                                                                                                                                                                       |
| `turned_away` | With `wake.mode: Ask`, anything else, such as a crawler, a scanner or an API client, was told it’s asleep. Nothing woke. Many mean something automated calls it. `status.turnedAway` counts them for each pause                                                                                                                                                                                           |
| `timeout`     | No pod was Ready within `wake.maxWait`                                                                                                                                                                                                                                                                                                                                                                    |
| `error`       | The woken pods couldn’t be reached, or the wake couldn’t be recorded, by `wake.maxWait`                                                                                                                                                                                                                                                                                                                   |
| `canceled`    | The caller left, or the doorman shut down, while the connection was held                                                                                                                                                                                                                                                                                                                                  |
| `ignored`     | A health check or scraper known by its `User-Agent`, answered `503`, or [Google’s load balancer health check](https://docs.napwake.dev/next/concepts/wake-on-request/#health-checks-the-doorman-cant-recognise), answered `200`. A request for a hidden file such as `/.env`, answered `404`. A connection that closed without sending anything. Each forwarder pod’s check that it reaches the doorman does that every 5 seconds |
| `limited`     | Refused by a cap on held connections or a wake rate limit ([Limits](https://docs.napwake.dev/next/concepts/wake-on-request/#limits))                                                                                                                                                                                                                                                                                              |
| `refused`     | The workload’s route is draining and none of its pods is Ready                                                                                                                                                                                                                                                                                                                                            |

## Forecast phases

The values of `napwake_prediction_phase`.

| Value | Phase            |
| ----- | ---------------- |
| 0     | Observing        |
| 1     | DailySuggesting  |
| 2     | DailyActive      |
| 3     | WeeklySuggesting |
| 4     | FullyActive      |

## Queries

```promql
# Workloads with low forecast confidence
napwake_prediction_confidence_percent{season="daily"} < 70


# Workloads that will pause within 10 minutes (with the default 1h idleAfter)
napwake_idle_seconds > 3000 and on (namespace, workload) napwake_workload_phase{phase="Running"} == 1


# How long a request to a paused workload waits for it to wake (p95)
histogram_quantile(0.95, sum by (le) (rate(napwake_doorman_wait_seconds_bucket{result="success"}[1h])))


# What pausing takes off the bill an hour, across the cluster
sum(napwake_savings_realised_dollars_per_hour)


# Pools where paused CPU still fits in spare capacity, so it's freed but not saved
(napwake_capacity_paused_cpu_cores > 0) <= on (pool) napwake_capacity_spare_cpu_cores
```

Next

* [Monitoring →Scrape these metrics, and load the alerts.](https://docs.napwake.dev/next/operations/monitoring/)
* [How saved is measured →Where the savings figures come from.](https://docs.napwake.dev/next/concepts/cost-tracking/#how-saved-is-measured)
* [Conditions and events →What each workload reports.](https://docs.napwake.dev/next/reference/conditions-and-events/)
