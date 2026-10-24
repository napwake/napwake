# Monitoring

Napwake ships with Prometheus metrics for the operator and the doorman, and a set of alerting rules. See the [Metrics Reference](../reference/metrics.md) for every metric.

## Prometheus

### Scraping

Metrics are served over HTTPS, and only to a scraper whose token is allowed to `get` the `/metrics` URL.

**With Helm**, turn on the ServiceMonitors (one for the operator, one for the doorman) and bind your Prometheus's ServiceAccount to the chart's `<fullname>-metrics-reader` ClusterRole:

```yaml title="napwake-values.yaml"
metrics:
  serviceMonitor:
    enabled: true
  prometheusRule:
    enabled: true
  readerSubjects:
    - kind: ServiceAccount
      name: prometheus-k8s
      namespace: monitoring
```

**With `install.yaml` or the Kustomize manifests**, apply the ServiceMonitors and the alerting rules into `napwake-system`, from the `config/prometheus` base pinned to your release, or add it to your kustomization's `resources`:

```bash
kubectl apply -k "https://github.com/napwake/napwake//config/prometheus?ref=v0.2.0"
```

and bind your Prometheus's ServiceAccount to the `napwake-metrics-reader` ClusterRole yourself:

```bash
kubectl create clusterrolebinding napwake-metrics-reader \
  --clusterrole=napwake-metrics-reader --serviceaccount=monitoring:prometheus-k8s
```

Without that binding, the targets show as down with `401` or `403`, and the `NapwakeDown` and `NapwakeDoormanDown` alerts fire. With `metrics.secure: false` in the chart, metrics are plain HTTP and need no binding, but anyone who can reach the port can read them.

**Prometheus must select the ServiceMonitors.** A Prometheus picks up only the ServiceMonitors that match its `serviceMonitorSelector` (and `serviceMonitorNamespaceSelector`). The chart's carry no label a Prometheus selects by default, so targets that never appear usually mean the selector doesn't match. [kube-prometheus-stack](https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack) selects `release: <its release name>`, and its Prometheus runs as the `<its release name>-kube-prometheus-prometheus` ServiceAccount. For a stack installed as `kube-prometheus-stack` in `monitoring`:

```yaml title="napwake-values.yaml"
metrics:
  serviceMonitor:
    enabled: true
    additionalLabels:
      release: kube-prometheus-stack
  prometheusRule:
    enabled: true
    additionalLabels:
      release: kube-prometheus-stack
  readerSubjects:
    - kind: ServiceAccount
      name: kube-prometheus-stack-kube-prometheus-prometheus
      namespace: monitoring
```

Check what yours selects with `kubectl get prometheus -A -o jsonpath='{range .items[*]}{.metadata.name}: {.spec.serviceMonitorSelector}{"\n"}{end}'`; the same goes for `ruleSelector` and the PrometheusRule. Find its ServiceAccount with `kubectl get prometheus -A -o jsonpath='{.items[*].spec.serviceAccountName}'`.

The ServiceMonitors set `honorLabels: true`. The metrics' `namespace` label is the workload's namespace, and without it Prometheus would overwrite it with the scraped pod's (`napwake-system`), so alerts would name the wrong namespace and merge same-named workloads. With your own scrape config, set `honor_labels: true` for the same reason.

They authenticate with the Prometheus's own ServiceAccount token, through `bearerTokenFile`. The Prometheus Operator marks that field deprecated in favor of `authorization`, which only takes a token from a Secret, so a long-lived token would have to be created and kept for it. A Prometheus with `arbitraryFSAccessThroughSMs.deny: true` refuses `bearerTokenFile`; scrape it with `metrics.secure: false` behind a NetworkPolicy instead, or with a scrape config of your own.

### Key Metrics to Watch

**Cluster health dashboard:**

```promql
# Is the operator acting on workloads?
rate(napwake_lifecycle_transitions_total[1h])

# Are there errors?
rate(napwake_reconcile_errors_total[5m]) > 0

# Are requests to paused workloads waking them?
sum by (result) (rate(napwake_doorman_wakes_total[1h]))
```

**Per-workload health:**

```promql
# Is the prediction engine learning?
napwake_prediction_confidence_percent{season="daily"}

# Are workloads cycling too fast?
rate(napwake_lifecycle_transitions_total[1h])

# Is a workload paused while it's in use?
increase(napwake_wrong_pauses_total[1h]) > 0
```

A workload paused while in use gets a `PausedWhileInUse` warning with what to change. See [Paused while in use](../concepts/idle-detection.md#paused-while-in-use).

## Alerting Rules

The Helm chart creates these rules when `metrics.prometheusRule.enabled` is `true`; `config/prometheus`, [above](#scraping), creates the same rules for an install from `install.yaml` or the Kustomize manifests.

| Alert | Fires when | Severity |
|-------|-----------|----------|
| `NapwakeReconcileErrorsHigh` | Reconcile errors exceed 0.1/sec for 10 minutes | critical |
| `NapwakeDown` | No healthy operator target has been scraped for 5 minutes, so nothing is paused or woken | critical |
| `NapwakeDoormanDown` | No healthy doorman target has been scraped for 5 minutes, so requests to paused workloads fail instead of waking them. Only with the doorman enabled | critical |
| `NapwakeDoormanCantListen` | For 5 minutes, a doorman pod can't listen on a routed port, or two routes claim one port, which every pod refuses. Requests to the paused workloads behind those ports fail | critical |
| `NapwakeWorkloadStuck` | A workload stays in `Pausing` or `Resuming` for 15 minutes | warning |
| `NapwakeTargetUnavailable` | A ManagedWorkload's target goes missing more than 3 times in an hour | warning |
| `NapwakeDoormanWakesFailing` | More than 3 requests to one paused workload timed out or failed (`result` `timeout` or `error`) in 15 minutes, for 5 minutes | warning |
| `NapwakeWakesSlow` | For 5 minutes, one in ten requests the doorman held in the last 30 minutes waited over a minute, half the default `wake.maxWait`. Requests passed straight to woken pods aren't counted, and it takes 3 waits at least | warning |
| `NapwakeDoormanNearCapacity` | For 5 minutes, a doorman pod carries over 80% of the 2048 held or 1024 proxied connections it takes. Past them it refuses or delays requests, as [Limits](../concepts/wake-on-request.md#limits) says | warning |
| `NapwakePausesUndone` | Something outside Napwake, such as a GitOps tool, scaled one paused workload back up 3 times or more in 6 hours | warning |
| `NapwakeActivityUnreadable` | For 30 minutes, Napwake can't read a workload's CPU while it's awake, or its Prometheus activity queries. Until it can, it doesn't pause the workload | warning |
| `NapwakeResumeIncomplete` | A woken workload has run with replicas not Ready for 15 minutes (`ResumeIncomplete`) | warning |
| `NapwakePausedWhileInUse` | Napwake paused a workload while it was in use, so something woke it soon after, 3 times or more in a day | warning |

`NapwakeDown` and `NapwakeDoormanDown` match the scrape job of the operator's and the doorman's metrics Service that the ServiceMonitors above make, so the chart creates them only with `metrics.serviceMonitor.enabled`: with no such job, they'd fire forever. If you scrape Napwake some other way, write them on your own job's `up` series.

## Health Checks

The operator and the doorman expose health endpoints on port 8081, which the chart and the Kustomize manifests already use as liveness and readiness probes. To check them yourself, forward the port first, here the operator's, from a Helm install named `napwake`:

```bash
kubectl port-forward -n napwake-system deployment/napwake 8081:8081

# Liveness
curl http://localhost:8081/healthz

# Readiness
curl http://localhost:8081/readyz
```

## Operator Logs

Logs are JSON by default. For debugging, check the operator logs; the label selectors work for every install:

```bash
kubectl logs -n napwake-system -l control-plane=controller-manager -f
kubectl logs -n napwake-system -l control-plane=doorman -f
```

The Deployments themselves are `napwake` and `napwake-doorman` for a Helm release named `napwake`, and `napwake-controller-manager` and `napwake-doorman` with `install.yaml` or the Kustomize manifests.

Log entries to look for:

- `"phase transition"`: lifecycle state changes, with `from`, `to` and `reason`
- `"paused workload scaled up outside Napwake"`: someone scaled up a paused workload
- `"managing workload from its label"`: the opt-in controller created a ManagedWorkload
- `"routing requests through the doorman"`: an error routing a paused workload's Services to the doorman
- `"ignoring malformed activity annotation"`: an activity annotation that isn't an RFC 3339 time

Forecast regime changes, idle detection and wakes are recorded as [events](../concepts/lifecycle.md#events) on the ManagedWorkload rather than logged.
