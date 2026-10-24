# HPA and KEDA

Napwake works alongside a HorizontalPodAutoscaler or a KEDA ScaledObject, with nothing to set up. While a workload runs, its autoscaler decides how many replicas it needs, and Napwake leaves the replica count alone. Once it's idle, Napwake pauses it to zero, and on a wake brings it back within the autoscaler's range.

The ManagedWorkload says what scales it:

```
Autoscaled=True   HPA web scales it between 2 and 10 replicas; Napwake pauses it at zero, where the HPA stops scaling, and resumes it within that range
```

## HPA

An HPA stops scaling a workload at zero replicas, so a pause holds: Napwake scales the workload to zero, and the HPA leaves it there. On a wake, Napwake restores the replicas it had, kept within the HPA's `minReplicas` and `maxReplicas` should those have changed while it was paused, and the HPA takes over again.

CPU from the workload's own pods counts as activity whatever the HPA does, so a workload busy enough for its HPA to scale it up isn't idle.

## KEDA

KEDA scales its workloads itself, and would scale a paused one straight back up to its `minReplicaCount`, or as soon as a trigger fired. So for a workload a ScaledObject scales, Napwake pauses through KEDA: it sets `autoscaling.keda.sh/paused-replicas: "0"` on the ScaledObject, which has KEDA hold the workload at zero and stop scaling it, and records the ScaledObject in `status.pause.scaledObject`. If the ScaledObject already had a `paused-replicas` value of your own, it's recorded too, in `status.pause.scaledObjectPausedReplicas`.

On a wake, Napwake has KEDA hold the workload at the replicas it's restored to until they're all Ready, or Napwake stops waiting for those that aren't (see [Resume](pause.md#resume)), then puts back your own `paused-replicas` value, or removes the annotation if there was none, so KEDA carries on from there. Released any earlier, a ScaledObject with `minReplicaCount: 0` and no active trigger could take a starting workload straight back to zero, and a request the doorman held would wait for nothing.

When Napwake lets go of a paused workload, because its ManagedWorkload is deleted, it's labelled `napwake.dev/ignore`, or its namespace is protected, the ScaledObject is released the same way. If the ScaledObject, or KEDA itself, is removed while the workload is paused, there's nothing to release, and the workload is woken as usual.

The annotation isn't in your manifests, so Argo CD and Flux leave it alone.

### KEDA or Napwake?

KEDA already scales a workload to zero when its triggers say there's no work, such as an empty queue. Napwake leaves a workload KEDA has taken to zero to KEDA: it doesn't pause it, which would stop KEDA's triggers from starting it, and doesn't route its Services to the doorman; see [Workloads Already at Zero](pause.md#workloads-already-at-zero). Napwake adds what KEDA doesn't do: waking on the first HTTP request, holding dependencies awake, the forecast, and pausing by activity it can see without a trigger. A workload KEDA already takes to zero gains little; a KEDA workload with a `minReplicaCount` above zero, such as one that should stay warm during working hours, is where pausing it frees capacity.

## Permissions

The operator reads HPAs, and reads and annotates ScaledObjects. Without KEDA installed there are no ScaledObjects, and Napwake looks for them again every 10 minutes in case it's installed later. The `Autoscaled` condition reflects a ScaledObject created or changed within 15 seconds; a pause or wake always reads them afresh.
