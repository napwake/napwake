# Napwake

Napwake pauses idle Kubernetes workloads in staging, preview and dev, and wakes them on the first request. Label a Deployment, StatefulSet or namespace `napwake.dev/managed: "true"` to opt it in. A paused workload is scaled to zero, with nothing deleted.

## Install

Put your settings in a values file, `napwake-values.yaml`, starting with your users' time zone:

```yaml title="napwake-values.yaml"
timezone: Europe/London
```

Install from the OCI registry, with no `helm repo` to add:

```bash
helm install napwake oci://ghcr.io/napwake/charts/napwake \
  --version 0.2.0 \
  --namespace napwake-system \
  --create-namespace \
  -f napwake-values.yaml
```

Keep the file and upgrade with it, since an upgrade given `--set` alone drops your other values.

## Main values

The `defaults` apply where a workload's own annotations and its namespace's don't say otherwise.

| Value | Default | What it does |
|---|---|---|
| `timezone` | `UTC` | IANA zone, such as `Europe/London`, whose hours and weekdays the forecast learns. Use your users' zone, so forecasts follow daylight saving |
| `defaults.idleAfter` | `1h` | Time without activity before pausing, a Go duration of at least `1m` |
| `defaults.cpuThreshold` | `10` | CPU use, as a percentage of requests from 0.1 to 100, above which a workload is in use |
| `defaults.dryRun` | `false` | Measure without pausing |
| `defaults.pauseEarly` | `false` | Let a confident forecast of quiet hours pause a workload before `idleAfter` runs out |
| `defaults.forecast` | `balanced` | How soon the forecast acts, `eager`, `balanced` or `cautious` |
| `defaults.wake` | `request` | What wakes a paused workload through the doorman. `request` is any request but a known probe's. `ask` is only a person's click on **Wake it** |
| `doorman.enabled` | `true` | Deploy the doorman, which wakes a paused workload on the first request. With `false`, requests to paused workloads fail |
| `watchNamespaces` | `[]` | Namespaces Napwake works in, all if empty. Set, it gets a Role in each, not a ClusterRole |
| `protectedNamespaces` | `[]` | Name patterns, such as `prod-*`, of namespaces Napwake never manages, unless labelled `napwake.dev/allow-protected=true`. `kube-*` and Napwake's own namespace are protected as well |
| `prometheus.url` | `""` | The Prometheus API, such as `http://prometheus.monitoring.svc:9090`, for activity queries and to start each new workload's forecast from up to four weeks of its CPU history |

## Docs

- [Installation](https://docs.napwake.dev/getting-started/installation/), with Argo CD and Flux too
- [Helm values](https://docs.napwake.dev/reference/helm-values/), every value of the chart
- [Configuration](https://docs.napwake.dev/reference/configuration/), every flag of the operator
