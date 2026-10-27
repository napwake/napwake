# Dependencies

> Keep a database awake while the apps that use it are, and wake it with them.

Source: https://docs.napwake.dev/next/concepts/dependencies/

A request wakes api, and the doorman holds it. Napwake wakes api's dependencies, postgres and cache, at the same time.

Databases, message brokers, caches and workers never see outside traffic. So their [activity clock](https://docs.napwake.dev/next/concepts/idle-detection/) can run out while the workloads using them are busy. Napwake [learns](https://docs.napwake.dev/next/concepts/dependencies/#learned-dependencies) who needs what from each workload’s environment, or you declare it with `dependsOn`:

managedworkload.yaml

```yaml
apiVersion: napwake.dev/v1alpha1
kind: ManagedWorkload
metadata:
  name: api
  namespace: preview-42
spec:
  target: {kind: Deployment, name: api}
  dependsOn:
    - {kind: StatefulSet, name: postgres}                         # same namespace
    - {namespace: messaging, kind: StatefulSet, name: nats}       # another namespace
  idlePolicy:
    idleAfter: 1h
  prediction: {}                                                  # the default, balanced
```

| `dependsOn`      |                                                                                                                                                               |
| ---------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Goes on          | The workload that **needs** the other                                                                                                                         |
| Names            | The dependency’s Deployment or StatefulSet, not its ManagedWorkload                                                                                           |
| `namespace`      | Defaults to the ManagedWorkload’s own                                                                                                                         |
| As an annotation | `napwake.dev/depends-on: "statefulset/postgres, messaging/statefulset/nats"` on the workload or its namespace. See [Opting in](https://docs.napwake.dev/next/guides/opt-in/#settings) |

## The rules

| Rule                                                                  |                                                                                                                                                                                                                                                                                                                                                     |
| --------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| A dependency doesn’t pause while anything that depends on it is awake | Its own clock keeps running meanwhile                                                                                                                                                                                                                                                                                                               |
| The last dependent pauses                                             | The dependency pauses on its next check, if its own clock has run out                                                                                                                                                                                                                                                                               |
| A dependent that’s waking holds it too                                | Paused, with a wake already asked for: a request the doorman holds for it, an [activity annotation](https://docs.napwake.dev/next/concepts/idle-detection/#activity-annotations) on its ManagedWorkload or its workload, or a wake request, set since its pause. So the dependency doesn’t start to pause as the dependent wakes                                            |
| The dependents are read again before it pauses                        | From the API server, which can have a wake, or a dependency just learned, that the operator’s cache hasn’t seen yet. Not in dry-run, where nothing pauses. Once more just before it’s scaled to zero, after the doorman’s takeover. A dependent awake then, such as one that learned it depends on it since, calls the pause off (`PauseCancelled`) |
| Waking a workload wakes its dependencies                              | Before it resumes, Napwake sets `napwake.dev/last-activity` on each dependency’s ManagedWorkload, the usual [activity annotation](https://docs.napwake.dev/next/concepts/idle-detection/#activity-annotations) wake                                                                                                                                                         |
| Seeing it                                                             | `kubectl describe` shows it                                                                                                                                                                                                                                                                                                                         |
| It chains                                                             | If Postgres depends on something, that wakes too                                                                                                                                                                                                                                                                                                    |
| Everything wakes at once                                              | By default, so a chain doesn’t add up to cold starts in a row                                                                                                                                                                                                                                                                                       |
| Waking a dependency never holds up its dependent                      | The dependent wakes, or is handed back or deleted, regardless                                                                                                                                                                                                                                                                                       |
| A dependency can’t be woken                                           | As when a policy engine won’t let Napwake annotate its ManagedWorkload. The dependent gets a `DependencyWakeFailed` warning event                                                                                                                                                                                                                   |

A dependency held awake says why:

```plaintext
HeldByDependents=True   kept awake for preview-42/api, preview-43/api
```

## Waiting for a dependency to be ready

`waitForReady` keeps the dependent from starting until the dependency serves. Set it for an app that fails if it starts first. An example is an API that gives up after a few retries while its database replays its write-ahead log:

```yaml
dependsOn:
  - {kind: StatefulSet, name: postgres, waitForReady: true}
  - {kind: Deployment, name: cache}          # wakes at the same time
```

The dependent stays in `Resuming`, not scaled up:

```plaintext
WaitingForDependencies=True   waiting for preview-42/statefulset/postgres (0 ready)
```

| The dependency has                                 | It serves                                                                                                                      |
| -------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| Every replica Ready                                | At once                                                                                                                        |
| Some replicas Ready, as with one pod crash-looping | With those, so the dependent doesn’t wait for good                                                                             |
| Some Ready, and Napwake manages it                 | Once its own [resume](https://docs.napwake.dev/next/concepts/lifecycle/#automatic) has finished, even short of every replica (`ResumeIncomplete=True`) |
| Some Ready, and Napwake doesn’t manage it          | Once the dependent has waited 2 minutes, the time a resume gives its other replicas                                            |
| No replica Ready                                   | Never. It’s always waited for, up to the limit below                                                                           |

| The wait                                              |                                                                                                                                           |
| ----------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------- |
| Limit                                                 | The dependency Deployment’s `progressDeadlineSeconds`, 600 by default, when Kubernetes reports the rollout failed                         |
| Limit for a StatefulSet                               | 10 minutes, as a StatefulSet has none                                                                                                     |
| Counted from                                          | When the dependent began resuming                                                                                                         |
| Why it’s long                                         | A slow start is why you set it                                                                                                            |
| Why a limit                                           | Otherwise a dependency that’s up but never Ready would leave the dependent at zero for good                                               |
| Never Ready, such as                                  | Crash-looping, or with an image that can’t be pulled                                                                                      |
| Past the limit                                        | The dependent starts anyway, with `WaitingForDependencies=False`, reason `DependencyWontStart`, and a warning event naming the dependency |
| No wait, for an unmanaged dependency at zero          | Waiting can’t start it. It’s reported the same way                                                                                        |
| No wait, for one that waits for the dependent in turn | Through `waitForReady`, directly or through others, as each would wait for the other                                                      |
| Then                                                  | Reason `DependencyCycle`, and both start together                                                                                         |
| While waiting                                         | A managed dependency still paused is woken again every few seconds                                                                        |

Past the limit, the condition reads:

```plaintext
WaitingForDependencies=False   not waiting for preview-42/statefulset/postgres (it isn't Ready after 10 minutes)
```

## Edge cases

| Situation                                                                                                                                                                | What happens                                                                                                                                              |
| ------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------- |
| The dependency has no ManagedWorkload                                                                                                                                    | It’s never paused, so there’s nothing to hold or wake                                                                                                     |
| The dependency doesn’t exist                                                                                                                                             | `DependencyNotFound=True` on the dependent. Nothing is blocked, a `waitForReady` resume included                                                          |
| It’s outside `watchNamespaces`, or Napwake can’t read it                                                                                                                 | `DependencyNotFound=True`, reason `DependencyNotVisible`, saying which. It isn’t held or woken, and nothing is blocked                                    |
| A cycle in `dependsOn`, A on B and B on A                                                                                                                                | Each would hold the other awake forever. Neither pauses, and both report `DependencyCycle=True` until it’s removed                                        |
| A workload listed as its own dependency                                                                                                                                  | Ignored. It’s neither a cycle nor something to wait for                                                                                                   |
| A namespace’s `napwake.dev/depends-on` names several workloads, such as `statefulset/postgres, statefulset/redis`                                                        | Its other workloads take the list. The ones it names don’t, so postgres and redis don’t depend on each other                                              |
| A named workload depends on another by its own annotation, as auth’s naming `statefulset/postgres` under a namespace naming `deployment/auth`                            | That one doesn’t take the list either, so postgres doesn’t depend on auth                                                                                 |
| Learned dependencies in a loop, such as two services that call each other                                                                                                | Not a cycle. A learned dependent doesn’t hold a workload it’s itself a dependency of                                                                      |
| Then                                                                                                                                                                     | Each pauses when idle, and waking either wakes the other                                                                                                  |
| `kubectl napwake pause` on a dependency in use                                                                                                                           | Not paused, as for an idle pause. `PauseRequest=False`, reason `HeldByDependents`, and a `HeldByDependents` event naming the dependents. Pause them first |
| `dryRun: true`                                                                                                                                                           | The hold applies and is reported, and nothing changes                                                                                                     |
| A dry-run dependent that would have paused (`Idle`)                                                                                                                      | It stops holding, so a chain’s would-be pauses all count                                                                                                  |
| A dependency that pauses for real, held by a dry-run dependent                                                                                                           | It stays held, as the dependent’s pods are still up                                                                                                       |
| A dependent managing nothing, as one being deleted, a duplicate (`DuplicateTarget=True`), or one whose target is gone (`TargetAvailable=False`, reason `TargetNotFound`) | It holds nothing up. There’s no running workload to keep them up for                                                                                      |
| Many dependents awake at once                                                                                                                                            | `HeldByDependents` names the first 10, by namespace and name, and counts the rest                                                                         |

## Across namespaces

A dependency can be in another namespace if that namespace allows it. So preview environments can share a database or broker. List the namespaces allowed in its `napwake.dev/dependents-from`, or `"*"` for any:

```bash
kubectl annotate namespace shared napwake.dev/dependents-from="preview-42, preview-43"
```

| What        | Rule                                                                                                                                   |
| ----------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| Why         | Without it, anyone who can write a ManagedWorkload, or a workload’s environment, in one namespace could keep another’s workloads awake |
| Not allowed | A dependency, declared or learned, isn’t held, woken or waited for. The dependent reports it without saying whether it exists          |
| Never       | Dependents never pause, scale down or delete a dependency. They only keep it awake or wake it                                          |
| At most     | So one in another namespace can only keep it running                                                                                   |
| Holders     | `HeldByDependents` always names them by namespace                                                                                      |

A dependency that isn’t allowed reads:

```plaintext
DependencyNotAllowed=True   not in a namespace that allows dependents from preview-42 with napwake.dev/dependents-from, so not held or woken: shared/statefulset/postgres
```

## Learned dependencies

Napwake learns dependencies from each workload’s environment, and from the requests that wake it. Each is held and woken exactly like a `dependsOn`, with no YAML to write.

| From the environment          |                                                                                                                                           |
| ----------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------- |
| Read                          | The environment of every workload Napwake manages, as [`kubectl napwake scan`](https://docs.napwake.dev/next/getting-started/kubectl-plugin/#scan-a-cluster) does |
| Counts                        | A variable, literal or from a ConfigMap, holding an address that names a Service                                                          |
| In any form cluster DNS gives | `postgres`, `postgres.preview-42.svc.cluster.local`, or a headless Service’s pod, `postgres-0.postgres-hl`                                |
| Followed to                   | The workload the Service selects                                                                                                          |
| Read again                    | When the pod template changes, and hourly for ConfigMaps changed since                                                                    |
| Never read                    | Secrets. An address set only in a Secret isn’t learned from the environment                                                               |
| Reported                      | A `DependenciesLearned` event when the set changes                                                                                        |
| The event says                | Whether each is woken with the workload. Only one Napwake manages, in a namespace that [allows it](https://docs.napwake.dev/next/concepts/dependencies/#across-namespaces), is                |

status.learnedDependencies

```yaml
learnedDependencies:
  from: 3f9c2a71d0e4b815
  at: "2026-10-04T09:12:00Z"
  dependencies:
    - {namespace: preview-42, kind: StatefulSet, name: postgres, source: environment, via: PGHOST, address: postgres-0.postgres-hl}
    - {namespace: preview-42, kind: StatefulSet, name: redis, source: wake}
```

Opt the dependency in too, to have them paused and woken together:

```plaintext
api: depends on preview-42/postgres (PGHOST), which isn't managed by Napwake, so it isn't woken with api: opt it in to have them paused and woken together
```

### From wakes

A request that wakes a workload teaches which workload sent it.

| What            | How                                                                                                                         |
| --------------- | --------------------------------------------------------------------------------------------------------------------------- |
| How             | The [doorman](https://docs.napwake.dev/next/concepts/wake-on-request/) records where the request came from. The operator finds the pod that sent it |
| Learned         | If Napwake manages that pod’s workload, it depends on the one it woke, with `source: wake`                                  |
| Catches         | Addresses in Secrets, built in code, or read from files, the first time they’re used                                        |
| Teaches nothing | A request from a workload Napwake doesn’t manage, such as an ingress controller                                             |
| Nor             | A request from a pod on the node’s network, or a `napwake.dev/last-request-from` set by hand                                |
| Checked         | Only an address the doorman wrote counts. The API server’s field managers must show Napwake’s own set it                    |
| Admission       | The chart’s admission policy lets only the doorman set `napwake.dev/last-request-from`                                      |
| Looked for in   | The woken workload’s namespace, and those its [`napwake.dev/dependents-from`](https://docs.napwake.dev/next/concepts/dependencies/#across-namespaces) allows                    |
| Kept            | For 14 days after the last request that woke the dependency, through new reads of the environment                           |
| `lastSeen`      | Records that request, at most once a day                                                                                    |
| While it’s kept | The dependency is held and woken with the workload                                                                          |
| Renewed         | Not while it’s kept, as the workload’s requests no longer reach the doorman                                                 |
| Dropped         | Even in steady use, 14 days after it was learned or last renewed                                                            |
| Learned again   | From the next request that wakes the dependency. That costs one cold start, about once a fortnight                          |
| One-off calls   | Such as someone’s `curl` from a pod. Forgotten the same way, so they don’t tie two workloads together for good              |

### Dropping a learned link

A wrong learned link can only keep a workload awake longer, or wake it, never pause it. To drop one, list it in `napwake.dev/ignore-dependencies` on the workload or its ManagedWorkload. Separate entries with commas, as `namespace/name` or a name in its own namespace:

```yaml
metadata:
  annotations:
    napwake.dev/ignore-dependencies: "redis, messaging/nats"
```

`dependsOn` still applies alongside, and is the only way to set `waitForReady`. [`kubectl napwake deps`](https://docs.napwake.dev/next/getting-started/kubectl-plugin/#show-a-workloads-dependencies) shows a workload’s links both ways, and where each came from.

## When you need `dependsOn`

[Wake on request](https://docs.napwake.dev/next/concepts/wake-on-request/) wakes a workload behind a normal Service by itself. Learned dependencies cover its environment, headless addresses included. `dependsOn` is for the rest:

* `waitForReady`, so the first request doesn’t wait for the database too, or the app doesn’t fail starting before it.
* Addresses not seen in use yet, from Secrets, code or files.
* A workload behind no Service, which no address names.

[`kubectl napwake scan`](https://docs.napwake.dev/next/getting-started/kubectl-plugin/#scan-a-cluster) lists the dependencies in each workload’s environment, and what Napwake does with each:

| When                             | What Napwake does                                                   |
| -------------------------------- | ------------------------------------------------------------------- |
| Napwake manages both             | Woken and held with the workload that needs it, declared or learned |
| It doesn’t manage the dependency | Never paused                                                        |

Next

* [Wake on request →How the doorman wakes a paused workload.](https://docs.napwake.dev/next/concepts/wake-on-request/)
* [kubectl napwake deps →A workload's links, and where each came from.](https://docs.napwake.dev/next/getting-started/kubectl-plugin/#show-a-workloads-dependencies)
