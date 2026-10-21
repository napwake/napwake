# Cost Tracking

Hybernate tracks what each workload costs, what pausing it frees, and what that has saved, in its ManagedWorkload's `status.cost`. The figures cover the current calendar month in UTC.

## Freed and Saved

Hybernate reports two numbers for a pause, because they answer different questions:

- **Freed** (`freedThisMonth`) is what the paused replicas requested, priced at their nodes' list price, for as long as they were paused: what pausing released on its nodes.
- **Saved** (`savedThisMonth`) is the part of that which provably came off your bill.

Hybernate works at the **workload layer**: pausing removes pods, which frees the CPU and memory they requested on their nodes. Your cloud bill is for **nodes**. Freed capacity becomes money only once nodes go, which takes a cluster autoscaler (Cluster Autoscaler, Karpenter, or a managed equivalent) able to remove them. With no autoscaler, or with scale-down blocked, a pause frees capacity and saves nothing, and `savedThisMonth` says so. See the [Cluster Autoscaler Guide](../guides/cluster-autoscaler.md) for settings that let freed capacity go, and [Node Pools for Real Savings](../guides/node-pools.md) for giving non-prod workloads nodes that pausing empties.

### How saved is measured

Every 5 minutes, the operator's leader looks at each **node pool**, and asks how much of what Hybernate holds paused there its nodes could hold if it came back now:

- **Paused** is what the replicas Hybernate holds paused in the pool requested, in CPU and memory. A pause records which pools its pods ran in, and how many in each, in `status.pause.resources.nodePools`.
- **Spare** is what the pool's schedulable, Ready nodes have left: their allocatable CPU and memory, less what the pods bound to them request, finished pods aside.
- **Shortfall** is how much of the paused requests the spare capacity can't hold, in each of CPU and memory: `max(0, paused − spare)`.

The shortfall is capacity that would have to be added to bring the paused replicas back, so it costs at least what that much CPU and memory costs. Over each 5 minutes it's credited to the paused workloads in the pool, shared in proportion to what each holds paused there, in CPU and in memory, as `savedCPUHours` and `savedMemoryHours`. `savedThisMonth` is those hours priced at the workload's rates, like the rest of its cost, so `costTracking.rates` applies to saved as it does to freed.

Each dimension is compared on its own, and a pool's spare capacity is summed across its nodes, as if paused pods could be split between nodes. Real scheduling only ever needs more, so saved is a **lower bound**: it never credits capacity that's still there, and can undercount, never overcount.

**Why this needs no attribution.** It never asks why a node went away. If other workloads scale down, they leave more spare capacity, and Hybernate's credit falls, since the paused replicas would fit in what they left. If nodes are removed for any reason, by an autoscaler after a pause, by consolidation, or by hand, and the paused replicas would need them back, that counts, because it's the capacity those replicas would need that's missing. What's credited is what Hybernate's pauses are actually holding off the bill right now, whoever made the nodes go.

**Node pools.** A node's pool is the first of its `karpenter.sh/nodepool`, `cloud.google.com/gke-nodepool`, `eks.amazonaws.com/nodegroup` and `kubernetes.azure.com/agentpool` labels. Nodes with none of them are one pool. A pool's spare capacity isn't lent to another: pods that ran on GPU nodes can't come back on general-purpose ones. The instance type isn't a pool: one pool can run several. A pause whose pools weren't recorded, because none of its pods was on a node as it paused, is compared with what every pool has left once its own paused replicas are held.

**Worked example.** A pool of three `m6i.xlarge` nodes in us-east-1, 4 vCPU and 16 GiB each, holds pods requesting 11 vCPU and 34 GiB, leaving 1 vCPU and 14 GiB spare. Hybernate holds paused `api`, 4 replicas of 500m and 1 GiB, and `worker`, 2 replicas of 1 vCPU and 4 GiB: 4 vCPU and 12 GiB in all.

| | CPU | Memory |
|-|-----|--------|
| Paused | 4 vCPU | 12 GiB |
| Spare | 1 vCPU | 14 GiB |
| Shortfall | 3 vCPU | 0 |

The memory would fit, so it saves nothing; 3 vCPU wouldn't, so that much came off the bill. `api` and `worker` hold 2 vCPU each, so each is credited 1.5 vCPU, 0.125 vCPU-hours every 5 minutes. At an `m6i.xlarge`'s $0.0317 per vCPU-hour, `api` frees $0.080 an hour (2 vCPU and 4 GiB) and saves $0.048 an hour. If the autoscaler then removes a node, spare capacity falls and saved rises towards freed; if another team scales up into the spare vCPU, saved rises too, since the paused replicas now couldn't come back without a node.

**What isn't credited.** Time the operator wasn't sampling, such as across a restart or a change of leader, and an interval in which a sample failed: each sample credits at most the 5 minutes before it. What's credited is held in memory until the workload's cost is next brought up to date, at most 5 minutes later; a restart in between loses it. Each is a few minutes of a lower bound.

**Where it can't be measured.** An operator limited to some namespaces with `watchNamespaces` can't see what other namespaces' pods request of the nodes, so it can't know their spare capacity. There, `savedThisMonth` and `projectedMonthlySavings` read `unknown`, and `savingsUnknown` says why; freed is still measured.

To see pools' figures over time, the operator exports them as [metrics](../reference/metrics.md#savings).

To measure spare capacity, the operator caches nodes, without their image lists, and the pods bound to them, keeping of each only its node, its phase, and what it requests: about 2.6 KB a pod, so 26 MB for 10,000 pods.

## How Costs Are Calculated

Cost tracking is always on. Everything is priced on what the workload's pods **request**, not on what they use: requests are what a pod reserves on a node, so they're what the workload costs in capacity, and what a pause frees. That includes sidecars injected when the pods were created, such as a service mesh proxy, which the Deployment's own template doesn't list but which pausing frees too.

### Resource Hours

Hybernate keeps the month's totals as resource-hours, brought up to date at every phase change, so time counts in the phase it was spent in, and at least every 5 minutes in between:

| Field | What it counts |
|-------|----------------|
| `awakeCPUHours`, `awakeMemoryHours` | vCPU-hours and GiB-hours the workload's replicas requested while it was awake: in any phase but `Paused` |
| `storageHours` | GiB-hours of the PersistentVolumeClaims its pods mount, in every phase: a pause doesn't free storage |
| `pausedCPUHours`, `pausedMemoryHours` | vCPU-hours and GiB-hours the replicas Hybernate paused had requested, for as long as they were paused: what pausing freed |
| `savedCPUHours`, `savedMemoryHours` | The workload's share of the vCPU-hours and GiB-hours its node pools were short of holding the paused replicas: what pausing saved, at most the paused hours. See [How saved is measured](#how-saved-is-measured) |

While the workload is awake, its replicas are read from the target each time, or, if they can't be read, taken as last read, so the time before a phase change is still counted in the phase it was spent in; what one replica requests is read from its pods hourly and whenever its pod template changes. While it's paused, the paused hours are priced on what it ran when the pause began (`status.pause.resources`), and the saved hours are added as they're measured. If its pods can't be read as it pauses, that snapshot keeps what was last read of it awake, as it does the replicas of a pause finished after an interruption, which finds the target at zero already.

A gap between updates longer than 2 hours, such as while the operator wasn't running, counts as 2 hours, since what the workload did meanwhile isn't known. `tracked` is how much of the month the totals cover: less than the time since the month began when the workload was created during the month, or after such a gap.

The hours are kept to a billionth of an hour, as Kubernetes quantities, so the short pauses of small workloads still add up: `83333333n` is 0.083 vCPU-hours. When a new month begins, the totals start again from zero; last month's aren't kept.

### Prices

Resource-hours are turned into dollars at a rate per vCPU-hour, per GiB-hour of memory, and per GiB-month of storage. For each of the three, the first of these that sets it applies:

1. **Your own rates**, in `spec.costTracking.rates`.
2. **The list price of its nodes**, in `status.cost.listRates`. Hybernate reads each pod's node's instance type and region from its labels (`node.kubernetes.io/instance-type`, `topology.kubernetes.io/region`), looks up its on-demand list price, and averages over the workload's pods. These are read again hourly while the workload is awake, and kept while it's paused, so what a pause frees and saves is priced at the nodes it last ran on. They set CPU and memory only.
3. **The defaults**, for nodes Hybernate has no list price for, such as on-premises, custom, or kind nodes:

| Resource | Default Rate | Source |
|----------|-------------|--------|
| CPU | $0.031/vCPU-hour | AWS on-demand (m6i.large, us-east-1, 2026) |
| Memory | $0.004/GiB-hour | AWS on-demand (m6i.large, us-east-1, 2026) |
| Storage | $0.08/GiB-month (priced at 730 hours a month) | AWS EBS gp3, us-east-1 |

The rates are applied to the whole month's hours each time the dollar figures are worked out, so changing `costTracking.rates` reprices the month so far, not only what comes after.

**How a node's price becomes rates.** A cloud provider prices an instance as a whole, so Hybernate splits its hourly price between its vCPUs and its memory in the defaults' proportion: a vCPU costs as much as 7.75 GiB of memory. An `m6i.large` (2 vCPU, 8 GiB, $0.096 an hour) comes out at $0.0317 per vCPU-hour and $0.0041 per GiB-hour. A memory-optimized instance's memory and a compute-optimized one's CPU come out at what they cost there, and a pod pays for the share of the node it requests.

**Where the prices come from.** Hybernate ships with the on-demand Linux list price of every AWS, Google Cloud, and Azure instance type, in every region it's sold, taken from the providers' public price lists through [instances.vantage.sh](https://instances.vantage.sh) and refreshed each release. Nothing is looked up at runtime, so it works offline and air-gapped. An Azure constrained vCPU size, such as `Standard_E4-2s_v5`, is billed as its base size, `Standard_E4s_v5`, so it's priced at the base size's price spread over the vCPUs it has. Instances on GPU nodes are priced the same way, so a pod on a GPU node pays its share of the GPU too.

**What list prices leave out.** Spot and preemptible nodes are priced at on-demand, so their workloads cost less than shown. Savings plans, reserved instances, committed-use discounts, and negotiated rates aren't known to Hybernate; set `costTracking.rates` for those.

Hybernate reads Nodes' labels to do this.

### Custom Rates

Set your own rates to match what you pay:

```yaml title="managedworkload.yaml" linenums="1"
spec:
  costTracking:
    rates:
      cpuPerHour: "0.045"      # GKE Autopilot pricing
      memoryPerHour: "0.005"    # GKE Autopilot pricing
      storagePerMonth: "0.10"   # Premium SSD
```

## Status Fields

```yaml title="status.cost" linenums="1"
status:
  cost:
    tracked: 412h30m0s
    lastAccumulatedAt: "2026-10-18T14:30:00Z"
    awakeCPUHours: "160500m"
    awakeMemoryHours: "321"
    storageHours: "4125"
    pausedCPUHours: "664500m"
    pausedMemoryHours: "1329"
    savedCPUHours: "398700m"
    savedMemoryHours: "0"
    costThisMonth: "$6.84"
    freedThisMonth: "$26.47"
    savedThisMonth: "$12.62"
    costWithoutHybernateThisMonth: "$33.31"
    projectedMonthlyCost: "$12.35"
    projectedMonthlyFreed: "$47.74"
    projectedMonthlySavings: "$22.76"
    running:
      replicas: 2
      cpuMillis: 1000
      memoryBytes: 2147483648
      storageBytes: 10737418240
    pricedAt: "2026-10-18T14:00:00Z"
    pricedTemplateHash: 3f9c2a71d0e4b815
    listRates:
      cpuPerHour: 31660u      # $0.031660 per vCPU-hour
      memoryPerHour: 4085u    # $0.004085 per GiB-hour
```

| Field | Description |
|-------|-------------|
| `tracked` | How much of the month the totals cover |
| `lastAccumulatedAt` | When the totals were last brought up to date |
| `awakeCPUHours`, `awakeMemoryHours`, `storageHours`, `pausedCPUHours`, `pausedMemoryHours`, `savedCPUHours`, `savedMemoryHours` | The resource-hours above: the record the dollar figures are worked out from |
| `costThisMonth` | The awake hours and the storage hours, priced |
| `freedThisMonth` | The paused hours, priced: what pausing freed. Money only once nodes go. Also the `Freed` column of `kubectl get managedworkloads` |
| `savedThisMonth` | The saved hours, priced: what pausing took off the bill, at least. Also the `Saved` column of `kubectl get managedworkloads`. `unknown` while `savingsUnknown` is set |
| `savingsUnknown` | Why saved can't be measured, such as an operator limited by `watchNamespaces`; unset when it's measured |
| `costWithoutHybernateThisMonth` | What the workload would have cost this month had it never been paused: `costThisMonth` plus `freedThisMonth`, to within a cent |
| `projectedMonthlyCost`, `projectedMonthlyFreed`, `projectedMonthlySavings` | `costThisMonth`, `freedThisMonth` and `savedThisMonth` carried to the whole month at the same rate: the amount ÷ `tracked` × the hours in the month. `pending` until a day has been tracked, including on the first day of each month, since a workload's pattern is daily |
| `running` | What the workload ran when the totals were last brought up to date while it was awake |
| `pricedAt`, `pricedTemplateHash` | When what a replica requests, and `listRates`, were last read, and the pod template's hash then |
| `listRates` | The on-demand list rates of the nodes the workload's pods last ran on. Unset when its nodes aren't in Hybernate's price table, and the defaults apply |
| `resourceReduction` | What the current pause freed; only while the workload is paused |

The dollar figures are rounded to the cent for display; the hours are the record.

## Resource Reduction

While a workload is paused, `resourceReduction` says what the pause freed on its nodes:

```yaml title="status.cost.resourceReduction" linenums="1"
resourceReduction:
  cpuMillis: 3000            # 3 vCPUs freed
  memoryBytes: 6442450944    # 6 GiB freed
  replicas: 3                # 3 pod replicas removed
```

How much of that has come off the bill is what `savedCPUHours` and `savedMemoryHours` measure.

## In Dry-Run

A workload in [dry-run](../guides/dry-run.md) is never paused, so all its time is awake time. What pausing would have freed is kept apart, in `status.dryRun`: `freedCPUHours`, `freedMemoryHours`, and `estimatedFreed`, those hours priced at the same rates. Dry-run measures what pausing would free, never what it would save: that depends on whether the cluster's nodes would have gone, which they can't while the workload runs.

## Viewing Costs Across Workloads

`kubectl get managedworkloads -A` shows what pausing each workload has `Freed` this month, what of that it `Saved`, and, for one in dry-run, what it `Would Free`. For more:

```bash
kubectl get managedworkloads -A -o custom-columns=\
NAMESPACE:.metadata.namespace,NAME:.metadata.name,PHASE:.status.phase,\
COST:.status.cost.costThisMonth,FREED:.status.cost.freedThisMonth,\
SAVED:.status.cost.savedThisMonth,PROJECTED:.status.cost.projectedMonthlyCost
```

[`kubectl hybernate status`](../getting-started/kubectl-plugin.md#see-what-hybernate-is-doing) sums what pausing freed, and what of it was saved, across the cluster, and [`kubectl hybernate nodes`](../getting-started/kubectl-plugin.md#see-why-nodes-dont-scale-down) shows what keeps nodes from going.

Per-workload cost isn't exported as Prometheus metrics; each node pool's paused and spare capacity, and what its shortfall saves an hour, are. See the [Metrics Reference](../reference/metrics.md#savings).
