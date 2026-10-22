# API Reference

## ManagedWorkload

**Group:** `hybernate.io` | **Version:** `v1alpha1` | **Kind:** `ManagedWorkload` | **Scope:** Namespaced

`kubectl get managedworkloads` shows each one's `Phase`, what pausing it has `Freed` this month and what of that it `Saved`, what dry-run measured it `Would Free`, blank for one that isn't in dry-run, and its `Age`.

### Spec (`ManagedWorkloadSpec`)

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `target` | `WorkloadRef` | Yes | | The workload to manage. Can't be changed once set |
| `target.kind` | `Deployment` \| `StatefulSet` | No | `Deployment` | Target kind |
| `target.name` | string | Yes | | Target name (same namespace) |
| `idlePolicy` | `IdlePolicySpec` | No | | Idle detection configuration. Without it, the workload is never paused automatically |
| `idlePolicy.idleAfter` | duration (at least `1m`) | No | `1h` | Time without activity before pausing |
| `idlePolicy.activity.cpuThreshold` | int | No | `10` | CPU utilization % of requests above which the workload is active (1-100) |
| `idlePolicy.activity.prometheus[].promQL` | string | Yes (per entry) | | PromQL instant query; a result above zero is activity. At most 10 queries |
| `idlePolicy.autoResume` | bool | No | `false` | Wake ahead of confident forecast demand |
| `idlePolicy.pauseEarly` | bool | No | `false` | Pause before `idleAfter` runs out, once idle for 20 minutes, when a confident forecast expects the hours until then to be quiet and a request would wake the workload through the doorman. See [Pausing Early](../concepts/forecasting.md#pausing-early) |
| `dependsOn[]` | `DependencyRef` | No | | Workloads this one needs, at most 20 |
| `dependsOn[].namespace` | string | No | this namespace | Namespace of the dependency |
| `dependsOn[].kind` | `Deployment` \| `StatefulSet` | Yes | | Kind of the dependency |
| `dependsOn[].name` | string | Yes | | Name of the dependency's workload |
| `dependsOn[].waitForReady` | bool | No | `false` | Hold this workload's resume until the dependency is Ready |
| `wake` | `WakeSpec` | No | | Waking on request while paused |
| `wake.onRequest` | bool | No | `true` | Route the workload's Services to the doorman while paused |
| `wake.maxWait` | duration (at most `1h`) | No | `2m` | How long a request is held while the workload wakes |
| `wake.page` | bool | No | `true` | Answer a browser loading a page with a waking-up page instead of holding it |
| `prediction` | `PredictionSpec` | No | `{}` | Forecast engine config; left out or `{}`, it takes the defaults |
| `prediction.mode` | `eager` \| `balanced` \| `cautious` | No | `balanced` | How soon the forecast acts on what it has learned: at a confidence of 60, 75 or 85. See [Forecast setting](../concepts/forecasting.md#forecast-setting) |
| `prediction.confidence` | int (50-100) | No | the mode's | Accuracy (1 − WAPE, %) a season's forecasts must reach before they drive decisions, in place of the mode's |
| `costTracking` | `CostTrackingSpec` | No | | Custom cost rate overrides |
| `costTracking.rates` | `CostRates` | No | node list prices, then AWS defaults | Custom cost rates, none of them negative |
| `dryRun` | bool | No | `false` | Measure without pausing; see [Dry Run](../guides/dry-run.md). A workload already paused when it's turned on is woken |

A duration is a Go duration above zero, such as `45s`, `90m` or `1h30m`. Days and weeks (`1d`, `2w`) and spaces aren't durations: write `24h` or `168h`. The API server rejects any other value.

### CostRates

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `cpuPerHour` | quantity | `0.031` | USD per vCPU-hour |
| `memoryPerHour` | quantity | `0.004` | USD per GiB-hour |
| `storagePerMonth` | quantity | `0.08` | USD per GiB-month |

### Status (`ManagedWorkloadStatus`)

| Field | Type | Description |
|-------|------|-------------|
| `phase` | `WorkloadPhase` | Current lifecycle phase |
| `conditions[]` | `Condition` | Standard Kubernetes conditions; see [Lifecycle](../concepts/lifecycle.md#status-conditions) |
| `lastActedAt` | time | When the target was last paused or woken: by Hybernate, or scaled up from zero outside it, such as with `kubectl scale` or by a GitOps tool |
| `lastPauseRequest` | string | The last `hybernate.io/pause-requested` value acted on, so each request is acted on once; the `PauseRequest` condition says what came of it. See [Pause Now](../guides/pause.md#pause-now) |
| `lastTransitionTime` | time | Last phase change |
| `activity.lastActivityTime` | time | Most recent activity from any source |
| `activity.lastActivitySource` | string | `created`, `woke`, `request`, `cpu`, `rollout`, `annotation`, `prometheus`, `unobserved`, or `scaled-up` |
| `activity.pauseAt` | time | When the workload pauses if no further activity is seen |
| `activity.lastEvaluatedTime` | time | When activity was last checked |
| `activity.templateHash` | string | Fingerprint of the target's pod template, used to detect deploys |
| `activity.heldByDependents` | []string | The awake workloads that use this one, as `namespace/name`, while they keep it from pausing (`HeldByDependents`): at most 10, the first by name |
| `activity.forecastBusyFrom` | time | The start of the hour the forecast expects demand in, while it keeps the workload from pausing (`IdleVetoed`) |
| `activity.forecastQuietUntil` | time | The end of the hours the forecast expected to be quiet when it brought the workload's pause forward (`PausedEarly`), until the pause, or the would-be pause in dry-run, ends |
| `pause` | `PauseStatus` | What a pause changed, from before the workload is scaled to zero until it's running again |
| `pause.previousReplicas` | int32 | Replicas before the pause, restored on resume |
| `pause.pausedAt` | time | When it was scaled to zero; unset until then. The pause is `Paused` once its pods have stopped |
| `pause.resumedAt` | time | When the resume under way scaled it back up; scaled to zero after that by something other than Hybernate, it's left off |
| `pause.firstReadyAt` | time | When the resume under way first saw one of its replicas Ready; the rest get 2 minutes from then |
| `pause.scaledObject` | string | The KEDA ScaledObject held at zero while paused; see [HPA and KEDA](../guides/autoscalers.md) |
| `pause.scaledObjectPausedReplicas` | string | The ScaledObject's own `autoscaling.keda.sh/paused-replicas` from before the pause, put back after it |
| `pause.resources` | `ResourceSnapshot` | What the workload ran when the pause began: `replicas`, and per replica `cpuMillis`, `memoryBytes`, and its claims' `storageBytes`; and `nodePools`, the node pools its pods ran in (`name`, `""` for nodes without a pool label) and how many `pods` in each, which what it saves is measured against |
| `pause.wakeAnnotations` | object | The activity annotations on the ManagedWorkload (`workload`) and its target (`target`) as the pause began; one that changes since wakes it |
| `pause.queryActiveSincePause` | bool | A [Prometheus activity query](../guides/prometheus-signals.md#while-the-workload-is-paused) has shown activity at every check since the pause began, as one can when it was requested; until a check finds every query quiet, they don't wake it |
| `learnedDependencies` | `LearnedDependencies` | Dependencies Hybernate found, held and woken like `dependsOn`: `from`, `at`, and `dependencies` (`namespace`, `kind`, `name`, `source`: `environment` or `wake`, `via` and `address` for one found in the environment, and `lastSeen` for one learned from a wake). See [Learned dependencies](../concepts/dependencies.md#learned-dependencies) |
| `lastScaledUp` | `ScaledUp` | The last time the workload was scaled up outside Hybernate while paused, which wakes it: `at`, `replicas`, `by` (the field manager, such as `argocd-controller`, or `unknown` for a scale through the scale subresource, as `kubectl scale` makes, which records none), and `gitOps` (`Argo CD`, `Flux`, `Fleet`, `kapp` or `Kluctl`, when it was set from Git; see [GitOps tools](../guides/gitops.md)) |
| `doorman[]` | `DoormanRoute` | Service ports routed to the doorman while paused: `service`, `portName`, `doormanPort`, and `forwarded`, set for a Service behind GKE NEGs routed through the [forwarder](../concepts/wake-on-request.md#gke-container-native-load-balancing-negs) |
| `doormanDraining[]` | `DrainingPort` | Doorman ports that routed the workload until it woke: `port`, and `until`, before which no other workload is given it, so a client still sending the workload's traffic there never reaches another. Kept in status so an operator that restarts or takes over leadership honours it |
| `dryRun` | `DryRunStatus` | What dry-run has measured; only while `spec.dryRun` is true |
| `dryRun.since` | time | When dry-run started measuring |
| `dryRun.pauses` | int32 | Times the workload would have been paused, one under way included |
| `dryRun.slept` | duration | How long finished would-be pauses lasted in all |
| `dryRun.freedCPUHours`, `dryRun.freedMemoryHours` | quantity | vCPU-hours and GiB-hours the replicas requested during `slept` |
| `dryRun.estimatedFreed` | string | Those hours priced, for display, with the would-be pause under way counted up to the last status write: what pausing would have freed. Also the `Would Free` column of `kubectl get managedworkloads` |
| `dryRun.resources` | `ResourceSnapshot` | What the workload ran when the current would-be pause began |
| `prediction.dailyPhase`, `prediction.weeklyPhase` | string | Each season's phase: `Observing`, `Suggesting`, or `Active` |
| `prediction.dailyConfidence`, `prediction.weeklyConfidence` | int | Accuracy % over the last 24 and 168 observed hours |
| `prediction.timeZone` | string | The IANA time zone the forecast counts hours and weekdays in, the operator's `--timezone` (Helm value `timezone`), such as `Europe/London` |
| `prediction.threshold` | int | The confidence the forecast acts at: `spec.prediction.confidence`, or the mode's |
| `prediction.acting` | bool | Whether the forecast drives decisions: holds off pauses and, with `autoResume`, wakes the workload ahead of demand |
| `prediction.startsActing` | time | When the forecast is expected to start acting, if the coming days repeat the last week it saw; unset while it's acting or when it isn't expected to within four weeks |
| `prediction.reason`, `prediction.message` | string | Why the forecast is or isn't acting: `Acting`, `Observing`, `EarningConfidence`, `Relearning` or `NotLearning`, and in a sentence. See [When it acts](../concepts/forecasting.md#when-it-acts) |
| `prediction.history` | `PredictionHistory` | What the forecast learned from Prometheus history when Hybernate began managing the workload: `reason` (`Learned`, `NoPrometheusURL`, `NoHistory`, `NoCPURequests`, `QueryFailed`), `hours`, `from`, `to`, `message`. See [Learning from history](../concepts/forecasting.md#learning-from-history) |
| `prediction.state` | string | The engine's learned state, gzipped and base64-encoded |
| `cost` | `CostStatus` | Cost for the current calendar month, in UTC; see [Cost Tracking](../concepts/cost-tracking.md#status-fields) |
| `cost.tracked` | duration | How much of the month the totals cover |
| `cost.lastAccumulatedAt` | time | When the totals were last brought up to date |
| `cost.awakeCPUHours`, `cost.awakeMemoryHours` | quantity | vCPU-hours and GiB-hours requested while awake |
| `cost.storageHours` | quantity | GiB-hours of the claims its pods mount, in every phase |
| `cost.pausedCPUHours`, `cost.pausedMemoryHours` | quantity | vCPU-hours and GiB-hours the paused replicas had requested, while paused: what pausing freed |
| `cost.savedCPUHours`, `cost.savedMemoryHours` | quantity | The workload's share of what its node pools' spare capacity couldn't hold of the paused replicas, measured every 5 minutes: what pausing saved, at least. See [How saved is measured](../concepts/cost-tracking.md#how-saved-is-measured) |
| `cost.costThisMonth` | string | Awake and storage hours, priced |
| `cost.freedThisMonth` | string | Paused hours, priced |
| `cost.savedThisMonth` | string | Saved hours, priced; `unknown` while `savingsUnknown` is set |
| `cost.savingsUnknown` | string | Why saved can't be measured, such as pods outside `watchNamespaces` not being visible; unset when it's measured |
| `cost.costWithoutHybernateThisMonth` | string | `costThisMonth` plus `freedThisMonth` |
| `cost.projectedMonthlyCost`, `cost.projectedMonthlyFreed`, `cost.projectedMonthlySavings` | string | The month so far carried to the whole month; `pending` until a day is tracked |
| `cost.running` | `ResourceSnapshot` | What the workload ran when last brought up to date while awake |
| `cost.pricedAt`, `cost.pricedTemplateHash` | time, string | When requests and list rates were last read, and the pod template then |
| `cost.listRates` | `CostRates` | List rates of the nodes its pods last ran on, spot nodes at spot. Unset when its nodes have no list price |
| `cost.listPrice` | string | Which list prices `listRates` are: `OnDemand`, `Spot`, or `SpotAndOnDemand`. Unset with `listRates` |
| `cost.resourceReduction` | `ResourceReduction` | What the current pause freed: `cpuMillis`, `memoryBytes`, `replicas`. Only while paused |

### WorkloadPhase Values

`Running`, `Idle`, `Pausing`, `Paused`, `Resuming`. A ManagedWorkload has no phase until its first reconcile. `Creating` is accepted by the schema but never set.
