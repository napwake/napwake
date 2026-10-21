# Installation

## Prerequisites

- Kubernetes v1.30+ (tested in CI on 1.30 and 1.37)
- kubectl v1.30+
- [metrics-server](https://github.com/kubernetes-sigs/metrics-server) installed (Hybernate reads pod CPU via the Kubernetes Metrics API)
- Helm v3.8+ (if using Helm install), for OCI charts

## Install

Hybernate is packaged two ways: a Helm chart, and the Kustomize manifests, the repository's `config/default`, which `install.yaml` is built from. Both install the same operator and doorman. Use Helm unless your cluster's config is kept as Kustomize.

=== "Helm"

    Install directly from the OCI registry; there's no `helm repo` to add:

    ```bash
    helm install hybernate oci://ghcr.io/okedeji/charts/hybernate \
      --version 0.2.0 \
      --namespace hybernate-system \
      --create-namespace \
      --set timezone=Europe/London
    ```

    Set `timezone` to your users' [time zone](#time-zone), such as `America/New_York`. The chart's version has no `v`; the image it runs is tagged with one, such as `v0.2.0`. The chart installs the ManagedWorkload CRD too, and upgrades it with each `helm upgrade`.

    ??? tip "Helm Values"

        | Value | Default | Description |
        |-------|---------|-------------|
        | `replicaCount` | `1` | Number of operator replicas |
        | `image.repository` | `ghcr.io/okedeji/hybernate` | Container image |
        | `image.tag` | the chart's `appVersion` | Image tag |
        | `leaderElection.enabled` | `true` | Enable HA leader election |
        | `metrics.secure` | `true` | Serve metrics over HTTPS |
        | `metrics.readerSubjects` | `[]` | Who may scrape the metrics, such as your Prometheus's ServiceAccount |
        | `timezone` | `UTC` | Time zone the forecast learns hours and weekdays in |
        | `watchNamespaces` | `[]` | Namespaces to work in; empty means all |
        | `defaults.idleAfter` | `1h` | Idle time before pausing, for opted-in workloads that don't set their own |
        | `defaults.cpuThreshold` | `10` | CPU percentage of requests that counts as active |
        | `defaults.dryRun` | `false` | Measure every opted-in workload without pausing, unless it sets its own |
        | `defaults.forecast` | `balanced` | How soon the forecast acts: `eager`, `balanced` or `cautious` |
        | `resources.limits.cpu` | `500m` | CPU limit |
        | `resources.limits.memory` | `512Mi` | Memory limit |

        Every value is in the [Helm Values Reference](../reference/helm-values.md).

=== "kubectl"

    Apply `install.yaml`, the Kustomize manifests built into one file:

    ```bash
    kubectl apply -f https://github.com/okedeji/hybernate/releases/download/v0.2.0/install.yaml
    ```

    This installs the CRD, RBAC, the operator Deployment, and the doorman Deployment, Service and PodDisruptionBudget into the `hybernate-system` namespace. The operator is `deployment/hybernate-controller-manager`.

    Then set your users' [time zone](#time-zone), such as `America/New_York`, on the operator:

    ```bash
    kubectl patch deployment hybernate-controller-manager -n hybernate-system --type=json \
      -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--timezone=Europe/London"}]'
    ```

    An `install.yaml` that changes the operator's arguments replaces them, so after upgrading, check `--timezone` is still there, or install with Kustomize, which keeps it in your kustomization. For Prometheus and NetworkPolicies, see [below](#prometheus-and-networkpolicies-without-helm).

=== "Kustomize"

    Use the Kustomize manifests as a remote base, pinned to the release, in a kustomization of your own, here with your users' [time zone](#time-zone) set on the operator:

    ```yaml title="kustomization.yaml"
    apiVersion: kustomize.config.k8s.io/v1beta1
    kind: Kustomization
    resources:
      - https://github.com/okedeji/hybernate//config/default?ref=v0.2.0
    patches:
      - target:
          kind: Deployment
          name: hybernate-controller-manager
        patch: |-
          - op: add
            path: /spec/template/spec/containers/0/args/-
            value: --timezone=Europe/London
    ```

    ```bash
    kubectl apply -k .
    ```

    It installs what `install.yaml` does, into `hybernate-system`, and the operator's other [flags](../reference/configuration.md) are set the same way. To upgrade, change `ref` to the new release. Kustomize fetches each base with git, so whatever runs it, `kubectl` or a GitOps tool, needs to reach github.com. For Prometheus and NetworkPolicies, see [below](#prometheus-and-networkpolicies-without-helm).

=== "Source"

    Clone the repo and build the image:

    ```bash
    git clone https://github.com/okedeji/hybernate.git
    cd hybernate
    make docker-build IMG=hybernate:dev
    ```

    The image is only in your local Docker, so make it available to the cluster's nodes before deploying. For a [Kind](https://kind.sigs.k8s.io/) cluster, load it into the nodes:

    ```bash
    kind load docker-image hybernate:dev --name <cluster>
    make deploy IMG=hybernate:dev
    ```

    For any other cluster, push it to a registry the nodes can pull from:

    ```bash
    make docker-build docker-push IMG=<registry>/hybernate:dev
    make deploy IMG=<registry>/hybernate:dev
    ```

    `make deploy` installs the Kustomize manifests, `config/default`, with the operator and the doorman running `IMG`. It writes `IMG` into `config/manager/kustomization.yaml`; `git checkout config/manager` puts it back. From the clone, `kubectl apply -k config/prometheus` and `kubectl apply -k config/network-policy` add the bases [below](#prometheus-and-networkpolicies-without-helm).

### Prometheus and NetworkPolicies without Helm

With `install.yaml` or the Kustomize manifests, two more bases, pinned to the same release, add what these chart values turn on in a Helm install. Add them to your kustomization's `resources`, or apply one on its own, such as `kubectl apply -k "https://github.com/okedeji/hybernate//config/prometheus?ref=v0.2.0"`.

| Base | Adds | Chart values |
|------|------|--------------|
| `https://github.com/okedeji/hybernate//config/prometheus?ref=v0.2.0` | ServiceMonitors for the operator and the doorman, and the [alerting rules](../operations/monitoring.md#alerting-rules). Needs the Prometheus Operator's CRDs, and your Prometheus [allowed to read the metrics](../operations/monitoring.md#scraping) | `metrics.serviceMonitor.enabled`, `metrics.prometheusRule.enabled` |
| `https://github.com/okedeji/hybernate//config/network-policy?ref=v0.2.0` | NetworkPolicies: only namespaces labelled `metrics: enabled` may scrape the operator and the doorman, and the doorman's routing ports (TCP 20000-29999) stay open to every caller of a paused workload's Services | `networkPolicy.enabled` |

### With Argo CD or Flux

The chart is published only to an OCI registry, which Argo CD and Flux each name their own way. Commit one of these to the repository your GitOps tool syncs from, with your users' [time zone](#time-zone):

=== "Argo CD"

    ```yaml title="application.yaml"
    apiVersion: argoproj.io/v1alpha1
    kind: Application
    metadata:
      name: hybernate
      namespace: argocd
    spec:
      project: default
      source:
        repoURL: ghcr.io/okedeji/charts
        chart: hybernate
        targetRevision: 0.2.0
        helm:
          valuesObject:
            timezone: Europe/London
      destination:
        server: https://kubernetes.default.svc
        namespace: hybernate-system
      syncPolicy:
        automated: {}
        syncOptions:
          - CreateNamespace=true
    ```

    `repoURL` is the registry and path without `oci://`, which is how Argo CD knows the chart is in an OCI registry, and `chart` is the chart's name. The registry is public, so Argo CD needs no credentials for it. Argo CD 3.3 and later runs the chart's uninstall guard when the Application is deleted; see [Uninstall](#uninstall). To install the Kustomize manifests instead, point an Application's `path` at your kustomization.

=== "Flux"

    ```yaml title="hybernate.yaml"
    apiVersion: source.toolkit.fluxcd.io/v1
    kind: OCIRepository
    metadata:
      name: hybernate
      namespace: flux-system
    spec:
      interval: 1h
      url: oci://ghcr.io/okedeji/charts/hybernate
      ref:
        tag: 0.2.0
      layerSelector:
        mediaType: application/vnd.cncf.helm.chart.content.v1.tar+gzip
        operation: copy
    ---
    apiVersion: helm.toolkit.fluxcd.io/v2
    kind: HelmRelease
    metadata:
      name: hybernate
      namespace: flux-system
    spec:
      interval: 1h
      chartRef:
        kind: OCIRepository
        name: hybernate
      releaseName: hybernate
      targetNamespace: hybernate-system
      storageNamespace: hybernate-system
      install:
        createNamespace: true
      values:
        timezone: Europe/London
    ```

    This takes Flux 2.6 or later, where `OCIRepository` is `v1`. `releaseName` keeps the release `hybernate`, as these docs call it, rather than Flux's default of `hybernate-system-hybernate`, and `storageNamespace` keeps its Helm history beside it, where `helm list -n hybernate-system` shows it. To install the Kustomize manifests instead, point a Flux `Kustomization` at your kustomization.

To upgrade, change the version, `targetRevision` or `tag`, to the new release's.

## Time zone

The [forecast](../concepts/forecasting.md) learns when each workload is busy by hour of day and day of week, counted in the operator's time zone, holds off pausing ahead of those hours and, with `autoResume`, wakes workloads before them. Set it to your users' IANA time zone, such as `Europe/London`, `America/New_York` or `Asia/Kolkata`: Helm's `timezone` value, or the operator's `--timezone` flag. Left at the default, UTC, a team whose clocks change for daylight saving finds its workloads woken an hour early or late for half the year. For teams in different zones, pick the zone most of them work in, or give each team an install of its own, as below. The chart's install notes say which zone the forecast uses, and [Wall-Clock Alignment](../concepts/forecasting.md#wall-clock-alignment) has the details.

## More than one install

One install per cluster is the usual setup. Several can share a cluster, such as one per team, as long as each:

- runs in a namespace of its own, with a release name of its own, since the chart names its ClusterRoles and admission policies after the release, and
- sets `watchNamespaces`, with no namespace in two installs' lists.

All but one set `crds.install: false`: they share the ManagedWorkload CRD, which belongs to the release that installed it and is upgraded with it, so make that one the newest Hybernate.

Each operator stamps the ManagedWorkloads it takes on, and labels the doorman EndpointSlices it writes, with `hybernate.io/operator`, its namespace. Another install leaves a ManagedWorkload stamped by a different one alone, with a `ManagedByAnotherInstall` warning event, and doesn't move that one's EndpointSlices to its own doorman, so two installs watching the same namespace by mistake don't both pause and wake its workloads: the first to take each one on keeps it. The uninstall and upgrade guards don't count the ManagedWorkloads another install took on. The Kustomize manifests, and so `install.yaml`, are a single install, in `hybernate-system`, watching every namespace.

## Verify Installation

```bash
# Check the operator and the doorman are running
kubectl get pods -n hybernate-system

# Verify the CRD is installed
kubectl get crd managedworkloads.hybernate.io
```

## Uninstall

Stop managing workloads while the operator is still running, so it can scale any paused ones back up:

1. Remove the `hybernate.io/managed` label from your workloads and namespaces, in Git if that's where they live. Hybernate deletes the ManagedWorkloads it created from them.
2. Delete the ManagedWorkloads you wrote yourself:

    ```bash
    kubectl delete managedworkloads --all --all-namespaces
    ```

    Deleting a ManagedWorkload scales its workload back to the replicas it had before it was paused.

3. Remove the operator:

    ```bash
    helm uninstall hybernate -n hybernate-system
    ```

    `helm uninstall` keeps the CRD, since deleting it deletes every ManagedWorkload. Once none is left, delete it yourself:

    ```bash
    kubectl delete crd managedworkloads.hybernate.io
    ```

    Installed from `install.yaml` or the Kustomize manifests, delete what you applied, the same way: `kubectl delete -f` with the `install.yaml` URL, `kubectl delete -k .` in your kustomization, or `make undeploy` from a clone. These delete the CRD along with the operator, with no guard like `helm uninstall`'s below, so first check `kubectl get managedworkloads -A` lists none.

`helm uninstall` refuses to go ahead while any ManagedWorkload is left. A pre-delete hook, the `<fullname>-uninstall-guard` Job, first waits up to two and a half minutes for ManagedWorkloads being deleted, so the operator can restore them and drain their doorman ports, which takes up to a minute; then, if any are left, it lists them and fails, so the uninstall stops before removing anything, with `job hybernate-uninstall-guard failed` from Helm 3 and `resource Job/hybernate-system/hybernate-uninstall-guard not ready` from Helm 4. A ManagedWorkload deleted just before you uninstall doesn't hold it up. Above that error, Helm 3.18 and later print the hook's output, which starts with a sentence naming the ManagedWorkloads left, as they are after the wait, what to do, and a link here. Helm 4 prints it as one log message, its line breaks escaped:

```text
level=INFO msg="Hybernate refuses to uninstall: these ManagedWorkloads' workloads would stay paused with no operator: staging/web. Remove the hybernate.io/managed label from them and their namespaces, and delete ManagedWorkloads you created, then uninstall again; see https://okedeji.io/hybernate/getting-started/installation/#uninstall\n\n..."
```

The rest, after the first `\n`, lists each one and the steps above. Helm before 3.18, such as 3.16, doesn't print a hook's output. Nor does any Helm for a release revision such a Helm wrote, as with a `helm rollback` after a refused uninstall: it drops the hook's `helm.sh/hook-output-log-policy` from the revision, until a newer Helm upgrades the release. Whenever Helm prints only the error, `kubectl logs job/hybernate-uninstall-guard -n hybernate-system` shows the output, with its line breaks, for an hour.

With `watchNamespaces`, the hook counts those in the watched namespaces, and any elsewhere that the operator took on, with its `hybernate.io/cleanup` finalizer. It never counts those [another install](#more-than-one-install) took on. The hook runs the operator's own image. Pass `--no-hooks` to skip it, or set `guard.uninstall: false` to drop it.

A refused uninstall leaves the release marked `uninstalling`, which `helm list` shows only with `-a`, though everything still runs. Uninstalling again, once the ManagedWorkloads are gone, finishes it. Until then `helm upgrade` fails with `has no deployed releases`; `helm rollback hybernate <revision>`, with the last revision `helm history hybernate` lists, marks it deployed again. Argo CD 3.3 and later runs the guard as a PreDelete hook: deleting the Application while ManagedWorkloads are left is blocked, with a `DeletionError` condition, and Argo CD retries the hook until they're gone. To delete it anyway, set `guard.uninstall: false` in Git and sync first. Earlier versions of Argo CD don't run Helm's pre-delete hooks, so deleting the Application isn't guarded: stop managing the workloads before deleting it.

!!! warning
    Removing the operator or the CRD first skips step 2's restore: workloads that were paused stay at zero replicas, and their Services still send traffic to the doorman's deleted pods. Restore them by hand, as below.

### Restoring workloads after the operator is gone

If the operator was removed while ManagedWorkloads were left, with `helm uninstall --no-hooks`, `kubectl delete -f install.yaml`, or by deleting an Argo CD Application before Argo CD 3.3, nothing restores their workloads. Each ManagedWorkload also keeps its `hybernate.io/cleanup` finalizer, which only the operator removes, so deleting it, or the CRD, hangs in `Terminating`. Reinstalling the operator, then following the steps above, is the simplest fix. Without it:

1. Scale each paused workload back to the replicas it had, which its ManagedWorkload records:

    ```bash
    kubectl get managedworkloads -A -o jsonpath='{range .items[?(@.status.pause)]}{.metadata.namespace} {.spec.target.kind} {.spec.target.name} {.status.pause.previousReplicas}{"\n"}{end}' |
      while read -r ns kind name replicas; do
        kubectl scale "$kind/$name" -n "$ns" --replicas="$replicas"
      done
    ```

    A workload scaled by KEDA was held at zero through its ScaledObject, named in the ManagedWorkload's `status.pause.scaledObject`. Remove that ScaledObject's `autoscaling.keda.sh/paused-replicas` annotation, or set it back to `status.pause.scaledObjectPausedReplicas` if that's set.

    Each paused workload also carries the same record in its `hybernate.io/paused-from` annotation, which you can read when its ManagedWorkload is gone. Remove the annotation once the workload is restored:

    ```bash
    kubectl annotate deployments,statefulsets --all -A hybernate.io/paused-from-
    ```

2. Delete the EndpointSlices that route paused workloads' Services to the doorman, so the Services use their own pods again:

    ```bash
    kubectl delete endpointslices -A -l endpointslice.kubernetes.io/managed-by=doorman.hybernate.io
    ```

3. Remove the finalizers, so the ManagedWorkloads, and the CRD, can be deleted:

    ```bash
    kubectl get managedworkloads -A -o jsonpath='{range .items[*]}{.metadata.namespace} {.metadata.name}{"\n"}{end}' |
      while read -r ns name; do
        kubectl patch managedworkload "$name" -n "$ns" --type=merge -p '{"metadata":{"finalizers":null}}'
      done
    kubectl delete managedworkloads --all --all-namespaces
    kubectl delete crd managedworkloads.hybernate.io
    ```

## Next Steps

Follow the [Quickstart](quickstart.md) to manage your first workload.
