# Node pools for real savings

> Give non-prod workloads node pools of their own, so pausing an environment empties whole nodes.

Source: https://docs.napwake.dev/next/guides/node-pools/

Pausing frees what a workload’s pods requested on their nodes. You pay for nodes, so freed is saved only when a node goes ([Freed and saved](https://docs.napwake.dev/next/concepts/cost-tracking/#freed-and-saved)).

Every environment on the pool is paused, and nothing else runs there. Its nodes are empty, so the autoscaler removes them.

## Why a pool of their own

On a node shared with busy workloads, a pause frees space but no node. The bill doesn’t change.

Consolidation helps, but packing is never perfect. Karpenter’s `WhenEmptyOrUnderutilized` repacks pods, and Cluster Autoscaler removes nodes under its threshold. A node a quarter full still costs as much as a full one, and [some pods can’t move](https://docs.napwake.dev/next/guides/cluster-autoscaler/) at all.

On a pool of their own, staging, preview and dev are all that hold its nodes. As they pause the pool empties, the autoscaler removes its nodes, and saved rises to meet freed. At zero nodes, it saves all that its paused workloads freed.

| You need                                        | Why                                                                                                                                                                                                                               |
| ----------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| A taint on its nodes                            | Keeps off what doesn’t tolerate it. That’s always-on workloads, and most `kube-system` pods, which hold a node Cluster Autoscaler won’t remove. It’s also Napwake’s operator and doorman, which are never paused and must stay up |
| A label on its nodes                            | For the workloads’ `nodeSelector` (or node affinity), with a matching toleration                                                                                                                                                  |
| An autoscaler that removes empty nodes promptly | And a minimum size of zero, where the provider allows it                                                                                                                                                                          |

## Set up the pool

1. Make the pool, with a taint, a label and a minimum of zero. In Karpenter it’s a NodePool. With Cluster Autoscaler, managed or your own, it’s a node group of its own.

   * Karpenter

     A non-prod NodePool with its own taint and label, which consolidates and prefers spot. It’s for `karpenter.sh/v1` and AWS’s `EC2NodeClass`. Elsewhere, or on EKS Auto Mode, change `nodeClassRef` (on Auto Mode, `group: eks.amazonaws.com` and `kind: NodeClass`).

     nodepool.yaml

     ```yaml
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

     | Field                      | Why                                                                                                                                                                                                                 |
     | -------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
     | `WhenEmptyOrUnderutilized` | Repacks underused nodes too, for when only some environments are paused. `WhenEmpty` needs nodes with only DaemonSets left, rare in a shared pool. `Balanced` repacks only when the saving outweighs the disruption |
     | `consolidateAfter: 5m`     | It restarts whenever a pod is added to or removed from the node. So a node stays 5 minutes after a pause, and a wake soon after finds it                                                                            |
     | `budgets`                  | When a whole environment pauses, the first lets every empty node go together. The second limits repacking busy ones                                                                                                 |
     | `limits`                   | Caps the pool, so a runaway preview environment can’t grow it                                                                                                                                                       |

     | Spot               | Why                                                                                                                                                                                  |
     | ------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
     | Suits non-prod     | Staging can live with the odd interruption. Karpenter picks spot when the requirements allow                                                                                         |
     | Emptied spot nodes | Deleted by default. Swapping one for a cheaper spot node needs the `SpotToSpotConsolidation` feature gate                                                                            |
     | Its price          | Napwake prices spot at its list price, so freed and saved are close to what you pay. AWS’s spot price moves through the day ([Spot nodes](https://docs.napwake.dev/next/concepts/cost-tracking/#spot-nodes)) |

     [Cluster Autoscaler](https://docs.napwake.dev/next/guides/cluster-autoscaler/) has Karpenter’s defaults, `Never`, `expireAfter`, and what keeps a node.

   * GKE Standard

     ```bash
     gcloud container node-pools create non-prod \
       --cluster my-cluster --location us-central1 \
       --machine-type e2-standard-4 --spot --num-nodes 0 \
       --enable-autoscaling --total-min-nodes 0 --total-max-nodes 20 \
       --node-labels example.com/tier=non-prod \
       --node-taints example.com/tier=non-prod:NoSchedule


     gcloud container clusters update my-cluster --location us-central1 \
       --autoscaling-profile optimize-utilization
     ```

     | For GKE Standard       |                                                                                                                                                |
     | ---------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------- |
     | Zero nodes             | GKE never scales a Standard cluster to zero, since system pods need a home. Keep another pool for them. This one can go to zero                |
     | `--num-nodes 0`        | Starts the pool empty. gcloud’s default is 3 nodes in each of the cluster’s zones, 9 for a regional cluster, until the autoscaler removes them |
     | Labels                 | Its nodes also carry `cloud.google.com/gke-nodepool: non-prod`, which you can select instead                                                   |
     | `optimize-utilization` | Scales down sooner and more often than the default `balanced`, across the whole cluster                                                        |

   * AKS

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

     | For AKS pools |                                                                                                              |
     | ------------- | ------------------------------------------------------------------------------------------------------------ |
     | Zero nodes    | User pools scale to zero with `--min-count 0`. System pools can’t                                            |
     | Spot          | Spot pools get AKS’s own `kubernetes.azure.com/scalesetpriority=spot:NoSchedule` taint, so tolerate that too |
     | The profile   | Applies to every autoscaled pool in the cluster                                                              |
     | Labels        | Nodes carry `kubernetes.azure.com/agentpool: nonprod`                                                        |

   * EKS managed node group

     nodegroup.yaml

     ```yaml
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

     | For EKS node groups |                                                                                                                                                                                                                                                                                                                                                             |
     | ------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
     | Scaling             | EKS doesn’t scale managed node groups, but tags their Auto Scaling groups for Cluster Autoscaler. Run it with `--node-group-auto-discovery=asg:tag=k8s.io/cluster-autoscaler/enabled,k8s.io/cluster-autoscaler/my-cluster`                                                                                                                                  |
     | Zero nodes          | To scale up from zero, Cluster Autoscaler must know the nodes’ labels and taints first. Give its IAM role `eks:DescribeNodegroup` to read them from the EKS API. Or tag the group with `k8s.io/cluster-autoscaler/node-template/label/example.com/tier: non-prod` and `k8s.io/cluster-autoscaler/node-template/taint/example.com/tier: non-prod:NoSchedule` |
     | Labels              | Nodes carry `eks.amazonaws.com/nodegroup: non-prod`                                                                                                                                                                                                                                                                                                         |

   * Self-managed Cluster Autoscaler

     cluster-autoscaler-values.yaml

     ```yaml
     autoDiscovery:
       clusterName: my-cluster
     extraArgs:
       scale-down-unneeded-time: 5m
       scale-down-delay-after-add: 5m
       scale-down-utilization-threshold: "0.5"
     ```

     Set the group’s minimum to zero in your cloud. Set its labels and taints as your provider’s Cluster Autoscaler docs say, so it scales up from zero.

   With Cluster Autoscaler, [How soon nodes go](https://docs.napwake.dev/next/guides/node-pools/#how-soon-nodes-go) explains its settings.

2. Steer the workloads to the pool. Give them both a toleration, which lets them past the taint, and a `nodeSelector`, which keeps them on the pool. In the manifests you keep in Git:

   deployment.yaml

   ```yaml
   apiVersion: apps/v1
   kind: Deployment
   metadata:
     name: my-api
     namespace: staging
     labels:
       napwake.dev/managed: "true"
     annotations:
       napwake.dev/wake-max-wait: "5m"
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

   `example.com/tier: non-prod` stands for your label and taint. `wake-max-wait` is there because a wake may wait for a node ([What waking costs](https://docs.napwake.dev/next/guides/node-pools/#what-waking-costs)). The pods run on the pool’s nodes, as the NODE column of `kubectl get pods -n staging -o wide` shows.

   For a whole namespace, add the selector and toleration as pods are created. Any of these works, and Napwake depends on none:

   | Way                                                                    | How                                                                                                                                   |
   | ---------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
   | A policy engine                                                        | A Kyverno `mutate` rule or a Gatekeeper `Assign` mutation, matching pods in your namespaces                                           |
   | The `PodNodeSelector` and `PodTolerationRestriction` admission plugins | They read defaults from the namespace’s annotations. Only where your control plane enables them, which managed providers mostly don’t |
   | Your templating                                                        | A Helm chart’s or Kustomize component’s `nodeSelector` and `tolerations`, in every non-prod release                                   |

   Keep anything always on off the pool, such as a shared database no ManagedWorkload pauses. One always-on pod holds its node, and every pod that fits beside it.

   If paused workloads depend on it, consider managing it. Napwake [won’t pause it](https://docs.napwake.dev/next/concepts/dependencies/) while a dependent is awake, and wakes it first when they wake.

3. Once the pool’s environments pause, compare freed and saved.

   ```bash
   kubectl napwake status
   ```

   [`status`](https://docs.napwake.dev/next/getting-started/kubectl-plugin/#see-what-napwake-is-doing) shows freed beside saved this month, per workload and in total. SAVED follows FREED a little behind, as a node takes `consolidateAfter` or `scale-down-unneeded-time` to go. If under half the freed was saved, Needs attention says nodes aren’t scaling down.

4. If SAVED lags, see what keeps each node.

   ```bash
   kubectl napwake nodes
   ```

   [`nodes`](https://docs.napwake.dev/next/getting-started/kubectl-plugin/#see-why-nodes-dont-scale-down) names the autoscaler, and for each node what keeps it, in BLOCKED BY. A non-prod node with low utilisation and something in BLOCKED BY is the one to look at. [Freed but not saved](https://docs.napwake.dev/next/operations/troubleshooting/#freed-but-not-saved) explains each reason.

CheckpointAsleep · the pool's nodes gone

SAVED in `kubectl napwake status` follows FREED, a little behind.

## How soon nodes go

With Cluster Autoscaler, these decide how soon a paused environment’s nodes go. [Cluster Autoscaler](https://docs.napwake.dev/next/guides/cluster-autoscaler/) says what each setting means.

| Setting                            | Default                         | For a non-prod pool                                                                                                                                 |
| ---------------------------------- | ------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------- |
| `scale-down-unneeded-time`         | `10m`                           | A paused environment’s node costs money this long. `5m` is plenty after an hour idle                                                                |
| `scale-down-utilization-threshold` | `0.5`                           | An emptied pool doesn’t depend on it, since an empty node is always under                                                                           |
| `scale-down-delay-after-add`       | `10m`                           | A wake scales up, so a shorter delay lets the next pause’s nodes go sooner                                                                          |
| `--skip-nodes-with-local-storage`  | `true` upstream, `false` on AKS | Annotate `emptyDir` and `hostPath` pods with `cluster-autoscaler.kubernetes.io/safe-to-evict-local-volumes` rather than turning it off cluster-wide |
| `--skip-nodes-with-system-pods`    | `true`                          | The pool’s taint keeps most `kube-system` pods off                                                                                                  |

The settings follow these pages. They live outside Napwake and change with your provider’s releases, so check them for your versions.

* The [Cluster Autoscaler FAQ](https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/FAQ.md), and its [AWS provider README](https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/cloudprovider/aws/README.md)
* GKE’s [cluster autoscaler](https://cloud.google.com/kubernetes-engine/docs/concepts/cluster-autoscaler) and [`node-pools create`](https://cloud.google.com/sdk/gcloud/reference/container/node-pools/create) pages
* AKS’s [cluster autoscaler](https://learn.microsoft.com/en-us/azure/aks/cluster-autoscaler) page

## What waking costs

On a pool scaled to nothing, a waking workload needs a node first. After a quiet night, the first request waits for one to be provisioned and join. Then the images pull onto the fresh node, and the app starts.

In Napwake’s own GKE test, a wake that needed a new node answered in 52 seconds, node and app included. That was nginx on a spot `e2-standard-4` pool at zero. Other providers, bigger images and slower apps take longer.

| Do                                                                                                           | Why                                                                                                                                                                                            |
| ------------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Raise `wake.maxWait` from its 2-minute default, such as to `5m` (`napwake.dev/wake-max-wait` with the label) | Otherwise the doorman gives up on held requests before the node is ready. The wake carries on ([How long a connection is held](https://docs.napwake.dev/next/concepts/wake-on-request/#how-long-a-connection-is-held)) |
| Raise your ingress controller’s timeout to match                                                             | ingress-nginx gives up on an upstream after 60 seconds by default. Browsers get the [waking-up page](https://docs.napwake.dev/next/concepts/wake-on-request/#browsers) at once                                         |
| Use `autoResume`, for traffic the forecast can learn such as a team in at 9 every weekday                    | It wakes the workload 15 minutes before an hour it expects to be busy. The node is up before anyone arrives ([Resume](https://docs.napwake.dev/next/guides/pause/#resume))                                             |
| Keep one node warm, if every first request must be quick                                                     | A minimum of one, or in Karpenter a small always-on workload in the pool. You pay for that node, and pausing saves the rest                                                                    |

## The metrics

By `pool`, the node pool’s name:

```promql
# What the pool's paused workloads requested, against its spare capacity
napwake_capacity_paused_cpu_cores{pool="non-prod"}
napwake_capacity_spare_cpu_cores{pool="non-prod"}


# What pausing takes off the bill an hour, in the pool
napwake_savings_realised_dollars_per_hour{pool="non-prod"}


# Which node autoscaler the operator found
napwake_node_autoscaler
```

As nodes go, spare capacity falls towards zero, and saved an hour rises towards what the paused replicas would cost. Spare capacity above what’s paused means the nodes are still there, so check `kubectl napwake nodes` ([Metrics reference](https://docs.napwake.dev/next/reference/metrics/#savings)).

Next

* [Cluster Autoscaler →Settings for each node autoscaler.](https://docs.napwake.dev/next/guides/cluster-autoscaler/)
* [Cost tracking →How freed and saved are measured.](https://docs.napwake.dev/next/concepts/cost-tracking/)
