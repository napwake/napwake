# Pause and Resume

Napwake's one action is to **pause**: scale a workload to zero replicas. The Deployment or StatefulSet stays, and so do its PVCs and their data; only the pods are removed. Napwake never deletes a workload or its storage. A StatefulSet whose `persistentVolumeClaimRetentionPolicy.whenScaled` is `Delete` has its own controller delete its PVCs as it scales down, so Napwake never pauses one: it reports `Protected=True`, reason `PVCsDeletedOnScaleDown`, until `whenScaled` is `Retain`.

## Pause

### What Happens When a Workload Is Paused

1. What the pause will change is recorded in `status.pause`, in the same status write that moves the phase to `Pausing`, before anything is scaled: when the pause began (`startedAt`), the replica count (`previousReplicas`), the KEDA ScaledObject and its own `autoscaling.keda.sh/paused-replicas` value if it has one, what each replica requests (for [cost tracking](../concepts/cost-tracking.md)), and the activity annotations as they stand
2. The [doorman](../concepts/wake-on-request.md) steps in front of the workload's Services, beside its pods, so no request is refused as they stop. Its routes go into `status.doorman` with the move to `Pausing`, and every doorman pod listens on them. `status.pause.routesWrittenAt` records when they were stored. Five seconds after, its EndpointSlice stands beside the pods' own, and it passes what it gets straight to a Ready pod. `status.pause.routedAt` records when. Five seconds after that, once kube-proxy and ingress controllers send to it too, the pause goes on, however late Napwake gets to it. A workload the doorman doesn't route goes on at once, and none waits more than a minute for the doorman
3. A copy of what undoing the pause needs, the replica count and the KEDA ScaledObject with its own `paused-replicas` value, is written to the workload's `napwake.dev/paused-from` annotation, where it stays until the workload is handed back. Should the ManagedWorkload lose its status while the workload is paused, such as to a Velero restore without status, or being deleted without its finalizer and created again, Napwake takes the pause up again from it, with a `PauseRecovered` event, and the workload wakes as it would have
4. A KEDA ScaledObject, if the workload has one, is held at zero with `autoscaling.keda.sh/paused-replicas: "0"`
5. The workload is scaled to 0 through its scale subresource. The API server records no field manager for a write that way, so the replicas show as no one's rather than Napwake's
6. `status.pause.pausedAt` is set. Once its pods have stopped, the phase moves to `Paused`, with a `Paused` event. Pods that don't stop, such as a StatefulSet's under `OrderedReady` pod management waiting on one that isn't Ready, are waited for as long as their termination grace period and a minute more (one pod after another for a StatefulSet); then it's `Paused` anyway, with `PauseIncomplete=True` and a `PauseIncomplete` warning event in place of `Paused`, until they've gone

Because the record is written first, a pause interrupted at any point, by an operator restart or a conflicting write, is finished or undone from the record, and a resume always goes back to the replica count the workload had.

### Work in Progress at a Pause

The pods stop as Kubernetes stops any pod. Each gets `SIGTERM`, and its `terminationGracePeriodSeconds` (30 seconds by default) to finish what it's doing before `SIGKILL`. Napwake waits for them to stop. Requests that come meanwhile go to the doorman, which holds them.

An app that should finish a job or drain connections needs to handle `SIGTERM` the way it would in a rollout. One that exits the moment it gets `SIGTERM` can refuse a connection kube-proxy sends it in the second before it drops the pod. A `preStop` sleep of a few seconds closes that gap, in a pause as in any rollout.

### A Request Calls the Pause Off

A request while a workload pauses shows it's in use. Whatever would wake it once paused, coming while it's `Pausing`, calls the pause off:

- a request that reaches the doorman, which holds it, or passes it on to a pod that's still Ready
- for a workload that [asks before waking](../concepts/wake-on-request.md#asking-before-waking), only a person's click on the asleep page
- a [wake request](#wake-now)
- an activity annotation, which a waking [dependent](../concepts/dependencies.md) stamps

The workload is `Running` again, scaled back up if its pods had begun to stop, and the request is answered, held while no pod is Ready. A `PauseCancelled` event says what came:

```text
a request came as it paused; the pause is cancelled
```

The doorman doesn't see every request as a workload pauses. The pods answer these, and the pause goes on:

- requests in the first 5 seconds, before the doorman's EndpointSlice stands beside the pods' own
- the share kube-proxy and ingress controllers still send to the pods, for about 5 seconds more, until they're scaled down
- requests on connections opened before the pause
- every request after the first on a connection the doorman passed on
- traffic from a GKE load balancer through [NEGs](../concepts/wake-on-request.md#gke-container-native-load-balancing-negs), which goes to the pods' own addresses

A probe, a scrape, a hunt for hidden files, or a TCP check that sends nothing, doesn't call it off. Dry-run never pauses, so there's nothing to call off. A pause the idle clock made, called off this way, counts as [made in use](../concepts/idle-detection.md#paused-while-in-use), and the clock waits longer before the next.

### Triggering a Pause

**Automatically:** once the workload has had no activity for `idlePolicy.idleAfter`; see [Idle Detection](../concepts/idle-detection.md).

**Now:** with [`kubectl napwake pause`](#pause-now), to wake when it's next used.

Either way it's the same pause, which wakes on a request, on activity, or by `autoResume`. To keep a workload off until someone starts it again, scale it to zero yourself: Napwake leaves it alone; see [Workloads Already at Zero](#workloads-already-at-zero).

### Pause Now

When you're done with a workload for the day, such as a preview environment, pause it now rather than waiting for `idleAfter`:

```bash
kubectl napwake pause my-api -n staging
```

The result is an ordinary pause, the same as one the idle clock makes: a request to the workload through the [doorman](../concepts/wake-on-request.md), a change to its [activity annotations](../concepts/idle-detection.md#activity-annotations), a [Prometheus activity query](prometheus-signals.md#while-the-workload-is-paused), `autoResume`, or [`kubectl napwake wake`](../getting-started/kubectl-plugin.md#wake-a-workload) wakes it. A query already showing activity as you pause it, such as a queue with messages still on it, wakes it only once it has been quiet, so it doesn't undo the pause. It doesn't need an `idlePolicy`, and it works on a ManagedWorkload created from the `napwake.dev/managed` label as on one you wrote: name the workload, as `kubectl napwake status` shows it.

The plugin asks by setting the `napwake.dev/pause-requested` annotation on the ManagedWorkload to a token, the time of the request. Any tool can do the same; the value only has to differ from the last request's:

```bash
kubectl annotate managedworkload my-api -n staging --overwrite \
  napwake.dev/pause-requested="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
```

To pause it even when the forecast expects demand within the hour, also set `napwake.dev/pause-overrides-forecast: "true"`; set it, or remove it, with every request, since it applies to the request it comes with.

It's an annotation rather than a spec field so that Argo CD and Flux, which own the spec of a ManagedWorkload kept in Git, leave it alone. Napwake acts on each value once: it records the value it handled in `status.lastPauseRequest`, which survives an operator restart, and says what came of it in the `PauseRequest` condition.

The request runs the idle clock out now, and the workload is paused by the same rules as an idle pause:

| The workload | What Napwake does | `PauseRequest` condition |
|--------------|-------------------|--------------------------|
| `Running` or `Idle` | Pauses it, with a `PauseRequested` event, then `Paused` | `True`, `Paused` |
| A request comes as it pauses | [Calls the pause off](#a-request-calls-the-pause-off), and says so. `kubectl napwake pause` says it wasn't paused | `False`, `PauseCancelled` |
| A confident forecast expects demand within the hour | Leaves it running and says when the demand is expected, since a pause would likely be woken straight back. `kubectl napwake pause` asks whether to pause it anyway; a request with `napwake.dev/pause-overrides-forecast: "true"`, which `--yes` sets, pauses it | `False`, `ForecastExpectsDemand` |
| Within the hour Napwake waits after a GitOps tool (Argo CD, Flux, Fleet, kapp or Kluctl) undid its last pause | Pauses it: you asked. If the tool undoes it again, that's a new [`GitOpsConflict`](gitops.md) | `True`, `Paused` |
| `Pausing` | Finishes the pause, then marks the request handled | `True`, `AlreadyPaused` |
| `Paused` | Marks the request handled | `True`, `AlreadyPaused` |
| `Resuming` | Lets the wake finish, then pauses it | `True`, `Paused` |
| In [dry-run](dry-run.md) | Never scales it: counts a would-be pause in `status.dryRun`, moves it to `Idle`, and emits a `[dry-run]` `PauseRequested` event. Activity after the request ends the would-be pause, as it would wake a paused workload | `False`, `DryRun` |
| Held awake by `napwake.dev/active-until` on it or its workload | Leaves it running, with a Warning event | `False`, `ActiveUntil` |
| Depended on by awake workloads | Leaves it running, with a `HeldByDependents` event, as for an idle pause | `False`, `HeldByDependents` |
| In a `dependsOn` cycle | Leaves it running, with a Warning event | `False`, `DependencyCycle` |
| In a [protected namespace](opt-in.md#protected-namespaces), or Napwake's own operator or doorman | Leaves it running, with a Warning event | `False`, `Protected` |
| Scaled to zero outside Napwake | Nothing to do: it's off already | `False`, `ScaledToZero` |
| Its workload is missing, labelled `napwake.dev/ignore`, or managed by another ManagedWorkload | Nothing, with a Warning event | `False`, `TargetNotFound`, `TargetIgnored` or `DuplicateTarget` |
| Already on the ManagedWorkload as Napwake takes it on, or takes a pause up again after its status was lost | Nothing: it may have been answered before, and brought back with the ManagedWorkload, as by a backup restored without status. Set a new value to ask | `False`, `PredatesStatus` |

The condition answers the last request until the next one, through any wakes and idle pauses since, so its message says what happened and when, in UTC: `paused as requested at 2026-10-21 03:32 UTC`, `already paused at …`, or `not paused at 2026-10-21 03:32 UTC:` and why. Napwake keeps it rather than dropping it at the next wake, so `kubectl napwake pause`, or a script that set the annotation, can always read the answer to its request, and `kubectl describe` shows why last night's pause didn't happen long after its events have expired. `status.lastPauseRequest` says which request it answers.

A request Napwake won't act on is answered once, not retried: ask again once the reason is gone. Napwake's record of which requests it answered is the ManagedWorkload's status, so a request already on it as Napwake takes it on is taken as answered, not acted on: a backup restored without status, `kubectl replace --force` or an exported manifest applied again would otherwise bring back requests answered long ago. Set the annotation once the ManagedWorkload is `Running`. Activity from before the request doesn't undo it: the pause records the activity annotations as they stood, and only a change made since wakes the workload.

## Resume

Resuming wakes the workload's [dependencies](../concepts/dependencies.md), scales it back to `status.pause.previousReplicas` (kept within its HPA's or ScaledObject's range if that changed), and moves it to `Running` once all those replicas are Ready, by a status its Deployment or StatefulSet controller wrote after seeing the scale-up. A workload that wasn't wholly Ready before its pause, such as one with a pod crash-looping, would never get there, so Napwake stops waiting 2 minutes after the first replica is Ready, or, while none is, once the Deployment's `progressDeadlineSeconds` has passed since the scale-up (10 minutes by default, and for a StatefulSet). It's then `Running` with the replicas that are Ready, and `ResumeIncomplete=True` until they all are. With none Ready, its doorman routes are removed all the same, so requests fail until a pod is Ready rather than wait at the doorman for a workload its own controller reports stuck. A StatefulSet with the default `OrderedReady` pod management starts its pods one at a time, so a slow-starting one may have only its first pods Ready when the 2 minutes are up: it runs with those, and the rest follow. Once it's scaled back up, its replicas are left to its autoscaler, so an HPA scaling it down as it starts isn't undone. A KEDA ScaledObject is held at the restored count until then, and afterwards gets back the `paused-replicas` value it had before the pause, or none.

**Automatically:**

- A request to the workload's Service, through [wake on request](../concepts/wake-on-request.md)
- A change to `napwake.dev/last-activity`, `napwake.dev/last-request` or `napwake.dev/active-until` on the ManagedWorkload or its target since the pause began, whatever time the new value states
- A [Prometheus activity query](prometheus-signals.md#while-the-workload-is-paused) showing activity, such as messages waiting for a queue worker
- With `autoResume: true`, 15 minutes ahead of the hour a confident forecast expects demand in
- Something else scaling it up, such as `kubectl scale`: it's counted as activity and the workload goes straight to `Running`; see [GitOps tools](gitops.md) for when that's a GitOps tool
- Turning on [dry-run](dry-run.md), which never leaves a workload paused (`DryRunWake` event)

A paused workload is looked at again at least every 5 minutes, or every minute when it has Prometheus activity queries, 15 minutes before each hour and on the hour, as well as whenever it or its target changes.

**On demand:** with [`kubectl napwake wake`](#wake-now), or the `napwake.dev/wake-requested` annotation it sets, from any tool.

### Wake Now

```bash
kubectl napwake wake my-api -n staging
```

`wake` asks Napwake to wake the workload now, whatever its [wake setting](../concepts/wake-on-request.md), so it wakes one whose doorman asks before waking (`wake: ask`) too. A paused workload wakes, with the workloads it [depends on](../concepts/dependencies.md), and a running one has its idle clock restarted; either way it pauses again once it's had no activity for `idleAfter`. `--for 2h` keeps it awake for at least that long, as for a demo. `wake` waits for Napwake's answer, then until the workload is Running. To keep a workload running for good, remove the `idlePolicy` of a ManagedWorkload you wrote. One created from the `napwake.dev/managed` label always has an `idlePolicy`, rewritten from its annotations, so label the workload `napwake.dev/ignore: "true"` instead: Napwake stops managing it, and wakes it first if it's paused. See [kubectl Plugin](../getting-started/kubectl-plugin.md#wake-a-workload).

The plugin asks by setting the `napwake.dev/wake-requested` annotation on the ManagedWorkload to a token, the time of the request, as `pause` sets `napwake.dev/pause-requested`. Any tool can do the same, such as a pull request bot, a developer portal or a chat command waking a preview environment for someone. The value only has to differ from the last request's, such as the time or a CI run's ID:

```bash
kubectl annotate managedworkload my-api -n staging --overwrite \
  napwake.dev/wake-requested="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
```

A ManagedWorkload created from the `napwake.dev/managed` label is named after its workload, or after the workload and its kind, such as `my-api-statefulset`, when that name is taken. Setting the annotation takes `get` and `patch` on `managedworkloads` in the namespace with `kubectl annotate`, or `patch` alone from an HTTP client. Waiting for the answer with `kubectl wait` takes `get`, `list` and `watch` too, and `kubectl napwake wake` takes `get`, `list` and `patch`. Kubernetes' built-in `edit` and `admin` roles include all of them once Napwake is installed; the [preview environments guide](preview-environments.md#waking-it-from-tooling) gives a minimal Role. It's an annotation, like `pause-requested`, so a GitOps tool that owns the ManagedWorkload's spec leaves it alone.

Napwake acts on each value once: it records the value it handled in `status.lastWakeRequest`, which survives an operator restart, and says what came of it in the `WakeRequest` condition, so the tool can tell its request was taken up:

| The workload | What Napwake does | `WakeRequest` condition |
|--------------|-------------------|-------------------------|
| `Paused` | Wakes it, with a `WakeRequested` event, then `Resumed` once it's Ready | `True`, `Woken` |
| `Pausing` | [Calls the pause off](#a-request-calls-the-pause-off), and has it `Running` again | `True`, `Woken` |
| `Resuming` | Lets the wake finish, then marks the request handled | `True`, `AlreadyAwake` |
| `Running` or `Idle` | Restarts its idle clock, as activity does | `True`, `AlreadyAwake` |
| In [dry-run](dry-run.md) | Never paused, so it's awake: restarts its idle clock, which ends a would-be pause, as activity does | `True`, `AlreadyAwake` |
| Scaled to zero outside Napwake | Leaves it off: Napwake only wakes what it paused | `False`, `ScaledToZero` |
| In a [protected namespace](opt-in.md#protected-namespaces), Napwake's own operator or doorman, or a StatefulSet that deletes its PVCs when scaled down | Nothing: Napwake never pauses it | `False`, `Protected` |
| Its workload is missing, labelled `napwake.dev/ignore`, or managed by another ManagedWorkload | Nothing, with a Warning event | `False`, `TargetNotFound`, `TargetIgnored` or `DuplicateTarget` |
| Its ManagedWorkload is being deleted | Hands the workload back, with the replicas it had, as it lets go | `False`, `BeingDeleted` |
| Set before Napwake has taken the ManagedWorkload on, as by a tool that creates it and asks for its wake at once | Takes it on `Running`, so it's awake: restarts its idle clock, as for any awake workload | `True`, `AlreadyAwake` |
| Already on the ManagedWorkload as Napwake takes up a pause again after its status was lost | Nothing: it may have been answered before, and brought back with the ManagedWorkload, as by a backup restored without status, so it leaves the workload paused. Set a new value to ask | `False`, `PredatesStatus` |

As with a pause request, the condition answers the last request until the next one, through the pauses and wakes since, so its message says what happened and when, in UTC: `woken as requested at 2026-10-23 09:05 UTC`, `already awake at …`, or `not woken at 2026-10-23 09:05 UTC:` and why:

```bash
kubectl get managedworkload my-api -n staging \
  -o jsonpath='{.status.conditions[?(@.type=="WakeRequest")]}'
```

A request Napwake can't act on is answered once, not retried: ask again once the reason is gone. Unlike a pause request, a wake request may be set at any time, even on a ManagedWorkload Napwake hasn't taken on yet: it takes the ManagedWorkload on `Running` and answers the request as already awake, since at worst an old request restarts the idle clock. Only one already there when Napwake takes a pause up again, after the ManagedWorkload's status was lost, is answered `PredatesStatus` and left paused.

## When Napwake Lets Go

Napwake hands a paused workload back, scaled to the replicas it had and released from KEDA without waiting for it to be Ready, and stops routing its Services to the doorman, whenever it stops managing it:

- The ManagedWorkload is deleted, or the `napwake.dev/managed` label that created it is removed
- The workload is labelled `napwake.dev/ignore: "true"`
- Its namespace becomes [protected](opt-in.md#protected-namespaces), or it's Napwake's own operator or doorman Deployment (labelled `control-plane: controller-manager` or `doorman` in Napwake's namespace), which is never paused whatever its namespace allows; or it's a StatefulSet that deletes its PVCs when scaled down
- Another, older ManagedWorkload for the same workload takes it over, and this one becomes the `DuplicateTarget`

Each of these emits a `Resumed` event saying why and how many replicas it has. No longer managing a workload never leaves it switched off.

If the target itself is deleted while paused, its Services stop routing to the doorman, since nothing would start the pods a held request waits for; `TargetAvailable` turns `False` with reason `TargetNotFound`.

## Workloads Already at Zero

A workload scaled to zero outside Napwake, by `kubectl scale`, a pipeline, or KEDA with no active trigger, is off on purpose, and Napwake leaves it that way:

- It isn't paused, whatever its idle clock says, and a [pause request](#pause-now) is answered with `ScaledToZero`: there's nothing running to pause, and a pause would record zero replicas to restore
- It isn't woken: Napwake only wakes what it paused, so neither a request, an activity annotation, `autoResume` nor `kubectl napwake wake` scales it up
- Its Services aren't routed to the doorman, so a request to it fails as it would without Napwake, rather than being held for pods that nothing will start

The ManagedWorkload stays `Running`, with the `ScaledToZero` condition and one `ScaledToZero` event saying so. That makes scaling it to zero yourself the way to keep a workload off until someone starts it. Once its replicas are set above zero, by whoever scaled it down, Napwake manages it again, and counts that as activity, so its idle clock starts afresh.

Napwake never scales up a workload it didn't scale down. Handing back a pause recorded at zero replicas, Napwake releases it at zero, with a `Resumed` event that says so.

## Cost While Paused

Cost tracking is always on. While a workload is paused, what its paused replicas requested in CPU and memory accrues as freed, and the part of that its nodes' spare capacity couldn't hold accrues as saved; its PVCs still cost, since they stay. See [Cost Tracking](../concepts/cost-tracking.md).

## Workloads Nobody Comes Back To

A paused workload costs only its storage. To remove one for good, delete it the way it was created: from Git, by the pipeline that made the environment, or with a TTL tool such as kube-janitor. Those know when an environment is finished; Napwake only knows it's idle.

## Finalizer

The `napwake.dev/cleanup` finalizer is added to every ManagedWorkload, so that deleting one while its workload is paused scales the workload back to the replicas it had first. A hand-back that fails is retried, except when the namespace itself is being deleted: there the workload goes with it, so after 5 minutes of failures, such as a KEDA webhook that's down rejecting the release of its ScaledObject, the ManagedWorkload lets go with a `HandBackAbandoned` Warning event rather than hold the namespace's deletion forever.
