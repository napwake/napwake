# ManagedWorkload

> The ManagedWorkload is Napwake's core resource. Each one names a Deployment or StatefulSet whose lifecycle Napwake manages.

Source: https://docs.napwake.dev/next/guides/managed-workload/

You may not need one

The `napwake.dev/managed` label creates one from annotations, as in [Opting in](https://docs.napwake.dev/next/guides/opt-in/). Yours wins over the label.

1. Name the target.

   managedworkload.yaml

   ```yaml
   apiVersion: napwake.dev/v1alpha1
   kind: ManagedWorkload
   metadata:
     name: my-api
     namespace: staging
   spec:
     target:
       kind: Deployment
       name: my-api
   ```

   Only `target` is required, and the rest takes its defaults. Napwake then watches the workload, tracks its cost and learns its forecast. It pauses it only when you ask, with [`kubectl napwake pause`](https://docs.napwake.dev/next/guides/pause/#pause-now).

2. Add an idle policy, and what else you need.

   managedworkload.yaml

   ```yaml
   apiVersion: napwake.dev/v1alpha1
   kind: ManagedWorkload
   metadata:
     name: my-api
     namespace: staging
   spec:
     target:
       kind: Deployment
       name: my-api


     idlePolicy:
       idleAfter: 1h
       activity:
         cpuThreshold: 10
         prometheus:
           - promQL: 'sum(rate(nginx_ingress_controller_requests{exported_namespace="staging", exported_service="my-api"}[5m]))'
       autoResume: true


     wake:
       mode: Request
       maxWait: 2m
       page: true


     prediction:
       mode: balanced


     costTracking:
       rates:
         cpuPerHour: "0.031"
         memoryPerHour: "0.004"
         storagePerMonth: "0.08"


     dryRun: false
   ```

   With an `idlePolicy`, Napwake pauses it on its own. Each field’s type, default and limits are in the [API reference](https://docs.napwake.dev/next/reference/api/).

CheckpointAwake · until idle

Once `my-api` has had no activity for `idleAfter`, Napwake pauses it. A request to its Service wakes it.

## What each part does

| Field          | What it does                                                                                                                                                                                                                                                                       |
| -------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `target`       | The workload, in the same namespace. It can’t change once set, since a paused target would be left at zero. The API server refuses the edit. Create another ManagedWorkload for another workload                                                                                   |
| `idlePolicy`   | When it’s idle, and pauses. Deploys and the `napwake.dev/last-activity` and `napwake.dev/active-until` annotations always count as activity. See [Idle detection](https://docs.napwake.dev/next/concepts/idle-detection/#activity-annotations) and [Prometheus activity](https://docs.napwake.dev/next/guides/prometheus-signals/) |
| `dependsOn`    | Up to 20 workloads this one needs. They aren’t paused while it’s awake, and waking it wakes them. See [Dependencies](https://docs.napwake.dev/next/concepts/dependencies/)                                                                                                                                 |
| `wake`         | What wakes it through the doorman while paused. `mode: Request` is any request, and `mode: Ask` only a person’s click. See [Wake on request](https://docs.napwake.dev/next/concepts/wake-on-request/#asking-before-waking)                                                                                 |
| `prediction`   | How soon the forecast acts. Leave it out, or write `prediction: {}`, for the default. See [Forecast setting](https://docs.napwake.dev/next/concepts/forecasting/#forecast-setting) and [Forecasting](https://docs.napwake.dev/next/concepts/forecasting/#phase-lifecycle)                                                          |
| `costTracking` | Your own `rates`, in place of the nodes’ list price. None may be negative. See [Cost tracking](https://docs.napwake.dev/next/concepts/cost-tracking/#prices)                                                                                                                                               |
| `dryRun`       | Measures without pausing, waking the workload if it was paused. See [Dry run](https://docs.napwake.dev/next/guides/dry-run/)                                                                                                                                                                               |

## Edit the one the label created

Settings with more than one value aren’t annotations. Set them on the label’s ManagedWorkload, such as with `kubectl patch`, and Napwake keeps them.

| The annotations own these. Edits are overwritten                                                                                                                                        | Yours, and kept                                                                                                                                            |
| --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `dryRun`, `idlePolicy` (`idleAfter`, `activity.cpuThreshold`, `autoResume`, `pauseEarly`), the workloads that `dependsOn` lists, `wake`, and `prediction.mode` (`napwake.dev/forecast`) | `idlePolicy.activity.prometheus`, `waitForReady` on a listed dependency, `costTracking`, and `prediction.confidence`, which overrides the mode’s threshold |

## Scaled up outside Napwake

A paused workload scaled up by anything else, such as `kubectl scale` or a deploy, wakes. Whoever did it wants it running. `status.lastScaledUp` records when, and what did it.

While it runs, Napwake leaves its replicas to its team or HPA. For GitOps, see [GitOps tools](https://docs.napwake.dev/next/guides/gitops/).

### A pause undone at once

An operator that owns a workload and sets its replicas, such as mattermost-operator, sets them back within seconds of each pause. Napwake counts a scale-up as undoing the pause at once when a field manager set the replicas, and either:

* the workload has a controller owner, at any time, or
* it came within 5 minutes of the scale to zero.

`kubectl scale`, an HPA and KEDA scale through the scale subresource, which records no field manager, so they never count. A GitOps tool gets a [`GitOpsConflict`](https://docs.napwake.dev/next/guides/gitops/#what-napwake-does) instead.

The workload still wakes, as for any scale-up. A `PauseUndone` condition and warning event say who, how soon, its owner, and when Napwake pauses it again:

```text
PauseUndone=True   mattermost-operator set the replicas back 3 seconds after the pause, undoing it. Mattermost/chat controls the workload. Napwake can pause it again from 15:00 UTC. To stop this, opt the workload out, or have its owner leave the replicas alone: https://docs.napwake.dev/guides/managed-workload/#a-pause-undone-at-once
```

Each pause undone in a row doubles the wait, so the pods restart a few times a day at most:

| Undone in a row (`status.lastScaledUp.undone`) | Napwake pauses again after the scale-up, in |
| ---------------------------------------------- | ------------------------------------------- |
| 1                                              | 1 hour                                      |
| 2                                              | 2 hours                                     |
| 3                                              | 4 hours                                     |
| 4                                              | 8 hours                                     |
| 5                                              | 16 hours                                    |
| 6 or more                                      | 24 hours                                    |

A pause that holds an hour clears the condition and the count, with a `PauseUndoneResolved` event. A scale-up that doesn’t count, such as `kubectl scale`, leaves both. [`kubectl napwake status`](https://docs.napwake.dev/next/getting-started/kubectl-plugin/#see-what-napwake-is-doing) lists it under Needs attention, and NEXT shows `pauses at 15:00 UTC (after its pause was undone)`.

To stop it, take the workload out of Napwake:

| It’s opted in by                                         | Do this                                                                                                                                                                          |
| -------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| A ManagedWorkload you wrote                              | Delete the ManagedWorkload                                                                                                                                                       |
| A label, on it or its namespace                          | Label it `napwake.dev/ignore: "true"` through its owner, such as in the labels its custom resource sets on the Deployment. A label put on by hand goes at the owner’s next write |
| Its namespace’s label, with an owner that can’t label it | Narrow the opt-in. Remove the namespace’s label, and label the workloads you want one by one ([Opting in](https://docs.napwake.dev/next/guides/opt-in/))                                                 |

If the owner can be told to leave the replicas alone, that stops it too.

Before any pause, a `ControllerOwned` condition names the workload’s controller owner. `kubectl napwake status` and [`kubectl napwake scan`](https://docs.napwake.dev/next/getting-started/scan/) note it. An owner that leaves the replicas alone never undoes a pause.

## One workload per target

Only one ManagedWorkload can manage a Deployment or StatefulSet. A newer one for the same target gets a `DuplicateTarget` condition naming the other. It does nothing until the older one is gone.

Next

* [Pause and resume →What a pause changes.](https://docs.napwake.dev/next/guides/pause/)
* [API reference →Every field and its default.](https://docs.napwake.dev/next/reference/api/)
