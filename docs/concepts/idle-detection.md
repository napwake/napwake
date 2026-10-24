# Idle Detection

Napwake keeps an **activity clock** for every ManagedWorkload: the last time anything showed the workload was in use. When the clock is older than `idlePolicy.idleAfter`, the workload is paused: scaled to zero, keeping its replica count and a resource snapshot. Any single sign of activity resets it. With `idlePolicy.pauseEarly`, a confident forecast can bring the pause forward (see [Pausing Early](forecasting.md#pausing-early)).

There's no learning period. A new workload can be paused as soon as it has gone `idleAfter` without activity.

```yaml title="managedworkload.yaml" linenums="1"
spec:
  idlePolicy:
    idleAfter: 1h        # default
    activity:
      cpuThreshold: 10   # percent of CPU requests, 0.1 to 100; default
```

A threshold that isn't a whole number is written as a string, such as `cpuThreshold: "0.5"`, as Kubernetes APIs write decimals. It suits a workload that idles at a fraction of a percent.

## What counts as activity

Every source is checked, and the most recent one wins. None has priority over another.

| Source | Counts as activity when | Setup |
|--------|------------------------|-------|
| **CPU** | Usage is above `activity.cpuThreshold`% of the CPU requested by the pods reporting it, from 0.1 to 100 | Requires metrics-server |
| **Deploy** | The target's pod template changes: a new image, environment variable, or other template edit | None |
| **`napwake.dev/last-activity`** | The annotation holds a time newer than the last recorded activity | Set by your own tooling |
| **`napwake.dev/active-until`** | The annotation holds a time in the future (a hold, not an activity time) | Set by your own tooling |
| **`napwake.dev/last-request`** | The annotation holds a time newer than the last recorded activity | Set by the [doorman](wake-on-request.md) when it holds a request for the paused workload |
| **Prometheus** | Any query in `activity.prometheus` returns a value above zero | `--prometheus-url`; see the [Prometheus Activity Guide](../guides/prometheus-signals.md) |

Changes to the replica count don't count as a deploy, so Napwake's own pause and resume never look like activity.

Memory isn't a source: it stays allocated whether or not anyone uses the workload.

CPU is measured for the containers in the target's pod template, including native sidecars (init containers with `restartPolicy: Always`). Sidecars injected when the pod is created, such as an Istio or Linkerd proxy, aren't counted: their own background work isn't use of the workload, and they aren't in the template's requests. [Cost](cost-tracking.md) does count them, since they cost the same as any container and pausing frees them too.

Usage is measured over the pods metrics-server reports and compared with what those pods request, so one pod serving while the others are Pending or crash-looping, or before new pods from a scale-up report, reads at its own utilization rather than a fraction of it. Usage is compared with what those containers request in a running pod, not in the template, so a request set when the pod was created counts: a LimitRange default, or a Vertical Pod Autoscaler's recommendation. The pod read is a running one of the target's current revision: a failed pod, such as one evicted, keeps the requests of whatever revision it ran until it's cleaned up, and isn't read. The request is read again every 5 minutes, at once when the target's spec changes, and on every check while it's unclear which revision a replica will have, as during a Deployment's rollout. With no pod running, the newest that's starting is read, such as a Pending one in a `Recreate` rollout, and only with none at all are the template's requests used, a container that sets only a CPU limit taken to request its limit, as Kubernetes does.

### Activity annotations

Set either annotation on the ManagedWorkload or on its target Deployment or StatefulSet. Values are RFC 3339 times.

```bash
# Record activity now, e.g. from a developer portal when someone opens an environment
kubectl annotate deployment my-api -n staging --overwrite \
  napwake.dev/last-activity="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# Keep the workload awake until a time, e.g. during a demo
kubectl annotate deployment my-api -n staging --overwrite \
  napwake.dev/active-until="2026-10-03T18:00:00Z"
```

To wake a workload on purpose, rather than record its use, ask for it with [`napwake.dev/wake-requested`](../guides/pause.md#wake-now), which Napwake answers, and which wakes it whatever its wake setting; [`kubectl napwake wake`](../getting-started/kubectl-plugin.md#wake-a-workload) sets it for you and waits until the workload is Running.

- A `last-activity` time in the future is treated as now. Use `active-until` for a deliberate hold, since it says when the hold ends.
- Annotations can only keep a workload awake or wake it. They never cause a pause.
- A value that isn't an RFC 3339 time, such as epoch seconds or a space in place of the `T`, is ignored everywhere: it neither keeps an awake workload awake nor wakes a paused one. Each such value gets one `InvalidActivityAnnotation` warning event on the ManagedWorkload, naming the annotation and where it is.

## The decision

A workload at zero replicas that Napwake didn't pause isn't checked at all: it's already off, and [left that way](../guides/pause.md#workloads-already-at-zero). For any other, each check runs in this order:

1. CPU, or a configured Prometheus query, can't be read: stay awake (see [below](#when-a-source-cant-be-read)).
2. `active-until` is in the future: stay awake.
3. The last activity is less than `idleAfter` ago, or longer after [a pause made in use](#paused-while-in-use): stay awake, unless `pauseEarly` is on and the [forecast brings the pause forward](forecasting.md#pausing-early): the last activity was at least 20 minutes ago, the forecast is confident every hour until `idleAfter` would run out, and at least the hour under way and the next, is quiet, and a request would wake the workload through the doorman once it's paused. Then the pause goes on through the steps below, as if the clock had run out.
4. The [forecast](forecasting.md) is confident (shown as `DailyActive` or later, `dailyPhase: Active`) and predicts utilization above `cpuThreshold` in the hour under way or the next: stay awake. The `IdleVetoed` condition is `True`, with reason `ForecastExpectsDemand` and a message naming the hour, while this holds the pause back, and an `IdleVetoed` event marks the start of each veto. The hour after one far quieter than it forecast, as on a holiday, the forecast doubts what it expects and holds nothing back (see [Anomaly Detection](forecasting.md#anomaly-detection)).
5. Another workload that [depends on](dependencies.md) this one is awake, or `dependsOn` forms a cycle: stay awake (`HeldByDependents` or `DependencyCycle`).
6. Otherwise the clock has run out, or the forecast brought the pause forward. The phase becomes `Idle`, with an `IdleDetected` event, or a `PausedEarly` event naming the hours the forecast expects to be quiet, and the workload is [paused](../guides/pause.md).

Without an `idlePolicy`, none of this runs, and the workload is only paused when someone asks.

A [pause request](../guides/pause.md#pause-now), from `kubectl napwake pause`, runs the clock out at once. Steps 1 and 3 don't apply, and nor does the hour Napwake waits after a GitOps tool undid its last pause, since someone asked; an `active-until` hold, awake dependents and a `dependsOn` cycle still keep the workload up, and so does the forecast (step 4) unless the request comes with `napwake.dev/pause-overrides-forecast: "true"`, which `kubectl napwake pause --yes` sets. A request held back is answered with the reason.

With `dryRun: true`, the phase still becomes `Idle` and the `IdleDetected` or `PausedEarly` event carries a `[dry-run]` prefix, but nothing is changed. If activity resumes, the phase goes back to `Running`. See [Dry Run](../guides/dry-run.md).

The forecast never blocks a pause until it's confident, so a workload with no history is handled by the clock alone.

## Paused while in use

Napwake can't see requests to an awake workload. One in steady light use, under its CPU threshold, can look idle and be paused while people use it. The next request wakes it, with a cold start.

A pause is wrong when the workload's own use undoes it soon after it begins, within `idleAfter` or 15 minutes, whichever is shorter. Any of these undoes it:

- a request, or a person's click for a workload that [asks first](wake-on-request.md#asking-before-waking)
- a [wake request](../guides/pause.md#wake-now)
- an activity annotation

One that comes while the workload is still pausing [calls the pause off](../guides/pause.md#a-request-calls-the-pause-off). That pause was wrong too.

After a wrong pause, the idle clock waits longer before the next. Each wrong pause in a row doubles the wait, up to `idleAfter` plus an hour:

| Wrong pauses in a row | Wait with `idleAfter: 5m` | Wait with `idleAfter: 1h` |
|---|---|---|
| 0 | 5 minutes | 1 hour |
| 1 | 10 minutes | 2 hours |
| 2 | 20 minutes | 2 hours |
| 3 | 40 minutes | 2 hours |
| 4 or more | 1 hour 5 minutes | 2 hours |

A pause that lasts the window ends the wait. The count is kept in `status.idleWait`, so a restart keeps it. The wait only ever delays a pause.

These don't make a pause wrong, since Napwake didn't misjudge the workload's use:

- a pause someone asked for
- the forecast waking it ahead of demand
- a Prometheus activity query
- a wake for an awake dependent that uses it

Steady light use wakes a wrong pause within seconds or a minute or two. A pause that lasted a quarter of an hour paid for its cold start, and a short `idleAfter` is what you said idle means. Hence the window. In the simulator, `orders/api` on the test cluster, with `idleAfter: 5m` under steady requests all working day, goes from 79 cold starts a working day to 10, and is awake about 3.5 hours a week more, on weekday evenings.

### What Napwake tells you

A `PausedWhileInUse` warning event says how soon the pause was undone, how long the clock waits now, and what to change. The `PausedWhileInUse` condition says the same until a pause holds, and [`kubectl napwake status`](../getting-started/kubectl-plugin.md#see-what-napwake-is-doing) lists it under Needs attention.

```text
a request came 40 seconds after it paused, so it was in use: it now waits 10 minutes without activity
before pausing, not 5 minutes, until a pause holds. It used up to 0.6% of its CPU requests while in use,
and up to 0.2% idle: try napwake.dev/cpu-threshold: "0.4"
```

The suggested number comes from the CPU Napwake measures through each stretch the clock counts as idle:

- A wrong pause shows what the workload uses in use: the most in the stretch before it.
- A pause that holds shows what it uses idle: the most in the 15 minutes before it, or `idleAfter` if shorter. Napwake keeps the most over the last week.

When the CPU in use is at least twice the idle CPU, and 0.2 points more, Napwake suggests a threshold halfway between, to a tenth. A workload you opted in with the label is told the annotation, and one you wrote the field. Otherwise CPU can't tell its use from its idle, and Napwake suggests a [Prometheus query](../guides/prometheus-signals.md#example-queries) that counts its requests. Until a pause has held, it hasn't measured idle use, and says so rather than guess.

```yaml
status:
  idleWait:
    wrongPauses: 1
    lastWrongPause:
      pausedAt: "2026-10-26T09:02:11Z"
      after: 40s
      by: request          # request, click, wakeRequest or annotation
      from: 10.4.1.7
      inUseCPU: "0.6"
    idleCPU: "0.2"
    idleCPUAt: "2026-10-25T17:06:00Z"
```

`napwake_wrong_pauses_total` counts wrong pauses, by workload. Dry-run never pauses, so its would-be pauses aren't judged.

## When a source can't be read

Napwake never pauses a workload while one of its activity sources can't be read: without that data a busy workload looks idle. Nor does it count that time as idle: a check where a source can't be read doesn't count as watching, so a source out for longer than the gap allowed [below](#restarts-and-outages) restarts the clock (`lastActivitySource: unobserved`), with an `Unobserved` event once it's back, and once the source is back, the workload pauses only after a full `idleAfter` in which every source saw nothing. A shorter blind spot, such as the minute before a woken workload's new pods have metrics, leaves its last activity as it was. A Prometheus query that answers "no requests in the last 5 minutes" as it comes back doesn't make the hour before it look idle. For CPU, the `MetricsAvailable` condition says why:

| Reason | Meaning |
|--------|---------|
| `NoPodMetrics` | The target's Ready pods have had no metrics for over 3 minutes. Check that metrics-server is installed and reports them (`kubectl top pods`). A warning event says so once |
| `NoCPURequests` | The target's running pods request no CPU for its containers, or, with none running, its pod template requests none, so utilization can't be measured. Set CPU requests, or a default in the namespace's LimitRange. |
| `MetricsUnavailable` | The Metrics API itself failed, or didn't answer within 2 seconds. The condition message has the error. Once it fails, it isn't read again for a minute, so every workload's check fails at once rather than waiting out a timeout. |

A target scaled to zero isn't an error: it has no usage, which is real data. Nor are new pods: a pod has no metrics until it runs, and for a scrape or two of metrics-server's after, so after a wake or a rollout `MetricsAvailable` is `Unknown`, with no warning, until they arrive: reason `NoReadyPods` while none of the target's pods is Ready, then `AwaitingPodMetrics` for up to 3 minutes. Only Ready pods that go on without metrics after that are `NoPodMetrics`.

For Prometheus queries, the `PrometheusAvailable` condition reports `EndpointNotConfigured` or `QueryFailed`. A query that answers but has never matched a series, as one naming a metric or label that doesn't exist does, is reported by `PrometheusQueryEmpty=True` and a warning event, since it can never keep the workload awake; see the [Prometheus Activity Guide](../guides/prometheus-signals.md#when-a-query-cant-be-evaluated).

## Restarts and outages

The clock is stored in `status.activity`, so it survives operator restarts. While a workload is awake it's checked about once a minute; the clock's timestamps are written to status at most every 5 minutes, along with cost, and immediately whenever the phase or a condition changes, or when new activity is more than a tenth of `idleAfter` (at most 5 minutes) ahead of what's written. Activity seen between writes is held in memory, so it still counts while the operator runs; after a restart, the operator has only what was written.

If the operator stops watching a workload for a while, it can't know whether there was activity in the meantime, so the clock restarts from the time it resumes watching (`lastActivitySource: unobserved`). How long a gap it allows depends on `idleAfter`: half of it, but at least 2 minutes (two missed checks) and at most 7. When a gap restarts the clock, an `Unobserved` event says how long it was: `Napwake wasn't watching for 9 minutes, so its idle clock starts again`, or `couldn't read its activity for 9 minutes` when a source was out. It comes with the first check that can read every source. A gap isn't activity, so it only restarts the clock of a workload that's awake: a paused workload stays paused, and in dry-run a would-be pause goes on, as the real one would (see [Dry Run](../guides/dry-run.md)). An outage can delay a pause, but never cause one.

After a restart or a change of leader, the operator has only what was written, which can be up to 5 minutes behind its last check, and can be missing activity seen since, up to a tenth of `idleAfter`. So an awake workload's clock always restarts at a restart, however recently it was written, with an `Unobserved` event: `the operator restarted, or another took over, so its idle clock starts again; it wasn't watching for up to 4 minutes`: up to, since the last check may have come later than what's written. A restart can delay a pause by up to `idleAfter`, but never brings one forward. A paused workload stays paused through it, and in dry-run a would-be pause goes on.

```yaml
status:
  activity:
    lastActivityTime: "2026-10-02T09:14:00Z"
    lastActivitySource: cpu    # created, woke, request, cpu, rollout, annotation, prometheus, unobserved, or scaled-up
    pauseAt: "2026-10-02T10:14:00Z"
    lastEvaluatedTime: "2026-10-02T09:41:00Z"
```

## Waking up

A paused workload wakes when:

- A request reaches one of its Services. The request is held while the workload starts, then answered; see [Wake on Request](wake-on-request.md).
- A `napwake.dev/last-activity`, `last-request` or `active-until` annotation on the ManagedWorkload or its target changes after the pause began to an RFC 3339 time, whatever time it states, so a clock that's behind the operator's can't lose a wake. Only a change counts: the pause records the annotations as they stood, so a value set before it, even one stating a later time, doesn't wake it.
- A [wake request](../guides/pause.md#wake-now): a new value of `napwake.dev/wake-requested` on the ManagedWorkload, whatever its wake setting. `kubectl napwake wake` sets it, and so can a "start environment" button in a developer portal, which can then read Napwake's answer in the `WakeRequest` condition.
- A [Prometheus activity query](../guides/prometheus-signals.md#while-the-workload-is-paused) shows activity: the queries are run about once a minute while it's paused, as while it's awake. One that already showed activity as a [requested pause](../guides/pause.md#pause-now) began wakes it only once it has been quiet, so it doesn't undo the request, and one that can't be evaluated never wakes it.
- `autoResume: true` is set and a confident forecast (`dailyPhase: Active`) predicts utilization above `cpuThreshold` for the current hour, or for the next hour once it's 15 minutes away, so the workload is ready before people arrive; not the hour after one far quieter than it forecast.
- Something other than Napwake scales it up, such as `kubectl scale`.
- Dry-run is turned on: dry-run never leaves a workload paused.

Whatever would wake it, coming while it's still pausing, [calls the pause off](../guides/pause.md#a-request-calls-the-pause-off) instead.

Each wake restarts the clock, so a woken workload gets a full `idleAfter` before it can pause again, or longer after [a pause made in use](#paused-while-in-use), unless [`pauseEarly`](forecasting.md#pausing-early) is on and the forecast expects the coming hours quiet: then the pause can come 20 minutes after the wake. The clock records `lastActivitySource: request` when a held request woke it, `prometheus` when an activity query did, `scaled-up` when something else scaled it up, and `woke` for any other wake.
