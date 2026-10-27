# Cluster Autoscaler

> Set your node autoscaler to remove the nodes pausing empties, so what Napwake frees comes off the bill.

Source: https://docs.napwake.dev/next/guides/cluster-autoscaler/

Napwake scales idle workloads to zero, at the pod layer. You pay for nodes, so what a pause frees comes off the bill once an autoscaler removes them.

1. `Napwake pauses pods`
2. `Capacity freed on their nodes``freedThisMonth`, the CPU and memory its pods requested
3. `Autoscaler removes nodes`What this page sets up
4. `Saved``savedThisMonth`

With no node autoscaler, or scale-down blocked, pausing saves nothing. Saved is measured every 5 minutes from each node pool’s spare capacity ([Freed and saved](https://docs.napwake.dev/next/concepts/cost-tracking/#freed-and-saved)). It grows fastest on [nodes of their own](https://docs.napwake.dev/next/guides/node-pools/), which pausing empties.

## What Napwake detects

Every 10 minutes, the operator looks for what scales the nodes. It logs what it finds when that changes, and exports it as [`napwake_node_autoscaler`](https://docs.napwake.dev/next/reference/metrics/#savings).

| Found              | From                                                                                                                                 |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------------------ |
| Karpenter          | `nodepools.karpenter.sh` NodePools (`karpenter.sh/v1`), or nodes labelled `karpenter.sh/nodepool`. Covers AKS node auto-provisioning |
| EKS Auto Mode      | Nodes labelled `eks.amazonaws.com/compute-type: auto`                                                                                |
| Cluster Autoscaler | The `cluster-autoscaler-status` ConfigMap it writes in `kube-system`. Self-managed, or run for you by GKE Standard and AKS           |
| GKE Autopilot      | That ConfigMap, and nodes named `gk3-`                                                                                               |
| `none`             | Nothing, anywhere. The operator logs that pausing won’t save until something scales nodes down                                       |
| `unknown`          | Nothing, but it couldn’t look everywhere. A `watchNamespaces` install can’t read that ConfigMap in `kube-system`                     |

`kubectl napwake status` and `scan` name it at the top, and [`kubectl napwake nodes`](https://docs.napwake.dev/next/getting-started/kubectl-plugin/#see-why-nodes-dont-scale-down) adds what keeps each node.

## Settings

* Cluster Autoscaler

  [Cluster Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler) manages node groups on EKS, GKE, AKS and self-managed clusters.

  cluster-autoscaler-values.yaml

  ```yaml
  autoDiscovery:
    clusterName: my-cluster


  extraArgs:
    # How long a node must be unneeded before it's removed. The default is 10m.
    # A paused workload's node keeps costing money for this long, so keep it
    # short next to idleAfter.
    scale-down-unneeded-time: 5m


    # A node is unneeded when the CPU or memory its pods request, whichever is
    # higher, is below this fraction of what it can allocate. 0.5 is the default.
    # It counts requests, not usage. A node frees up once paused pods are gone,
    # not when they go quiet.
    scale-down-utilization-threshold: "0.5"


    # How long after a scale-up before scale-down is considered again. The
    # default is 10m. A wake can scale nodes up, so a shorter delay lets the
    # next pause free them sooner. Much shorter, and nodes churn.
    scale-down-delay-after-add: 5m


    # How long after removing a node before scale-down is considered again.
    # The default, 0s, lets the nodes of several workloads paused together go
    # one after another, so leave it.
    scale-down-delay-after-delete: 0s


    # How often the autoscaler evaluates the cluster. 10s is the default and
    # needs no change for Napwake.
    scan-interval: 10s
  ```

  | Provider | Setting these flags                                                                            |
  | -------- | ---------------------------------------------------------------------------------------------- |
  | GKE      | You can’t. Its `optimize-utilization` profile scales down sooner than the default `balanced`   |
  | AKS      | Cluster-wide, in its cluster autoscaler profile (`az aks update --cluster-autoscaler-profile`) |

  See [Node pools](https://docs.napwake.dev/next/guides/node-pools/#set-up-the-pool) for both.

  | When Napwake          | Cluster Autoscaler                                                                                                                             |
  | --------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------- |
  | Pauses (scale to 0)   | The pods go. A node whose remaining pods request under the threshold goes after `scale-down-unneeded-time`, unless something on it blocks that |
  | Wakes (scale back up) | Adds nodes if there’s no room                                                                                                                  |

  ### What keeps a node

  | However empty, a node stays with                                           | When                                                                                                                                        |
  | -------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
  | A pod annotated `cluster-autoscaler.kubernetes.io/safe-to-evict: "false"`  | Always                                                                                                                                      |
  | A pod whose PodDisruptionBudget allows no disruptions                      | Always                                                                                                                                      |
  | A pod with a `hostPath` volume, or an `emptyDir` not in memory             | While `--skip-nodes-with-local-storage` is on, as it is upstream by default. AKS turns it off, and GKE 1.22 and later ignores local storage |
  | A pod no controller would recreate elsewhere                               | Always                                                                                                                                      |
  | A `kube-system` pod without a PodDisruptionBudget                          | While `--skip-nodes-with-system-pods` is on, its default                                                                                    |
  | `cluster-autoscaler.kubernetes.io/scale-down-disabled: "true"` on the node | Always                                                                                                                                      |
  | DaemonSet and static pods                                                  | Never                                                                                                                                       |

  Napwake’s pauses set replica counts and never evict, so PodDisruptionBudgets don’t stop them. What’s left on the node holds it, and `kubectl napwake nodes` lists that per node, with the fix.

* Karpenter

  [Karpenter](https://karpenter.sh) is an open-source node autoscaler from Kubernetes SIG Autoscaling, for AWS, Azure (as AKS node auto-provisioning) and other clouds. Napwake reads its `karpenter.sh/v1` NodePools.

  nodepool.yaml

  ```yaml
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
      # it's considered. It restarts whenever a pod is added or removed, so a
      # node stays 5 minutes after a pause, and a wake soon after finds it.
      # Never turns consolidation off.
      consolidateAfter: 5m
  ```

  | With Karpenter    |                                                                                                                                                                                     |
  | ----------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
  | `nodeClassRef`    | Your provider’s node class. `EC2NodeClass` in `karpenter.k8s.aws` on AWS, `NodeClass` in `eks.amazonaws.com` on EKS Auto Mode                                                       |
  | After a pause     | The node is empty or underused, and is consolidated after `consolidateAfter`. Karpenter repacks underused nodes too, so more of what pausing frees on shared nodes is saved, sooner |
  | On a wake         | Instead of growing a node group, Karpenter provisions a node sized for the pending pods, so it doesn’t over-provision                                                               |
  | What keeps a node | `karpenter.sh/do-not-disrupt: "true"` on it or on one of its pods, or a pod whose PodDisruptionBudget allows no disruptions                                                         |
  | Without `budgets` | It disrupts at most 10% of a NodePool’s nodes at once                                                                                                                               |
  | Spot              | Suits non-prod. Napwake prices spot nodes at their spot list price ([Spot nodes](https://docs.napwake.dev/next/concepts/cost-tracking/#spot-nodes))                                                         |

  `kubectl napwake nodes` shows each NodePool’s consolidation settings and what holds each node.

* Autopilot and serverless

  | Service                    | How it works                                        | With Napwake                                                   |
  | -------------------------- | --------------------------------------------------- | -------------------------------------------------------------- |
  | GKE Autopilot              | Google manages the nodes, and bills per pod request | Freed stops being billed with the pods. No autoscaler settings |
  | EKS with Fargate           | Each pod gets its own microVM. No shared nodes      | Pausing a pod stops its billing. No autoscaler settings        |
  | AKS node auto-provisioning | Karpenter, run by Azure                             | Use the Karpenter settings                                     |
  | EKS Auto Mode              | Karpenter, run by AWS. You pay for its nodes        | Use the Karpenter settings in your own NodePools               |

  On Autopilot and Fargate, what pausing frees is what you stop paying. Saved still measures spare node capacity, so it can read lower than your bill’s drop, never higher. Set `costTracking.rates` to the provider’s per-pod prices to price freed at what you pay.

## Verifying it works

1. Pause a workload now, without waiting for its idle clock:

   ```bash
   kubectl napwake pause my-api -n staging
   ```

   It returns once it’s `Paused`, or says why not, such as workloads that depend on it being awake ([Pause now](https://docs.napwake.dev/next/guides/pause/#pause-now)).

2. Watch the nodes:

   ```bash
   kubectl get nodes -w
   ```

   One goes after the autoscaler’s delay. If none does, `kubectl napwake nodes` says what keeps each.

3. Watch SAVED grow with FREED:

   ```bash
   kubectl napwake status -n staging
   ```

   The node’s going shows within 5 minutes, and in the workload’s status within another 5.

4. Wake it, with a request to its Service or:

   ```bash
   kubectl napwake wake my-api -n staging
   ```

   It returns once it’s `Running`, all replicas Ready. Its idle clock restarts, so it pauses again once idle.

5. Check a node is provisioned and the pods get Ready. A wake that waits for a node may need a longer `wake.maxWait` ([What waking costs](https://docs.napwake.dev/next/guides/node-pools/#what-waking-costs)).

CheckpointAwake

A node went with the pause and came back with the wake. SAVED grew with FREED.

If nodes stay after a pause:

| Check                              | For                                                                                                   |
| ---------------------------------- | ----------------------------------------------------------------------------------------------------- |
| `kubectl napwake nodes`            | What keeps each node                                                                                  |
| The autoscaler’s logs              | Why it skipped scale-down                                                                             |
| The nodes’ other pods              | Pods Napwake doesn’t manage. A [pool of their own](https://docs.napwake.dev/next/guides/node-pools/) for non-prod avoids them |
| PodDisruptionBudgets               | Any that allow no disruptions                                                                         |
| `scale-down-utilization-threshold` | A threshold set too low                                                                               |

See [Freed but not saved](https://docs.napwake.dev/next/operations/troubleshooting/#freed-but-not-saved).

Next

* [Node pools for real savings →Nodes that pausing empties.](https://docs.napwake.dev/next/guides/node-pools/)
* [Cost tracking →How freed and saved are measured.](https://docs.napwake.dev/next/concepts/cost-tracking/)
