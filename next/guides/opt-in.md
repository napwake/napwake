# Opting in

> Install Napwake once per cluster, then label what it should manage. The label says which workloads, and annotations say how.

Source: https://docs.napwake.dev/next/guides/opt-in/

1. Label the workload, measuring first.

   deployment.yaml

   ```yaml
   apiVersion: apps/v1
   kind: Deployment
   metadata:
     name: checkout-api
     namespace: preview-42
     labels:
       napwake.dev/managed: "true"
     annotations:
       napwake.dev/dry-run: "true"      # measure first; nothing is paused
       napwake.dev/idle-after: "2h"
   ```

   Napwake creates a [ManagedWorkload](https://docs.napwake.dev/next/guides/managed-workload/) for it, kept in step with the annotations. The label is usually one line in Helm values or manifests in Git.

2. See what pausing would free.

   ```bash
   kubectl napwake scan
   ```

   It lists every workload in [dry-run](https://docs.napwake.dev/next/guides/dry-run/#what-dry-run-measured).

3. Start pausing.

   ```bash
   kubectl napwake enable checkout-api -n preview-42
   kubectl napwake enable --all -n preview-42     # every workload in the namespace
   ```

   `enable` sets the workload’s own `napwake.dev/dry-run` to `"false"`, which wins wherever the dry-run came from. See [`enable`](https://docs.napwake.dev/next/getting-started/kubectl-plugin/#enable-a-workload) for `--all`, GitOps and Helm.

CheckpointAwake · until idle

Napwake pauses the workload once it has had no activity for `idle-after`, 2 hours here.

Put the label on the workload itself

A chart’s `podLabels` and `podAnnotations` label the pods, where Napwake doesn’t look, and a `LabelIgnored` warning event says so. For Kustomize, see [With Kustomize](https://docs.napwake.dev/next/guides/opt-in/#with-kustomize).

## A whole namespace

Label a namespace to opt in every Deployment and StatefulSet in it, with its annotations as their defaults. It suits dev and preview namespaces, and third-party charts you can’t label.

* Namespace

  ```yaml
  apiVersion: v1
  kind: Namespace
  metadata:
    name: preview-42
    labels:
      napwake.dev/managed: "true"
    annotations:
      napwake.dev/dry-run: "true"
      napwake.dev/idle-after: "2h"
  ```

* Argo CD

  An Application with `CreateNamespace=true` has no Namespace in Git. Put the label and annotations in `managedNamespaceMetadata`, which Argo CD sets on the namespace it creates.

  application.yaml

  ```yaml
  apiVersion: argoproj.io/v1alpha1
  kind: Application
  metadata:
    name: preview-42
    namespace: argocd
  spec:
    project: default
    source:
      repoURL: https://github.com/your-org/environments.git
      targetRevision: main
      path: previews/preview-42
    destination:
      server: https://kubernetes.default.svc
      namespace: preview-42
    syncPolicy:
      automated: {}
      managedNamespaceMetadata:
        labels:
          napwake.dev/managed: "true"
        annotations:
          napwake.dev/dry-run: "true"
          napwake.dev/idle-after: "2h"
      syncOptions:
        - CreateNamespace=true
  ```

  Argo CD applies it only in a sync with `CreateNamespace=true`, so an existing namespace gets it at the next sync. Adding it starts that sync only with `prune: true` and `selfHeal: true`. Otherwise press **Sync** in the UI, which takes the sync options, or give them with kubectl:

  ```bash
  kubectl patch application preview-42 -n argocd --type merge -p '{"operation":{"sync":{"syncOptions":["CreateNamespace=true"]}}}'
  ```

  To stop, remove the label from `managedNamespaceMetadata` and sync. Removing `managedNamespaceMetadata` leaves the label on, as Argo CD then leaves the namespace alone. Leave replicas to Napwake too, as in [With GitOps](https://docs.napwake.dev/next/guides/opt-in/#with-gitops).

* Flux

  Flux doesn’t create a Kustomization’s namespace, so it’s in Git. Label and annotate it there, as `namespace.yaml` beside the kustomization:

  kustomization.yaml

  ```yaml
  apiVersion: kustomize.config.k8s.io/v1beta1
  kind: Kustomization
  namespace: preview-42
  resources:
    - namespace.yaml
    - ../../base
  ```

  A HelmRelease’s `install.createNamespace` leaves its namespace unlabelled, so add the Namespace to the Kustomization that applies it. To stop, remove the label and keep the Namespace. With `prune: true`, Flux deletes a Namespace that leaves Git, and everything in it.

To leave one workload out, label it `napwake.dev/ignore: "true"` or `napwake.dev/managed: "false"`.

## With Kustomize

Use one of these. Each leaves the workloads’ selectors alone.

* The Namespace

  If the kustomization has the Namespace, label and annotate it there, as in [A whole namespace](https://docs.napwake.dev/next/guides/opt-in/#a-whole-namespace). Nothing on the workloads changes.

* Every resource

  `labels` with `includeSelectors: false` labels each resource’s own metadata only:

  kustomization.yaml

  ```yaml
  resources:
    - ../../base
  labels:
    - pairs:
        napwake.dev/managed: "true"
      includeSelectors: false
  ```

  Leave out `includeTemplates`. It labels the pods too, restarting them for nothing. It also labels a StatefulSet’s `volumeClaimTemplates`, which fails as the selector does. Services and ConfigMaps get the label, which Napwake ignores. So does a Namespace in the kustomization, opting in the whole namespace.

* Chosen workloads

  A patch changes only the workload’s own labels and annotations, settings included:

  kustomization.yaml

  ```yaml
  resources:
    - ../../base
  patches:
    - target:
        kind: Deployment
        name: checkout-api
      patch: |
        apiVersion: apps/v1
        kind: Deployment
        metadata:
          name: checkout-api
          labels:
            napwake.dev/managed: "true"
          annotations:
            napwake.dev/dry-run: "true"
            napwake.dev/idle-after: "2h"
  ```

  Keep the `target`. Without it, the build fails with `no resource matches strategic merge patch` if the manifest has a namespace the patch doesn’t give. For every Deployment and StatefulSet, target `kind: Deployment|StatefulSet` with no name, and keep the patch’s `name`, which is ignored.

Don’t use `commonLabels`

It changes selectors, which Kubernetes doesn’t let change. Kustomize puts it in each workload’s `spec.selector` and a StatefulSet’s `volumeClaimTemplates` too.

| Applied to                                                                                           | What happens                                                                                                                              |
| ---------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------- |
| An existing workload                                                                                 | The apply fails with `field is immutable`. Its Service can take the new selector, which no running pod has, and stop sending them traffic |
| An existing workload, by a tool that recreates what it can’t change, such as Flux with `force: true` | The tool deletes and recreates the workloads                                                                                              |
| A new workload                                                                                       | It applies, but removing the label later fails the same way                                                                               |

`labels` with `includeSelectors: true`, which `kustomize edit fix` makes of `commonLabels`, does the same.

`commonAnnotations` puts settings on the pod templates too. Adding or changing one, such as `dry-run`, then restarts every workload’s pods. Napwake counts each restart as a [deploy](https://docs.napwake.dev/next/concepts/idle-detection/#what-counts-as-activity), which is activity. Use the Namespace or a patch.

## Settings

Set them on the workload or its namespace. [Settings annotations](https://docs.napwake.dev/next/reference/labels-annotations/#settings-annotations) lists the values each accepts.

| Annotation                                                             | Default      | What it does                                                                                                                                            |
| ---------------------------------------------------------------------- | ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `napwake.dev/dry-run`                                                  | `"false"`    | Measures, and records what pausing would free, without pausing                                                                                          |
| `napwake.dev/idle-after`                                               | `1h`         | Time without activity before pausing, at least `1m`                                                                                                     |
| `napwake.dev/cpu-threshold`                                            | `10`         | CPU use, as a percentage of requests, above which it’s active                                                                                           |
| [`napwake.dev/auto-resume`](https://docs.napwake.dev/next/concepts/forecasting/)               | `"false"`    | Wakes it ahead of the demand a confident forecast predicts                                                                                              |
| [`napwake.dev/pause-early`](https://docs.napwake.dev/next/concepts/forecasting/#pausing-early) | `"false"`    | Pauses before `idle-after` runs out when a confident forecast expects quiet. Never with `wake: "ask"`                                                   |
| [`napwake.dev/forecast`](https://docs.napwake.dev/next/concepts/forecasting/#forecast-setting) | `"balanced"` | How soon the forecast acts. `"eager"`, `"balanced"` or `"cautious"`                                                                                     |
| [`napwake.dev/depends-on`](https://docs.napwake.dev/next/concepts/dependencies/)               | none         | Workloads this one needs                                                                                                                                |
| [`napwake.dev/wake`](https://docs.napwake.dev/next/concepts/wake-on-request/#the-wake-setting) | `"request"`  | What wakes it through the doorman. `"request"` is any request. `"ask"` is only a person’s click, for a [public URL](https://docs.napwake.dev/next/guides/preview-environments/) |
| `napwake.dev/wake-max-wait`                                            | `2m`         | How long a request is held while it wakes, at most `1h`                                                                                                 |
| `napwake.dev/wake-page`                                                | `"true"`     | Shows browsers a waking-up page                                                                                                                         |

Leave one out for its default. A value that can’t be read is skipped, with an `InvalidSetting` warning event, and the next place below decides. An unreadable `dry-run` turns dry-run on instead, and an unreadable `wake` asks.

### Which setting wins

1. 1The workload's annotation
2. 2Its namespace's
3. 3The Helm defaults`defaults.idleAfter`, `defaults.cpuThreshold`, `defaults.dryRun`, `defaults.pauseEarly`, `defaults.forecast`, `defaults.wake`
4. 4The built-in default

So a namespace can measure everything with `dry-run: "true"` while one workload in it goes live with `dry-run: "false"`.

## Protected namespaces

```bash
kubectl label namespace payments napwake.dev/protected=true
```

Napwake never manages anything in a protected namespace, such as production, whatever the labels. A warning event on each workload says why.

| In a protected namespace      | What happens                                        |
| ----------------------------- | --------------------------------------------------- |
| A labelled workload           | It isn’t opted in                                   |
| A ManagedWorkload you wrote   | It reports `Protected=True` and never pauses        |
| A workload Napwake had paused | It’s woken, so protecting never leaves anything off |

| Always protected, with no label                                 | So that                                                         |
| --------------------------------------------------------------- | --------------------------------------------------------------- |
| `kube-system` and every other `kube-*`                          | Labelling every namespace can’t pause CoreDNS or metrics-server |
| Napwake’s own namespace, and the doorman’s if it runs elsewhere | Napwake can’t pause itself                                      |

Protect more by name, such as every `prod-*`, with the Helm value `protectedNamespaces`. Only `napwake.dev/allow-protected: "true"`, set on purpose, lets Napwake manage a protected namespace. Napwake never sets either label. The operator’s and the doorman’s own Deployments are never managed, since nothing would be left to wake them. To keep Napwake out by Kubernetes RBAC, install it with [`watchNamespaces`](https://docs.napwake.dev/next/reference/helm-values/#namespaces).

## With GitOps

GitOps tools leave the ManagedWorkload alone, but must leave the replicas to Napwake. The ManagedWorkload is owned by the workload, like a Deployment’s ReplicaSets, so Argo CD shows it under the workload. It never takes the workload’s labels, so a tool’s tracking label can’t make the tool claim and prune it.

A tool would scale a paused workload back up. [GitOps tools](https://docs.napwake.dev/next/guides/gitops/) shows how to stop that for Argo CD, Flux, Fleet, kapp and Kluctl. Under Flux, many charts let you leave `replicas` out, such as when their autoscaling value is on.

## What Napwake does with the label

| When you                                                    | Napwake                                                                                                                                                                                                                                                     |
| ----------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Label a workload                                            | Creates a ManagedWorkload named after it, owned by it and marked `napwake.dev/from-label: "true"`, with a `Managed` event on the workload. When the name is taken, as by a StatefulSet named like a Deployment, it adds the kind, such as `api-statefulset` |
| Change its annotations                                      | Updates the ManagedWorkload within seconds                                                                                                                                                                                                                  |
| Remove the label, or add `napwake.dev/ignore`               | Deletes it, first scaling a paused workload back to its replicas                                                                                                                                                                                            |
| Delete the workload                                         | Deletes its ManagedWorkload too                                                                                                                                                                                                                             |
| Wrote a ManagedWorkload for it                              | Yours wins, and the label does nothing                                                                                                                                                                                                                      |
| Set the label to anything but `"true"`, such as `"dry-run"` | Doesn’t opt it in, and says so in an event. Settings like dry-run are annotations                                                                                                                                                                           |

Settings with more than one value, such as Prometheus activity queries, aren’t annotations. [Set them on the ManagedWorkload](https://docs.napwake.dev/next/guides/managed-workload/#edit-the-one-the-label-created) the label created, which keeps them.

Next

* [Dry run →What dry-run measures.](https://docs.napwake.dev/next/guides/dry-run/)
* [ManagedWorkload →Settings annotations don't cover.](https://docs.napwake.dev/next/guides/managed-workload/)
* [GitOps tools →Leave replicas to Napwake.](https://docs.napwake.dev/next/guides/gitops/)
