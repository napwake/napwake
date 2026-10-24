# Installation

## Prerequisites

- Kubernetes v1.30+ (tested in CI on 1.30 and 1.37)
- kubectl v1.30+
- [metrics-server](https://github.com/kubernetes-sigs/metrics-server) installed (Napwake reads pod CPU via the Kubernetes Metrics API)
- Helm v3.8+ (if using Helm install), for OCI charts

## Install

Napwake is packaged two ways: a Helm chart, and the Kustomize manifests, the repository's `config/default`, which `install.yaml` is built from. Both install the same operator and doorman. Use Helm unless your cluster's config is kept as Kustomize.

=== "Helm"

    Put your settings in a values file, starting with your users' [time zone](#time-zone), such as `America/New_York`:

    ```yaml title="napwake-values.yaml"
    timezone: Europe/London
    ```

    Then install directly from the OCI registry; there's no `helm repo` to add:

    ```bash
    helm install napwake oci://ghcr.io/napwake/charts/napwake \
      --version 0.2.0 \
      --namespace napwake-system \
      --create-namespace \
      -f napwake-values.yaml
    ```

    Keep the file, and upgrade with it too: Helm reuses a release's values only on an upgrade given none, so one given `--set` alone drops the rest; see [Keeping your settings](../operations/upgrading.md#keeping-your-settings). The chart's version has no `v`; the image it runs is tagged with one, such as `v0.2.0`. The chart installs the ManagedWorkload CRD too, and upgrades it with each `helm upgrade`.

    ??? tip "Helm Values"

        | Value | Default | Description |
        |-------|---------|-------------|
        | `replicaCount` | `1` | Number of operator replicas |
        | `image.repository` | `ghcr.io/napwake/napwake` | Container image |
        | `image.tag` | the chart's `appVersion` | Image tag |
        | `leaderElection.enabled` | `true` | Enable HA leader election |
        | `metrics.secure` | `true` | Serve metrics over HTTPS |
        | `metrics.readerSubjects` | `[]` | Who may scrape the metrics, such as your Prometheus's ServiceAccount |
        | `timezone` | `UTC` | Time zone the forecast learns hours and weekdays in |
        | `watchNamespaces` | `[]` | Namespaces to work in; empty means all |
        | `defaults.idleAfter` | `1h` | Idle time before pausing, for opted-in workloads that don't set their own |
        | `defaults.cpuThreshold` | `10` | CPU percentage of requests that counts as active |
        | `defaults.dryRun` | `false` | Measure every opted-in workload without pausing, unless it sets its own |
        | `defaults.pauseEarly` | `false` | Let a confident forecast pause workloads before `idleAfter` runs out |
        | `defaults.forecast` | `balanced` | How soon the forecast acts: `eager`, `balanced` or `cautious` |
        | `resources.limits.cpu` | `500m` | CPU limit |
        | `resources.limits.memory` | `512Mi` | Memory limit |

        Every value is in the [Helm Values Reference](../reference/helm-values.md).

=== "kubectl"

    Apply `install.yaml`, the Kustomize manifests built into one file:

    ```bash
    kubectl apply -f https://github.com/napwake/napwake/releases/download/v0.2.0/install.yaml
    ```

    This installs the CRD, RBAC, the operator Deployment, and the doorman Deployment, Service and PodDisruptionBudget into the `napwake-system` namespace. The operator is `deployment/napwake-controller-manager`.

    Then set your users' [time zone](#time-zone), such as `America/New_York`, on the operator:

    ```bash
    kubectl patch deployment napwake-controller-manager -n napwake-system --type=json \
      -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--timezone=Europe/London"}]'
    ```

    An `install.yaml` that changes the operator's arguments replaces them, so after upgrading, check `--timezone` is still there, or install with Kustomize, which keeps it in your kustomization. For Prometheus and NetworkPolicies, see [below](#prometheus-and-networkpolicies-without-helm).

=== "Kustomize"

    Use the Kustomize manifests as a remote base, pinned to the release, in a kustomization of your own, here with your users' [time zone](#time-zone) set on the operator:

    ```yaml title="kustomization.yaml"
    apiVersion: kustomize.config.k8s.io/v1beta1
    kind: Kustomization
    resources:
      - https://github.com/napwake/napwake//config/default?ref=v0.2.0
    patches:
      - target:
          kind: Deployment
          name: napwake-controller-manager
        patch: |-
          - op: add
            path: /spec/template/spec/containers/0/args/-
            value: --timezone=Europe/London
    ```

    ```bash
    kubectl apply -k .
    ```

    It installs what `install.yaml` does, into `napwake-system`, and the operator's other [flags](../reference/configuration.md) are set the same way. To upgrade, change `ref` to the new release. Kustomize fetches each base with git, so whatever runs it, `kubectl` or a GitOps tool, needs to reach github.com. For Prometheus and NetworkPolicies, see [below](#prometheus-and-networkpolicies-without-helm).

=== "Source"

    Clone the repo and build the image:

    ```bash
    git clone https://github.com/napwake/napwake.git
    cd napwake
    make docker-build IMG=napwake:dev
    ```

    The image is only in your local Docker, so make it available to the cluster's nodes before deploying. For a [Kind](https://kind.sigs.k8s.io/) cluster, load it into the nodes:

    ```bash
    kind load docker-image napwake:dev --name <cluster>
    make deploy IMG=napwake:dev
    ```

    For any other cluster, push it to a registry the nodes can pull from:

    ```bash
    make docker-build docker-push IMG=<registry>/napwake:dev
    make deploy IMG=<registry>/napwake:dev
    ```

    `make deploy` installs the Kustomize manifests, `config/default`, with the operator and the doorman running `IMG`. It writes `IMG` into `config/manager/kustomization.yaml`; `git checkout config/manager` puts it back. From the clone, `kubectl apply -k config/prometheus` and `kubectl apply -k config/network-policy` add the bases [below](#prometheus-and-networkpolicies-without-helm).

### Prometheus and NetworkPolicies without Helm

With `install.yaml` or the Kustomize manifests, two more bases, pinned to the same release, add what these chart values turn on in a Helm install. Add them to your kustomization's `resources`, or apply one on its own, such as `kubectl apply -k "https://github.com/napwake/napwake//config/prometheus?ref=v0.2.0"`.

| Base | Adds | Chart values |
|------|------|--------------|
| `https://github.com/napwake/napwake//config/prometheus?ref=v0.2.0` | ServiceMonitors for the operator and the doorman, and the [alerting rules](../operations/monitoring.md#alerting-rules). Needs the Prometheus Operator's CRDs, and your Prometheus [allowed to read the metrics](../operations/monitoring.md#scraping) | `metrics.serviceMonitor.enabled`, `metrics.prometheusRule.enabled` |
| `https://github.com/napwake/napwake//config/network-policy?ref=v0.2.0` | NetworkPolicies: only namespaces labelled `metrics: enabled` may scrape the operator and the doorman, and the doorman's routing ports (TCP 20000-29999) stay open to every caller of a paused workload's Services | `networkPolicy.enabled` |

### With Argo CD or Flux

The chart is published only to an OCI registry, which Argo CD and Flux each name their own way. Commit one of these to the repository your GitOps tool syncs from, with your users' [time zone](#time-zone):

=== "Argo CD"

    ```yaml title="application.yaml"
    apiVersion: argoproj.io/v1alpha1
    kind: Application
    metadata:
      name: napwake
      namespace: argocd
    spec:
      project: default
      source:
        repoURL: ghcr.io/napwake/charts
        chart: napwake
        targetRevision: 0.2.0
        helm:
          valuesObject:
            timezone: Europe/London
      destination:
        server: https://kubernetes.default.svc
        namespace: napwake-system
      syncPolicy:
        automated: {}
        syncOptions:
          - CreateNamespace=true
    ```

    `repoURL` is the registry and path without `oci://`, which is how Argo CD knows the chart is in an OCI registry, and `chart` is the chart's name. The registry is public, so Argo CD needs no credentials for it. Argo CD 3.3 and later runs the chart's uninstall guard when the Application is deleted; see [Uninstall](#uninstall). To install the Kustomize manifests instead, point an Application's `path` at your kustomization.

=== "Flux"

    ```yaml title="napwake.yaml"
    apiVersion: source.toolkit.fluxcd.io/v1
    kind: OCIRepository
    metadata:
      name: napwake
      namespace: flux-system
    spec:
      interval: 1h
      url: oci://ghcr.io/napwake/charts/napwake
      ref:
        tag: 0.2.0
      layerSelector:
        mediaType: application/vnd.cncf.helm.chart.content.v1.tar+gzip
        operation: copy
    ---
    apiVersion: helm.toolkit.fluxcd.io/v2
    kind: HelmRelease
    metadata:
      name: napwake
      namespace: flux-system
    spec:
      interval: 1h
      chartRef:
        kind: OCIRepository
        name: napwake
      releaseName: napwake
      targetNamespace: napwake-system
      storageNamespace: napwake-system
      install:
        createNamespace: true
      values:
        timezone: Europe/London
    ```

    This takes Flux 2.6 or later, where `OCIRepository` is `v1`. `releaseName` keeps the release `napwake`, as these docs call it, rather than Flux's default of `napwake-system-napwake`, and `storageNamespace` keeps its Helm history beside it, where `helm list -n napwake-system` shows it. To install the Kustomize manifests instead, point a Flux `Kustomization` at your kustomization.

To upgrade, change the version, `targetRevision` or `tag`, to the new release's.

#### Moving a Helm install to Argo CD or Flux

An install made with `helm install`, as in the Helm tab, can be handed to Argo CD or Flux where it runs, given the same values: nothing is reinstalled, the operator isn't restarted, and paused workloads stay paused. Don't uninstall it first. `helm uninstall` refuses while ManagedWorkloads are left, as in [Uninstall](#uninstall), and doing what it asks, stopping managing every workload, wakes every paused one and throws away what Napwake has learned of each, its forecast and its tally of what pausing freed.

Give Argo CD or Flux the values the release has now, which this prints, and the version it runs, which `helm list -n napwake-system` shows in its `CHART` column:

```bash
helm get values napwake -n napwake-system
```

=== "Argo CD"

    1. Commit the Application above, with those values under `valuesObject` and that version as `targetRevision`. Argo CD renders the chart, finds every resource already there as it would make it, and shows the Application `Synced` and `Healthy` without applying anything, so an automated sync doesn't start.
    2. Sync it once by hand, which applies the resources and marks them as the Application's own, with Argo CD's `argocd.argoproj.io/tracking-id` annotation: **Sync** in Argo CD's UI, or with kubectl, giving the Application's sync options:

        ```bash
        kubectl patch application napwake -n argocd --type merge -p '{"operation":{"sync":{"syncOptions":["CreateNamespace=true"]}}}'
        ```

        The sync's result warns, for each resource, that it's missing the `kubectl.kubernetes.io/last-applied-configuration` annotation, which Helm doesn't set and the sync adds.

    3. Check it's `Synced` and `Healthy`:

        ```bash
        kubectl get application napwake -n argocd
        ```

    4. Delete Helm's record of the release, its `sh.helm.release.v1.napwake.v<revision>` Secrets, so `helm` no longer acts on it:

        ```bash
        kubectl delete secrets -n napwake-system -l owner=helm,name=napwake
        ```

    `helm list -n napwake-system` no longer shows the release. From then on, change it in Git: Argo CD applies a change to the values or `targetRevision`, and, from 3.3, runs the uninstall guard when the Application is deleted.

=== "Flux"

    Commit the `OCIRepository` and `HelmRelease` above, with those values under `values` and that version as `tag`. Their `releaseName` and `storageNamespace` are the release's own, so Flux's helm-controller finds it in Helm's storage and takes it over in place: it upgrades the release to a new revision, with the same values, and nothing is deleted. `helm list -n napwake-system` goes on showing it, its chart version now followed by `+` and the start of the chart's digest, as Flux names it. From then on, change it in Git: a `helm upgrade` by hand is undone at Flux's next reconcile.

## Time zone

The [forecast](../concepts/forecasting.md) learns when each workload is busy by hour of day and day of week, counted in the operator's time zone, holds off pausing ahead of those hours and, with `autoResume`, wakes workloads before them. Set it to your users' IANA time zone, such as `Europe/London`, `America/New_York` or `Asia/Kolkata`: Helm's `timezone` value, or the operator's `--timezone` flag. Left at the default, UTC, a team whose clocks change for daylight saving finds its workloads woken an hour early or late for half the year. For teams in different zones, pick the zone most of them work in, or give each team an install of its own, as below. The chart's install notes say which zone the forecast uses, and [Wall-Clock Alignment](../concepts/forecasting.md#wall-clock-alignment) has the details.

To change it on a Helm install, change it in your values file:

```yaml title="napwake-values.yaml"
timezone: America/New_York
```

and upgrade with the file, at the version you run, which `helm list -n napwake-system` shows in its `CHART` column, so the change doesn't upgrade Napwake too:

```bash
helm upgrade napwake oci://ghcr.io/napwake/charts/napwake \
  --version 0.2.0 \
  --namespace napwake-system \
  -f napwake-values.yaml
```

The operator restarts with it. Installed without a values file, write one from the release first, or change the one value without it; see [Keeping your settings](../operations/upgrading.md#keeping-your-settings). With Argo CD or Flux, change it in Git, in the Application's `valuesObject` or the HelmRelease's `values`.

## More than one install

One install per cluster is the usual setup. Several can share a cluster, such as one per team, as long as each:

- runs in a namespace of its own, with a release name of its own, since the chart names its ClusterRoles and admission policies after the release, and
- sets `watchNamespaces`, with no namespace in two installs' lists.

All but one set `crds.install: false`: they share the ManagedWorkload CRD, which belongs to the release that installed it and is upgraded with it, so make that one the newest Napwake.

Each operator stamps the ManagedWorkloads it takes on, and labels the doorman EndpointSlices it writes, with `napwake.dev/operator`, its namespace. Another install leaves a ManagedWorkload stamped by a different one alone, with a `ManagedByAnotherInstall` warning event, and doesn't move that one's EndpointSlices to its own doorman, so two installs watching the same namespace by mistake don't both pause and wake its workloads: the first to take each one on keeps it. The uninstall and upgrade guards don't count the ManagedWorkloads another install took on. The Kustomize manifests, and so `install.yaml`, are a single install, in `napwake-system`, watching every namespace.

## Verify Installation

```bash
# Check the operator and the doorman are running
kubectl get pods -n napwake-system

# Verify the CRD is installed
kubectl get crd managedworkloads.napwake.dev
```

## Uninstall

Stop managing workloads while the operator is still running, so it can scale any paused ones back up:

1. Remove the `napwake.dev/managed` label from your workloads and namespaces, in Git if that's where they live. Napwake deletes the ManagedWorkloads it created from them.
2. Delete the ManagedWorkloads you wrote yourself:

    ```bash
    kubectl delete managedworkloads --all --all-namespaces
    ```

    Deleting a ManagedWorkload scales its workload back to the replicas it had before it was paused.

3. Remove the operator:

    ```bash
    helm uninstall napwake -n napwake-system
    ```

    `helm uninstall` keeps the CRD, since deleting it deletes every ManagedWorkload. Once none is left, delete it yourself:

    ```bash
    kubectl delete crd managedworkloads.napwake.dev
    ```

    Installed from `install.yaml` or the Kustomize manifests, delete what you applied, the same way: `kubectl delete -f` with the `install.yaml` URL, `kubectl delete -k .` in your kustomization, or `make undeploy` from a clone. These delete the CRD along with the operator, with no guard like `helm uninstall`'s below, so first check `kubectl get managedworkloads -A` lists none.

`helm uninstall` refuses to go ahead while any ManagedWorkload is left. A pre-delete hook, the `<fullname>-uninstall-guard` Job, first waits up to two and a half minutes for ManagedWorkloads being deleted, so the operator can restore them and drain their doorman ports, which takes up to a minute; then, if any are left, it lists them and fails, so the uninstall stops before removing anything, with `job napwake-uninstall-guard failed` from Helm 3 and `resource Job/napwake-system/napwake-uninstall-guard not ready` from Helm 4. A ManagedWorkload deleted just before you uninstall doesn't hold it up. Above that error, Helm 3.18 and later print the hook's output, which starts with a sentence naming the ManagedWorkloads left, as they are after the wait, what to do, and a link here. Helm 4 prints it as one log message, its line breaks escaped:

```text
level=INFO msg="Napwake refuses to uninstall: these ManagedWorkloads' workloads would stay paused with no operator: staging/web. Remove the napwake.dev/managed label from them and their namespaces, and delete ManagedWorkloads you created, then uninstall again; see https://docs.napwake.dev/getting-started/installation/#uninstall\n\n..."
```

The rest, after the first `\n`, lists each one and the steps above. Helm before 3.18, such as 3.16, doesn't print a hook's output. Nor does any Helm for a release revision such a Helm wrote, as with a `helm rollback` after a refused uninstall: it drops the hook's `helm.sh/hook-output-log-policy` from the revision, until a newer Helm upgrades the release. Whenever Helm prints only the error, `kubectl logs job/napwake-uninstall-guard -n napwake-system` shows the output, with its line breaks, for an hour.

With `watchNamespaces`, the hook counts those in the watched namespaces, and any elsewhere that the operator took on, with its `napwake.dev/cleanup` finalizer. It never counts those [another install](#more-than-one-install) took on. The hook runs the operator's own image. Pass `--no-hooks` to skip it, or set `guard.uninstall: false` to drop it.

A refused uninstall leaves the release marked `uninstalling`, which `helm list` shows only with `-a`, though everything still runs. Uninstalling again, once the ManagedWorkloads are gone, finishes it. Until then `helm upgrade` fails with `has no deployed releases`; `helm rollback napwake <revision>`, with the last revision `helm history napwake` lists, marks it deployed again. Argo CD 3.3 and later runs the guard as a PreDelete hook: deleting the Application while ManagedWorkloads are left is blocked, with a `DeletionError` condition, and Argo CD retries the hook until they're gone. To delete it anyway, set `guard.uninstall: false` in Git and sync first. Earlier versions of Argo CD don't run Helm's pre-delete hooks, so deleting the Application isn't guarded: stop managing the workloads before deleting it.

!!! warning
    Removing the operator or the CRD first skips step 2's restore: workloads that were paused stay at zero replicas, and their Services still send traffic to the doorman's deleted pods. Restore them by hand, as below.

### Restoring workloads after the operator is gone

If the operator was removed while ManagedWorkloads were left, with `helm uninstall --no-hooks`, `kubectl delete -f install.yaml`, or by deleting an Argo CD Application before Argo CD 3.3, nothing restores their workloads. Each ManagedWorkload also keeps its `napwake.dev/cleanup` finalizer, which only the operator removes, so deleting it, or the CRD, hangs in `Terminating`. Reinstalling the operator, then following the steps above, is the simplest fix. Without it:

1. Scale each paused workload back to the replicas it had, which its ManagedWorkload records:

    ```bash
    kubectl get managedworkloads -A -o jsonpath='{range .items[?(@.status.pause)]}{.metadata.namespace} {.spec.target.kind} {.spec.target.name} {.status.pause.previousReplicas}{"\n"}{end}' |
      while read -r ns kind name replicas; do
        kubectl scale "$kind/$name" -n "$ns" --replicas="$replicas"
      done
    ```

    A workload scaled by KEDA was held at zero through its ScaledObject, named in the ManagedWorkload's `status.pause.scaledObject`. Remove that ScaledObject's `autoscaling.keda.sh/paused-replicas` annotation, or set it back to `status.pause.scaledObjectPausedReplicas` if that's set.

    Each paused workload also carries the same record in its `napwake.dev/paused-from` annotation, which you can read when its ManagedWorkload is gone. Remove the annotation once the workload is restored:

    ```bash
    kubectl annotate deployments,statefulsets --all -A napwake.dev/paused-from-
    ```

2. Delete the EndpointSlices that route paused workloads' Services to the doorman, so the Services use their own pods again:

    ```bash
    kubectl delete endpointslices -A -l endpointslice.kubernetes.io/managed-by=doorman.napwake.dev
    ```

3. Remove the finalizers, so the ManagedWorkloads, and the CRD, can be deleted:

    ```bash
    kubectl get managedworkloads -A -o jsonpath='{range .items[*]}{.metadata.namespace} {.metadata.name}{"\n"}{end}' |
      while read -r ns name; do
        kubectl patch managedworkload "$name" -n "$ns" --type=merge -p '{"metadata":{"finalizers":null}}'
      done
    kubectl delete managedworkloads --all --all-namespaces
    kubectl delete crd managedworkloads.napwake.dev
    ```

## Next Steps

Follow the [Quickstart](quickstart.md) to manage your first workload.
