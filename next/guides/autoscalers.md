# HPA and KEDA

> Napwake works alongside a HorizontalPodAutoscaler or a KEDA ScaledObject, with nothing to set up. While the workload runs, its autoscaler sets the replicas.

Source: https://docs.napwake.dev/next/guides/autoscalers/

1. Opt the workload in as usual ([Opting in](https://docs.napwake.dev/next/guides/opt-in/)). Leave its HPA or ScaledObject as it is.

2. Read what Napwake found in the `Autoscaled` condition.

   ```bash
   kubectl get managedworkload my-api -n staging \
     -o jsonpath='{.status.conditions[?(@.type=="Autoscaled")].message}'
   ```

   ```text
   HPA my-api scales it between 2 and 10 replicas; Napwake pauses it at zero, where the HPA stops scaling, and resumes it within that range
   ```

   No `Autoscaled` condition means Napwake found no HPA or ScaledObject for it.

CheckpointAwake

`Autoscaled=True`, reason `HPA` or `KEDA`. Napwake pauses the workload alongside its autoscaler.

## What Napwake does with each

|          | HPA                                                                                                            | KEDA ScaledObject                                                                                                                                                                                |
| -------- | -------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| paused   | Napwake scales it to zero, where an HPA stops scaling                                                          | Napwake sets `autoscaling.keda.sh/paused-replicas: "0"` on the ScaledObject. KEDA then holds it at zero, instead of scaling it back to `minReplicaCount` or on a trigger                         |
| resuming | Napwake restores its replicas, within `minReplicas` and `maxReplicas` if they changed. Then the HPA takes over | KEDA holds it at the restored replicas until all are Ready, or Napwake [stops waiting](https://docs.napwake.dev/next/guides/pause/#resume). Then Napwake puts back your own `paused-replicas`, or removes the annotation |

CPU from the workload’s own pods counts as activity, whatever the HPA does. So a workload busy enough for its HPA to scale it up isn’t idle.

## When the HPA scales to zero

An HPA with `minReplicas: 0` and an object or external metric can scale a workload to zero itself. From Kubernetes 1.37 that’s on by default. Napwake leaves that zero to the HPA, as it does any [workload already at zero](https://docs.napwake.dev/next/guides/pause/#workloads-already-at-zero). Nothing holds requests while the HPA has it at zero, so they fail as they would without Napwake. Napwake can still pause it while it runs, and wakes it to at least one replica. But while the metric is at zero, a wake doesn’t last. Once the HPA hasn’t needed replicas for its scale-down window, 5 minutes by default, its next check takes the workload back to zero. A request the doorman is still holding is then closed unanswered, with a `RequestNotServed` warning saying why. To have the doorman hold the first request while the workload wakes, keep `minReplicas` at 1 or more and let Napwake do the pausing.

Its `Autoscaled` condition reads:

```text
HPA my-api scales it between 0 and 10 replicas, and to zero itself: Napwake leaves that zero to the HPA, and holds no requests there. While it runs, Napwake pauses it at zero, where the HPA stops scaling, and resumes it to between 1 and 10 replicas
```

A ScaledObject’s `minReplicaCount` is 0 unless it sets one, and its `Autoscaled` says the same of KEDA.

## KEDA

Napwake releases KEDA only once the wake is done. Released sooner, a ScaledObject with `minReplicaCount: 0` and no active trigger could take a starting workload straight back to zero. A request the doorman held would then wait for nothing.

[Letting go](https://docs.napwake.dev/next/guides/pause/#when-napwake-lets-go) of a paused workload releases it the same way. If the ScaledObject or KEDA goes while the workload is paused, it wakes as usual. The annotation isn’t in your manifests, so Argo CD and Flux leave it alone.

| A KEDA workload                                                             | What Napwake does                                                                                                                                               |
| --------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Taken to zero by KEDA, such as on an empty queue                            | Leaves it to KEDA, as [already at zero](https://docs.napwake.dev/next/guides/pause/#workloads-already-at-zero). Pausing it would stop KEDA’s triggers from starting it. It gains little |
| With a `minReplicaCount` above zero, such as one kept warm in working hours | Pauses it once idle, which frees its capacity                                                                                                                   |

On top of KEDA, Napwake adds waking on the first HTTP request, holding dependencies awake, and the forecast. It also judges idle from the activity it sees, with no trigger to set up.

If KEDA isn’t installed, Napwake looks for ScaledObjects every 10 minutes, in case it’s installed later. `Autoscaled` shows a ScaledObject change within 15 seconds, and a pause or wake reads them afresh.

Next

* [Pause and resume →What a pause changes, and what wakes it.](https://docs.napwake.dev/next/guides/pause/)
* [GitOps tools →Leave replicas to Napwake.](https://docs.napwake.dev/next/guides/gitops/)
