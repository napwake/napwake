# Lifecycle

> Every ManagedWorkload moves through a defined set of phases. Napwake pauses a workload and wakes it again. It never deletes a workload or its PVCs.

Source: https://docs.napwake.dev/next/concepts/lifecycle/

1. Awake

   Running

   Active. Its activity clock is checked on each reconcile.

2. No activity for `idleAfter`, or a pause asked for

3. Awake

   Idle

   Napwake pauses it, or in dry-run reports the pause it would make.

4. At once, unless held back

5. Asleep

   Pausing

   What the pause will change is recorded. Then it's scaled to zero and its pods stopped.

6.

7. Asleep

   Paused

   At zero replicas.

8. A request, activity, or `autoResume`

9. Waking up

   Resuming

   Its previous replica count is being restored.

Running when its pods are Ready, or when Napwake stops waiting for those that aren't

The operator moves a workload on from its activity, its forecast, and pauses asked for with `kubectl napwake pause`.

* A new ManagedWorkload has no phase until its first reconcile sets `Running`. `kubectl napwake status` shows `creating`. The API accepts `Creating` but never sets it.
* A workload a person, a pipeline or KEDA scaled to zero stays `Running`, with the `ScaledToZero` condition. Napwake only pauses what’s running, and only wakes what it paused. See [Workloads already at zero](https://docs.napwake.dev/next/guides/pause/#workloads-already-at-zero).

## Transition triggers

### Automatic

| Transition                   | When                                                                                                                                                                                                                                                                                                                                                                                                              |
| ---------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Running → Idle               | No activity for `idleAfter`. Or, with [`pauseEarly`](https://docs.napwake.dev/next/concepts/forecasting/#pausing-early), for 20 minutes before hours a confident forecast expects quiet. And nothing may [hold it up](https://docs.napwake.dev/next/concepts/idle-detection/#the-decision)                                                                                                                                                                        |
| Idle → Pausing               | At once, unless it’s in dry-run, or a GitOps tool undid its last pause within the hour                                                                                                                                                                                                                                                                                                                            |
| Idle → Running               | Activity resumes while dry-run or a GitOps tool’s undoing holds the pause off. Or it’s scaled to zero outside Napwake                                                                                                                                                                                                                                                                                             |
| Paused → Resuming            | A request to its Service, or with [`wake.mode: Ask`](https://docs.napwake.dev/next/concepts/wake-on-request/#asking-before-waking) a person’s click on **Wake it**. A new `napwake.dev/wake-requested` value. A change to an activity annotation since the pause. A [Prometheus activity query](https://docs.napwake.dev/next/guides/prometheus-signals/#while-the-workload-is-paused) showing activity. `autoResume` ahead of forecast demand. Dry-run turned on |
| Pausing → Resuming → Running | Whatever would wake it once paused comes as it pauses, which [calls the pause off](https://docs.napwake.dev/next/guides/pause/#a-request-calls-the-pause-off). A `PauseCancelled` event says what came. It’s then scaled back up if its pods had begun to stop                                                                                                                                                                            |
| Paused → Running             | Something else scales it up ([GitOps tools](https://docs.napwake.dev/next/guides/gitops/)), or Napwake [lets go of it](https://docs.napwake.dev/next/guides/pause/#when-napwake-lets-go). Also from `Pausing`, once it’s at zero (`status.pause.pausedAt`) while its pods stop. It isn’t scaled back down                                                                                                                                                         |
| Resuming → Running           | Every replica it was restored to is Ready, or Napwake stops waiting (below). Scaled back to zero by something else after the resume scaled it up (`status.pause.resumedAt`), it’s left off with `ScaledToZero`                                                                                                                                                                                                    |

The resume’s wait starts once its `waitForReady` [dependencies](https://docs.napwake.dev/next/concepts/dependencies/) serve. Napwake stops waiting 2 minutes after the first replica is Ready. With none Ready, it stops at the Deployment’s `progressDeadlineSeconds` after the scale-up.

That’s 10 minutes by default. A StatefulSet has no such field, so it gets 10 minutes too. Stopping short sets `ResumeIncomplete`, and [Resume](https://docs.napwake.dev/next/guides/pause/#resume) has what follows.

### On request

| Transition                         | When                                                                                                                                                                                                  |
| ---------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Running or Idle → Pausing → Paused | [`kubectl napwake pause`](https://docs.napwake.dev/next/guides/pause/#pause-now), which sets a new `napwake.dev/pause-requested` value                                                                                        |
| Running → Idle                     | The same in dry-run, as a would-be pause                                                                                                                                                              |
| Paused → Resuming → Running        | [`kubectl napwake wake`](https://docs.napwake.dev/next/guides/pause/#wake-now), which sets a new `napwake.dev/wake-requested` value, whatever the wake setting. Or any automatic wake. A requested pause wakes like any other |
| Pausing → Resuming → Running       | `kubectl napwake wake` as it pauses, which calls the pause off, with a `PauseCancelled` event                                                                                                         |
| Idle → Running                     | The same in dry-run, ending a would-be pause. Awake, a wake request restarts the idle clock                                                                                                           |

A request runs the idle clock out at once, and the rules of an idle pause apply. The hour after a GitOps conflict doesn’t hold it back. A forecast expecting demand within the hour declines it, saying when, unless the request overrides the forecast.

## Idempotency

Every lifecycle operation is idempotent. The operator can crash and restart at any point without leaving a workload inconsistent.

Before anything is scaled, the status write that enters `Pausing` records what the pause changes in `status.pause`. That’s `previousReplicas`, the KEDA ScaledObject, and its own `paused-replicas` value. So:

* Wherever a restart or a conflicting write interrupts a pause, it’s finished or undone from the record.
* Resuming restores the recorded replica count, not a hardcoded value.
* Pausing an already-paused workload does nothing.

Before scaling, the record is also copied to the target’s `napwake.dev/paused-from` annotation, until it’s handed back. If the ManagedWorkload loses its status while paused, by a restore without status or being created again:

| It finds                                         | Then                                                    |
| ------------------------------------------------ | ------------------------------------------------------- |
| The copy, and the workload still at zero         | Back to `Paused` (`PauseRecovered`)                     |
| The copy, and the workload scaled up since       | Released (`PauseRecovered`)                             |
| Neither, while `Pausing`, `Paused` or `Resuming` | Nothing was scaled. `Running` again (`PauseRecordLost`) |

`spec.target` can’t change once set, so a paused target is never left at zero. To manage another workload, create another ManagedWorkload.

## Status conditions

A ManagedWorkload’s conditions report what the operator found, such as what holds it awake or why it won’t pause. [`IdleVetoed`](https://docs.napwake.dev/next/reference/conditions-and-events/#condition-idlevetoed) and [`HeldByDependents`](https://docs.napwake.dev/next/reference/conditions-and-events/#condition-heldbydependents) hold a pause off. [`ScaledToZero`](https://docs.napwake.dev/next/reference/conditions-and-events/#condition-scaledtozero) leaves a workload off.

[`ResumeIncomplete`](https://docs.napwake.dev/next/reference/conditions-and-events/#condition-resumeincomplete) says a resume stopped waiting. [Conditions and events](https://docs.napwake.dev/next/reference/conditions-and-events/#status-conditions) lists every condition, with each reason.

## Events

Each user-visible change emits one event, as it happens, shown by `kubectl describe`. The phases above show as [`IdleDetected`](https://docs.napwake.dev/next/reference/conditions-and-events/#event-idledetected), [`Paused`](https://docs.napwake.dev/next/reference/conditions-and-events/#event-paused) and [`Resumed`](https://docs.napwake.dev/next/reference/conditions-and-events/#event-resumed), and a wake by a request as [`WokenByRequest`](https://docs.napwake.dev/next/reference/conditions-and-events/#event-wokenbyrequest). [Conditions and events](https://docs.napwake.dev/next/reference/conditions-and-events/#events) lists every event, by what it’s about.

Next

* [Idle detection →When a running workload goes idle.](https://docs.napwake.dev/next/concepts/idle-detection/)
* [Pause and resume →Pausing and waking by hand.](https://docs.napwake.dev/next/guides/pause/)
