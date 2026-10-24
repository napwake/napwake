# HPA and KEDA

> Napwake works alongside a HorizontalPodAutoscaler or a KEDA ScaledObject, with nothing to set up. While the workload runs, its autoscaler sets the replicas.

Source: https://docs.napwake.dev/next/guides/autoscalers/

The ManagedWorkload’s `Autoscaled` condition says what scales it:

```plaintext
Autoscaled=True   HPA web scales it between 2 and 10 replicas; Napwake pauses it at zero, where the HPA stops scaling, and resumes it within that range
```

|          | HPA                                                                                                            | KEDA ScaledObject                                                                                                                                                                                |
| -------- | -------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| paused   | Napwake scales it to zero, where an HPA stops scaling                                                          | Napwake sets `autoscaling.keda.sh/paused-replicas: "0"` on the ScaledObject. KEDA then holds it at zero, instead of scaling it back to `minReplicaCount` or on a trigger                         |
| resuming | Napwake restores its replicas, within `minReplicas` and `maxReplicas` if they changed. Then the HPA takes over | KEDA holds it at the restored replicas until all are Ready, or Napwake [stops waiting](https://docs.napwake.dev/next/guides/pause/#resume). Then Napwake puts back your own `paused-replicas`, or removes the annotation |

CPU from the workload’s own pods counts as activity whatever the HPA does. So a workload busy enough for its HPA to scale it up isn’t idle.

## KEDA

KEDA is released only once the wake is done. Released sooner, a ScaledObject with `minReplicaCount: 0` and no active trigger could take a starting workload straight back to zero. A request the doorman held would then wait for nothing. [Letting go](https://docs.napwake.dev/next/guides/pause/#when-napwake-lets-go) of a paused workload releases it the same way. If the ScaledObject or KEDA goes while the workload is paused, it wakes as usual.

The annotation isn’t in your manifests, so Argo CD and Flux leave it alone.

Napwake leaves a workload KEDA took to zero, such as on an empty queue, to KEDA, as [already at zero](https://docs.napwake.dev/next/guides/pause/#workloads-already-at-zero). Pausing it would stop KEDA’s triggers from starting it. Such a workload gains little. Napwake adds waking on the first HTTP request, holding dependencies awake, and the forecast. It also pauses by activity it sees without a trigger. Pausing frees capacity on a KEDA workload with a `minReplicaCount` above zero, such as one kept warm in working hours.

Without KEDA, Napwake looks for ScaledObjects every 10 minutes, in case it’s installed later. `Autoscaled` shows a ScaledObject change within 15 seconds, and a pause or wake reads them afresh.

Next

* [Pause and resume →What a pause changes, and what wakes it.](https://docs.napwake.dev/next/guides/pause/)
* [GitOps tools →Leave replicas to Napwake.](https://docs.napwake.dev/next/guides/gitops/)
