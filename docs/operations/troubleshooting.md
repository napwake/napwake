# Troubleshooting

## Common Issues

### A labelled workload has no ManagedWorkload

`kubectl get managedworkloads -n <namespace>` doesn't list it. The opt-in controller says why in events on the Deployment or StatefulSet itself:

```bash
kubectl describe deployment <name> -n <namespace>
```

- **`LabelIgnored`, the label is on the pod template**: it was put on the pods, as a chart's `podLabels` does, where Napwake doesn't look. Put it on the Deployment or StatefulSet itself, or on its namespace; see [Opting In](../guides/opt-in.md).
- **`LabelIgnored`, a value that isn't `"true"`**: `napwake.dev/managed` opts in only with `"true"`; settings such as `dry-run` are annotations.
- **`Protected`**: its namespace is protected, such as `kube-system`; see [Protected namespaces](../guides/opt-in.md#protected-namespaces).
- **`LabelIgnored`, Napwake's own**: the operator's and the doorman's Deployments are never managed.
- **No event**: it's labelled `napwake.dev/ignore: "true"`, or someone wrote a ManagedWorkload for it, which wins over the label. With Kustomize, check the label reached the workload at all: `commonLabels` fails to apply to an existing workload (see [With Kustomize](../guides/opt-in.md#with-kustomize)).

### Workload is idle but not being paused

**Check 1: Is dry run enabled?**

```bash
kubectl get managedworkload my-api -n staging -o jsonpath='{.spec.dryRun}'
```

If `true`, the operator evaluates but doesn't act. To end it, run `kubectl napwake enable my-api -n staging`, which does the right thing however the workload was set up. On a ManagedWorkload created from the `napwake.dev/managed` label, `spec.dryRun` follows the `napwake.dev/dry-run` annotation on the workload, its namespace, or the cluster default (`defaults.dryRun`), so setting it on the ManagedWorkload is overwritten: set the annotation to `"false"` instead. A value that can't be read, such as `"False"`, counts as dry-run, with an `InvalidSetting` event on the workload. Only on a ManagedWorkload you wrote yourself do you set `spec.dryRun: false`.

**Check 2: When was it last active?**

```bash
kubectl get managedworkload my-api -n staging -o jsonpath='{.status.activity}'
```

`lastActivitySource` says what last kept it awake, and `pauseAt` is when it will pause if nothing else happens. If `pauseAt` keeps moving forward, something is still counting as activity:

- **`cpu`**: usage is above `activity.cpuThreshold`% of requests. Background work (polling, cron loops) can keep a workload busy; raise the threshold if that's expected. It takes a decimal, such as `"0.5"`.
- **`annotation`**: a `napwake.dev/last-activity` annotation is being refreshed, or `napwake.dev/active-until` is in the future. Check both the ManagedWorkload and its target.
- **`rollout`**: the pod template changed recently.
- **`prometheus`**: one of `activity.prometheus` returns a value above zero.
- **`unobserved`**: not activity, but a gap in watching longer than the clock allows: the operator was down or restarted, or CPU or a Prometheus query couldn't be read for longer than a couple of checks, so the clock restarted as watching resumed. An `Unobserved` event says how long the gap was. If it keeps moving, check the `MetricsAvailable` and `PrometheusAvailable` conditions; see [Restarts and outages](../concepts/idle-detection.md#restarts-and-outages).

**Check 3: Can Napwake read CPU?**

```bash
kubectl get managedworkload my-api -n staging \
  -o jsonpath='{.status.conditions[?(@.type=="MetricsAvailable")]}'
```

Without CPU data a busy workload looks idle, so Napwake doesn't act. `MetricsAvailable=False` says why:

- **`NoPodMetrics`**: the target's Ready pods have had no metrics for over 3 minutes. Check that metrics-server is installed and reports them: `kubectl top pods -n staging`.
- **`NoCPURequests`**: the target's containers request no CPU, in its running pods or, with none running, in its pod template, so utilization can't be measured. Set CPU requests on its containers, or a default in the namespace's LimitRange; a LimitRange default applies to pods created after it, so restart the workload's pods once it's set.
- **`MetricsUnavailable`**: the Metrics API itself failed. The condition message has the error; a missing `metrics.k8s.io` API means metrics-server isn't installed.

A target scaled to zero replicas is not an error: it's recorded as zero usage. Nor is `MetricsAvailable=Unknown` just after a wake or a rollout: new pods have no metrics until they're running and metrics-server has scraped them, so it's `NoReadyPods` while none is Ready, then `AwaitingPodMetrics` for up to 3 minutes, without a warning.

**Check 4: Is the forecast holding it awake?**

A confident forecast that expects demand in the hour under way or the next defers the pause. While it does, the `IdleVetoed` condition is `True`, and its message says when the forecast expects demand; an `IdleVetoed` event marks when the veto began.

```bash
kubectl get managedworkload my-api -n staging \
  -o jsonpath='{.status.conditions[?(@.type=="IdleVetoed")]}'
```

**Check 5: Is anything else holding it?**

`kubectl napwake status` gives the reason in its NEXT column. Otherwise:

- Without an `idlePolicy`, nothing pauses it on its own; [`kubectl napwake pause`](../guides/pause.md#pause-now) pauses it when you ask.
- `PrometheusAvailable=False`: a Prometheus activity query fails, or the operator has no `--prometheus-url`.
- `Protected=True`: its namespace is protected (`ProtectedNamespace`), it's Napwake's own operator or doorman (`NapwakeComponent`), which is never paused, or it's a StatefulSet whose `persistentVolumeClaimRetentionPolicy.whenScaled` is `Delete` (`PVCsDeletedOnScaleDown`), which would lose its PVCs to a pause: set `whenScaled: Retain` to let Napwake pause it.
- `GitOpsConflict=True`: a GitOps tool undid its last pause, and Napwake waits an hour before pausing again.

**Check 6: Is something depending on it?**

```bash
kubectl get managedworkload postgres -n staging \
  -o jsonpath='{.status.conditions[?(@.type=="HeldByDependents")]}'
```

`HeldByDependents=True` names the awake workloads that depend on this one, declared in `dependsOn` or [learned](../concepts/dependencies.md#learned-dependencies). It pauses once they have; `kubectl napwake deps postgres -n staging` shows each link and where it came from. `DependencyCycle=True` means two workloads depend on each other; remove the cycle. See [Dependencies](../concepts/dependencies.md).

### Workload stuck in Resuming

`WaitingForDependencies=True` means a `waitForReady` dependency isn't Ready yet; the condition names it and how many replicas are ready. Check that dependency's pods. A dependency that serves with some of its replicas Ready counts once its own resume has finished, or, if Napwake doesn't manage it, after 2 minutes; one with none Ready is waited for until the dependency's Deployment `progressDeadlineSeconds`, or 10 minutes for a StatefulSet, has passed since the resume began, and then the workload starts without it, with `WaitingForDependencies=False`, reason `DependencyWontStart`. Otherwise, the workload's own pods aren't becoming Ready. Resume completes once every replica it was restored to is Ready. Short of that, Napwake stops waiting 2 minutes after the first replica is Ready, or, while none is, once the Deployment's `progressDeadlineSeconds` has passed since the scale-up (10 minutes by default, and for a StatefulSet), and the workload is `Running` with the replicas that are Ready, possibly none, and `ResumeIncomplete=True`. A StatefulSet with the default `OrderedReady` pod management starts its pods one at a time, so a slow-starting one may have only its first pods Ready when the 2 minutes are up: it runs with those, and the rest follow. The `NapwakeWorkloadStuck` alert fires after 15 minutes, and `kubectl napwake status` lists it under `Stuck` after 10.

### Workload paused with pods still running

A pause is `Paused` once the workload's pods have stopped. `PauseIncomplete=True`, with a `PauseIncomplete` warning event in place of `Paused`, means some were still running after their termination grace period and a minute more (one pod after another for a StatefulSet), and Napwake stopped waiting so the workload isn't stuck `Pausing`: it's `Paused`, and wakes as usual. A StatefulSet with the default `OrderedReady` pod management stops its pods one at a time and won't stop the rest while one isn't Ready; a pod stuck terminating, such as on a finalizer or an unreachable node, holds a Deployment's. `kubectl describe` the pods to see why. It's set `False` once they've gone, or when the workload wakes.

### Workload resumed with replicas not Ready

`ResumeIncomplete=True`, with a `ResumeIncomplete` warning event, means the last resume stopped waiting with some replicas not Ready, as above: it's `Running` with those that are, its KEDA ScaledObject scales it again, and requests go to its Ready pods. With none Ready, the doorman no longer holds its requests either, so they fail until a pod is Ready. This is common for a workload that wasn't wholly Ready before it was paused, such as one with a pod crash-looping or unschedulable; `kubectl describe` its pods to see why. Once every replica is Ready, later or at the next resume, it's set `False` with an `AllReplicasReady` event.

### Waking a workload by hand

```bash
kubectl napwake wake my-api -n staging
```

It wakes the workload whatever its wake setting, and waits until it's Running, or tells you why Napwake won't wake it. The answer to the last request stays in the `WakeRequest` condition until the next, as for a pause below: `False`, with a message starting `not woken at` and the time, gives the reason. See [Wake Now](../guides/pause.md#wake-now) and the [kubectl plugin](../getting-started/kubectl-plugin.md#wake-a-workload).

### Pausing a workload by hand

```bash
kubectl napwake pause my-api -n staging
```

It waits until the workload is Paused, or prints why Napwake didn't pause it. The answer to the last request stays in the `PauseRequest` condition until the next request, saying what happened and when, such as `paused as requested at 2026-10-21 03:32 UTC`; a pause since, or a wake, doesn't change it:

```bash
kubectl get managedworkload my-api -n staging \
  -o jsonpath='{.status.conditions[?(@.type=="PauseRequest")]}'
```

`True` is `Paused`, or `AlreadyPaused` when it was paused already. `False`, with a message starting `not paused at` and the time, gives the reason: a confident forecast expects demand within the hour (`ForecastExpectsDemand`; `kubectl napwake pause --yes` pauses it anyway), workloads that depend on it are awake (`HeldByDependents`) or it's in a `dependsOn` cycle (`DependencyCycle`), a `napwake.dev/active-until` annotation holds it (`ActiveUntil`), it's in dry-run (`DryRun`) or protected (`Protected`), it's scaled to zero already (`ScaledToZero`), or its target is missing, ignored or managed by another ManagedWorkload (`TargetNotFound`, `TargetIgnored`, `DuplicateTarget`), or the request was already on the ManagedWorkload as Napwake took it on, as after a backup restored without status (`PredatesStatus`). Each request is answered once: fix the reason, then run `pause` again. See [Pause Now](../guides/pause.md#pause-now).

### Requests to a paused workload fail instead of waking it

```bash
kubectl get managedworkload my-api -n staging \
  -o jsonpath='{.status.conditions[?(@.type=="WakeOnRequest")]}'
```

- **No condition, or `NotPaused`**: the workload isn't paused, so nothing is routed yet.
- **A message starting `at its last pause:`**: the workload is awake now, and what follows is what its last pause found with its Services. The next pause meets it again unless it's fixed, so fix it now, by the reason below.
- **`NoServices`**: no Service with a selector and a ClusterIP selects the workload's pods on a TCP port, or every such port is in `napwake.dev/doorman-ignore-ports`. Headless Services aren't routed; see [Wake on Request](../concepts/wake-on-request.md#when-a-workload-isnt-routed). A workload no request reaches, such as a queue worker, can wake on a [Prometheus activity query](../guides/prometheus-signals.md#while-the-workload-is-paused) instead.
- **`ServedByOtherPods`**: another workload's Ready pods are behind the same Service, so requests go to them. See [Services shared with other workloads](../concepts/wake-on-request.md#services-shared-with-other-workloads).
- **`UnsupportedLoadBalancer`**, or a Service named in the message as not routed: the Service is behind GKE container-native load balancing, which can't use the doorman: a GKE Ingress uses it, it has standalone NEGs, or `cloud.google.com/neg-status` lists NEGs for it. GKE's own `cloud.google.com/neg: '{"ingress": true}'`, on every Service, doesn't count by itself. The optional [forwarder](../concepts/wake-on-request.md#gke-container-native-load-balancing-negs) routes it. See [On GKE](../concepts/wake-on-request.md#on-gke).
- **`ForwarderNotReady`**: the Service is behind GKE container-native load balancing, and the [forwarder](../concepts/wake-on-request.md#gke-container-native-load-balancing-negs) that stands in for the workload's pods has no Ready pod yet; the message says why. For a few seconds after a pause that's expected. Otherwise, look at its pods with `kubectl describe pods -n staging -l app.kubernetes.io/name=napwake-forwarder`: an image that can't be pulled needs the operator's image reachable from the workload's namespace, a quota or LimitRange there may refuse its pods (`doorman.negForwarder.resources`), and a pod that runs but isn't Ready can't reach the doorman, usually because of a NetworkPolicy; see [Network policies behind NEGs](../concepts/wake-on-request.md#network-policies-behind-negs).
- **`NodeLocalTraffic`**: the workload's Services have `internalTrafficPolicy: Local`, which sends each caller only to endpoints on its own node, so they aren't routed. See [Wake on Request](../concepts/wake-on-request.md#when-a-workload-isnt-routed).
- **`UnsupportedIPFamily`**: no doorman pod has an address in the Service's IP family, such as an IPv6-only Service in a cluster whose doorman pods have only IPv4 addresses.
- **`DoormanUnavailable`**: no doorman pod is Ready. Check `kubectl get pods -A -l control-plane=doorman`.
- **`NoFreePort`**: every doorman port (20000-29999) is in use.
- **`TooManyPorts`**: the workload would hold more than `doorman.portsPerWorkload` doorman ports (32 by default), one per Service port, or its namespace more than `doorman.portsPerNamespace` (256); the message says which. Raise the cap, or list ports that needn't wake it in `napwake.dev/doorman-ignore-ports`.
- **`RoutingFailed`**: writing the doorman's EndpointSlices failed; the message has the error, and it's retried with backoff. Check the operator's RBAC and logs.
- **`DoormanDisabled`**: the doorman isn't installed (`doorman.enabled: false`).
- **`TargetNotFound`**: the Deployment or StatefulSet is gone, so there's nothing to wake.

If the condition is `DoormanRouted` but requests still fail:

- **A `503` whose body says the environment is asleep, and how to wake it**: the workload's `wake.mode` is `Ask`, so the doorman wakes it only for a person who clicks **Wake it** on the page it shows a browser, or for `napwake.dev/wake-requested` and `kubectl napwake wake`. That's what it's for: see [Asking before waking](../concepts/wake-on-request.md#asking-before-waking). If something that isn't a browser must wake it, such as another service or a webhook, set `wake.mode: Request`, or `napwake.dev/wake: request` with the label. `status.turnedAway` counts these requests.

- **The request is closed after a while with no response**: the workload didn't become Ready within `wake.maxWait`, or its pods couldn't be reached. A `RequestNotServed` warning event says which, and `napwake_doorman_wakes_total{result=~"timeout|error"}` counts these. Raise `maxWait`, or check why the pods are slow to become Ready.
- **The connection is closed at once, with nothing sent**: a [limit](../concepts/wake-on-request.md#limits) on held connections or wakes was hit; `napwake_doorman_wakes_total{result="limited"}` counts these.
- **An error from the app itself, such as a `503` with the app's own body, on the first requests after a wake**: its pods were Ready before it could answer, as a pod with no readiness probe is as soon as its containers start. A `NoReadinessProbe` warning event, and the `WakeOnRequest` message, say when that's so. Give the app a readiness probe that passes only once it can serve; see [Readiness probes](../concepts/wake-on-request.md#readiness-probes).
- **A `503` with no body**: the caller's `User-Agent` looks like a health check or scraper, such as `kube-probe/` or `Prometheus/`, which the doorman never lets wake a workload. It answers them this way only while none of the workload's pods is Ready.
- **A 502 from ingress-nginx, or a refused request, as the workload paused**: a pod exited the moment it got `SIGTERM`, before kube-proxy and nginx dropped it. The doorman stands in front of the Service before the pods stop, so it's the pod's shutdown. Give the app a `preStop` sleep of a few seconds; see [Work in Progress at a Pause](../guides/pause.md#work-in-progress-at-a-pause).
- **A 502 from a GKE Ingress or GKE Gateway while the workload is paused**: its Service is in NEGs and the optional forwarder isn't on, so the Service isn't routed and Google's load balancer has no healthy backend; see [On GKE](../concepts/wake-on-request.md#on-gke). Right after a pause, GKE also takes up to a minute to see the doorman, or the forwarder, pass its health checks.
- **A 504 from an Ingress after 60 seconds**: the ingress controller gave up first. Raise its upstream timeout, for ingress-nginx `nginx.ingress.kubernetes.io/proxy-read-timeout`.
- **The request is refused or times out right away, every time**: a NetworkPolicy may block traffic to the doorman (ports 20000-29999) or from it to the workload's pods. See [Network policies](../concepts/wake-on-request.md#network-policies).

The doorman's logs name the workload and Service for each held connection: `kubectl logs -n napwake-system -l control-plane=doorman` (use the namespace you installed into).

### Workload keeps cycling between paused and running

Something keeps waking it. Check the events for `WokeByActivity` (an activity annotation changed since the pause, or a held request: `WokenByRequest` names the Service) or `AutoResume` (the forecast expected demand). A health check or monitor that calls the Service on a timer and sends a request wakes the workload each time. The doorman already ignores connections that close without sending anything, and HTTP requests from common probes and scrapers (see [Wake on Request](../concepts/wake-on-request.md#what-wakes-a-workload-and-what-doesnt)); for anything else, point it elsewhere, or list its port in `napwake.dev/doorman-ignore-ports` on the Service. On a public URL, where crawlers and scanners wake it, have the doorman [ask before waking](../concepts/wake-on-request.md#asking-before-waking) it, with `wake.mode: Ask` (`napwake.dev/wake: ask`), so only a person's click does. A tool that refreshes `napwake.dev/last-activity` on a timer, rather than on real use, will keep waking the workload.

A `PausedWhileInUse` warning means its use undid a pause soon after it: see [Workload paused while in use](#workload-paused-while-in-use).

`ScaledUp` means something scaled it up outside Napwake, named in the event and `status.lastScaledUp`. If it's a GitOps tool, the workload also reports `GitOpsConflict=True`: it sets the replicas from Git and will undo every pause until it's told to leave them; see [GitOps tools](../guides/gitops.md).

### Workload paused while in use

A `PausedWhileInUse` warning event, and `kubectl napwake status` under Needs attention, say Napwake paused the workload while it was in use. A request, a click, a wake request or an activity annotation undid the pause soon after it began. Napwake didn't see the use, usually because the workload uses less CPU in use than its threshold:

```bash
kubectl describe managedworkload my-api -n staging
```

```text
Warning  PausedWhileInUse  a request came 40 seconds after it paused, so it was in use: it now waits 10 minutes
without activity before pausing, not 5 minutes, until a pause holds. It used up to 0.6% of its CPU requests while
in use, and up to 0.2% idle: try napwake.dev/cpu-threshold: "0.4"
```

Its idle clock waits longer meanwhile, doubling with each wrong pause up to `idleAfter` plus an hour, so it's paused in use less often. Fix the cause, and it pauses on time again:

1. **A suggested threshold:** set it, on the workload or the ManagedWorkload's `idlePolicy.activity.cpuThreshold`. Napwake suggests one only when the workload's CPU in use sits clearly above its idle CPU.
2. **No threshold suggested:** CPU can't tell its use from its idle. Count its requests with a [Prometheus activity query](../guides/prometheus-signals.md#example-queries), such as its ingress's request rate.
3. **A caller that keeps a connection open,** such as a gRPC client or a connection pool from another workload: add that workload with `dependsOn`, or opt it in, so it holds this one awake.

The warning stops once a pause lasts 15 minutes, or `idleAfter` if that's shorter, without being undone. See [Paused while in use](../concepts/idle-detection.md#paused-while-in-use).

### Wake it doesn't wake the workload

With `wake.mode: Ask`, a click on the asleep page's **Wake it** button wakes the workload and shows the waking-up page. When the asleep page comes back instead, its notice says why:

- **"The button had run out"**, a `403`: the page's token expired because its reloads failed, as after a laptop slept, since the page reloads with a fresh one every minute; or it was shown during an earlier pause. Click the button on the page shown now. If it keeps coming back with this notice, the doorman's replicas may be signing with different keys: their logs say `loading the key the Wake it button is signed with` when they can't read the wake key Secret, `napwake-doorman-wake-key` in the Napwake namespace. Check it exists and the doorman's Role still allows reading it; with the Kustomize manifests, or your own, include the Secret and its Role.
- **"This click didn't come from this page's address"**, a `403`: the browser says the click came from a page at another address. It came from another site or subdomain, by its `Sec-Fetch-Site`. Or, over plain HTTP, its `Origin` isn't the address the doorman saw the page at, the one at the top of the page. That happens when a proxy in front of the doorman rewrites the `Host` header, such as ingress-nginx's `upstream-vhost`. Pass the browser's `Host` on, or serve the preview over HTTPS. Clicking again won't help, so wake it with `kubectl napwake wake` or the `napwake.dev/wake-requested` annotation meanwhile.
- **"This browser didn't say the click came from this page"**: the click came with neither `Sec-Fetch-Site` nor an `Origin`, or with `Origin: null`, as some browsers from before about 2020 send it, so the doorman can't tell it from a spider posting the page's token. Open it in a current browser, or wake it with `kubectl napwake wake` or the `napwake.dev/wake-requested` annotation. If a current browser gets this, something between the browser and the doorman drops those headers, or a browser extension does: ingress-nginx and most proxies pass them on as they are. See [What browsers, robots and API clients see](../concepts/wake-on-request.md#what-browsers-robots-and-api-clients-see).
- **"Try again in a minute"**, a `429` with `Retry-After: 60`: a click wakes a workload at most once a minute. Try again shortly.
- **"It couldn't be woken just now"**: the doorman couldn't stamp the wake, such as when the API server didn't answer, or a wake rate limit was hit. Its logs say which.

A browser that gets a plain-text `503` instead of the page doesn't look like a person's browser to the doorman: its `User-Agent` names a crawler or a headless browser, or doesn't start with `Mozilla/`. Wake it with `kubectl napwake wake` or `napwake.dev/wake-requested` instead.

### A dependency isn't held awake, woken or waited for

```bash
kubectl napwake deps my-api -n preview-42
```

`deps` lists the workload's dependencies and where each came from. The ManagedWorkload's conditions say why one is left out:

- **`DependencyNotAllowed=True`**: the dependency is in another namespace, and that namespace's `napwake.dev/dependents-from` annotation doesn't list this one, so it isn't held, woken or waited for. Annotate the dependency's namespace; see [Across namespaces](../concepts/dependencies.md#across-namespaces).
- **`DependencyNotFound=True`**: a `dependsOn` workload doesn't exist (reason `DependencyNotFound`), or is outside `watchNamespaces` or can't be read by the operator (reason `DependencyNotVisible`). Neither blocks the workload.
- **A `DependencyWakeFailed` warning event**: Napwake couldn't set the activity annotation that wakes a dependency, such as when a policy engine refuses the write; the message has the error. The workload wakes anyway, and the dependency may still be paused.

### A Prometheus query never matches

`PrometheusQueryEmpty=True`, reason `NoSeries`, with a warning event, means one of the workload's Prometheus activity queries has matched no series since Napwake first evaluated it, which usually means a misspelt metric or label. Such a query can never keep the workload awake. It doesn't stop a pause either: the other sources decide. Run the query in Prometheus to check it; see [Test the query directly](../guides/prometheus-signals.md#test-the-query-directly).

### An activity annotation is ignored

An `InvalidActivityAnnotation` warning event means a `napwake.dev/last-activity`, `last-request` or `active-until` value isn't an RFC 3339 time, such as epoch seconds or a date without a time. It neither keeps the workload awake nor wakes it. Write the time as `date -u +%Y-%m-%dT%H:%M:%SZ` gives it; see [Activity annotations](../concepts/idle-detection.md#activity-annotations).

### A paused workload's ManagedWorkload lost its status

A ManagedWorkload restored from a backup without its status, or deleted without its finalizer and created again, no longer has the record of its pause. Napwake takes the pause up again from the copy on the workload's `napwake.dev/paused-from` annotation, with a `PauseRecovered` event: the workload is `Paused` again if it's still at zero, and wakes as usual. Leave the annotation alone; Napwake removes it when it hands the workload back.

A `PauseRecordLost` warning event means there was no record to finish from, or the annotation couldn't be read; the message says which. A workload left `Pausing`, `Paused` or `Resuming` with no record anywhere was never scaled, since the copy is written first, and is `Running` again. If its replicas are at zero all the same, Napwake takes it for a workload scaled to zero outside it and leaves it off: scale it back up yourself.

### A ManagedWorkload let go of its workload without handing it back

A `HandBackAbandoned` warning event means the ManagedWorkload's namespace is being deleted, and handing its paused workload back failed for 5 minutes, such as when a KEDA webhook that's down refuses the release of its ScaledObject. The ManagedWorkload lets go rather than hold up the namespace's deletion; the workload is deleted with the namespace. The message has the error.

### Prediction confidence stays at 0

The confidence scorer needs a full 24-hour window of observed hours before reporting daily confidence, and 168 for weekly. An hour isn't observed while the operator is down, while CPU can't be read, or while the workload is paused with neither the doorman nor a [Prometheus activity query](../guides/prometheus-signals.md#while-the-workload-is-paused) to see its demand, so a workload paused every night that way never gets a full day; its `status.prediction.message` says why requests don't wake it and how to fix it. `status.prediction.reason` and `message` say what the forecast waits for, and `startsActing` when it's expected to act; see [When It Acts](../concepts/forecasting.md#when-it-acts). With a Prometheus URL, a new workload starts from its history instead: `status.prediction.history` says what came of reading it.

Also check that metrics-server is running and returning data:

```bash
kubectl top pods -n staging
```

### Target not found

```bash
kubectl get managedworkload my-api -n staging -o jsonpath='{.status.conditions}'
```

If `TargetAvailable` is `False` with reason `TargetNotFound` (a `TargetNotFound` warning event says so too):

- Verify the target exists: `kubectl get deployment my-api -n staging`
- Check that `target.kind` matches (Deployment vs StatefulSet)
- Ensure the ManagedWorkload is in the same namespace as the target

`spec.target` can't be changed. To manage another workload, create another ManagedWorkload. Reason `TargetIgnored` means the target is labelled `napwake.dev/ignore`.

### Duplicate target error

Only one ManagedWorkload can manage a given workload: the oldest wins, and the others report `DuplicateTarget=True` naming it, and do nothing. Check for duplicates:

```bash
kubectl get managedworkloads -n staging -o jsonpath='{range .items[*]}{.metadata.name}: {.spec.target.name}{"\n"}{end}'
```

### A `ManagedByAnotherInstall` warning

Another Napwake install, in the namespace the event names, took this ManagedWorkload on first, and this one leaves it alone: two installs watch its namespace. Give each install its own `watchNamespaces`, as in [More than one install](../getting-started/installation.md#more-than-one-install). If the install it names is gone, `kubectl annotate managedworkload <name> -n <namespace> napwake.dev/operator-` hands it over.

### Cost data shows $0.00

- Cost is priced on what the workload's pods request, read from its pods. A workload whose containers set no requests costs nothing.
- `projectedMonthlyCost`, `projectedMonthlyFreed` and `projectedMonthlySavings` show `pending` until 24 hours of the month have been tracked, including on the first day of every month.
- `freedThisMonth` stays `$0.00` until Napwake has paused the workload this month: dry-run workloads record what they would have freed in `status.dryRun` instead.
- `savedThisMonth` stays `$0.00` while the cluster's nodes have room for everything Napwake holds paused: nothing has come off the bill until nodes go. See [Freed but not saved](#freed-but-not-saved). It reads `unknown`, with why in `savingsUnknown`, for an operator limited by `watchNamespaces`; see [Saved reads `unknown`](#saved-reads-unknown).
- Totals start again from zero each calendar month, in UTC. See [Cost Tracking](../concepts/cost-tracking.md).

### Freed but not saved

`kubectl napwake status` shows FREED THIS MONTH well above SAVED, and may say under Needs attention that nodes aren't scaling down. Pausing freed capacity on the nodes, but the nodes are still there, so little of it came off the bill. Saved is measured from the spare capacity left on each node pool: while a pool's nodes could hold everything Napwake holds paused there, nothing is credited (see [How saved is measured](../concepts/cost-tracking.md#how-saved-is-measured)).

**Is anything scaling the nodes?** The first lines of `kubectl napwake status` and `kubectl napwake nodes` name the node autoscaler, as does the operator's `napwake_node_autoscaler` metric. With `none found`, nothing removes nodes, and what pausing frees never comes off the bill: enable Cluster Autoscaler on the node groups, or install Karpenter. See the [Cluster Autoscaler Guide](../guides/cluster-autoscaler.md).

**What's keeping the nodes?** `kubectl napwake nodes` lists, for each node, what keeps the autoscaler from removing it, in BLOCKED BY, with how to clear each below the table:

- **`not autoscaled`**: no autoscaler found scales the node, such as a node group Cluster Autoscaler doesn't manage
- **`NodePool never consolidates`** and **`NodePool consolidates only empty nodes`**: a Karpenter NodePool with `consolidateAfter: Never`, or with `consolidationPolicy: WhenEmpty` on a node that runs anything but DaemonSets
- **`scale-down disabled`** and **`do-not-disrupt on the node`**: the node is annotated `cluster-autoscaler.kubernetes.io/scale-down-disabled: "true"` or `karpenter.sh/do-not-disrupt: "true"`
- **`PDB allows no disruption`**: a pod's PodDisruptionBudget allows no disruptions, so it can't be moved
- **`safe-to-evict=false`** and **`do-not-disrupt`**: a pod annotated `cluster-autoscaler.kubernetes.io/safe-to-evict: "false"` or `karpenter.sh/do-not-disrupt: "true"`
- **`local storage`**: a pod with an `emptyDir` or `hostPath` volume, which Cluster Autoscaler won't move while `--skip-nodes-with-local-storage` is on
- **`bare pod`**: a pod no controller would recreate elsewhere
- **`kube-system pod without PDB`**: which Cluster Autoscaler won't move while `--skip-nodes-with-system-pods` is on

**Are paused workloads sharing nodes with busy ones?** A node is removed only once everything on it can go. A paused workload on a node that also runs busy pods frees room on it, but the node stays, and a cluster where every node is a little busy can keep all of them. The UTILISATION column of `kubectl napwake nodes` shows it: low, with nothing in BLOCKED BY, and the node is waiting for the autoscaler's threshold or delay; middling, and it's still in use. Putting non-prod workloads on a [node pool of their own](../guides/node-pools.md) lets pausing them empty whole nodes.

**Is the autoscaler keeping spare capacity?** Some settings keep nodes around on purpose: a node group's minimum size above what's in use, a long `scale-down-unneeded-time` or `scale-down-delay-after-add`, a `scale-down-utilization-threshold` lower than the nodes' use, a long `consolidateAfter`, Karpenter disruption `budgets` that allow few nodes at once, or over-provisioning placeholder pods kept for fast scale-up. Saved catches up only as those nodes go. See [Node Pools for Real Savings](../guides/node-pools.md) for settings that let a non-prod pool empty.

`napwake_capacity_spare_cpu_cores` and `napwake_capacity_paused_cpu_cores`, by `pool`, show which pools still have room for what's paused there; see the [Metrics Reference](../reference/metrics.md#savings).

### Saved reads `unknown`

`savedThisMonth` and `projectedMonthlySavings` read `unknown`, and `kubectl napwake status` shows `unknown` in SAVED and says why above the table. `status.cost.savingsUnknown` has the reason:

```bash
kubectl get managedworkload my-api -n staging -o jsonpath='{.status.cost.savingsUnknown}'
```

An operator limited to some namespaces with `watchNamespaces` can't see the pods in other namespaces, so it can't tell how much of each node they request, nor what's left over for the paused replicas. Saved can't be measured there, and the operator exports no `napwake_capacity_*` or `napwake_savings_realised_dollars_per_hour` series. Freed is still measured. To measure saved, install the operator without `watchNamespaces`; it then watches pods cluster-wide, caching only what each pod bound to a node requests (see [Data and Access](../reference/data-and-access.md)).

### `helm uninstall` fails with `job napwake-uninstall-guard failed`

Helm 4 reports it as `resource Job/napwake-system/napwake-uninstall-guard not ready`. ManagedWorkloads are left, and uninstalling now would leave their workloads paused. Helm 3.18 and later print them above the error, in a message that starts `Napwake refuses to uninstall:` and names them; when Helm prints only the error, as Helm before 3.18 does, and any Helm after a `helm rollback` with one, `kubectl logs job/napwake-uninstall-guard -n napwake-system` shows them. Remove the `napwake.dev/managed` label from those workloads and their namespaces, delete the ManagedWorkloads you wrote yourself, and uninstall again, as in [Uninstall](../getting-started/installation.md#uninstall).

The refused uninstall leaves the release marked `uninstalling`, which `helm list` hides without `-a`, though the operator and the doorman keep running. If you decide to keep Napwake instead, `helm upgrade` fails with `has no deployed releases` until `helm rollback napwake -n napwake-system` marks the release deployed again.

The hook waits two and a half minutes for ManagedWorkloads being deleted before it lists what's left, so one it still lists as being deleted is one the operator didn't finish in that time, usually because it isn't running: uninstall with `--no-hooks` and restore the workloads by hand. Without the CRD, as can happen with `crds.install: false`, nothing is left to guard, and the hook lets the uninstall go ahead. With Argo CD 3.3 and later the same refusal blocks deleting the Application, with a `DeletionError` condition; set `guard.uninstall: false` in Git to delete it anyway.

### `helm upgrade` fails with `job napwake-upgrade-guard failed`

The upgrade sets `watchNamespaces` to leave out namespaces where the operator manages workloads, which would leave them paused with no operator; Helm 4 reports it as `resource Job/napwake-system/napwake-upgrade-guard not ready`. Helm 3.18 and later print them above the error, in a message that starts `Napwake refuses this upgrade:` and names them, or `kubectl logs job/napwake-upgrade-guard -n napwake-system` shows them. Keep those namespaces in `watchNamespaces`, or stop managing the workloads there first, then upgrade again; see [Narrowing watchNamespaces](upgrading.md#narrowing-watchnamespaces). Under Argo CD the same refusal fails the sync, in its PreSync hook.

`helm rollback` to a revision with a narrower `watchNamespaces` is refused the same way, by the pre-rollback hook, but Helm still records the attempt as a new revision and leaves it `pending-rollback` in `helm history napwake`, while the operator keeps running as it was. Until another revision replaces it, `helm upgrade` fails with `another operation (install/upgrade/rollback) is in progress`. `helm rollback napwake -n napwake-system`, with no revision, rolls back to the one before it, the release as it runs, and marks that deployed again.

### Workloads stay at zero after the operator was removed

The operator was removed, or its CRD deleted, while ManagedWorkloads were left, so nothing scales their workloads back up, their Services still send traffic to the doorman's deleted pods, and deleting the ManagedWorkloads or the CRD hangs in `Terminating` on the `napwake.dev/cleanup` finalizer. Reinstall the operator and stop managing the workloads, or restore them by hand: see [Restoring workloads after the operator is gone](../getting-started/installation.md#restoring-workloads-after-the-operator-is-gone).

## Debug Checklist

1. **Operator running?** `kubectl get pods -n napwake-system` (or the namespace you installed into)
2. **CRDs installed?** `kubectl get crd managedworkloads.napwake.dev`
3. **Metrics server running?** `kubectl top nodes`
4. **RBAC correct?** `kubectl auth can-i get pods --as=system:serviceaccount:napwake-system:napwake` (the ServiceAccount is `napwake` for a Helm release named `napwake`, and `napwake-controller-manager` with `install.yaml` or the Kustomize manifests)
5. **Events?** `kubectl describe managedworkload <name> -n <ns>`
6. **Logs?** `kubectl logs -n napwake-system -l control-plane=controller-manager`
7. **Status?** `kubectl get managedworkload <name> -n <ns> -o yaml`

## Getting Help

If you've gone through this checklist and still have issues:

1. Check the [GitHub Issues](https://github.com/napwake/napwake/issues) for known problems
2. Open a new issue with:
   - Operator version (`kubectl get deploy -n napwake-system -o wide` shows the image), plugin version (`kubectl napwake version`), and Kubernetes version
   - ManagedWorkload YAML (sanitized)
   - Relevant operator logs
   - Output of `kubectl describe managedworkload`
