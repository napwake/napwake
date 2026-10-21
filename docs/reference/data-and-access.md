# Data and Access

Everything the Hybernate operator and its doorman read, write, and send, for a security review. The [RBAC](#rbac) the Helm chart grants matches this list.

## What it reads

| Resource | Why | How |
|---|---|---|
| Deployments, StatefulSets | The workloads it manages: replicas, pod template, labels, and annotations | Watched |
| Pods | What a workload's running pods request: the app containers', at most every 5 minutes, to judge CPU activity against, and all containers', sidecars included, to price it; and which pod sent a request that woke a workload. What every pod bound to a node requests, to measure the spare capacity on each node pool that [saved](../concepts/cost-tracking.md#how-saved-is-measured) is worked out from. The doorman reads the pod each of a woken workload's endpoints names, to check the endpoint's address is that pod's. With `doorman.negForwarder.enabled`, a forwarder's pods, for their addresses and readiness | Watched, with only bound, unfinished pods cached, and of each only its name, node, phase and requests; read from the API server for the rest. Not watched by an operator limited by `watchNamespaces`. The doorman keeps a pod's addresses for 30 seconds |
| Pod metrics (`metrics.k8s.io`) | CPU use, the activity clock's main signal | Read when needed, not cached |
| Services | Which Services to route a paused workload's requests through, and which workload an address in an environment names | Watched |
| Ingresses | Their class and the Services they send traffic to, to tell which Services a GKE Ingress puts behind container-native load balancing, which can't be routed to the doorman | Watched |
| EndpointSlices | Its own, routing paused workloads to the doorman, and the doorman Service's, to find the doorman's pods. The doorman reads every slice in the watched namespaces, to find a woken workload's Ready pods | Watched |
| ConfigMaps | The ones a managed workload takes environment variables from, to learn its dependencies; and `kube-system/cluster-autoscaler-status`, whose existence shows Cluster Autoscaler runs | Read one by one with `get`, not cached; never listed or written. Of `cluster-autoscaler-status`, only its metadata, every 10 minutes |
| PersistentVolumeClaims | Their size, to price the storage a paused workload keeps | Watched |
| HorizontalPodAutoscalers, KEDA ScaledObjects | What scales a workload, and its range | Watched |
| Namespaces | Their labels: opt-in, settings, and protection | Watched |
| Nodes | Their labels (instance type, region, spot, node pool), to price workloads and group them into pools; and their allocatable CPU and memory, whether they're schedulable and Ready, to measure each pool's spare capacity | Watched, keeping only those fields; their image lists and the rest are dropped. Their metadata is also listed every 10 minutes, to tell which node autoscaler runs |
| Karpenter NodePools (`karpenter.sh/v1`) | Their names and `spec.disruption`, to report which node autoscaler runs | Listed every 10 minutes, not cached. Without Karpenter installed, nothing is read |
| Deployments in `kube-system` | Their names, to find a Cluster Autoscaler that hasn't written its status ConfigMap | Metadata listed every 10 minutes, only while that ConfigMap is missing |
| ManagedWorkloads | Its own configuration | Watched |

**Never read:** Secrets, of any kind, anywhere. An address set in a Secret is learned only from the request it's used for.

## What it writes

| Resource | What |
|---|---|
| Deployments, StatefulSets | Their replica count, through the scale subresource, and, while one is paused, the `hybernate.io/paused-from` annotation: a copy of what the pause changed, removed when it's handed back. No other field, and never a delete |
| Deployments of its own, with `doorman.negForwarder.enabled` | A forwarder, `hybernate-forwarder-<workload>`, in the namespace of each paused workload whose Service is behind GKE container-native load balancing, owned by its ManagedWorkload and deleted a minute after the workload wakes. See [GKE container-native load balancing](../concepts/wake-on-request.md#gke-container-native-load-balancing-negs) |
| KEDA ScaledObjects | The `autoscaling.keda.sh/paused-replicas` annotation, while their workload is paused or resuming; afterwards the value it had before, or none |
| EndpointSlices | Its own, routing a paused workload's Services to the doorman; deleted when the workload wakes |
| ManagedWorkloads | Its own status, including each workload's forecast state; the ones it creates for labelled workloads; the `hybernate.io/cleanup` finalizer, added to each so that deleting it restores its workload first, and removed once it has, with `hybernate.io/operator`, the operator's namespace; `hybernate.io/last-activity` on a dependency it wakes; and, from the doorman, `hybernate.io/last-request` and `hybernate.io/last-request-from` (the caller's IP address) on one a request wakes |
| Events | On ManagedWorkloads and workloads, to say what it did and why |
| Leases | Leader election, in its own namespace |

**Never written:** any other resource, any other field of a workload, and nothing is ever deleted but its own ManagedWorkloads, EndpointSlices and forwarder Deployments.

## What it sends

Nothing leaves the cluster, unless configured to:

| Destination | When |
|---|---|
| The Kubernetes API server | Always: everything above |
| Prometheus, at `prometheus.url` | Only with `prometheus.url` set: the Prometheus activity queries workloads configure, and, once for each new ManagedWorkload, one range query for up to four weeks of its own containers' CPU (`container_cpu_usage_seconds_total`), an hour a point, to start its forecast from, narrowed by `prometheus.selector` when it's set |

No telemetry, no usage reporting, no update checks. The doorman answers requests to paused workloads and forwards them to the woken pod; it doesn't store or log their contents. Nor does a forwarder, which reads nothing from the API server and only passes connections on to the doorman.

## RBAC

What the Helm chart grants, for a release named `hybernate`; resource names start with the release's full name. Without `watchNamespaces`, the rules marked _namespaced_ are in ClusterRoles; with it, they're in a Role in each watched namespace, and nothing marked _namespaced_ is granted anywhere else.

**Operator** (ServiceAccount `hybernate`), ClusterRole and ClusterRoleBinding `hybernate-manager`:

| API group | Resources | Verbs | |
|---|---|---|---|
| `""` | `namespaces`, `nodes` | `get`, `list`, `watch` | always cluster-wide |
| `karpenter.sh` | `nodepools` | `list` | always cluster-wide |
| `""` | `configmaps` | `get` | namespaced |
| `""` | `events` | `create`, `patch` | namespaced |
| `events.k8s.io` | `events` | `create`, `patch` | namespaced |
| `""` | `services` | `get`, `list`, `watch` | namespaced |
| `networking.k8s.io` | `ingresses` | `get`, `list`, `watch` | namespaced |
| `""` | `pods` | `get`, `list`, `watch` | namespaced |
| `""` | `persistentvolumeclaims` | `get`, `list`, `watch` | namespaced |
| `discovery.k8s.io` | `endpointslices` | `get`, `list`, `watch`, `create`, `update`, `patch`, `delete` | namespaced |
| `apps` | `deployments`, `statefulsets` | `get`, `list`, `watch`, `patch` | namespaced |
| `apps` | `deployments/scale`, `statefulsets/scale` | `get`, `update` | namespaced |
| `autoscaling` | `horizontalpodautoscalers` | `get`, `list`, `watch` | namespaced |
| `keda.sh` | `scaledobjects` | `get`, `list`, `watch`, `patch` | namespaced |
| `hybernate.io` | `managedworkloads` | `get`, `list`, `watch`, `create`, `update`, `patch`, `delete` | namespaced |
| `hybernate.io` | `managedworkloads/status` | `get`, `update`, `patch` | namespaced |
| `hybernate.io` | `managedworkloads/finalizers` | `update` | namespaced |
| `metrics.k8s.io` | `pods` | `get`, `list` | namespaced |

With `watchNamespaces` and the doorman enabled, a Role `hybernate-doorman-endpoints` in the release namespace lets the operator `get`, `list` and `watch` `endpointslices` there, to find the doorman's pods.

With leader election, a Role `hybernate-leader-election` in the release namespace grants `coordination.k8s.io` `leases` (`get`, `create`, `update`) and `""` `events` (`create`, `patch`).

With `doorman.negForwarder.enabled`, a ClusterRole and ClusterRoleBinding `hybernate-forwarder`, or a Role and RoleBinding of that name in each watched namespace, grant `apps` `deployments` (`create`, `delete`), for the forwarders. A forwarder runs as its namespace's `default` ServiceAccount, without its token, and has no access to the API server.

**Doorman** (ServiceAccount `hybernate-doorman`), ClusterRole and ClusterRoleBinding `hybernate-doorman`, or a Role in each watched namespace:

| API group | Resources | Verbs |
|---|---|---|
| `hybernate.io` | `managedworkloads` | `get`, `list`, `watch`, `patch` |
| `discovery.k8s.io` | `endpointslices` | `get`, `list`, `watch` |
| `""` | `pods` | `get` |
| `""`, `events.k8s.io` | `events` | `create`, `patch` |

**Guards**, with `guard.uninstall` and, with `watchNamespaces`, `guard.upgrade` (the defaults): a ServiceAccount `hybernate-uninstall-guard`, and a ClusterRole and ClusterRoleBinding of the same name, allowing `list` on `hybernate.io` `managedworkloads`, cluster-wide even with `watchNamespaces`; and the same named `hybernate-upgrade-guard`. They're Helm hooks, like the Jobs that use them: they exist only while `helm uninstall` runs its pre-delete hook, or `helm upgrade` or `helm rollback` its pre-upgrade or pre-rollback hook, which refuse to leave ManagedWorkloads without an operator.

**People**: ClusterRoles `hybernate-view` and `hybernate-edit`, which grant nothing to Hybernate itself. They're aggregated into Kubernetes' built-in roles: `view`, `edit` and `admin` gain `get`, `list` and `watch` on `hybernate.io` `managedworkloads`, and `get` on their status; `edit` and `admin` gain `create`, `update`, `patch` and `delete`.

**Admission policy**, with `doorman.admissionPolicy.enabled` (the default): a ValidatingAdmissionPolicy and binding `hybernate-doorman`, cluster-scoped, which refuse any change the doorman's ServiceAccount makes to a ManagedWorkload other than to its `hybernate.io/last-request` and `last-request-from` annotations; and a ValidatingAdmissionPolicy and binding `hybernate-request-stamps`, cluster-scoped, which refuse anyone else setting those two annotations, limited to `watchNamespaces` when set.

**Metrics**, with `metrics.enabled` and `metrics.secure` (the defaults): ClusterRoleBindings `hybernate-auth-delegator` and `hybernate-doorman-auth-delegator` to the built-in `system:auth-delegator` ClusterRole, so the operator and the doorman can check the tokens of whoever scrapes them; and a ClusterRole `hybernate-metrics-reader` allowing `get` on the `/metrics` URL, bound to `metrics.readerSubjects` by a ClusterRoleBinding of the same name.

## The plugin

`kubectl hybernate` runs with your own kubeconfig and permissions, reads the same kinds of resources and their events, and changes nothing except for `wake` (activity annotations on a ManagedWorkload), `pause` (the `hybernate.io/pause-requested` annotation on a ManagedWorkload, with `hybernate.io/pause-overrides-forecast` when told to pause one the forecast expects demand for, and, with `--clear-hold`, removing its `hybernate.io/active-until`) and `enable` (the `hybernate.io/dry-run` annotation, on a workload or its namespace, or `spec.dryRun` on a ManagedWorkload you wrote). `scan` reads Prometheus through the API server's service proxy, or at `--prometheus-url`, and writes its report only to your machine, in a file only you can read, as `status --html` does its page. For a workload Argo CD applies, `scan`, `status`, `pause` and `enable` read Argo CD's Applications and its `argocd-cm` ConfigMap, to tell whether it leaves the workload's replicas to Hybernate.
