# API reference

> Every field of the ManagedWorkload resource: its type, its default and what it does.

Source: https://docs.napwake.dev/next/reference/api/

* Group

  `napwake.dev`

* Version

  `v1alpha1`

* Kind

  `ManagedWorkload`

* Scope

  Namespaced

[Spec 7](https://docs.napwake.dev/next/reference/api/#spec-managedworkloadspec)

* [`target`](https://docs.napwake.dev/next/reference/api/#spec-target)
* [`idlePolicy`](https://docs.napwake.dev/next/reference/api/#spec-idlepolicy)
* [`dependsOn`](https://docs.napwake.dev/next/reference/api/#spec-dependson)
* [`wake`](https://docs.napwake.dev/next/reference/api/#spec-wake)
* [`prediction`](https://docs.napwake.dev/next/reference/api/#spec-prediction)
* [`costTracking`](https://docs.napwake.dev/next/reference/api/#spec-costtracking)
* [`dryRun`](https://docs.napwake.dev/next/reference/api/#spec-dryrun)

[Status 17](https://docs.napwake.dev/next/reference/api/#status-managedworkloadstatus)

* [`phase`](https://docs.napwake.dev/next/reference/api/#status-phase)
* [`conditions`](https://docs.napwake.dev/next/reference/api/#status-conditions)
* [`lastActedAt`](https://docs.napwake.dev/next/reference/api/#status-lastactedat)
* [`lastPauseRequest`](https://docs.napwake.dev/next/reference/api/#status-lastpauserequest)
* [`lastWakeRequest`](https://docs.napwake.dev/next/reference/api/#status-lastwakerequest)
* [`turnedAway`](https://docs.napwake.dev/next/reference/api/#status-turnedaway)
* [`lastTransitionTime`](https://docs.napwake.dev/next/reference/api/#status-lasttransitiontime)
* [`activity`](https://docs.napwake.dev/next/reference/api/#activity)
* [`pause`](https://docs.napwake.dev/next/reference/api/#status-pause)
* [`learnedDependencies`](https://docs.napwake.dev/next/reference/api/#status-learneddependencies)
* [`lastScaledUp`](https://docs.napwake.dev/next/reference/api/#status-lastscaledup)
* [`doorman`](https://docs.napwake.dev/next/reference/api/#status-doorman)
* [`doormanDraining`](https://docs.napwake.dev/next/reference/api/#status-doormandraining)
* [`idleWait`](https://docs.napwake.dev/next/reference/api/#status-idlewait)
* [`dryRun`](https://docs.napwake.dev/next/reference/api/#status-dryrun)
* [`prediction`](https://docs.napwake.dev/next/reference/api/#prediction)
* [`cost`](https://docs.napwake.dev/next/reference/api/#status-cost)

[Types and values 3](https://docs.napwake.dev/next/reference/api/#costrates)

* [`CostRates`](https://docs.napwake.dev/next/reference/api/#costrates)
* [`ResourceSnapshot`](https://docs.napwake.dev/next/reference/api/#status-pause-resources)
* [`WorkloadPhase`](https://docs.napwake.dev/next/reference/api/#workloadphase-values)

## Columns

What `kubectl get managedworkloads` shows.

| Column       | Shows                                                           |
| ------------ | --------------------------------------------------------------- |
| `Phase`      | The phase                                                       |
| `Freed`      | What pausing freed this month                                   |
| `Saved`      | What of that it saved                                           |
| `Would Free` | What dry-run measured it would free. Blank outside dry-run      |
| `Forecast`   | With `-o wide`, whether the forecast acts, or what it waits for |
| `Age`        | Age                                                             |

## Spec (`ManagedWorkloadSpec`)

| Field                                     | Type                                  | Default                             | Description                                                                                                                                                                                                                                      |
| ----------------------------------------- | ------------------------------------- | ----------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `target`                                  | `WorkloadRef`                         | required                            | The workload. Can’t be changed once set                                                                                                                                                                                                          |
| `target.kind`                             | `Deployment` or `StatefulSet`         | `Deployment`                        | Its kind                                                                                                                                                                                                                                         |
| `target.name`                             | string                                | required                            | Its name, same namespace                                                                                                                                                                                                                         |
| `idlePolicy`                              | `IdlePolicySpec`                      |                                     | Idle detection. Without it, never paused automatically                                                                                                                                                                                           |
| `idlePolicy.idleAfter`                    | duration, at least `1m`               | `1h`                                | Time without activity before pausing                                                                                                                                                                                                             |
| `idlePolicy.activity.cpuThreshold`        | int or string, 0.1-100, to two places | `10`                                | CPU % of requests above which it’s active. A decimal is written as a string, such as `"0.5"`                                                                                                                                                     |
| `idlePolicy.activity.prometheus[].promQL` | string                                | required in each entry              | PromQL instant query. Above zero is activity. At most 10                                                                                                                                                                                         |
| `idlePolicy.autoResume`                   | bool                                  | `false`                             | Wake ahead of confident forecast demand                                                                                                                                                                                                          |
| `idlePolicy.pauseEarly`                   | bool                                  | `false`                             | Pause after 20 idle minutes, before `idleAfter`. Only if a confident forecast expects quiet until then, and the doorman would wake it. See [Pausing early](https://docs.napwake.dev/next/concepts/forecasting/#pausing-early)                                            |
| `dependsOn[]`                             | `DependencyRef`                       |                                     | Workloads this one needs. At most 20                                                                                                                                                                                                             |
| `dependsOn[].namespace`                   | string                                | this namespace                      | Its namespace                                                                                                                                                                                                                                    |
| `dependsOn[].kind`                        | `Deployment` or `StatefulSet`         | required                            | Its kind                                                                                                                                                                                                                                         |
| `dependsOn[].name`                        | string                                | required                            | Its name                                                                                                                                                                                                                                         |
| `dependsOn[].waitForReady`                | bool                                  | `false`                             | Hold this one’s resume until it’s Ready                                                                                                                                                                                                          |
| `wake`                                    | `WakeSpec`                            |                                     | What wakes it through the doorman while paused                                                                                                                                                                                                   |
| `wake.mode`                               | `Request` or `Ask`                    | `Request`                           | What wakes it through the doorman. `Request` is any request but a known probe’s. `Ask` is only a person’s click on **Wake it**, and anything else gets a `503`. See [Asking before waking](https://docs.napwake.dev/next/concepts/wake-on-request/#asking-before-waking) |
| `wake.maxWait`                            | duration, at most `1h`                | `2m`                                | How long a request is held while it wakes                                                                                                                                                                                                        |
| `wake.page`                               | bool                                  | `true`                              | Answer a browser’s page load with the waking-up page, instead of holding it. With `mode: Ask`, a browser always gets a page                                                                                                                      |
| `prediction`                              | `PredictionSpec`                      | `{}`                                | The forecast. Left out or `{}`, it takes the defaults                                                                                                                                                                                            |
| `prediction.mode`                         | `eager`, `balanced` or `cautious`     | `balanced`                          | How soon the forecast acts, at confidence 60, 75 or 85. See [Forecast setting](https://docs.napwake.dev/next/concepts/forecasting/#forecast-setting)                                                                                                                     |
| `prediction.confidence`                   | int, 50-100                           | the mode’s                          | Accuracy (1 − WAPE, %) a season’s forecasts must reach to drive decisions. Overrides the mode’s                                                                                                                                                  |
| `costTracking`                            | `CostTrackingSpec`                    |                                     | Your own cost rates                                                                                                                                                                                                                              |
| `costTracking.rates`                      | [`CostRates`](https://docs.napwake.dev/next/reference/api/#costrates)             | node list prices, then AWS defaults | Custom rates, none negative                                                                                                                                                                                                                      |
| `dryRun`                                  | bool                                  | `false`                             | Measure without pausing. Turning it on wakes a paused workload. See [Dry run](https://docs.napwake.dev/next/guides/dry-run/)                                                                                                                                             |

### Durations

| Duration              |                                                     |
| --------------------- | --------------------------------------------------- |
| `45s`, `90m`, `1h30m` | Go durations above zero work                        |
| `1d`, `2w`, spaces    | Days, weeks and spaces don’t. Write `24h` or `168h` |
| Anything else         | The API server rejects it                           |

### CostRates

For `costTracking.rates` and `cost.listRates`.

| Field             | Type     | Default | Description       |
| ----------------- | -------- | ------- | ----------------- |
| `cpuPerHour`      | quantity | `0.031` | USD per vCPU-hour |
| `memoryPerHour`   | quantity | `0.004` | USD per GiB-hour  |
| `storagePerMonth` | quantity | `0.08`  | USD per GiB-month |

## Status (`ManagedWorkloadStatus`)

| Field                 | Type                                     | Description                                                                                                                                                                                                                                                                                                                                                                  |
| --------------------- | ---------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `phase`               | [`WorkloadPhase`](https://docs.napwake.dev/next/reference/api/#workloadphase-values) | Current lifecycle phase                                                                                                                                                                                                                                                                                                                                                      |
| `conditions[]`        | `Condition`                              | Standard Kubernetes conditions. See [Conditions and events](https://docs.napwake.dev/next/reference/conditions-and-events/#status-conditions)                                                                                                                                                                                                                                                        |
| `lastActedAt`         | time                                     | Last pause or wake, by Napwake or a scale-up from zero outside it                                                                                                                                                                                                                                                                                                            |
| `lastPauseRequest`    | string                                   | Last `napwake.dev/pause-requested` value acted on, so each acts once. Outcome in the `PauseRequest` condition. See [Pause now](https://docs.napwake.dev/next/guides/pause/#pause-now)                                                                                                                                                                                                                |
| `lastWakeRequest`     | string                                   | Last `napwake.dev/wake-requested` value acted on, so each acts once. Outcome in the `WakeRequest` condition. See [Wake now](https://docs.napwake.dev/next/guides/pause/#wake-now)                                                                                                                                                                                                                    |
| `turnedAway`          | `TurnedAway`                             | With `wake.mode: Ask`, what the doorman turned away during the current pause, or the last once it’s awake. `pausedAt`, when that pause began, and `requests`. Brought up to date about once a minute. See [What it turns away](https://docs.napwake.dev/next/concepts/wake-on-request/#what-it-turns-away)                                                                                           |
| `lastTransitionTime`  | time                                     | Last phase change                                                                                                                                                                                                                                                                                                                                                            |
| `learnedDependencies` | `LearnedDependencies`                    | Found by Napwake, held and woken like `dependsOn`. Fields `from`, `at` and `dependencies`. Each dependency has `namespace`, `kind`, `name` and `source`. `source` is `environment` (with `via`, `address`) or `wake` (with `lastSeen`). See [Learned dependencies](https://docs.napwake.dev/next/concepts/dependencies/#learned-dependencies)                                                        |
| `lastScaledUp`        | `ScaledUp`                               | Last scale-up outside Napwake while paused, which wakes it. Fields `at`, `replicas`, `by` and `gitOps`. `by` is the field manager, such as `argocd-controller`. It’s `unknown` through the scale subresource, which records none, as with `kubectl scale`. `gitOps` is `Argo CD`, `Flux`, `Fleet`, `kapp` or `Kluctl`, if from Git. See [GitOps tools](https://docs.napwake.dev/next/guides/gitops/) |
| `doorman[]`           | `DoormanRoute`                           | Service ports routed to the doorman while paused, with `service`, `portName`, `doormanPort` and `forwarded` (behind GKE NEGs, through the [forwarder](https://docs.napwake.dev/next/concepts/wake-on-request/#gke-container-native-load-balancing-negs))                                                                                                                                             |
| `doormanDraining[]`   | `DrainingPort`                           | Doorman ports that routed it until it woke, with `port` and `until`. No other workload gets the port before `until`, so a late client never reaches another. Kept in status, so an operator that restarts or becomes leader honours it                                                                                                                                       |

### `activity`

| Field                         | Type      | Description                                                                                                                                          |
| ----------------------------- | --------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| `activity.lastActivityTime`   | time      | Latest activity, any source                                                                                                                          |
| `activity.lastActivitySource` | string    | `created`, `woke`, `request`, `cpu`, `rollout`, `annotation`, `prometheus`, `unobserved` or `scaled-up`                                              |
| `activity.pauseAt`            | time      | When it pauses without more activity                                                                                                                 |
| `activity.lastEvaluatedTime`  | time      | Last activity check                                                                                                                                  |
| `activity.templateHash`       | string    | Pod template fingerprint, to detect deploys                                                                                                          |
| `activity.heldByDependents`   | \[]string | Awake dependents keeping it from pausing (`HeldByDependents`), as `namespace/name`. At most 10, first by name                                        |
| `activity.forecastBusyFrom`   | time      | Start of the hour of expected demand, while the forecast holds off the pause (`IdleVetoed`)                                                          |
| `activity.forecastQuietUntil` | time      | End of the quiet hours expected when the forecast brought the pause forward (`PausedEarly`). Kept until the pause, or dry-run’s would-be pause, ends |

### `pause`

| Field                                   | Type               | Description                                                                                                                                                                                                                                                                                                          |
| --------------------------------------- | ------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `pause`                                 | `PauseStatus`      | What a pause changed, from before scaling to zero until running again                                                                                                                                                                                                                                                |
| `pause.previousReplicas`                | int32              | Replicas before the pause, restored on resume                                                                                                                                                                                                                                                                        |
| `pause.startedAt`                       | time               | When the pause began, as it went `Pausing`, before anything was scaled. Or when Napwake took up a pause whose record was lost. The asleep page’s **Wake it** button is good for this pause alone. See [The Wake it button](https://docs.napwake.dev/next/concepts/wake-on-request/#the-wake-it-button)                                       |
| `pause.routesWrittenAt`                 | time               | When the doorman’s routes for this pause were stored in status, which every doorman pod listens on. Its EndpointSlices go in 5 seconds after. Unset for a workload the doorman doesn’t route                                                                                                                         |
| `pause.routedAt`                        | time               | When the doorman’s EndpointSlices first stood in front of its Services for this pause, beside its pods. It’s scaled to zero a few seconds after. Unset for a workload the doorman doesn’t route. See [What happens in a pause](https://docs.napwake.dev/next/guides/pause/#what-happens-in-a-pause)                                          |
| `pause.requested`                       | bool               | The pause was asked for, with `napwake.dev/pause-requested`, not decided by the idle clock or the forecast                                                                                                                                                                                                           |
| `pause.cpuBefore` `pause.cpuJustBefore` | string             | The most CPU it used, as a percentage of its requests. `cpuBefore` is through the stretch without activity that ran its clock out. `cpuJustBefore` is in its last 15 minutes, or `idleAfter` if shorter. Unset when none was measured. See [Paused while in use](https://docs.napwake.dev/next/concepts/idle-detection/#paused-while-in-use) |
| `pause.pausedAt`                        | time               | When scaled to zero, unset before. `Paused` once its pods stop                                                                                                                                                                                                                                                       |
| `pause.resumedAt`                       | time               | When the current resume scaled it up. Scaled to zero after that, not by Napwake, it’s left off                                                                                                                                                                                                                       |
| `pause.firstReadyAt`                    | time               | When the current resume first saw a replica Ready. The rest get 2 minutes more                                                                                                                                                                                                                                       |
| `pause.scaledObject`                    | string             | KEDA ScaledObject held at zero while paused. See [HPA and KEDA](https://docs.napwake.dev/next/guides/autoscalers/)                                                                                                                                                                                                                           |
| `pause.scaledObjectPausedReplicas`      | string             | Its own `autoscaling.keda.sh/paused-replicas` before the pause, put back after                                                                                                                                                                                                                                       |
| `pause.resources`                       | `ResourceSnapshot` | What it ran as the pause began, the base for savings. `replicas`, per replica `cpuMillis`, `memoryBytes`, claims’ `storageBytes`, and `nodePools` (`name`, `""` without a pool label, and its `pods`)                                                                                                                |
| `pause.wakeAnnotations`                 | object             | Activity annotations on the ManagedWorkload (`workload`) and target (`target`) as the pause began. A change wakes it                                                                                                                                                                                                 |
| `pause.queryActiveSincePause`           | bool               | A [Prometheus activity query](https://docs.napwake.dev/next/guides/prometheus-signals/#while-the-workload-is-paused) has been active at every check since a requested pause began. They don’t wake it until a check finds all quiet                                                                                                          |

### `idleWait`

| Field                                   | Type             | Description                                                                                                                                                                      |
| --------------------------------------- | ---------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `idleWait`                              | `IdleWaitStatus` | Pauses made while it was in use, which lengthen its idle clock, and its CPU use idle. See [Paused while in use](https://docs.napwake.dev/next/concepts/idle-detection/#paused-while-in-use)              |
| `idleWait.wrongPauses`                  | int32            | Pauses in a row its own use undid soon after they began. Each doubles how long the idle clock waits, up to `idleAfter` plus an hour. A pause that holds sets it back to zero     |
| `idleWait.lastWrongPause`               | `WrongPause`     | The latest. `pausedAt`, `after` (how soon it was undone), `by` (`request`, `click`, `wakeRequest` or `annotation`), `from` (the caller’s address, for a request), and `inUseCPU` |
| `idleWait.idleCPU` `idleWait.idleCPUAt` | string, time     | The most CPU it used, as a percentage of its requests, before its pauses that held, over the last week. `idleCPUAt` is when it was last measured                                 |

### `dryRun`

| Field                                            | Type               | Description                                                                                                                                                         |
| ------------------------------------------------ | ------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `dryRun`                                         | `DryRunStatus`     | What dry-run measured. Only while `spec.dryRun` is true                                                                                                             |
| `dryRun.since`                                   | time               | When measuring started                                                                                                                                              |
| `dryRun.pauses`                                  | int32              | Would-be pauses, including one under way                                                                                                                            |
| `dryRun.slept`                                   | duration           | Total length of finished would-be pauses                                                                                                                            |
| `dryRun.freedCPUHours` `dryRun.freedMemoryHours` | quantity           | vCPU-hours and GiB-hours requested during `slept`                                                                                                                   |
| `dryRun.estimatedFreed`                          | string             | Those hours priced, for display, counting a would-be pause under way to the last status write. What pausing would have freed. The `Would Free` column               |
| `dryRun.resources`                               | `ResourceSnapshot` | What it ran as the current would-be pause began                                                                                                                     |
| `dryRun.queryActiveSincePause`                   | bool               | As [`pause.queryActiveSincePause`](https://docs.napwake.dev/next/reference/api/#status-pause-queryactivesincepause), since the would-be pause under way was asked for. Queries don’t end it until all are quiet |

### `prediction`

| Field                                                      | Type                | Description                                                                                                                                                                                                                                                                  |
| ---------------------------------------------------------- | ------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `prediction.dailyPhase` `prediction.weeklyPhase`           | string              | Each season’s phase, `Observing`, `Suggesting` or `Active`                                                                                                                                                                                                                   |
| `prediction.dailyConfidence` `prediction.weeklyConfidence` | int                 | Accuracy % over the last 24 and 168 observed hours                                                                                                                                                                                                                           |
| `prediction.timeZone`                                      | string              | IANA zone for hours and weekdays, such as `Europe/London`. The operator’s `--timezone` (Helm value `timezone`)                                                                                                                                                               |
| `prediction.threshold`                                     | int                 | Confidence it acts at, `spec.prediction.confidence` or the mode’s                                                                                                                                                                                                            |
| `prediction.acting`                                        | bool                | Whether it drives decisions, holding off pauses and, with `autoResume`, waking ahead of demand                                                                                                                                                                               |
| `prediction.startsActing`                                  | time                | When it should start acting, if the coming days repeat the last week. Unset while acting, or if not within four weeks                                                                                                                                                        |
| `prediction.reason` `prediction.message`                   | string              | Why it is or isn’t acting, as `Acting`, `Observing`, `EarningConfidence`, `Relearning` or `NotLearning`, and a sentence. See [When it acts](https://docs.napwake.dev/next/concepts/forecasting/#when-it-acts)                                                                                        |
| `prediction.history`                                       | `PredictionHistory` | What it learned from Prometheus history when management began. Fields `reason` (`Learned`, `NoPrometheusURL`, `NoHistory`, `NoCPURequests`, `QueryFailed`), `hours`, `from`, `to`, `message`. See [Learning from history](https://docs.napwake.dev/next/concepts/forecasting/#learning-from-history) |
| `prediction.state`                                         | string              | Learned state, gzipped and base64-encoded                                                                                                                                                                                                                                    |

### `cost`

| Field                                                                                   | Type                      | Description                                                                                                                                                                                                 |
| --------------------------------------------------------------------------------------- | ------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `cost`                                                                                  | `CostStatus`              | This calendar month’s cost, in UTC. See [Cost tracking](https://docs.napwake.dev/next/concepts/cost-tracking/#status-fields)                                                                                                        |
| `cost.tracked`                                                                          | duration                  | How much of the month the totals cover                                                                                                                                                                      |
| `cost.lastAccumulatedAt`                                                                | time                      | When totals were last brought up to date                                                                                                                                                                    |
| `cost.awakeCPUHours` `cost.awakeMemoryHours`                                            | quantity                  | vCPU-hours and GiB-hours requested while awake                                                                                                                                                              |
| `cost.storageHours`                                                                     | quantity                  | GiB-hours of claims its pods mount, in every phase                                                                                                                                                          |
| `cost.pausedCPUHours` `cost.pausedMemoryHours`                                          | quantity                  | vCPU-hours and GiB-hours the paused replicas had requested, while paused. What pausing freed                                                                                                                |
| `cost.savedCPUHours` `cost.savedMemoryHours`                                            | quantity                  | Its share of the paused requests its node pools’ spare capacity can’t hold, every 5 minutes. What pausing saved, at least. See [How saved is measured](https://docs.napwake.dev/next/concepts/cost-tracking/#how-saved-is-measured) |
| `cost.costThisMonth`                                                                    | string                    | Awake and storage hours, priced                                                                                                                                                                             |
| `cost.freedThisMonth`                                                                   | string                    | Paused hours, priced                                                                                                                                                                                        |
| `cost.savedThisMonth`                                                                   | string                    | Saved hours, priced. `unknown` while `savingsUnknown` is set                                                                                                                                                |
| `cost.savingsUnknown`                                                                   | string                    | Why saved can’t be measured, such as pods outside `watchNamespaces` being invisible. Unset when measured                                                                                                    |
| `cost.costWithoutNapwakeThisMonth`                                                      | string                    | `costThisMonth` plus `freedThisMonth`                                                                                                                                                                       |
| `cost.projectedMonthlyCost` `cost.projectedMonthlyFreed` `cost.projectedMonthlySavings` | string                    | The month so far, carried to a full month. `pending` until a day is tracked                                                                                                                                 |
| `cost.running`                                                                          | `ResourceSnapshot`        | What it ran when last brought up to date while awake                                                                                                                                                        |
| `cost.pricedAt` `cost.pricedTemplateHash`                                               | time, string              | When requests and list rates were last read, and the pod template then                                                                                                                                      |
| `cost.listRates`                                                                        | [`CostRates`](https://docs.napwake.dev/next/reference/api/#costrates) | List rates of the nodes its pods last ran on, spot at spot. Unset without a list price                                                                                                                      |
| `cost.listPrice`                                                                        | string                    | Which list prices `listRates` are, `OnDemand`, `Spot` or `SpotAndOnDemand`. Unset with it                                                                                                                   |
| `cost.resourceReduction`                                                                | `ResourceReduction`       | What the current pause freed, as `cpuMillis`, `memoryBytes` and `replicas`. Only while paused                                                                                                               |

## WorkloadPhase values

| Value                                          | Meaning                                                                 |
| ---------------------------------------------- | ----------------------------------------------------------------------- |
| `Running` `Idle` `Pausing` `Paused` `Resuming` | The phases. [Lifecycle](https://docs.napwake.dev/next/concepts/lifecycle/) says what each means |
| No phase                                       | Until the first reconcile                                               |
| `Creating`                                     | Accepted by the schema but never set                                    |

Next

* [ManagedWorkload guide →Writing one yourself.](https://docs.napwake.dev/next/guides/managed-workload/)
* [Lifecycle →Phases, conditions, events.](https://docs.napwake.dev/next/concepts/lifecycle/)
* [Labels and annotations →Settings as annotations.](https://docs.napwake.dev/next/reference/labels-annotations/)
