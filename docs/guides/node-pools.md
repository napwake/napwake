# Node Pools for Real Savings

Pausing a workload frees what its pods requested on their nodes. Your cloud bill is for nodes, so what's freed becomes money only once a node goes. Hybernate reports the two apart: **freed** is what pausing released, and **saved** is what it measurably took off the bill (see [Freed and Saved](../concepts/cost-tracking.md#freed-and-saved)). This guide is about closing the gap between them, by giving non-prod workloads node pools of their own, so that pausing an environment empties whole nodes.

## Why a pool of their own

A paused app on a node shared with busy workloads frees space on that node, but no node. The node stays as long as anything else on it does, and the bill doesn't change. A node autoscaler that consolidates, such as Karpenter with `WhenEmptyOrUnderutilized`, moves pods around to empty nodes it can then remove, and Cluster Autoscaler removes nodes whose requests fall under its threshold, but packing is never perfect: a node a quarter full of busy pods costs as much as a full one, and some pods can't be moved at all (see [what keeps a node](#check-its-working)).

Put staging, preview and dev environments on a node pool that nothing else runs on, and pausing them takes away everything that holds those nodes. The pool empties as its workloads pause, the autoscaler removes the empty nodes, and saved rises to meet freed. A pool scaled to no nodes at all saves everything its paused workloads freed.

What makes it work:

- **A taint** on the pool's nodes, so nothing lands there that doesn't tolerate it: not your always-on workloads, not most `kube-system` pods, which would otherwise hold a node Cluster Autoscaler won't remove, and not Hybernate's own operator and doorman, which are never paused and must stay up to wake what is.
- **A label** on the pool's nodes, and a `nodeSelector` (or node affinity) with a matching toleration on the workloads that belong there.
- **An autoscaler that removes empty nodes promptly**, and a minimum size of zero where the provider allows it.

DaemonSet pods run on every node and don't keep a node from being removed, by either autoscaler.

## Steering workloads to the pool

The workloads need both the `nodeSelector` and the toleration: the taint keeps others off, the selector keeps these on. In the manifests you keep in Git:

```yaml title="deployment.yaml" linenums="1"
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-api
  namespace: staging
  labels:
    hybernate.io/managed: "true"
  annotations:
    hybernate.io/wake-max-wait: "5m"
spec:
  selector:
    matchLabels:
      app: my-api
  template:
    metadata:
      labels:
        app: my-api
    spec:
      nodeSelector:
        example.com/tier: non-prod
      tolerations:
        - key: example.com/tier
          operator: Equal
          value: non-prod
          effect: NoSchedule
      containers:
        - name: api
          image: ghcr.io/example/my-api:1.4.2
          resources:
            requests:
              cpu: 250m
              memory: 512Mi
```

`example.com/tier: non-prod` stands for whatever label and taint you give the pool. `wake-max-wait` is there because a wake may have to wait for a node; see [What waking costs](#what-waking-costs).

To put a whole namespace on the pool without editing every workload, add the selector and toleration as pods are created. Any of these works, and Hybernate depends on none of them:

- A mutating admission policy from a policy engine, such as a Kyverno `mutate` rule or a Gatekeeper `Assign` mutation, matching pods in the namespaces you choose.
- The `PodNodeSelector` and `PodTolerationRestriction` admission plugins, where your control plane enables them, which managed providers mostly don't: they read a default node selector and tolerations from annotations on the namespace.
- Your templating, such as a Helm chart's or a Kustomize component's `nodeSelector` and `tolerations` for every non-prod release.

Keep anything that never sleeps off the pool, such as a shared database no ManagedWorkload pauses: one always-on pod holds its node, and every pod that fits beside it. If it's a dependency of the paused workloads, consider managing it too: Hybernate [won't pause it](../concepts/dependencies.md) while anything that depends on it is awake, and wakes it first when they wake.

## Karpenter

A NodePool for non-prod, with its own taint and label, that consolidates empty and underutilized nodes, and prefers spot capacity. This uses Karpenter's `karpenter.sh/v1` API and the AWS provider's `EC2NodeClass`; for another provider, or EKS Auto Mode, change `nodeClassRef` (on EKS Auto Mode it's `group: eks.amazonaws.com`, `kind: NodeClass`).

```yaml title="nodepool.yaml" linenums="1"
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: non-prod
spec:
  template:
    metadata:
      labels:
        example.com/tier: non-prod
    spec:
      nodeClassRef:
        group: karpenter.k8s.aws
        kind: EC2NodeClass
        name: default
      taints:
        - key: example.com/tier
          value: non-prod
          effect: NoSchedule
      requirements:
        - key: karpenter.sh/capacity-type
          operator: In
          values: ["spot", "on-demand"]
        - key: kubernetes.io/arch
          operator: In
          values: ["amd64"]
      expireAfter: 720h
  disruption:
    consolidationPolicy: WhenEmptyOrUnderutilized
    consolidateAfter: 5m
    budgets:
      - nodes: "100%"
        reasons: [Empty]
      - nodes: "10%"
  limits:
    cpu: "200"
    memory: 800Gi
```

- **`consolidationPolicy: WhenEmptyOrUnderutilized`** removes empty nodes and repacks underutilized ones, which matters when only some of a pool's environments are paused. `WhenEmpty` removes only nodes with nothing but DaemonSet pods left, which a pool shared by several environments rarely has; `Balanced` repacks only when the saving outweighs the disruption.
- **`consolidateAfter: 5m`**: Karpenter restarts this timer whenever a pod is added to or removed from the node, so a node becomes a candidate 5 minutes after its last pod changed. A pause is deliberate, after `idleAfter` with no activity, so there's no need to wait long; much shorter, and a workload woken soon after its pause finds its node already gone. `Never` turns consolidation off.
- **`budgets`**: without any, Karpenter disrupts at most 10% of a pool's nodes at once. When a whole environment pauses at once, the first budget lets every empty node go together, while the second still limits repacking busy ones.
- **`expireAfter`** is under `spec.template.spec` in v1, not under `disruption`.
- **`limits`** caps what the pool can grow to, so a runaway preview environment can't.

**Spot capacity** suits non-prod: it's interrupted now and then, which a staging environment can live with, and Karpenter picks spot when the requirements allow it. Karpenter deletes spot nodes it can empty by default, but replacing one spot node with a cheaper spot node needs its `SpotToSpotConsolidation` feature gate. Hybernate prices spot nodes at their on-demand list price, so set [`costTracking.rates`](../concepts/cost-tracking.md#custom-rates) if you want freed and saved in what you actually pay.

Karpenter also won't consolidate a node whose pods are annotated `karpenter.sh/do-not-disrupt: "true"`, or whose pods' PodDisruptionBudgets allow no disruption. `kubectl hybernate nodes` lists both.

## Cluster Autoscaler, GKE, AKS and EKS managed node groups

With Cluster Autoscaler, managed or your own, the pool is a separate node group with a taint and a label, and a minimum size of zero.

=== "GKE Standard"

    ```bash
    gcloud container node-pools create non-prod \
      --cluster my-cluster --location us-central1 \
      --machine-type e2-standard-4 --spot \
      --enable-autoscaling --total-min-nodes 0 --total-max-nodes 20 \
      --node-labels example.com/tier=non-prod \
      --node-taints example.com/tier=non-prod:NoSchedule

    gcloud container clusters update my-cluster --location us-central1 \
      --autoscaling-profile optimize-utilization
    ```

    GKE never scales a Standard cluster to zero nodes, since its system pods need somewhere to run, so keep another pool for them; the non-prod pool itself can scale to zero. Its nodes are labelled `cloud.google.com/gke-nodepool: non-prod` too, which you can select instead. GKE doesn't let you set Cluster Autoscaler's flags; the `optimize-utilization` profile makes it scale down sooner and more often than the default `balanced`, and applies to the whole cluster. On GKE 1.22 and later, pods with local storage don't block scale-down.

=== "AKS"

    ```bash
    az aks nodepool add \
      --resource-group my-rg --cluster-name my-cluster \
      --name nonprod --mode User \
      --node-vm-size Standard_D4s_v5 \
      --priority Spot --eviction-policy Delete --spot-max-price -1 \
      --enable-cluster-autoscaler --min-count 0 --max-count 20 \
      --labels example.com/tier=non-prod \
      --node-taints example.com/tier=non-prod:NoSchedule

    az aks update --resource-group my-rg --name my-cluster \
      --cluster-autoscaler-profile scale-down-unneeded-time=5m,scale-down-delay-after-add=5m
    ```

    User pools can autoscale to zero with `--min-count 0`; system pools can't. AKS adds its own taint, `kubernetes.azure.com/scalesetpriority=spot:NoSchedule`, to spot pools, so workloads there tolerate that too. The autoscaler profile applies to every autoscaled pool in the cluster, not one. AKS already defaults `skip-nodes-with-local-storage` to `false`. Its nodes are labelled `kubernetes.azure.com/agentpool: nonprod`.

=== "EKS managed node group"

    ```yaml title="nodegroup.yaml" linenums="1"
    apiVersion: eksctl.io/v1alpha5
    kind: ClusterConfig
    metadata:
      name: my-cluster
      region: us-east-1
    managedNodeGroups:
      - name: non-prod
        instanceTypes: ["m6i.xlarge", "m5.xlarge", "m6a.xlarge"]
        spot: true
        minSize: 0
        desiredCapacity: 1
        maxSize: 20
        labels:
          example.com/tier: non-prod
        taints:
          - key: example.com/tier
            value: non-prod
            effect: NoSchedule
        iam:
          withAddonPolicies:
            autoScaler: true
    ```

    EKS doesn't scale managed node groups itself, but tags their Auto Scaling groups for Cluster Autoscaler to find: run it with `--node-group-auto-discovery=asg:tag=k8s.io/cluster-autoscaler/enabled,k8s.io/cluster-autoscaler/my-cluster`. To scale a group up from zero, Cluster Autoscaler must know its nodes' labels and taints before any node exists: give its IAM role `eks:DescribeNodegroup` and it reads them from the EKS API, or tag the Auto Scaling group with `k8s.io/cluster-autoscaler/node-template/label/example.com/tier: non-prod` and `k8s.io/cluster-autoscaler/node-template/taint/example.com/tier: non-prod:NoSchedule`. Its nodes are labelled `eks.amazonaws.com/nodegroup: non-prod`.

=== "Self-managed Cluster Autoscaler"

    ```yaml title="cluster-autoscaler-values.yaml" linenums="1"
    autoDiscovery:
      clusterName: my-cluster
    extraArgs:
      scale-down-unneeded-time: 5m
      scale-down-delay-after-add: 5m
      scale-down-utilization-threshold: "0.5"
    ```

    Give the node group a minimum of zero in your cloud, and set the node group's labels and taints as your provider's Cluster Autoscaler docs say, so it can scale up from zero.

The settings that decide how soon a paused environment's nodes go:

| Setting | Default | For a non-prod pool |
|---------|---------|---------------------|
| `scale-down-unneeded-time` | `10m` | How long a node must be unneeded before it's removed. A paused environment's node costs money for this long; `5m` is plenty for workloads paused after an hour idle |
| `scale-down-utilization-threshold` | `0.5` | A node is unneeded when the larger of its pods' CPU and memory requests is under this share of what it can allocate. A pool that empties when its environments pause doesn't depend on it: an empty node is always under |
| `scale-down-delay-after-add` | `10m` | How long after a scale-up before scale-down is considered again. A wake scales up, so a shorter delay lets the next pause's nodes go sooner |
| `--skip-nodes-with-local-storage` | `true` upstream, `false` on AKS | Whether pods with `emptyDir` or `hostPath` volumes keep a node. Annotate pods with `cluster-autoscaler.kubernetes.io/safe-to-evict-local-volumes` rather than turning it off cluster-wide |
| `--skip-nodes-with-system-pods` | `true` | Whether `kube-system` pods without a PodDisruptionBudget keep a node. The pool's taint keeps most of them off |

These follow the [Cluster Autoscaler FAQ](https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/FAQ.md), its [AWS provider README](https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/cloudprovider/aws/README.md), [GKE's cluster autoscaler](https://cloud.google.com/kubernetes-engine/docs/concepts/cluster-autoscaler) and [`node-pools create`](https://cloud.google.com/sdk/gcloud/reference/container/node-pools/create) pages, and [AKS's cluster autoscaler](https://learn.microsoft.com/en-us/azure/aks/cluster-autoscaler) page. They live outside Hybernate and change with your provider's releases, so check them for your versions.

## What waking costs

A paused workload whose pool has scaled to nothing needs a node before it can start. The first request after a quiet night waits for the node to be provisioned and join, often 30 to 90 seconds, longer for a node group scaling up from zero, then for its images to be pulled onto a fresh node, then for the app to start.

- **Raise `wake.maxWait`** (`hybernate.io/wake-max-wait` on a workload opted in with the label) above the 2-minute default, such as to `5m`, or the doorman gives up on held requests before the node is ready. The wake carries on regardless; see [How long a connection is held](../concepts/wake-on-request.md#how-long-a-connection-is-held).
- **Raise your ingress controller's timeout to match.** ingress-nginx gives up on an upstream after 60 seconds by default. Browsers don't care: they get the [waking-up page](../concepts/wake-on-request.md#browsers) at once.
- **Use `autoResume`** for traffic the forecast can learn, such as a team that starts at 9 every weekday. It wakes the workload 15 minutes before an hour the forecast expects to be busy, so the node has started before anyone arrives. See [Resume](pause.md#resume).
- **Keep one node warm** if every first request must be quick: a minimum of one node, or in Karpenter a small always-on workload in the pool. You pay for that node, and pausing saves the rest.

## Check it's working

**`kubectl hybernate status`** shows what pausing freed this month beside what it saved, per workload and in total. With the pool doing its job, SAVED follows FREED, a little behind, since a node takes `consolidateAfter` or `scale-down-unneeded-time` to go. When less than half of what was freed was saved, status says nodes aren't scaling down under Needs attention. See [See What Hybernate Is Doing](../getting-started/kubectl-plugin.md#see-what-hybernate-is-doing).

**`kubectl hybernate nodes`** names the node autoscaler it found and, for each node, what keeps the autoscaler from removing it, in BLOCKED BY: pods annotated `karpenter.sh/do-not-disrupt` or `cluster-autoscaler.kubernetes.io/safe-to-evict: "false"`, PodDisruptionBudgets that allow no disruption, local storage, pods without a controller, `kube-system` pods without a PodDisruptionBudget, nodes with scale-down disabled, NodePools that consolidate only empty nodes or never, and nodes no autoscaler scales. A node of the non-prod pool with a low utilisation and something in BLOCKED BY is the one to look at. See [See Why Nodes Don't Scale Down](../getting-started/kubectl-plugin.md#see-why-nodes-dont-scale-down).

**The metrics**, by node pool, with `pool` the pool's name, such as `non-prod`:

```promql
# What the pool's paused workloads requested, against its spare capacity
hybernate_capacity_paused_cpu_cores{pool="non-prod"}
hybernate_capacity_spare_cpu_cores{pool="non-prod"}

# What pausing takes off the bill an hour, in the pool
hybernate_savings_realised_dollars_per_hour{pool="non-prod"}

# Which node autoscaler the operator found
hybernate_node_autoscaler
```

Spare capacity should fall towards zero as the pool's nodes go, and what's saved an hour rise towards what the paused replicas would cost. Spare capacity that stays above what's paused means the pool's nodes are still there: check `kubectl hybernate nodes`. See the [Metrics Reference](../reference/metrics.md#savings), and [Freed but not saved](../operations/troubleshooting.md#freed-but-not-saved) for the causes.
