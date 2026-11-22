# Labels and annotations

> Every label, annotation and finalizer Napwake reads or writes: the values it takes, where it goes, and what it does.

Source: https://docs.napwake.dev/next/reference/labels-annotations/

[Labels 9](https://docs.napwake.dev/next/reference/labels-annotations/#labels)

* [`napwake.dev/managed`](https://docs.napwake.dev/next/reference/labels-annotations/#label-napwake-dev-managed)
* [`napwake.dev/ignore`](https://docs.napwake.dev/next/reference/labels-annotations/#label-napwake-dev-ignore)
* [`napwake.dev/protected`](https://docs.napwake.dev/next/reference/labels-annotations/#label-napwake-dev-protected)
* [`napwake.dev/allow-protected`](https://docs.napwake.dev/next/reference/labels-annotations/#label-napwake-dev-allow-protected)
* [`napwake.dev/from-label`](https://docs.napwake.dev/next/reference/labels-annotations/#label-napwake-dev-from-label)
* [`napwake.dev/managed-workload`](https://docs.napwake.dev/next/reference/labels-annotations/#label-napwake-dev-managed-workload)
* [`napwake.dev/operator`](https://docs.napwake.dev/next/reference/labels-annotations/#label-napwake-dev-operator)
* [`napwake.dev/forwarded`](https://docs.napwake.dev/next/reference/labels-annotations/#label-napwake-dev-forwarded)
* [`app.kubernetes.io/name`](https://docs.napwake.dev/next/reference/labels-annotations/#label-app-kubernetes-io-name)

[Settings 10](https://docs.napwake.dev/next/reference/labels-annotations/#settings-annotations)

* [`napwake.dev/dry-run`](https://docs.napwake.dev/next/reference/labels-annotations/#setting-napwake-dev-dry-run)
* [`napwake.dev/idle-after`](https://docs.napwake.dev/next/reference/labels-annotations/#setting-napwake-dev-idle-after)
* [`napwake.dev/cpu-threshold`](https://docs.napwake.dev/next/reference/labels-annotations/#setting-napwake-dev-cpu-threshold)
* [`napwake.dev/auto-resume`](https://docs.napwake.dev/next/reference/labels-annotations/#setting-napwake-dev-auto-resume)
* [`napwake.dev/pause-early`](https://docs.napwake.dev/next/reference/labels-annotations/#setting-napwake-dev-pause-early)
* [`napwake.dev/forecast`](https://docs.napwake.dev/next/reference/labels-annotations/#setting-napwake-dev-forecast)
* [`napwake.dev/depends-on`](https://docs.napwake.dev/next/reference/labels-annotations/#setting-napwake-dev-depends-on)
* [`napwake.dev/wake`](https://docs.napwake.dev/next/reference/labels-annotations/#setting-napwake-dev-wake)
* [`napwake.dev/wake-max-wait`](https://docs.napwake.dev/next/reference/labels-annotations/#setting-napwake-dev-wake-max-wait)
* [`napwake.dev/wake-page`](https://docs.napwake.dev/next/reference/labels-annotations/#setting-napwake-dev-wake-page)

[Activity 4](https://docs.napwake.dev/next/reference/labels-annotations/#activity-annotations)

* [`napwake.dev/last-activity`](https://docs.napwake.dev/next/reference/labels-annotations/#activity-napwake-dev-last-activity)
* [`napwake.dev/active-until`](https://docs.napwake.dev/next/reference/labels-annotations/#activity-napwake-dev-active-until)
* [`napwake.dev/last-request`](https://docs.napwake.dev/next/reference/labels-annotations/#activity-napwake-dev-last-request)
* [`napwake.dev/last-request-from`](https://docs.napwake.dev/next/reference/labels-annotations/#activity-napwake-dev-last-request-from)

[Other annotations 9](https://docs.napwake.dev/next/reference/labels-annotations/#other-annotations)

* [`napwake.dev/pause-requested`](https://docs.napwake.dev/next/reference/labels-annotations/#annotation-napwake-dev-pause-requested)
* [`napwake.dev/pause-overrides-forecast`](https://docs.napwake.dev/next/reference/labels-annotations/#annotation-napwake-dev-pause-overrides-forecast)
* [`napwake.dev/wake-requested`](https://docs.napwake.dev/next/reference/labels-annotations/#annotation-napwake-dev-wake-requested)
* [`napwake.dev/dependents-from`](https://docs.napwake.dev/next/reference/labels-annotations/#annotation-napwake-dev-dependents-from)
* [`napwake.dev/paused-from`](https://docs.napwake.dev/next/reference/labels-annotations/#annotation-napwake-dev-paused-from)
* [`napwake.dev/turned-away`](https://docs.napwake.dev/next/reference/labels-annotations/#annotation-napwake-dev-turned-away)
* [`napwake.dev/ignore-dependencies`](https://docs.napwake.dev/next/reference/labels-annotations/#annotation-napwake-dev-ignore-dependencies)
* [`napwake.dev/operator`](https://docs.napwake.dev/next/reference/labels-annotations/#annotation-napwake-dev-operator)
* [`napwake.dev/doorman-ignore-ports`](https://docs.napwake.dev/next/reference/labels-annotations/#annotation-napwake-dev-doorman-ignore-ports)

[Finalizers 1](https://docs.napwake.dev/next/reference/labels-annotations/#finalizers)

* [`napwake.dev/cleanup`](https://docs.napwake.dev/next/reference/labels-annotations/#finalizer-napwake-dev-cleanup)

## Labels

| Label                          | Value                | On                                    | Description                                                                                                                                                                                                                                                                                                           |
| ------------------------------ | -------------------- | ------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `napwake.dev/managed`          | `"true"`             | Deployments, StatefulSets, Namespaces | Opts in the workload, or every Deployment and StatefulSet in the namespace, with a ManagedWorkload made from its annotations. `"false"` on a workload leaves it out of a managed namespace, like `napwake.dev/ignore`. Other values don’t opt in, and an event says so ([Opting in](https://docs.napwake.dev/next/guides/opt-in/))            |
| `napwake.dev/ignore`           | `"true"`             | Deployments, StatefulSets             | Leaves the workload out, even in a managed namespace, scaled back up first if paused. A ManagedWorkload you wrote for it does nothing and reports `TargetAvailable=False`, reason `TargetIgnored`                                                                                                                     |
| `napwake.dev/protected`        | `"true"`             | Namespaces                            | Nothing here is managed, whatever its labels, and what was paused wakes. `kube-*`, `openshift-*`, `cattle-*`, `gke-*`, `gmp-*` and Napwake’s own namespace are protected without it ([Protected namespaces](https://docs.napwake.dev/next/guides/opt-in/#protected-namespaces))                                                               |
| `napwake.dev/allow-protected`  | `"true"`             | Namespaces                            | Lets Napwake manage a namespace labelled protected, matching `protectedNamespaces`, or always protected, such as `kube-system` or its own. The operator’s and doorman’s Deployments are never managed. Set it on purpose. Napwake never does                                                                          |
| `napwake.dev/from-label`       | `"true"`             | ManagedWorkloads                      | On ManagedWorkloads made from the `managed` label, the only ones Napwake updates and deletes. Those without it are yours                                                                                                                                                                                              |
| `napwake.dev/managed-workload` | ManagedWorkload name | EndpointSlices                        | On the EndpointSlices routing a paused workload’s Services to the doorman or its forwarder, with `endpointslice.kubernetes.io/managed-by: doorman.napwake.dev`. Names over 63 characters are shortened with a hash. Removed once Running                                                                              |
| `napwake.dev/operator`         | namespace            | EndpointSlices, forwarder pods        | The install’s namespace, on the doorman EndpointSlices an operator writes and its [forwarders’](https://docs.napwake.dev/next/concepts/wake-on-request/#gke-container-native-load-balancing-negs) pods. Another install doesn’t move them to its doorman ([More than one install](https://docs.napwake.dev/next/getting-started/installation/#more-than-one-install)) |
| `napwake.dev/forwarded`        | `"true"`             | EndpointSlices                        | On a doorman EndpointSlice pointing a Service behind GKE NEGs at the workload’s [forwarder](https://docs.napwake.dev/next/concepts/wake-on-request/#gke-container-native-load-balancing-negs), not the doorman                                                                                                                                |
| `app.kubernetes.io/name`       | `napwake-forwarder`  | Deployments, pods                     | On each forwarder Deployment the operator runs beside a paused workload behind GKE NEGs, and its pods. The Deployment also has `napwake.dev/ignore: "true"`, so Napwake doesn’t manage it in a namespace opted in by label                                                                                            |

### Usage

```bash
kubectl label deployment my-api napwake.dev/managed=true
kubectl label namespace preview-42 napwake.dev/managed=true
```

Opt in a workload, or a namespace.

```bash
kubectl label deployment my-critical-service napwake.dev/ignore=true
```

Leave one out of a managed namespace.

```bash
kubectl get managedworkloads -l napwake.dev/from-label=true --all-namespaces
```

List the ManagedWorkloads made from labels.

## Settings annotations

On an opted-in Deployment or StatefulSet, or its Namespace for all its workloads. A setting is the first of these that can be read ([Opting in](https://docs.napwake.dev/next/guides/opt-in/#settings), [Helm defaults](https://docs.napwake.dev/next/reference/helm-values/#opt-in-defaults)).

1. 1The workload's annotation
2. 2Its namespace's
3. 3The Helm defaultsonly `idle-after`, `cpu-threshold`, `dry-run`, `pause-early`, `forecast` and `wake` have one
4. 4The built-in default

| Annotation                  | Accepted values                                                                          | Default      | Description                                                                                                                                                                                                                                                                                                        |
| --------------------------- | ---------------------------------------------------------------------------------------- | ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `napwake.dev/dry-run`       | exactly `"true"` or `"false"`                                                            | `"false"`    | Measure without pausing                                                                                                                                                                                                                                                                                            |
| `napwake.dev/idle-after`    | a Go duration of at least `1m`, such as `"90m"` or `"2h"`                                | `1h`         | Time without activity before pausing                                                                                                                                                                                                                                                                               |
| `napwake.dev/cpu-threshold` | a percentage from `"0.1"` to `"100"`, to two places, such as `"10"` or `"0.5"`           | `10`         | CPU % of requests above which the workload is active                                                                                                                                                                                                                                                               |
| `napwake.dev/auto-resume`   | exactly `"true"` or `"false"`                                                            | `"false"`    | Wake ahead of forecast demand                                                                                                                                                                                                                                                                                      |
| `napwake.dev/pause-early`   | exactly `"true"` or `"false"`                                                            | `"false"`    | Pause after 20 idle minutes, before `idle-after`. Only when a confident forecast expects quiet until then, and the doorman would wake it. Sets `spec.idlePolicy.pauseEarly` ([Pausing early](https://docs.napwake.dev/next/concepts/forecasting/#pausing-early)). Never with `napwake.dev/wake: "ask"`                                     |
| `napwake.dev/forecast`      | exactly `"eager"`, `"balanced"` or `"cautious"`                                          | `"balanced"` | How soon the forecast acts, at confidence 60, 75 or 85. Eager acts sooner, waking it for nothing more often. Cautious acts later, for workloads costly to keep awake. Sets `spec.prediction.mode`, which `spec.prediction.confidence` overrides ([Forecast setting](https://docs.napwake.dev/next/concepts/forecasting/#forecast-setting)) |
| `napwake.dev/depends-on`    | comma-separated `kind/name` or `namespace/kind/name`, kind `deployment` or `statefulset` | none         | Workloads this one needs. Empty declares none, dropping the namespace’s. On a namespace, it leaves out the workloads it names, and those they depend on by their own `depends-on`. They’re dependencies, not dependents. One in another namespace needs its `napwake.dev/dependents-from`                          |
| `napwake.dev/wake`          | exactly `"request"` or `"ask"`                                                           | `"request"`  | What wakes it through the doorman. `"request"` is any request. `"ask"` is only a person’s click on **Wake it**. Sets `spec.wake.mode` ([Asking before waking](https://docs.napwake.dev/next/concepts/wake-on-request/#asking-before-waking))                                                                                               |
| `napwake.dev/wake-max-wait` | a Go duration above zero, at most `1h`                                                   | `2m`         | How long a request is held while it wakes                                                                                                                                                                                                                                                                          |
| `napwake.dev/wake-page`     | exactly `"true"` or `"false"`                                                            | `"true"`     | Show browsers a waking-up page                                                                                                                                                                                                                                                                                     |

Values that can’t be read, such as `"True"`, `"yes"`, `"soon"` or `"0"`.

| Annotation   | A value that can’t be read                                                                                                                                                                 |
| ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Any setting  | Skipped, with an `InvalidSetting` warning event on the workload. The setting comes from the next place in the order, never the default in place of a namespace’s value. Others still apply |
| `dry-run`    | Turns dry-run on, wherever it is. A typo in the setting meant to stop pauses must never start them                                                                                         |
| `wake`       | Asks before waking, wherever it is. A typo in the setting meant to keep robots out must never let them in                                                                                  |
| `depends-on` | A bad entry is skipped, the rest apply. Only a value with no readable entry falls through to the namespace’s                                                                               |

## Activity annotations

| Annotation                      | Value                                       | On                                          | Description                                                                                                                                                                                                                                                                                       |
| ------------------------------- | ------------------------------------------- | ------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `napwake.dev/last-activity`     | RFC 3339 time                               | ManagedWorkloads, Deployments, StatefulSets | Activity another tool saw, such as a developer portal. A future time counts as now. A change while paused wakes it ([Activity annotations](https://docs.napwake.dev/next/concepts/idle-detection/#activity-annotations))                                                                                                  |
| `napwake.dev/active-until`      | RFC 3339 time                               | ManagedWorkloads, Deployments, StatefulSets | Awake until then, woken if paused                                                                                                                                                                                                                                                                 |
| `napwake.dev/last-request`      | RFC 3339 time                               | ManagedWorkloads                            | Set by the doorman when it holds a request for a paused workload, and with `doorman.admissionPolicy.enabled` by no one else. To wake a workload yourself, set `wake-requested`. Wakes it like `last-activity`, the clock recording `request` ([Wake on request](https://docs.napwake.dev/next/concepts/wake-on-request/)) |
| `napwake.dev/last-request-from` | IP address                                  | ManagedWorkloads                            | Where the waking request came from, set by the doorman with `last-request` and, like it, by no one else. The operator [learns](https://docs.napwake.dev/next/concepts/dependencies/#learned-dependencies) from it which workload depends on this one                                                                      |
| The three that take a time      | not an RFC 3339 time, such as epoch seconds | as above                                    | Ignored. It neither keeps the workload awake nor wakes it. An `InvalidActivityAnnotation` warning event on the ManagedWorkload says so, once per value                                                                                                                                            |

## Other annotations

| Annotation                             | Value                                                                       | On                                          | Description                                                                                                                                                                                                                                                                                                                                                                                      |
| -------------------------------------- | --------------------------------------------------------------------------- | ------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `napwake.dev/pause-requested`          | any value that differs from the last request’s, such as the time            | ManagedWorkloads                            | Pause now, as if the idle clock had run out. Set by `kubectl napwake pause`. Each value acts once, recorded in `status.lastPauseRequest`. An annotation, so a GitOps tool owning the spec leaves it alone ([Pause now](https://docs.napwake.dev/next/guides/pause/#pause-now))                                                                                                                                           |
| `napwake.dev/pause-overrides-forecast` | `"true"`                                                                    | ManagedWorkloads                            | With a pause request, pauses even when a confident forecast expects demand within the hour. Without it, such a request is declined, saying when. It applies to the request it comes with, so set or remove it with each. `kubectl napwake pause --yes` sets it ([Pause now](https://docs.napwake.dev/next/guides/pause/#pause-now))                                                                                      |
| `napwake.dev/wake-requested`           | any value that differs from the last request’s, such as the time            | ManagedWorkloads                            | Wake now, whatever the wake setting, as for a pull request bot, a portal or a chat command. An awake workload’s idle clock restarts. Set by `kubectl napwake wake`. Each value acts once, recorded in `status.lastWakeRequest`, and the `WakeRequest` condition says what came of it. An annotation, so a GitOps tool owning the spec leaves it alone ([Wake now](https://docs.napwake.dev/next/guides/pause/#wake-now)) |
| `napwake.dev/dependents-from`          | comma-separated namespace names, or `"*"` for any                           | Namespaces                                  | Lets those namespaces’ workloads depend on this one’s. A dependency in another namespace, declared or learned, needs its namespace to list the dependent’s. Only then is it held awake, woken and waited for. Otherwise the dependent reports `DependencyNotAllowed=True` ([Across namespaces](https://docs.napwake.dev/next/concepts/dependencies/#across-namespaces))                                                  |
| `napwake.dev/paused-from`              | JSON, set by Napwake                                                        | Deployments, StatefulSets                   | What the pause changed, the replicas and the KEDA ScaledObject with its own `paused-replicas`. Written before scaling to zero, removed when handed back. Napwake takes up from it a pause whose status is lost, as in a backup restored without status. So the workload still wakes to its replicas. Don’t set or edit it                                                                        |
| `napwake.dev/turned-away`              | JSON, set by the doorman                                                    | ManagedWorkloads                            | Where the doorman counts the requests it turned away during a pause, for a workload with `wake.mode: Ask`. The operator copies it into `status.turnedAway`. Don’t set or edit it ([What it turns away](https://docs.napwake.dev/next/concepts/wake-on-request/#what-it-turns-away))                                                                                                                                      |
| `napwake.dev/ignore-dependencies`      | comma-separated `namespace/name`, or a name in the workload’s own namespace | Deployments, StatefulSets, ManagedWorkloads | Dependencies Napwake shouldn’t [learn](https://docs.napwake.dev/next/concepts/dependencies/#learned-dependencies)                                                                                                                                                                                                                                                                                                        |
| `napwake.dev/operator`                 | namespace, set by Napwake                                                   | ManagedWorkloads                            | The install that took it on, written with its `napwake.dev/cleanup` finalizer. Other installs leave it alone and don’t count it in their uninstall and upgrade guards ([More than one install](https://docs.napwake.dev/next/getting-started/installation/#more-than-one-install))                                                                                                                                       |
| `napwake.dev/doorman-ignore-ports`     | comma-separated port names or numbers                                       | Services                                    | Ports the doorman never routes, such as a metrics port scraped elsewhere. They fail while paused ([what wakes a workload](https://docs.napwake.dev/next/concepts/wake-on-request/#what-wakes-a-workload-and-what-doesnt))                                                                                                                                                                                                |

## Finalizers

| Finalizer             | On               | Description                                                                                                                                                        |
| --------------------- | ---------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `napwake.dev/cleanup` | ManagedWorkloads | Added by the operator. It’s removed once a paused workload is scaled back up and its Services stop routing to the doorman. Then the ManagedWorkload can be deleted |

Caution

Don’t remove `napwake.dev/cleanup` by hand. Without it, a deleted ManagedWorkload leaves a paused workload at zero replicas, and its KEDA ScaledObject held there. They stay so until a new ManagedWorkload for it takes the pause up from `napwake.dev/paused-from`.

Next

* [Opting in →Labels and settings in use.](https://docs.napwake.dev/next/guides/opt-in/)
* [Opt-in defaults →The Helm defaults for six settings.](https://docs.napwake.dev/next/reference/helm-values/#opt-in-defaults)
* [API reference →The ManagedWorkload these labels create.](https://docs.napwake.dev/next/reference/api/)
