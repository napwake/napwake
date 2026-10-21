# Configuration Reference

## Operator Flags

These flags are passed to the binary (`/manager`), which runs as the operator, as the doorman with `--doorman`, or as a [forwarder](../concepts/wake-on-request.md#gke-container-native-load-balancing-negs) with `--forwarder`. The Helm chart sets them from its [values](helm-values.md); you only need them for your own manifests.

| Flag | Default | Description |
|------|---------|-------------|
| `--metrics-bind-address` | `0` (disabled) | Address for the metrics endpoint. Use `:8443` for HTTPS or `:8080` for HTTP. The chart and the Kustomize manifests use `:8443` |
| `--metrics-secure` | `true` | Serve metrics over HTTPS, only to callers allowed to `get` `/metrics`, checked with a TokenReview and a SubjectAccessReview. `false` serves plain HTTP |
| `--metrics-cert-path` | | Directory containing the metrics server's TLS certificate. Without it, a self-signed certificate is generated |
| `--metrics-cert-name` | `tls.crt` | Metrics certificate file name |
| `--metrics-cert-key` | `tls.key` | Metrics key file name |
| `--enable-http2` | `false` | Allow HTTP/2 for the metrics server |
| `--health-probe-bind-address` | `:8081` | Address for the health and readiness probes |
| `--leader-elect` | `false` | Elect a leader, so only one replica reconciles. A leader that stops releases its lease, so the next replica, or the new one in a rollout, takes over at once. Ignored by the doorman |
| `--max-concurrent-reconciles` | `8` | How many ManagedWorkloads are reconciled at once, so one slow metrics or Prometheus query doesn't hold up wakes, which are queued ahead of periodic checks. At least 1 |
| `--timezone` | `UTC` | IANA time zone, such as `Europe/London`, whose hours and weekdays forecasts learn, so they follow daylight saving. The time zone database is built in |
| `--prometheus-url` | | Base URL of the Prometheus API, such as `http://prometheus.monitoring.svc:9090`; `http` or `https` with a host. Required for workloads that set `idlePolicy.activity.prometheus`; with it, each new workload's forecast also starts from up to four weeks of its CPU history (see [Learning from history](../concepts/forecasting.md#learning-from-history)) |
| `--watch-namespaces` | every namespace | Comma-separated namespaces to work in, with a Role in each |
| `--protected-namespaces` | | Comma-separated name patterns, such as `prod-*`, of namespaces Hybernate never manages, as if labelled `hybernate.io/protected=true`, unless labelled `hybernate.io/allow-protected=true`. The `kube-*` namespaces, `$POD_NAMESPACE`, and the doorman's namespace are always protected besides; see [Protected namespaces](../guides/opt-in.md#protected-namespaces) |
| `--default-idle-after` | `1h` | Idle time before pausing, for workloads opted in with the label whose annotations and namespace don't set it. At least `1m` |
| `--default-cpu-threshold` | `10` | CPU percentage of requests that counts as active, for opted-in workloads that don't set it. 1 to 100 |
| `--default-dry-run` | `false` | Measure opted-in workloads without pausing, unless they or their namespace set `hybernate.io/dry-run` |
| `--default-forecast` | `balanced` | How soon the forecast acts, `eager`, `balanced` or `cautious`, for opted-in workloads that don't set `hybernate.io/forecast` |
| `--doorman` | `false` | Run as the doorman instead of the operator. The doorman Deployment sets it |
| `--doorman-service` | `hybernate-doorman` | Name of the doorman's Service, which the operator routes paused workloads to. Empty disables waking on request |
| `--doorman-ports-per-workload` | `32` | The most doorman ports one workload may hold, one per routed Service port. A workload with more isn't routed (`WakeOnRequest=False`, reason `TooManyPorts`) |
| `--doorman-ports-per-namespace` | `256` | The most doorman ports the workloads of one namespace may hold together |
| `--doorman-namespace` | `$POD_NAMESPACE`, else `hybernate-system` | Namespace of the doorman's Service |
| `--doorman-health-check-user-agents` | | Comma-separated `User-Agent` prefixes of health checkers and scrapers, besides the built-in ones, whose `GET` and `HEAD` requests don't wake a paused workload. Helm value `doorman.healthCheckUserAgents` |
| `--neg-forwarder` | `false` | Route paused workloads' Services that sit behind GKE container-native load balancing (NEGs) through a forwarder Deployment the operator runs in each workload's namespace, instead of leaving them unrouted. Needs `create` and `delete` on Deployments. Helm value `doorman.negForwarder.enabled`; see [GKE container-native load balancing](../concepts/wake-on-request.md#gke-container-native-load-balancing-negs) |
| `--neg-forwarder-image` | | The image forwarders run: the operator's own. Required with `--neg-forwarder` |
| `--neg-forwarder-replicas` | `2` | Pods per forwarder. At least 1 |
| `--neg-forwarder-resources` | `{"requests":{"cpu":"10m","memory":"32Mi"},"limits":{"memory":"64Mi"}}` | Each forwarder pod's resources, as JSON |
| `--neg-forwarder-doorman-service` | `hybernate-doorman-pods` | Headless Service in `--doorman-namespace` selecting the doorman's pods, whose DNS name forwarders resolve to reach them |
| `--forwarder` | `false` | Run as a forwarder instead of the operator. The operator's forwarder Deployments set it, with `--forward`, the routes as comma-separated `listen=doorman` port pairs, and `--forwarder-doorman`, the DNS name of the doorman's pods |
| `--guard` | | Run as the chart's `uninstall` or `upgrade` hook instead of the operator. It waits up to two and a half minutes for ManagedWorkloads being deleted, then exits 1, listing those the operation would leave without an operator: for `uninstall`, every one in `--watch-namespaces` (every namespace when empty) or carrying the `hybernate.io/cleanup` finalizer; for `upgrade`, those carrying the finalizer outside the new `--watch-namespaces`. The chart's `guard.uninstall` and `guard.upgrade` hooks set it |
| `--kubeconfig` | in-cluster | Path to a kubeconfig, only when running outside the cluster |
| `--zap-log-level` | `info` | Log level: `debug`, `info`, `error`, `panic`, or a whole number above 0 for more verbose debug levels |
| `--zap-encoder` | `json` | Log format: `json` or `console` |
| `--zap-devel` | `false` | Development defaults: console encoding, debug level, stack traces from warnings |
| `--zap-stacktrace-level` | `error` | Level from which stack traces are logged: `info`, `error`, or `panic` |
| `--zap-time-encoding` | RFC 3339 | Time format of log entries: `epoch`, `millis`, `nano`, `iso8601`, `rfc3339` or `rfc3339nano` |

The operator checks its flags at startup and exits with an error naming the flag when one is invalid: a `--protected-namespaces` pattern that isn't a valid glob, a `--prometheus-url` that isn't `http` or `https` with a host, a `--default-idle-after` under `1m`, a `--default-cpu-threshold` outside 1 to 100, a `--default-forecast` that isn't `eager`, `balanced` or `cautious`, a `--max-concurrent-reconciles`, `--doorman-ports-per-workload` or `--doorman-ports-per-namespace` below 1, a `--timezone` that isn't a known IANA zone, or `--neg-forwarder` without `--neg-forwarder-image` or the doorman, with a `--neg-forwarder-replicas` below 1, or with `--neg-forwarder-resources` that aren't JSON requests and limits.

## Endpoints

| Endpoint | Port | Description |
|----------|------|-------------|
| `/healthz` | 8081 | Liveness probe. Returns 200 while the process is running |
| `/readyz` | 8081 | Readiness probe. For the doorman, 200 once it has loaded its routes and reached the API server in the last 30 seconds; failing while it can't listen on a routed port for more than 30 seconds, and while it shuts down. For a forwarder, 200 while it reaches the doorman on every port it passes connections to, and failing from the moment it starts shutting down; its probes are on 8081, or the next port the workload doesn't serve |
| `/metrics` | 8443 in the chart | Prometheus metrics, HTTPS by default; see [Metrics](metrics.md) |

## Leader Election

For HA deployments with multiple replicas, enable leader election:

```
--leader-elect=true
```

The leader election ID is `479a98fc.hybernate.io`, held as a Lease in the operator's namespace. Only the leader runs reconciliation loops; standby replicas take over if the leader fails.

The doorman doesn't use leader election: every replica serves traffic.

## TLS Configuration

### Metrics

By default, metrics are served over HTTPS, and a scraper needs a token allowed to `get` the `/metrics` URL; the chart's `metrics.readerSubjects` binds one, see [Monitoring](../operations/monitoring.md). To use HTTP (not recommended for production):

```
--metrics-secure=false --metrics-bind-address=:8080
```

### Custom Certificates

For your own TLS certificate, instead of a self-signed one:

```
--metrics-cert-path=/certs/metrics
```

## Logging

The operator and the doorman log JSON by default, through `logr` (controller-runtime). Key fields in log entries:

| Field | Description |
|-------|-------------|
| `name`, `namespace` | The object a reconcile is for, such as the ManagedWorkload, added by controller-runtime to every entry it logs, with `controller` and `reconcileID` |
| `workload` | The workload or ManagedWorkload an entry is about, on entries that name it themselves, with its `namespace` |
| `from`, `to` | The phases of a `phase transition` entry |
| `reason` | Why: what caused a phase transition |
| `phase` | The phase after a reconcile, on `reconciled` entries at debug level (`--zap-log-level=debug`) |
| `error` | What went wrong, on entries about a failure |

For logs to read by eye, use `--zap-encoder=console` (the chart's `logEncoder: console`).
