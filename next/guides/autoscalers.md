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
