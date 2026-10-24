# Helm values reference

> Every value of the chart, with its default and what it does.

Source: https://docs.napwake.dev/next/reference/helm-values/

napwake-values.yaml

```yaml
timezone: Europe/London
```

Put the values you set in a file, starting with your users’ [time zone](https://docs.napwake.dev/next/getting-started/installation/#time-zone).

```bash
helm install napwake oci://ghcr.io/napwake/charts/napwake --version 0.2.0-rc.9 \
  --namespace napwake-system --create-namespace \
  -f napwake-values.yaml
```

Install with it, and upgrade with it to [keep your settings](https://docs.napwake.dev/next/operations/upgrading/#keeping-your-settings).

```bash
helm show values oci://ghcr.io/napwake/charts/napwake --version 0.2.0-rc.9
```

Every default.

* Published

  As an OCI artifact only, no `helm repo`

* Chart version

  `0.2.0-rc.9`, no `v`

* Image tags

  `v0.2.0-rc.9`, with a `v`

What the schema catches

Values are checked against `values.schema.json`. A typo (`logLevel: warn`, `defaults.cpuThreshold: 0`) or an unknown value (`watchNamespace` for `watchNamespaces`), which Helm would ignore, fails the install or upgrade, not the operator. Kubernetes checks only what passes through as is, such as `resources`, `affinity`, `tolerations`, `nodeSelector`, pod annotations and labels. The schema can’t list every IANA zone, so an unknown `timezone` fails at startup instead. The operator exits with `invalid --timezone` and `unknown time zone` in its log, and the rollout doesn’t finish.

[Names 2](https://docs.napwake.dev/next/reference/helm-values/#names)

* [`nameOverride`](https://docs.napwake.dev/next/reference/helm-values/#nameoverride)
* [`fullnameOverride`](https://docs.napwake.dev/next/reference/helm-values/#fullnameoverride)

[Image 4](https://docs.napwake.dev/next/reference/helm-values/#image)

* [`image.repository`](https://docs.napwake.dev/next/reference/helm-values/#image-repository)
* [`image.tag`](https://docs.napwake.dev/next/reference/helm-values/#image-tag)
* [`image.pullPolicy`](https://docs.napwake.dev/next/reference/helm-values/#image-pullpolicy)
* [`imagePullSecrets`](https://docs.napwake.dev/next/reference/helm-values/#imagepullsecrets)

[CRD 1](https://docs.napwake.dev/next/reference/helm-values/#crd)

* [`crds.install`](https://docs.napwake.dev/next/reference/helm-values/#crds-install)

[Defaults version 1](https://docs.napwake.dev/next/reference/helm-values/#defaults-version)

* [`defaultsVersion`](https://docs.napwake.dev/next/reference/helm-values/#defaultsversion)

[Operator 6](https://docs.napwake.dev/next/reference/helm-values/#operator)

* [`replicaCount`](https://docs.napwake.dev/next/reference/helm-values/#replicacount)
* [`leaderElection.enabled`](https://docs.napwake.dev/next/reference/helm-values/#leaderelection-enabled)
* [`maxConcurrentReconciles`](https://docs.napwake.dev/next/reference/helm-values/#maxconcurrentreconciles)
* [`timezone`](https://docs.napwake.dev/next/reference/helm-values/#timezone)
* [`logLevel`](https://docs.napwake.dev/next/reference/helm-values/#loglevel)
* [`logEncoder`](https://docs.napwake.dev/next/reference/helm-values/#logencoder)

[Resources 1](https://docs.napwake.dev/next/reference/helm-values/#resources)

* [`resources`](https://docs.napwake.dev/next/reference/helm-values/#resources-requests-cpu)

[Namespaces 2](https://docs.napwake.dev/next/reference/helm-values/#namespaces)

* [`watchNamespaces`](https://docs.napwake.dev/next/reference/helm-values/#watchnamespaces)
* [`protectedNamespaces`](https://docs.napwake.dev/next/reference/helm-values/#protectednamespaces)

[Opt-in defaults 6](https://docs.napwake.dev/next/reference/helm-values/#opt-in-defaults)

* [`defaults.idleAfter`](https://docs.napwake.dev/next/reference/helm-values/#defaults-idleafter)
* [`defaults.cpuThreshold`](https://docs.napwake.dev/next/reference/helm-values/#defaults-cputhreshold)
* [`defaults.dryRun`](https://docs.napwake.dev/next/reference/helm-values/#defaults-dryrun)
* [`defaults.pauseEarly`](https://docs.napwake.dev/next/reference/helm-values/#defaults-pauseearly)
* [`defaults.forecast`](https://docs.napwake.dev/next/reference/helm-values/#defaults-forecast)
* [`defaults.wake`](https://docs.napwake.dev/next/reference/helm-values/#defaults-wake)

[Prometheus activity 2](https://docs.napwake.dev/next/reference/helm-values/#prometheus-activity)

* [`prometheus.url`](https://docs.napwake.dev/next/reference/helm-values/#prometheus-url)
* [`prometheus.selector`](https://docs.napwake.dev/next/reference/helm-values/#prometheus-selector)

[Metrics 9](https://docs.napwake.dev/next/reference/helm-values/#metrics)

* [`metrics.enabled`](https://docs.napwake.dev/next/reference/helm-values/#metrics-enabled)
* [`metrics.port`](https://docs.napwake.dev/next/reference/helm-values/#metrics-port)
* [`metrics.secure`](https://docs.napwake.dev/next/reference/helm-values/#metrics-secure)
* [`metrics.readerSubjects`](https://docs.napwake.dev/next/reference/helm-values/#metrics-readersubjects)
* [`metrics.serviceMonitor.enabled`](https://docs.napwake.dev/next/reference/helm-values/#metrics-servicemonitor-enabled)
* [`metrics.serviceMonitor.interval`](https://docs.napwake.dev/next/reference/helm-values/#metrics-servicemonitor-interval)
* [`metrics.serviceMonitor.additionalLabels`](https://docs.napwake.dev/next/reference/helm-values/#metrics-servicemonitor-additionallabels)
* [`metrics.prometheusRule.enabled`](https://docs.napwake.dev/next/reference/helm-values/#metrics-prometheusrule-enabled)
* [`metrics.prometheusRule.additionalLabels`](https://docs.napwake.dev/next/reference/helm-values/#metrics-prometheusrule-additionallabels)

[Doorman 19](https://docs.napwake.dev/next/reference/helm-values/#doorman)

* [`doorman.enabled`](https://docs.napwake.dev/next/reference/helm-values/#doorman-enabled)
* [`doorman.replicaCount`](https://docs.napwake.dev/next/reference/helm-values/#doorman-replicacount)
* [`doorman.portsPerWorkload`](https://docs.napwake.dev/next/reference/helm-values/#doorman-portsperworkload)
* [`doorman.portsPerNamespace`](https://docs.napwake.dev/next/reference/helm-values/#doorman-portspernamespace)
* [`doorman.healthCheckUserAgents`](https://docs.napwake.dev/next/reference/helm-values/#doorman-healthcheckuseragents)
* [`doorman.admissionPolicy.enabled`](https://docs.napwake.dev/next/reference/helm-values/#doorman-admissionpolicy-enabled)
* [`doorman.podDisruptionBudget.enabled`](https://docs.napwake.dev/next/reference/helm-values/#doorman-poddisruptionbudget-enabled)
* [`doorman.nodeSelector`](https://docs.napwake.dev/next/reference/helm-values/#doorman-nodeselector)
* [`doorman.tolerations`](https://docs.napwake.dev/next/reference/helm-values/#doorman-tolerations)
* [`doorman.affinity`](https://docs.napwake.dev/next/reference/helm-values/#doorman-affinity)
* [`doorman.topologySpreadConstraints`](https://docs.napwake.dev/next/reference/helm-values/#doorman-topologyspreadconstraints)
* [`doorman.podAnnotations`](https://docs.napwake.dev/next/reference/helm-values/#doorman-podannotations)
* [`doorman.podLabels`](https://docs.napwake.dev/next/reference/helm-values/#doorman-podlabels)
* [`doorman.priorityClassName`](https://docs.napwake.dev/next/reference/helm-values/#doorman-priorityclassname)
* [`doorman.extraEnv`](https://docs.napwake.dev/next/reference/helm-values/#doorman-extraenv)
* [`doorman.resources`](https://docs.napwake.dev/next/reference/helm-values/#doorman-resources-requests-cpu)
* [`doorman.negForwarder.enabled`](https://docs.napwake.dev/next/reference/helm-values/#doorman-negforwarder-enabled)
* [`doorman.negForwarder.replicas`](https://docs.napwake.dev/next/reference/helm-values/#doorman-negforwarder-replicas)
* [`doorman.negForwarder.resources`](https://docs.napwake.dev/next/reference/helm-values/#doorman-negforwarder-resources-requests-cpu)

[Network policy 6](https://docs.napwake.dev/next/reference/helm-values/#network-policy)

* [`networkPolicy.enabled`](https://docs.napwake.dev/next/reference/helm-values/#networkpolicy-enabled)
* [`doorman.networkPolicy.ingressFrom`](https://docs.napwake.dev/next/reference/helm-values/#doorman-networkpolicy-ingressfrom)
* [`doorman.networkPolicy.egress.enabled`](https://docs.napwake.dev/next/reference/helm-values/#doorman-networkpolicy-egress-enabled)
* [`doorman.networkPolicy.egress.podCIDRs`](https://docs.napwake.dev/next/reference/helm-values/#doorman-networkpolicy-egress-podcidrs)
* [`doorman.networkPolicy.egress.apiServer`](https://docs.napwake.dev/next/reference/helm-values/#doorman-networkpolicy-egress-apiserver)
* [`doorman.networkPolicy.egress.apiServerPorts`](https://docs.napwake.dev/next/reference/helm-values/#doorman-networkpolicy-egress-apiserverports)

[Service account 2](https://docs.napwake.dev/next/reference/helm-values/#service-account)

* [`serviceAccount.create`](https://docs.napwake.dev/next/reference/helm-values/#serviceaccount-create)
* [`serviceAccount.name`](https://docs.napwake.dev/next/reference/helm-values/#serviceaccount-name)

[Guards 9](https://docs.napwake.dev/next/reference/helm-values/#guards)

* [`guard.uninstall`](https://docs.napwake.dev/next/reference/helm-values/#guard-uninstall)
* [`guard.upgrade`](https://docs.napwake.dev/next/reference/helm-values/#guard-upgrade)
* [`guard.nodeSelector`](https://docs.napwake.dev/next/reference/helm-values/#guard-nodeselector)
* [`guard.tolerations`](https://docs.napwake.dev/next/reference/helm-values/#guard-tolerations)
* [`guard.affinity`](https://docs.napwake.dev/next/reference/helm-values/#guard-affinity)
* [`guard.priorityClassName`](https://docs.napwake.dev/next/reference/helm-values/#guard-priorityclassname)
* [`guard.podAnnotations`](https://docs.napwake.dev/next/reference/helm-values/#guard-podannotations)
* [`guard.podLabels`](https://docs.napwake.dev/next/reference/helm-values/#guard-podlabels)
* [`guard.resources`](https://docs.napwake.dev/next/reference/helm-values/#guard-resources)

[Pod scheduling 7](https://docs.napwake.dev/next/reference/helm-values/#pod-scheduling)

* [`nodeSelector`](https://docs.napwake.dev/next/reference/helm-values/#nodeselector)
* [`tolerations`](https://docs.napwake.dev/next/reference/helm-values/#tolerations)
* [`affinity`](https://docs.napwake.dev/next/reference/helm-values/#affinity)
* [`podAnnotations`](https://docs.napwake.dev/next/reference/helm-values/#podannotations)
* [`podLabels`](https://docs.napwake.dev/next/reference/helm-values/#podlabels)
* [`priorityClassName`](https://docs.napwake.dev/next/reference/helm-values/#priorityclassname)
* [`extraEnv`](https://docs.napwake.dev/next/reference/helm-values/#extraenv)

## Names

| Value              | Default | Description                                                  |
| ------------------ | ------- | ------------------------------------------------------------ |
| `nameOverride`     | `""`    | Replaces the chart name in resource names                    |
| `fullnameOverride` | `""`    | Replaces the full name the chart’s resources are named after |

The names and labels the chart gives.

|                                        |                                                                                                                                                                                    |
| -------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Full name                              | The release name if it contains `napwake`, else `<release>-napwake`, cut to 40 characters so names built from it, the longest being `<fullname>-doorman-auth-delegator`, fit in 63 |
| Operator Deployment and ServiceAccount | `napwake`, for a release named `napwake`                                                                                                                                           |
| The doorman’s                          | `napwake-doorman`                                                                                                                                                                  |
| Operator pod labels                    | `app.kubernetes.io/name: napwake`, `app.kubernetes.io/instance: <release>`, `control-plane: controller-manager`                                                                    |
| Doorman pod labels                     | The same, with `control-plane: doorman`                                                                                                                                            |

## Image

| Value              | Default                                               | Description                                 |
| ------------------ | ----------------------------------------------------- | ------------------------------------------- |
| `image.repository` | `ghcr.io/napwake/napwake`                             | Container image repository                  |
| `image.tag`        | `""`, the chart’s `appVersion`, such as `v0.2.0-rc.9` | Image tag, for the operator and the doorman |
| `image.pullPolicy` | `IfNotPresent`                                        | Image pull policy                           |
| `imagePullSecrets` | `[]`                                                  | Private registry credentials                |

## CRD

| Value          | Default | Description                                                                                                                                                           |
| -------------- | ------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `crds.install` | `true`  | Install the ManagedWorkload CRD and upgrade it with each `helm upgrade`. Set `crds.install: false` only if you manage it, such as from `config/crd` in the repository |

Uninstall keeps the CRD

Deleting the CRD would delete every ManagedWorkload, so it’s a chart template with `helm.sh/resource-policy: keep`, and `helm uninstall` leaves it and every ManagedWorkload. For Argo CD, which ignores that, it also carries `argocd.argoproj.io/sync-options: Delete=false`, so deleting or pruning the Application leaves it too.

## Defaults version

| Value             | Default                                   | Description                                                                                                                            |
| ----------------- | ----------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| `defaultsVersion` | the chart’s version, such as `0.2.0-rc.9` | Which chart version Helm’s defaults are from. Leave it. See [Keeping your settings](https://docs.napwake.dev/next/operations/upgrading/#keeping-your-settings) |

What it’s for

`helm upgrade --reuse-values` to a newer version renders with the old version’s defaults, missing every value added since. Seeing `defaultsVersion` differ, the chart fills each missing value from its own defaults, `files/defaults.yaml`, a copy of `values.yaml`, which templates can’t read. Values that are there stay, `false`, `0` and `""` included. With its own defaults it fills nothing, so a default removed with `null` stays removed.

## Operator

| Value                     | Default | Description                                                                                                                                                                                                                                                 |
| ------------------------- | ------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `replicaCount`            | `1`     | Operator replicas. More than one needs `leaderElection.enabled`, or the schema refuses it                                                                                                                                                                   |
| `leaderElection.enabled`  | `true`  | One replica reconciles, through a Lease. Without it, upgrades stop the old operator before starting the new (`strategy: Recreate`), so two never act at once                                                                                                |
| `maxConcurrentReconciles` | `8`     | Reconciled at once, so a slow metrics or Prometheus query doesn’t hold up waking another. Sets [`--max-concurrent-reconciles`](https://docs.napwake.dev/next/reference/configuration/#flag-max-concurrent-reconciles)                                                               |
| `timezone`                | `UTC`   | IANA zone, such as `Europe/London`, for the [forecast’s](https://docs.napwake.dev/next/concepts/forecasting/#wall-clock-alignment) hours and weekdays. Use your users’ zone, so forecasts follow daylight saving. Sets [`--timezone`](https://docs.napwake.dev/next/reference/configuration/#flag-timezone) |
| `logLevel`                | `info`  | `debug`, `info`, `error`, `panic`, or a whole number above 0 for more verbose debug levels. Operator and doorman                                                                                                                                            |
| `logEncoder`              | `json`  | `json`, or `console` to read by eye. Operator and doorman                                                                                                                                                                                                   |

## Resources

| Value                       | Default | Description                                                         |
| --------------------------- | ------- | ------------------------------------------------------------------- |
| `resources.requests.cpu`    | `10m`   | CPU request                                                         |
| `resources.requests.memory` | `128Mi` | Memory request                                                      |
| `resources.limits.cpu`      | `500m`  | CPU limit                                                           |
| `resources.limits.memory`   | `512Mi` | Memory limit. Raise it on clusters with many thousands of workloads |

What the operator keeps in memory

Its memory grows with the cluster.

| Objects                                                                                  | Held                                                                                                                     |
| ---------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------ |
| ManagedWorkloads, Deployments, StatefulSets, Services, Ingresses, PVCs, HPAs, Namespaces | Cached, only the watched namespaces’ with `watchNamespaces`                                                              |
| Nodes                                                                                    | Cached without image lists                                                                                               |
| Pods                                                                                     | Cached only as spare capacity needs, if bound to a node and not finished, as node, phase and requests. About 2.6 KB each |
| The EndpointSlices it writes, the doorman Service’s                                      | Cached                                                                                                                   |
| Other EndpointSlices, ConfigMaps, KEDA ScaledObjects                                     | Read when needed, not cached                                                                                             |

Go’s garbage collector, in the operator and the doorman, gets a soft limit of 90% of the memory limit, leaving the rest for memory outside its heap. It works harder near the limit instead of being OOM-killed. `GOMEMLIMIT` in the pod overrides it.

## Namespaces

| Value                 | Default | Description                                                                                                                                                                                                                                                                                                                                                                                                      |
| --------------------- | ------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `watchNamespaces`     | `[]`    | Namespaces Napwake works in, all if empty. Set, it gets a Role in each, not a ClusterRole, so Kubernetes refuses it anything elsewhere ([RBAC](https://docs.napwake.dev/next/reference/helm-values/#rbac)). Sets [`--watch-namespaces`](https://docs.napwake.dev/next/reference/configuration/#flag-watch-namespaces), for the doorman too. Each install in a cluster needs its own ([more than one install](https://docs.napwake.dev/next/getting-started/installation/#more-than-one-install))                     |
| `protectedNamespaces` | `[]`    | Patterns, such as `prod-*`, of namespaces never managed, as if labelled `napwake.dev/protected=true`, unless labelled `napwake.dev/allow-protected=true`. `kube-*` and the operator’s and doorman’s namespace always are, with the same exception ([protected namespaces](https://docs.napwake.dev/next/guides/opt-in/#protected-namespaces)). Sets [`--protected-namespaces`](https://docs.napwake.dev/next/reference/configuration/#flag-protected-namespaces) |

values.yaml

```yaml
watchNamespaces: [preview-1, preview-2, staging]
protectedNamespaces: ["prod-*", production]
```

What changes with `watchNamespaces`.

* Wake on request still works. The operator always reads the doorman’s EndpointSlices in the release namespace through a `<fullname>-doorman-endpoints` Role.
* A `dependsOn` into an unwatched namespace that allows the dependent’s (`napwake.dev/dependents-from`) shows as `DependencyNotFound=True`, reason `DependencyNotVisible`, and never blocks it.
* An upgrade or rollback that leaves out a namespace with managed workloads, or sets `watchNamespaces` where it was empty, is refused while they’re there, since they’d stay paused with no operator ([narrowing watchNamespaces](https://docs.napwake.dev/next/operations/upgrading/#narrowing-watchnamespaces)).

## Opt-in defaults

For workloads opted in with the `napwake.dev/managed` label, when neither they nor their namespace set the annotation ([which setting wins](https://docs.napwake.dev/next/guides/opt-in/#which-setting-wins)).

| Value                   | Default    | Description                                                                                                                                                                                                                                                  |
| ----------------------- | ---------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `defaults.idleAfter`    | `1h`       | Time without activity before pausing, a Go duration of at least `1m`, such as `90s` or `1h30m`. Sets [`--default-idle-after`](https://docs.napwake.dev/next/reference/configuration/#flag-default-idle-after)                                                                        |
| `defaults.cpuThreshold` | `10`       | CPU % of requests, from 0.1 to 100 to at most two decimal places, such as `10` or `0.5`, above which it’s active. Sets [`--default-cpu-threshold`](https://docs.napwake.dev/next/reference/configuration/#flag-default-cpu-threshold)                                                |
| `defaults.dryRun`       | `false`    | Measure without pausing. `true` rolls Napwake out across a cluster measuring first. Sets [`--default-dry-run`](https://docs.napwake.dev/next/reference/configuration/#flag-default-dry-run)                                                                                          |
| `defaults.pauseEarly`   | `false`    | Let a confident forecast of quiet hours [pause before `idleAfter`](https://docs.napwake.dev/next/concepts/forecasting/#pausing-early) runs out. Sets [`--default-pause-early`](https://docs.napwake.dev/next/reference/configuration/#flag-default-pause-early)                                              |
| `defaults.forecast`     | `balanced` | How soon the forecast acts, `eager`, `balanced` or `cautious` ([Forecast setting](https://docs.napwake.dev/next/concepts/forecasting/#forecast-setting)). Sets [`--default-forecast`](https://docs.napwake.dev/next/reference/configuration/#flag-default-forecast)                                          |
| `defaults.wake`         | `request`  | What wakes a paused workload through the doorman. `request` is any request. `ask` is only a person’s click on **Wake it**, for a [public URL](https://docs.napwake.dev/next/guides/preview-environments/). Sets [`--default-wake`](https://docs.napwake.dev/next/reference/configuration/#flag-default-wake) |

## Prometheus activity

| Value                 | Default | Description                                                                                                                                                                                                                                                                                                                                                                                                 |
| --------------------- | ------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `prometheus.url`      | `""`    | Prometheus API base URL, such as `http://prometheus.monitoring.svc:9090`, for activity queries and to start new workloads’ forecasts from up to four weeks of CPU [history](https://docs.napwake.dev/next/concepts/forecasting/#learning-from-history). Sets [`--prometheus-url`](https://docs.napwake.dev/next/reference/configuration/#flag-prometheus-url)                                                                                               |
| `prometheus.selector` | `""`    | Label matchers for the operator’s history query, such as `cluster="staging"`, when Prometheus holds several clusters’ series (Thanos, Mimir, a shared one). Written as inside PromQL’s braces, as `kubectl napwake scan --prometheus-selector` takes them. Activity queries run as written. Needs `prometheus.url`. Sets [`--prometheus-selector`](https://docs.napwake.dev/next/reference/configuration/#flag-prometheus-selector) |

## Metrics

| Value                    | Default | Description                                                                                                                                                                                                 |
| ------------------------ | ------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `metrics.enabled`        | `true`  | Metrics from the operator and the doorman. Also binds both to `system:auth-delegator` (`<fullname>-auth-delegator`, `<fullname>-doorman-auth-delegator`), to check scrapers’ tokens                         |
| `metrics.port`           | `8443`  | Operator and doorman. Not 8081, the probes’, nor 20000-29999, the doorman’s ports for paused workloads’ traffic                                                                                             |
| `metrics.secure`         | `true`  | HTTPS, only to callers allowed to `get` `/metrics`. `false` serves plain HTTP to anyone who reaches the port                                                                                                |
| `metrics.readerSubjects` | `[]`    | Subjects, such as your Prometheus’s ServiceAccount, bound to `<fullname>-metrics-reader`, a ClusterRole that may get `/metrics`. kube-prometheus-stack’s is `<its release name>-kube-prometheus-prometheus` |

With `metrics.secure`, bind your Prometheus here or with a binding of your own:

values.yaml

```yaml
metrics:
  readerSubjects:
    - kind: ServiceAccount
      name: prometheus-k8s
      namespace: monitoring
```

### ServiceMonitor

| Value                                     | Default | Description                                                                                                                                                                                         |
| ----------------------------------------- | ------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `metrics.serviceMonitor.enabled`          | `false` | Prometheus Operator ServiceMonitors for the operator and the doorman, with `honorLabels: true` so metrics keep the workload’s `namespace` label ([scraping](https://docs.napwake.dev/next/operations/monitoring/#scraping)) |
| `metrics.serviceMonitor.interval`         | `30s`   | Scrape interval                                                                                                                                                                                     |
| `metrics.serviceMonitor.additionalLabels` | `{}`    | Extra labels. Prometheus picks the ServiceMonitors up only if they match its `serviceMonitorSelector`. kube-prometheus-stack needs `release: <its release name>`                                    |

### PrometheusRule

| Value                                     | Default | Description                                                                                                                                                                                                                    |
| ----------------------------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `metrics.prometheusRule.enabled`          | `false` | A PrometheusRule with the [alerts](https://docs.napwake.dev/next/operations/monitoring/#alerting-rules). `NapwakeDown` and `NapwakeDoormanDown` match the ServiceMonitors’ scrape jobs, so they’re included only with `metrics.serviceMonitor.enabled` |
| `metrics.prometheusRule.additionalLabels` | `{}`    | Extra labels. Prometheus loads the rule only if they match its `ruleSelector`. kube-prometheus-stack needs `release: <its release name>`                                                                                       |

## Doorman

The doorman holds requests to paused workloads and wakes them ([Wake on request](https://docs.napwake.dev/next/concepts/wake-on-request/)). It uses the operator’s image, `metrics.*`, `logLevel` and `logEncoder`.

| Value                                            | Default | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| ------------------------------------------------ | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `doorman.enabled`                                | `true`  | Deploy the doorman. With `false`, requests to paused workloads fail                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| `doorman.replicaCount`                           | `2`     | Replicas, each serving traffic                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| `doorman.portsPerWorkload`                       | `32`    | Most ports one workload may hold, one per routed Service port, 1 to 10000, so one tenant can’t take all 10,000 the cluster shares. Over it, it isn’t routed (`WakeOnRequest=False`, reason `TooManyPorts`). Sets [`--doorman-ports-per-workload`](https://docs.napwake.dev/next/reference/configuration/#flag-doorman-ports-per-workload)                                                                                                                                                                                                                                                                           |
| `doorman.portsPerNamespace`                      | `256`   | Most ports one namespace’s workloads may hold together, 1 to 10000. Sets [`--doorman-ports-per-namespace`](https://docs.napwake.dev/next/reference/configuration/#flag-doorman-ports-per-namespace)                                                                                                                                                                                                                                                                                                                                                                                                                 |
| `doorman.healthCheckUserAgents`                  | `[]`    | Health checkers’ and scrapers’ `User-Agent` prefixes, or names after `(compatible; `in a browser-style one, whose `GET` and `HEAD` don’t wake a workload, such as `["MyCorpMonitor/"]`. The built-in ones are `kube-probe`, Prometheus, load balancers’ checks, `Blackbox Exporter/`, `Datadog/Synthetics`, `Amazon-Route53-Health-Check-Service`, `GoogleStackdriverMonitoring-UptimeChecks`, `UptimeRobot`, `Pingdom.com_bot`, `NewRelicPinger/`, `Uptime-Kuma/`, `Gatus/` and others. See [What wakes a workload](https://docs.napwake.dev/next/concepts/wake-on-request/#what-wakes-a-workload-and-what-doesnt) |
| `doorman.admissionPolicy.enabled`                | `true`  | Two ValidatingAdmissionPolicies. The doorman’s ServiceAccount may change only `napwake.dev/last-request`, `napwake.dev/last-request-from` and `napwake.dev/turned-away` on ManagedWorkloads, though its role may patch them. No one else may set the first two, so a forged `last-request-from` can’t teach the operator a dependency. See [Security](https://docs.napwake.dev/next/reference/security/#the-doorman-is-a-way-in-to-paused-workloads)                                                                                                                                                                |
| `doorman.podDisruptionBudget.enabled`            | `true`  | Keeps one doorman serving through node drains, when `replicaCount` is above 1                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| `doorman.nodeSelector`                           | `{}`    | Empty uses the operator’s                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `doorman.tolerations`                            | `[]`    | Empty uses the operator’s                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `doorman.affinity`                               | `{}`    | Empty uses the operator’s                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `doorman.topologySpreadConstraints`              | `[]`    | Empty spreads them across nodes and zones where it can                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `doorman.podAnnotations`                         | `{}`    | Empty uses the operator’s                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `doorman.podLabels`                              | `{}`    | Empty uses the operator’s                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `doorman.priorityClassName`                      | `""`    | Empty uses the operator’s                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `doorman.extraEnv`                               | `[]`    | Extra environment variables. The operator’s `extraEnv` doesn’t apply                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| `doorman.resources.requests.cpu`                 | `10m`   | CPU request                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| `doorman.resources.requests.memory`              | `64Mi`  | Memory request                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| `doorman.resources.limits.cpu`                   | `500m`  | CPU limit                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `doorman.resources.limits.memory`                | `256Mi` | Memory limit, enough for some 100,000 pods. Raise it beyond                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| `doorman.negForwarder.enabled`                   | `false` | Optional, for GKE. Routes paused workloads’ Services behind container-native load balancing (NEGs), as a GKE Ingress’s or Gateway’s are, through a forwarder Deployment the operator runs in the workload’s namespace while it’s paused, so requests through the load balancer wake them. Grants the operator `create` and `delete` on Deployments ([RBAC](https://docs.napwake.dev/next/reference/helm-values/#rbac)), adds a headless Service `<fullname>-doorman-pods`, and needs `doorman.enabled`. See [GKE NEGs](https://docs.napwake.dev/next/concepts/wake-on-request/#gke-container-native-load-balancing-negs)                                                |
| `doorman.negForwarder.replicas`                  | `2`     | Pods per forwarder, spread across nodes so one stays while a node drains                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| `doorman.negForwarder.resources.requests.cpu`    | `10m`   | CPU request, per pod                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| `doorman.negForwarder.resources.requests.memory` | `32Mi`  | Memory request, per pod                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| `doorman.negForwarder.resources.limits.memory`   | `64Mi`  | Memory limit, per pod                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |

The doorman’s memory, shutdown and rollouts

* It caches ManagedWorkloads and the EndpointSlice controller’s EndpointSlices in watched namespaces, cut to what it reads, about 300 bytes a pod and 700 a Service. Held connections take up to 72Mi more under a flood.
* On Kubernetes 1.30 and later, it sleeps 5 seconds before shutting down, so kube-proxy and load balancers stop sending new connections first.
* Rollouts wait 5 seconds after a new pod is Ready (`minReadySeconds`) before stopping an old one, so the operator has moved routes to it.

## Network policy

| Value                                         | Default       | Description                                                                                                                                                                                                                            |
| --------------------------------------------- | ------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `networkPolicy.enabled`                       | `false`       | NetworkPolicies for the operator and the doorman                                                                                                                                                                                       |
| `doorman.networkPolicy.ingressFrom`           | `[]`          | Peers allowed to reach paused workloads through the doorman, such as your ingress controller’s namespace. Empty leaves the doorman open to every caller. Needs `networkPolicy.enabled`                                                 |
| `doorman.networkPolicy.egress.enabled`        | `false`       | An egress policy letting the doorman connect only to `podCIDRs` and the API server. Separate from `networkPolicy.enabled`, and needs a CNI that enforces egress. The doorman needs no DNS, reaching the API server and pods by address |
| `doorman.networkPolicy.egress.podCIDRs`       | `[]`          | Pod CIDRs, in every IP family. Add node CIDRs if a routed workload uses `hostNetwork`. Required when enabled                                                                                                                           |
| `doorman.networkPolicy.egress.apiServer`      | `[]`          | The API server’s endpoint CIDRs (`kubectl get endpointslices -n default -l kubernetes.io/service-name=kubernetes`), not the `kubernetes` Service’s ClusterIP. Required when enabled                                                    |
| `doorman.networkPolicy.egress.apiServerPorts` | `[443, 6443]` | Their ports                                                                                                                                                                                                                            |

What the policies admit, with `networkPolicy.enabled`.

| Port                                          | Admits                                                                                                                                                                                                                                                                                                          |
| --------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| The operator’s and the doorman’s metrics port | Only namespaces labelled `metrics: enabled`                                                                                                                                                                                                                                                                     |
| The doorman’s routing ports, TCP 20000-29999  | Every source, since every client of a paused workload’s Services is sent there. With `ingressFrom` and `doorman.negForwarder.enabled`, the forwarders too, from any namespace. To narrow it, see [restricting who can reach the doorman](https://docs.napwake.dev/next/concepts/wake-on-request/#restricting-who-can-reach-the-doorman) |

On Cilium

An `ipBlock` may not match the API server, which Cilium knows as the `kube-apiserver` entity, not by address. The egress policy can then cut the doorman off, and its pods fail readiness and log API errors. Leave `doorman.networkPolicy.egress.enabled` off, and write a CiliumNetworkPolicy allowing egress `toEntities: [kube-apiserver]` and `toCIDR` your pod CIDRs.

## Service account

The operator’s ServiceAccount. The doorman always gets its own, `<fullname>-doorman`.

| Value                   | Default | Description                              |
| ----------------------- | ------- | ---------------------------------------- |
| `serviceAccount.create` | `true`  | Create the operator’s ServiceAccount     |
| `serviceAccount.name`   | `""`    | Name override. Defaults to the full name |

## Guards

Without the operator, a ManagedWorkload’s finalizer never runs. Its workload stays paused at 0 replicas, its Services routed to the doorman. The guards stop Helm first.

| Value                     | Default                              | Description                                                                                                                                                                                                                                                                                                                                     |
| ------------------------- | ------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `guard.uninstall`         | `true`                               | Refuse `helm uninstall` while ManagedWorkloads are left, through a pre-delete hook Job, `<fullname>-uninstall-guard`. Argo CD 3.3 and later runs it as PreDelete, blocking Application deletion too. Earlier versions skip it ([uninstall](https://docs.napwake.dev/next/getting-started/installation/#uninstall))                                                      |
| `guard.upgrade`           | `true`                               | Refuse `helm upgrade` or `helm rollback` to a `watchNamespaces` leaving out namespaces with managed workloads, through a pre-upgrade and pre-rollback hook Job, `<fullname>-upgrade-guard`, only with `watchNamespaces` set. Argo CD runs it as PreSync, before every sync ([narrowing](https://docs.napwake.dev/next/operations/upgrading/#narrowing-watchnamespaces)) |
| `guard.nodeSelector`      | `{}`                                 | The hooks’ node selector. Empty uses the operator’s                                                                                                                                                                                                                                                                                             |
| `guard.tolerations`       | `[]`                                 | Empty uses the operator’s                                                                                                                                                                                                                                                                                                                       |
| `guard.affinity`          | `{}`                                 | Empty uses the operator’s                                                                                                                                                                                                                                                                                                                       |
| `guard.priorityClassName` | `""`                                 | Empty uses the operator’s                                                                                                                                                                                                                                                                                                                       |
| `guard.podAnnotations`    | `{}`                                 | Over `sidecar.istio.io/inject: "false"` and `linkerd.io/inject: disabled`                                                                                                                                                                                                                                                                       |
| `guard.podLabels`         | `{}`                                 | Over `sidecar.istio.io/inject: "false"`. Can’t replace the chart’s labels                                                                                                                                                                                                                                                                       |
| `guard.resources`         | 10m/64Mi requests, 100m/128Mi limits | The hooks’ resources                                                                                                                                                                                                                                                                                                                            |

How the guards work

Each runs the operator’s image with [`--guard`](https://docs.napwake.dev/next/reference/configuration/#flag-guard), waits two and a half minutes for ManagedWorkloads being deleted, lists those it would leave without an operator, and fails. Helm stops before changing anything and prints the list.

| Guard     | Counts                                                                                                                                                                                                                              |
| --------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Uninstall | Every ManagedWorkload in the watched namespaces (all, without `watchNamespaces`), and any elsewhere with this operator’s `napwake.dev/cleanup` finalizer. None another install stamped with its namespace in `napwake.dev/operator` |
| Upgrade   | Those the operator took on in namespaces the new `watchNamespaces` leaves out                                                                                                                                                       |

* Each runs as a ServiceAccount of its own name, with a ClusterRole that may only list ManagedWorkloads’ metadata, cluster-wide even with `watchNamespaces`, since those it looks for may be outside it.
* The ServiceAccounts and RBAC are hooks too, deleted after the run. A failed Job stays an hour, for `kubectl logs`.
* The hooks use `image`, `imagePullSecrets` and `logLevel`.
* Mesh sidecars are kept out, since a running sidecar would keep the Job from finishing.
* Their pods carry `control-plane: uninstall-guard` or `control-plane: upgrade-guard`, so the operator’s Service and NetworkPolicy don’t select them.
* `--no-hooks` skips both.

## Pod scheduling

For the operator, and for the doorman unless its own `doorman.*` value is set.

| Value               | Default | Description                                                                                                                                                |
| ------------------- | ------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `nodeSelector`      | `{}`    | Node selector                                                                                                                                              |
| `tolerations`       | `[]`    | Tolerations                                                                                                                                                |
| `affinity`          | `{}`    | Affinity and anti-affinity rules                                                                                                                           |
| `podAnnotations`    | `{}`    | Pod annotations                                                                                                                                            |
| `podLabels`         | `{}`    | Pod labels. Can’t replace the labels the chart selects pods by                                                                                             |
| `priorityClassName` | `""`    | PriorityClass. Paused workloads’ requests wait on the doorman and operator, so on a cluster that preempts, give them a class above the workloads they wake |
| `extraEnv`          | `[]`    | Extra environment variables, operator only, as container `env` entries, such as `GOMAXPROCS`                                                               |

## Security

Fixed by the chart, not values. Override them with Helm post-rendering or Kustomize overlays.

| Setting                               | On the    |
| ------------------------------------- | --------- |
| `runAsNonRoot: true`                  | Pod       |
| `seccompProfile.type: RuntimeDefault` | Pod       |
| `readOnlyRootFilesystem: true`        | Container |
| `allowPrivilegeEscalation: false`     | Container |
| `capabilities.drop: ["ALL"]`          | Container |

## Health probes

| Pod      | Probe     | Path       | Port | Initial Delay | Period |
| -------- | --------- | ---------- | ---- | ------------- | ------ |
| Operator | Liveness  | `/healthz` | 8081 | 15s           | 20s    |
| Operator | Readiness | `/readyz`  | 8081 | 5s            | 10s    |
| Doorman  | Liveness  | `/healthz` | 8081 | 15s           | 20s    |
| Doorman  | Readiness | `/readyz`  | 8081 | 2s            | 5s     |

## RBAC

What the operator’s ClusterRole, `<fullname>-manager`, grants by default.

| Resource                                                                                                        | Access                                            | For                                                                                                                                                                                                                             |
| --------------------------------------------------------------------------------------------------------------- | ------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| ManagedWorkloads                                                                                                | Manage                                            | Including creating them for labelled workloads                                                                                                                                                                                  |
| Namespaces                                                                                                      | Read                                              | The `napwake.dev/managed` label and settings annotations                                                                                                                                                                        |
| Deployments, StatefulSets                                                                                       | Read, scale (scale subresource), patch            | Patching sets and removes `napwake.dev/paused-from` while paused. It can’t delete them, and changes nothing else                                                                                                                |
| PersistentVolumeClaims                                                                                          | Read                                              | Pricing a paused workload’s storage                                                                                                                                                                                             |
| HorizontalPodAutoscalers, KEDA ScaledObjects                                                                    | Read, and annotate ScaledObjects                  | Pausing a KEDA workload through KEDA                                                                                                                                                                                            |
| Pod metrics, from metrics-server                                                                                | Read                                              |                                                                                                                                                                                                                                 |
| Pods                                                                                                            | Read, watch                                       | Pricing what pausing frees, sidecars included, and measuring spare node capacity for [saved](https://docs.napwake.dev/next/concepts/cost-tracking/#how-saved-is-measured). Cached only if bound to a node and not finished, as node, phase and requests |
| Nodes                                                                                                           | Read                                              | Pricing at instance types’ list prices, measuring spare capacity. Cached without image lists                                                                                                                                    |
| Karpenter’s NodePools, Cluster Autoscaler’s `cluster-autoscaler-status` ConfigMap, Deployments in `kube-system` | List NodePools and Deployments, get the ConfigMap | Reporting the node autoscaler (`napwake_node_autoscaler`). Reads NodePools’ names and consolidation settings, only the rest’s metadata                                                                                          |
| ConfigMaps                                                                                                      | Get, one at a time, uncached                      | [Learning dependencies](https://docs.napwake.dev/next/concepts/dependencies/#learned-dependencies) from those a workload’s environment references. Can’t list or change them                                                                            |
| Events                                                                                                          | Create                                            | User-facing status updates                                                                                                                                                                                                      |
| Services                                                                                                        | Read                                              |                                                                                                                                                                                                                                 |
| EndpointSlices                                                                                                  | Manage                                            | Routing paused workloads to the doorman                                                                                                                                                                                         |

The other roles.

| Role                           | Kind, and when                                                                    | Grants                                                                                                                                                                                                                                                                                                                                                                  |
| ------------------------------ | --------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `<fullname>-doorman`           | ClusterRole, for the doorman’s own ServiceAccount                                 | Read ManagedWorkloads and EndpointSlices, get the pods a woken workload’s endpoints name (to check each address is its pod’s), patch ManagedWorkloads to wake them, create Events. With `doorman.admissionPolicy.enabled`, patches only set the two request annotations, and the one counting what it [turned away](https://docs.napwake.dev/next/concepts/wake-on-request/#what-it-turns-away) |
| `<fullname>-doorman-wake-key`  | Role in the release namespace, for the doorman                                    | `get` and `update` on one Secret of the same name, which the chart creates empty. The doorman fills it with the key it signs the [Wake it button](https://docs.napwake.dev/next/concepts/wake-on-request/#the-wake-it-button) with. It can’t read or list any other                                                                                                                             |
| `<fullname>-leader-election`   | Role in the release namespace, with leader election                               | Leases (`get`, `create`, `update`) and Events                                                                                                                                                                                                                                                                                                                           |
| `<fullname>-doorman-endpoints` | Role in the release namespace, with `watchNamespaces`                             | Read the doorman’s EndpointSlices                                                                                                                                                                                                                                                                                                                                       |
| `<fullname>-forwarder`         | ClusterRole, or a Role per watched namespace, with `doorman.negForwarder.enabled` | `create` and `delete` Deployments for the [forwarders](https://docs.napwake.dev/next/concepts/wake-on-request/#gke-container-native-load-balancing-negs). Deletes only its own, owned by a ManagedWorkload. Forwarders run as the namespace’s `default` ServiceAccount, without its token or API access                                                                                         |
| `<fullname>-view`              | Aggregated into `view`, `edit`, `admin`                                           | Namespace viewers may `get`, `list`, `watch` its ManagedWorkloads, as `kubectl napwake status` and `scan` do. Kustomize’s `managedworkload-viewer-role` is aggregated alike                                                                                                                                                                                             |
| `<fullname>-edit`              | Aggregated into `edit`, `admin`                                                   | Namespace editors may `create`, `update`, `patch`, `delete` its ManagedWorkloads, as `pause`, `wake` and `enable` do. Kustomize’s `managedworkload-editor-role` is aggregated alike                                                                                                                                                                                     |

What changes with `watchNamespaces`.

* The operator and the doorman get a Role and RoleBinding in each watched namespace instead.
* The operator keeps one ClusterRole, to read Namespaces and Nodes and list Karpenter’s NodePools, which only a ClusterRole can grant.
* Workloads elsewhere can’t be read, scaled or opted in. Dependencies on them aren’t held or woken.
* With `metrics.enabled` and `metrics.secure`, both keep cluster-wide `system:auth-delegator` bindings, good only for checking scrapers’ tokens.

Everything Napwake reads, writes and sends is in [Data and access](https://docs.napwake.dev/next/reference/data-and-access/).

## Example production configuration

values-production.yaml

```yaml
replicaCount: 2


leaderElection:
  enabled: true


timezone: Europe/London


resources:
  requests:
    cpu: 100m
    memory: 256Mi
  limits:
    cpu: "1"
    memory: 1Gi


metrics:
  enabled: true
  secure: true
  readerSubjects:
    - kind: ServiceAccount
      name: prometheus-k8s
      namespace: monitoring
  serviceMonitor:
    enabled: true
    interval: 15s
  prometheusRule:
    enabled: true


protectedNamespaces: ["prod-*"]


affinity:
  podAntiAffinity:
    preferredDuringSchedulingIgnoredDuringExecution:
      - weight: 100
        podAffinityTerm:
          labelSelector:
            matchLabels:
              app.kubernetes.io/name: napwake
              control-plane: controller-manager
          topologyKey: kubernetes.io/hostname
```

`affinity` applies to the doorman too, unless `doorman.affinity` is set. Its replicas spread across nodes by default.

```bash
helm install napwake oci://ghcr.io/napwake/charts/napwake \
  --version 0.2.0-rc.9 \
  -f values-production.yaml \
  -n napwake-system --create-namespace
```

Next

* [Configuration →The flags these values set.](https://docs.napwake.dev/next/reference/configuration/)
* [Upgrading →Keeping your settings across versions.](https://docs.napwake.dev/next/operations/upgrading/)
* [Data and access →What Napwake reads, writes and sends.](https://docs.napwake.dev/next/reference/data-and-access/)
