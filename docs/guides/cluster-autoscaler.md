# Cluster Autoscaler Configuration

Napwake manages workloads at the pod layer: it pauses idle Deployments and StatefulSets, scaling them to zero. But pods run on nodes, and your bill is for nodes. What a pause **frees**, the CPU and memory its pods requested, comes off the bill only once a node autoscaler removes the nodes it freed it on:

```
Napwake pauses pods → capacity freed on their nodes → autoscaler removes nodes → saved
```

Without a node autoscaler, or with scale-down blocked, pausing frees capacity and saves nothing. Napwake reports the two apart: `freedThisMonth` is what pausing released, and `savedThisMonth` is what it measurably took off the bill, worked out every 5 minutes from the spare capacity left on each node pool. See [Freed and Saved](../concepts/cost-tracking.md#freed-and-saved).

This guide covers the autoscaler settings that let freed capacity go. Freed becomes saved fastest when non-prod workloads have nodes of their own, so that pausing them empties whole nodes; [Node Pools for Real Savings](node-pools.md) shows how.

## What Napwake Detects

The operator looks every 10 minutes for what scales the cluster's nodes, logs what it finds when that changes, and exports it as [`napwake_node_autoscaler`](../reference/metrics.md#savings):

- **Karpenter**, from its `nodepools.karpenter.sh` NodePools (`karpenter.sh/v1`) or nodes labelled `karpenter.sh/nodepool`, which covers AKS node auto-provisioning
- **EKS Auto Mode**, from nodes labelled `eks.amazonaws.com/compute-type: auto`
- **Cluster Autoscaler**, self-managed or the one GKE Standard and AKS run for you, from the `cluster-autoscaler-status` ConfigMap it writes in `kube-system`
- **GKE Autopilot**, from that ConfigMap and nodes named `gk3-`
- `none`, when it looked everywhere and found nothing: the operator logs that what pausing frees won't come off the bill until something scales nodes down
- `unknown`, when it found nothing and wasn't allowed to look everywhere, as in a `watchNamespaces` install, which can't read Cluster Autoscaler's ConfigMap in `kube-system`

`kubectl napwake status` and `kubectl napwake scan` name it at the top. `kubectl napwake nodes` goes further: for each node, what keeps the autoscaler from removing it. See [See Why Nodes Don't Scale Down](../getting-started/kubectl-plugin.md#see-why-nodes-dont-scale-down).

## Cluster Autoscaler

[Cluster Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler) manages node groups across EKS, GKE, AKS, and self-managed clusters.

### Recommended Settings

```yaml title="cluster-autoscaler-values.yaml" linenums="1"
autoDiscovery:
  clusterName: my-cluster

extraArgs:
  # How long a node must be unneeded before it's removed. The default is 10m.
  # A paused workload's node keeps costing money for this long, so keep it
  # short next to idleAfter.
  scale-down-unneeded-time: 5m

  # A node is unneeded when the CPU or memory its pods request, whichever is
  # higher, is below this fraction of what it can allocate. 0.5 is the default.
  # It counts requests, not usage: a node frees up once paused pods are gone,
  # not when they go quiet.
  scale-down-utilization-threshold: "0.5"

  # How long after a scale-up before scale-down is considered again. The
  # default is 10m. A wake can scale nodes up, so a shorter delay lets the
  # next pause free them sooner; much shorter, and nodes churn.
  scale-down-delay-after-add: 5m

  # How long after removing a node before scale-down is considered again.
  # The default, 0s, lets the nodes of several workloads paused together go
  # one after another, so leave it.
  scale-down-delay-after-delete: 0s

  # How often the autoscaler evaluates the cluster. 10s is the default and
  # needs no change for Napwake.
  scan-interval: 10s
```

GKE doesn't let you set these flags: its `optimize-utilization` autoscaling profile scales down sooner than the default `balanced`. AKS sets them cluster-wide in its cluster autoscaler profile (`az aks update --cluster-autoscaler-profile`). See [Node Pools for Real Savings](node-pools.md#cluster-autoscaler-gke-aks-and-eks-managed-node-groups).

### Key Interactions

| Napwake Action | Autoscaler Response |
|-----------------|-------------------|
| Pause (scale to 0) | Pods removed; a node whose remaining pods request less than the threshold is removed after `scale-down-unneeded-time`, if nothing on it blocks that |
| Resume (scale back up) | Autoscaler provisions new nodes if no capacity exists |

### What Keeps a Node

Cluster Autoscaler won't remove a node, however little of it is used, when it runs:

- a pod annotated `cluster-autoscaler.kubernetes.io/safe-to-evict: "false"`
- a pod whose PodDisruptionBudget allows no disruptions
- a pod with an `emptyDir` not in memory or a `hostPath` volume, while `--skip-nodes-with-local-storage` is on, as it is by default upstream (AKS turns it off; GKE 1.22 and later ignores local storage)
- a pod no controller would recreate elsewhere
- a `kube-system` pod without a PodDisruptionBudget, while `--skip-nodes-with-system-pods` is on, its default

or when the node is annotated `cluster-autoscaler.kubernetes.io/scale-down-disabled: "true"`. DaemonSet and static pods never keep a node. Napwake scales replica counts directly, so its own pauses never go through eviction or PodDisruptionBudgets; it's what's left on the node that can hold it. `kubectl napwake nodes` lists each of these per node, with how to clear it.

## Karpenter

[Karpenter](https://karpenter.sh) is an open-source node autoscaler, a Kubernetes SIG Autoscaling project, with providers for AWS, Azure (as AKS node auto-provisioning), and other clouds. Instead of managing node groups, it provisions individual nodes sized to fit pending pods and consolidates underutilized nodes proactively. Napwake reads `karpenter.sh/v1` NodePools.

### Recommended Settings

```yaml title="nodepool.yaml" linenums="1"
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: default
spec:
  template:
    spec:
      nodeClassRef:
        group: karpenter.k8s.aws
        kind: EC2NodeClass
        name: default
      requirements:
        - key: karpenter.sh/capacity-type
          operator: In
          values: ["on-demand", "spot"]
        - key: kubernetes.io/arch
          operator: In
          values: ["amd64"]
      # Maximum time a node can live, regardless of utilization, so nodes
      # pick up new images. In v1 it's here, not under disruption.
      expireAfter: 720h
  disruption:
    # Removes empty nodes, and moves pods off underutilized ones to remove
    # those too. WhenEmpty never touches a node that runs anything but
    # DaemonSets, so pausing one workload on a shared node saves nothing.
    consolidationPolicy: WhenEmptyOrUnderutilized

    # How long after a pod was last added to or removed from a node before
    # it's considered. A pause is deliberate, not transient, so there's no
    # need to wait long. Never turns consolidation off.
    consolidateAfter: 1m
```

`nodeClassRef` names your provider's node class: `EC2NodeClass` in `karpenter.k8s.aws` on AWS, or `NodeClass` in `eks.amazonaws.com` on EKS Auto Mode.

### Key Interactions

| Napwake Action | Karpenter Response |
|-----------------|-------------------|
| Pause (scale to 0) | Node becomes empty or underutilized, consolidated after `consolidateAfter` |
| Resume (scale back up) | Karpenter provisions a right-sized node for the pending pods |

Karpenter won't consolidate a node that's annotated `karpenter.sh/do-not-disrupt: "true"`, or that runs a pod so annotated, or a pod whose PodDisruptionBudget allows no disruptions. Without `budgets`, it disrupts at most 10% of a NodePool's nodes at once. `kubectl napwake nodes` shows each NodePool's consolidation settings and what holds each node.

### Why Karpenter Works Well With Napwake

- **Right-sizing on resume**: When Napwake resumes a workload, Karpenter provisions a node sized to fit the exact pod requirements rather than falling back to a fixed node group size. This avoids over-provisioning on resume.
- **Consolidation**: Karpenter repacks underutilized nodes as well as removing empty ones, so more of what pausing frees on shared nodes becomes saved, and sooner.
- **Spot instances**: Non-critical workloads that Napwake manages are good candidates for spot capacity. Napwake prices spot nodes at their spot list price; see [Spot nodes](../concepts/cost-tracking.md#spot-nodes).

## Autopilot and Serverless Modes

Some managed Kubernetes offerings handle node scaling for you, and bill for what pods request rather than for nodes:

| Service | How It Works | With Napwake |
|---------|-------------|-----------------|
| **GKE Autopilot** | Google manages nodes entirely. You pay per pod resource request. | What pausing frees stops being billed with the pods. No autoscaler configuration needed. |
| **EKS with Fargate** | AWS runs each pod on a dedicated microVM. No shared nodes. | Pausing a pod stops billing for that pod. No autoscaler configuration needed. |
| **AKS node auto-provisioning** | Azure manages nodes using Karpenter under the hood. | Use the Karpenter settings above. |
| **EKS Auto Mode** | AWS runs Karpenter for you; you pay for the nodes it provisions. | Use the Karpenter settings above in your own NodePools. |

On GKE Autopilot and Fargate what pausing frees is what you stop paying for. Napwake's saved still measures the spare capacity left on nodes, so there it can read lower than what's taken off your bill, never higher. Set `costTracking.rates` to the provider's per-pod prices to price freed at what you pay.

## Verifying It Works

After configuring your autoscaler, verify the end-to-end flow:

1. **Pause a workload now,** rather than waiting for its idle clock:

    ```bash
    kubectl napwake pause my-api -n staging
    ```

    It returns once the workload is `Paused`, or says why Napwake won't pause it, such as workloads that depend on it being awake. See [Pause Now](pause.md#pause-now).

2. **Watch node count:**

    ```bash
    kubectl get nodes -w
    ```

3. **Confirm a node is removed** after the autoscaler's configured delay. If none is, `kubectl napwake nodes` shows what keeps each node.

4. **Watch SAVED grow with FREED:** the node's going is measured within 5 minutes, and written to the workload's status within another 5:

    ```bash
    kubectl napwake status -n staging
    ```

5. **Wake the workload,** with a request to its Service or:

    ```bash
    kubectl napwake wake my-api -n staging
    ```

    It returns once the workload is `Running`, all its replicas Ready. Its idle clock starts again, so it pauses on its own once it's idle.

6. **Confirm a node is provisioned** and the workload pods become ready. A wake that has to wait for a node may need a longer `wake.maxWait`; see [What waking costs](node-pools.md#what-waking-costs).

If nodes are not being removed after pause, run `kubectl napwake nodes`, and check:

- Autoscaler logs for why scale-down was skipped
- Whether non-managed pods on the same node prevent removal; a [node pool of their own](node-pools.md) for non-prod workloads avoids this
- Whether PodDisruptionBudgets allow no disruptions
- Whether the utilization threshold is set too low

See [Freed but not saved](../operations/troubleshooting.md#freed-but-not-saved).
