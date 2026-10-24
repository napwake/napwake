# Labels & Annotations Reference

## Labels

| Label | Value | Applied To | Description |
|-------|-------|-----------|-------------|
| `napwake.dev/managed` | `"true"` | Deployments, StatefulSets, Namespaces | Opts the workload, or every Deployment and StatefulSet in the namespace, in. Napwake creates a ManagedWorkload for it from its annotations. `"false"` on a workload leaves it out of a managed namespace, like `napwake.dev/ignore`. Any other value doesn't opt in, and is pointed out in an event. See [Opting In](../guides/opt-in.md). |
| `napwake.dev/ignore` | `"true"` | Deployments, StatefulSets | Leaves the workload out, even in a managed namespace. A ManagedWorkload you wrote for it reports `TargetAvailable=False`, reason `TargetIgnored`, and does nothing; a workload Napwake had paused is scaled back up first. |
| `napwake.dev/protected` | `"true"` | Namespaces | Napwake never manages anything in the namespace, whatever its workloads' labels, and wakes what it had paused there. `kube-*` namespaces and Napwake's own are protected without it. See [Protected namespaces](../guides/opt-in.md#protected-namespaces). |
| `napwake.dev/allow-protected` | `"true"` | Namespaces | Lets Napwake manage a namespace that's labelled protected, matches `protectedNamespaces`, or is always protected, such as `kube-system` or Napwake's own. The operator's and the doorman's own Deployments are never managed. Set on purpose; Napwake never sets it. |
| `napwake.dev/from-label` | `"true"` | ManagedWorkloads | Set on ManagedWorkloads Napwake created from the `managed` label. Napwake updates and deletes only these; ManagedWorkloads without it are yours. |
| `napwake.dev/managed-workload` | ManagedWorkload name | EndpointSlices | Set on the EndpointSlices that route a paused workload's Services to the doorman, or to its forwarder, along with `endpointslice.kubernetes.io/managed-by: doorman.napwake.dev`. A name longer than 63 characters is shortened, with a hash. They're removed when the workload is Running. |
| `napwake.dev/operator` | namespace | EndpointSlices, forwarder pods | Set on the doorman EndpointSlices an operator writes, and on its [forwarders'](../concepts/wake-on-request.md#gke-container-native-load-balancing-negs) pods: the namespace of its install. Another install doesn't move them to its own doorman. See [More than one install](../getting-started/installation.md#more-than-one-install) |
| `napwake.dev/forwarded` | `"true"` | EndpointSlices | Set on a doorman EndpointSlice that points a Service behind GKE NEGs at the workload's [forwarder](../concepts/wake-on-request.md#gke-container-native-load-balancing-negs) rather than the doorman |
| `app.kubernetes.io/name` | `napwake-forwarder` | Deployments, pods | Set on each forwarder Deployment the operator runs beside a paused workload behind GKE NEGs, and on its pods. The Deployment also carries `napwake.dev/ignore: "true"`, so a namespace opted in with its label doesn't have Napwake manage it |

### Usage

Opt a workload in, or a whole namespace:

```bash
kubectl label deployment my-api napwake.dev/managed=true
kubectl label namespace preview-42 napwake.dev/managed=true
```

Leave one workload in a managed namespace out:

```bash
kubectl label deployment my-critical-service napwake.dev/ignore=true
```

Find the ManagedWorkloads created from labels:

```bash
kubectl get managedworkloads -l napwake.dev/from-label=true --all-namespaces
```

## Settings Annotations

Set on an opted-in Deployment or StatefulSet, or on its Namespace to apply to every workload in it. Each setting is the first value that can be read from: the workload's annotation, its namespace's, the [Helm defaults](helm-values.md#opt-in-defaults) (only `idle-after`, `cpu-threshold`, `dry-run`, `pause-early`, `forecast` and `wake` have one), and the built-in default. See [Opting In](../guides/opt-in.md#settings).

| Annotation | Accepted values | Default | Description |
|------------|-----------------|---------|-------------|
| `napwake.dev/dry-run` | exactly `"true"` or `"false"` | `"false"` | Measure without pausing |
| `napwake.dev/idle-after` | a Go duration of at least `1m`, such as `"90m"` or `"2h"` | `1h` | How long without activity before pausing |
| `napwake.dev/cpu-threshold` | a whole number, `"1"` to `"100"` | `10` | CPU use, as a percentage of requests, above which the workload counts as active |
| `napwake.dev/auto-resume` | exactly `"true"` or `"false"` | `"false"` | Wake ahead of forecast demand |
| `napwake.dev/pause-early` | exactly `"true"` or `"false"` | `"false"` | Pause before `idle-after` runs out, once idle for 20 minutes, when a confident forecast expects the hours until then to be quiet and a request would wake the workload through the doorman, so never with `wake: ask`. Sets `spec.idlePolicy.pauseEarly`. See [Pausing Early](../concepts/forecasting.md#pausing-early) |
| `napwake.dev/forecast` | exactly `"eager"`, `"balanced"` or `"cautious"` | `"balanced"` | How soon the forecast acts: at a confidence of 60, 75 or 85. Eager acts sooner and wakes the workload for nothing more often; cautious acts later, for workloads costly to keep awake. Sets `spec.prediction.mode`; a `spec.prediction.confidence` set on the ManagedWorkload overrides it. See [Forecast setting](../concepts/forecasting.md#forecast-setting) |
| `napwake.dev/depends-on` | comma-separated `kind/name` or `namespace/kind/name`, kind `deployment` or `statefulset` | none | Workloads this one needs. An empty value declares none, so a workload can drop its namespace's. On a namespace, it leaves out the workloads it names, and those they depend on by their own `depends-on`, which are dependencies rather than dependents. One in another namespace needs that namespace's `napwake.dev/dependents-from` |
| `napwake.dev/wake` | exactly `"request"` or `"ask"` | `"request"` | What wakes the paused workload through the doorman: any request, or, with `ask`, only a person who clicks **Wake it** on the page the doorman shows a browser; everything else gets a `503` and wakes nothing. Sets `spec.wake.mode` to `Request` or `Ask`. See [Asking before waking](../concepts/wake-on-request.md#asking-before-waking) |
| `napwake.dev/wake-max-wait` | a Go duration above zero, at most `1h` | `2m` | How long a request is held while the workload wakes |
| `napwake.dev/wake-page` | exactly `"true"` or `"false"` | `"true"` | Show browsers a waking-up page |

A value that can't be read, such as `"True"`, `"yes"`, `"soon"` or `"0"`, is reported in an `InvalidSetting` warning event on the workload and skipped: the setting comes from the next place in the order above, never from the default in place of a namespace's value, and the other settings still apply. Two exceptions:

- **`dry-run`**: a value that can't be read turns dry-run on, wherever it is, since a typo in the setting meant to stop pauses must never start them.
- **`wake`**: a value that can't be read, such as `"Ask"` or `"true"`, has the doorman ask, wherever it is, since a typo must never let crawlers wake a workload meant to wake only for people.
- **`depends-on`**: an entry that can't be read is skipped and the rest apply; only a value with no readable entry at all falls through to the namespace's.

## Activity Annotations

| Annotation | Value | Applied To | Description |
|------------|-------|-----------|-------------|
| `napwake.dev/last-activity` | RFC 3339 time | ManagedWorkloads, Deployments, StatefulSets | Activity seen by another tool, such as a developer portal. A time in the future counts as now. A change while the workload is paused wakes it. See [Activity annotations](../concepts/idle-detection.md#activity-annotations) |
| `napwake.dev/active-until` | RFC 3339 time | ManagedWorkloads, Deployments, StatefulSets | Keeps the workload awake until that time, and wakes it if paused |
| `napwake.dev/last-request` | RFC 3339 time | ManagedWorkloads | Set by the doorman when it holds a request for a paused workload, and, with `doorman.admissionPolicy.enabled`, by no one else; set `napwake.dev/wake-requested` to wake a workload yourself. Wakes it like `last-activity`, and the clock records the wake as `request`. See [Wake on Request](../concepts/wake-on-request.md) |
| `napwake.dev/last-request-from` | IP address | ManagedWorkloads | Set by the doorman with `last-request`, and by no one else, like it: where the request that woke the workload came from, so the operator can [learn](../concepts/dependencies.md#learned-dependencies) which workload depends on it |
| `napwake.dev/turned-away` | JSON, set by the doorman | ManagedWorkloads | Where the doorman counts the requests it turned away during a pause, for a workload with `wake.mode: Ask`; the operator copies it into `status.turnedAway`. Don't set or edit it. See [What it turns away](../concepts/wake-on-request.md#what-it-turns-away) |

A value that isn't an RFC 3339 time, such as epoch seconds, is ignored: it neither keeps the workload awake nor wakes it, and an `InvalidActivityAnnotation` warning event on the ManagedWorkload says so, once per value.

## Other Annotations

| Annotation | Value | Applied To | Description |
|------------|-------|-----------|-------------|
| `napwake.dev/pause-requested` | any value that differs from the last request's, such as the time | ManagedWorkloads | Asks Napwake to pause the workload now, as if its idle clock had run out; set by `kubectl napwake pause`. Each value is acted on once and recorded in `status.lastPauseRequest`. An annotation, so a GitOps tool that owns the spec leaves it alone. See [Pause Now](../guides/pause.md#pause-now) |
| `napwake.dev/pause-overrides-forecast` | `"true"` | ManagedWorkloads | With a pause request, pauses the workload even when a confident forecast expects demand within the hour; without it, such a request is declined, saying when. It applies to the request it comes with, so set or remove it with every request; `kubectl napwake pause --yes` sets it. See [Pause Now](../guides/pause.md#pause-now) |
| `napwake.dev/wake-requested` | any value that differs from the last request's, such as the time | ManagedWorkloads | Asks Napwake to wake the workload now, whatever its `wake` setting, as a pull request bot, a portal or a chat command does for someone; set by `kubectl napwake wake`. An awake one has its idle clock restarted. Each value is acted on once and recorded in `status.lastWakeRequest`, and the `WakeRequest` condition says what came of it. An annotation, so a GitOps tool that owns the spec leaves it alone. See [Wake Now](../guides/pause.md#wake-now) |
| `napwake.dev/dependents-from` | comma-separated namespace names, or `"*"` for any | Namespaces | Lets workloads in those namespaces depend on this namespace's workloads. A dependency in another namespace, declared or learned, is only held awake, woken and waited for when its namespace lists the dependent's; otherwise the dependent reports `DependencyNotAllowed=True`. See [Across namespaces](../concepts/dependencies.md#across-namespaces) |
| `napwake.dev/paused-from` | JSON, set by Napwake | Deployments, StatefulSets | Written by Napwake on a workload it pauses, before scaling it to zero, and removed when it's handed back: a copy of what the pause changed (the replicas, and the KEDA ScaledObject with its own `paused-replicas` value). If the ManagedWorkload's status is lost while the workload is paused, such as to a backup restored without status, Napwake takes the pause up again from it, so the workload still wakes to the replicas it had. Don't set or edit it |
| `napwake.dev/ignore-dependencies` | comma-separated `namespace/name`, or a name in the workload's own namespace | Deployments, StatefulSets, ManagedWorkloads | Dependencies Napwake shouldn't learn for this workload. See [Learned dependencies](../concepts/dependencies.md#learned-dependencies) |
| `napwake.dev/operator` | namespace, set by Napwake | ManagedWorkloads | Written by the operator with its `napwake.dev/cleanup` finalizer: the namespace of the install that took the ManagedWorkload on. Another install leaves it alone, and doesn't count it in its uninstall and upgrade guards. See [More than one install](../getting-started/installation.md#more-than-one-install) |
| `napwake.dev/doorman-ignore-ports` | comma-separated port names or numbers | Services | Ports the doorman never routes, such as a metrics port something else scrapes. They fail while the workload is paused. See [Wake on Request](../concepts/wake-on-request.md#what-wakes-a-workload-and-what-doesnt) |

## Finalizers

| Finalizer | Applied To | Description |
|-----------|-----------|-------------|
| `napwake.dev/cleanup` | ManagedWorkloads | Added automatically by the operator. Ensures a paused workload is scaled back up, and its Services stop routing to the doorman, before the ManagedWorkload can be deleted. The operator removes it once that's done. |

!!! note
    Do not remove the `napwake.dev/cleanup` finalizer by hand. A ManagedWorkload deleted without it leaves a paused workload at zero replicas, and its KEDA ScaledObject held there, until a ManagedWorkload for the workload is created again and takes the pause up from its `napwake.dev/paused-from` annotation.
