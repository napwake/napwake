# Upgrading

## General Upgrade Process

1. **Read the release notes** in the [changelog](https://github.com/napwake/napwake/blob/main/CHANGELOG.md) for the target version. Check for breaking changes, CRD schema changes, or required steps.

2. **Upgrade.** The CRD is upgraded with the operator:

    === "Helm"

        With the values file you install with:

        ```bash
        helm upgrade napwake oci://ghcr.io/napwake/charts/napwake \
          --version X.Y.Z \
          --namespace napwake-system \
          -f napwake-values.yaml
        ```

        Give it the file every time, or no values at all: given `--set` alone, Helm drops the values set before; see [Keeping your settings](#keeping-your-settings). The chart is only published as an OCI artifact: there's no `helm repo` to update. Its version has no `v`. The CRD is a template in the chart, so `helm upgrade` upgrades it too, unless you installed with `crds.install: false` and manage it yourself.

    === "kubectl"

        ```bash
        kubectl apply -f https://github.com/napwake/napwake/releases/download/vX.Y.Z/install.yaml
        ```

    === "Kustomize"

        Change each Napwake base's `ref` in your kustomization to `vX.Y.Z`, then:

        ```bash
        kubectl apply -k .
        ```

    === "Argo CD or Flux"

        Change the Application's `targetRevision`, or the `OCIRepository`'s `tag`, to `X.Y.Z` in Git, and let it sync. To hand an install made with `helm install` to Argo CD or Flux, see [Moving a Helm install to Argo CD or Flux](../getting-started/installation.md#moving-a-helm-install-to-argo-cd-or-flux).

3. **Verify the upgrade:**

    ```bash
    kubectl get pods -n napwake-system
    kubectl logs -n napwake-system -l control-plane=controller-manager --tail=20
    ```

4. **Check workloads:** Verify ManagedWorkloads are reconciling correctly:

    ```bash
    kubectl get managedworkloads --all-namespaces
    ```

## Keeping your settings

Helm reuses a release's values only on an upgrade given none. Given any, with `--set` or `-f`, it starts again from the chart's defaults and adds only those. On a release installed with `timezone` and `prometheus.url`, an upgrade with `--set timezone=America/New_York` sets the time zone and drops `prometheus.url`, and the next upgrade with `--set` drops whatever that one didn't name. Helm 3 and Helm 4 do the same.

So keep every value you set in one file, give it to `helm install` and every `helm upgrade`, and change a setting by changing the file. The file is the whole of your settings, in one place you can review, the same in every Helm, and it's what you'd commit to Git: Argo CD and Flux take the same values, so [moving to one](../getting-started/installation.md#moving-a-helm-install-to-argo-cd-or-flux) is a copy. Installed with `--set`, write the values the release has now to a file first:

```bash
helm get values napwake -n napwake-system -o yaml > napwake-values.yaml
```

A release installed with no values writes `null`; replace it with your settings.

To change a setting, such as the [time zone](../getting-started/installation.md#time-zone), change it in the file and upgrade with it, at the version you run, which `helm list -n napwake-system` shows in its `CHART` column, so the change doesn't upgrade Napwake too:

```bash
helm upgrade napwake oci://ghcr.io/napwake/charts/napwake \
  --version X.Y.Z \
  --namespace napwake-system \
  -f napwake-values.yaml
```

Without a file, `--reuse-values` keeps the release's values and adds yours:

```bash
helm upgrade napwake oci://ghcr.io/napwake/charts/napwake \
  --version X.Y.Z \
  --namespace napwake-system \
  --reuse-values \
  --set timezone=America/New_York
```

Upgrading to a newer version with it, Helm renders with the installed version's defaults rather than the new one's, and those lack every value added since. From 0.2.0 on, the chart fills those in from its own defaults, so upgrading with `--reuse-values` from any 0.2.0 release candidate works, in Helm 3 and Helm 4 alike. From 0.1.x it doesn't: 0.1.x's values have settings 0.2.0 no longer takes, such as `grafana`, so install 0.2.0 new, as [0.2.0's changelog](https://github.com/napwake/napwake/blob/main/CHANGELOG.md) says. What `--reuse-values` still keeps is the installed version's default for each value both versions have, so a default the new version changes isn't picked up. Helm keeps those older defaults with the release, too, and goes on reusing them, which has one more catch: after such an upgrade, a default you remove by setting it to `null` doesn't stay removed. Helm 4 renders the default for the upgrade that sets it to `null`, and removes it from the next; Helm 3 brings it back at every other `--reuse-values` upgrade. To remove a default after such an upgrade, upgrade once with the values file, or with `--reset-then-reuse-values` (Helm 3.14 and later), which renders with the new version's defaults and keeps your values over them. From then on, `--reuse-values` keeps a removed default removed.

## Narrowing watchNamespaces

Upgrading to a `watchNamespaces` that leaves out a namespace, or setting it where it was empty, removes the operator's and the doorman's Roles in the namespaces left out, and stops them watching there. Workloads paused in those namespaces would stay at zero replicas, their Services still routed to doorman ports nothing serves, and their ManagedWorkloads' finalizers would never run.

So the chart refuses it. A pre-upgrade hook, the `<fullname>-upgrade-guard` Job, lists the ManagedWorkloads the running operator took on, with its `napwake.dev/cleanup` finalizer, in namespaces the new `watchNamespaces` leaves out, and fails if there are any, before anything changes: `helm upgrade` stops with `job napwake-upgrade-guard failed`, or with Helm 4 `resource Job/napwake-system/napwake-upgrade-guard not ready`, and Helm 3.18 and later print them in a message whose first sentence, `Napwake refuses this upgrade: ...`, names them and what to do, followed by the steps below; `kubectl logs job/napwake-upgrade-guard -n napwake-system` shows the same. It waits two and a half minutes for any being deleted first. `helm rollback` to a revision with a narrower `watchNamespaces` is guarded the same way.

To narrow it, stop managing the workloads in the namespaces you're dropping first, while the operator still watches them, so it scales each one back up:

1. Remove the `napwake.dev/managed` label from the workloads and namespaces there.
2. Delete the ManagedWorkloads you wrote yourself there:

    ```bash
    kubectl delete managedworkloads --all -n preview-1
    ```

3. Upgrade.

The hook reads ManagedWorkloads' metadata cluster-wide, through a ClusterRole that, like its ServiceAccount, exists only while it runs. It runs only when `watchNamespaces` is set: widening to every namespace strands nothing. Argo CD runs it as a PreSync hook, before every sync. `helm upgrade --no-hooks` skips it, and `guard.upgrade: false` drops it; then restore the workloads left behind by hand, as in [Restoring workloads after the operator is gone](../getting-started/installation.md#restoring-workloads-after-the-operator-is-gone), for those namespaces.

A refused upgrade is recorded as a failed revision, and the release keeps running the previous one. `helm uninstall` is still guarded after it, though it runs with the failed revision's values: it also counts ManagedWorkloads the operator took on outside them.

## CRD Compatibility

Napwake follows these CRD versioning rules:

- **v1alpha1**: Breaking changes may occur between minor versions. Always read release notes.
- Field additions are non-breaking (new optional fields with defaults).
- Field removals or type changes are breaking and will be called out in release notes.

The CRD is kept when the chart is uninstalled, so uninstalling and reinstalling never deletes your ManagedWorkloads.

## Forecast Engine State

The forecast engine state is serialized in each ManagedWorkload's status, in `status.prediction.state`. On upgrade:

- Compatible state versions are imported automatically
- State an older version saved is discarded, with a `ForecastReset` event, and the engine learns again: from the workload's [CPU history](../concepts/forecasting.md#learning-from-history), read again, when `prometheus.url` is set, otherwise from scratch
- State that can't be read otherwise, such as after a hand edit, is discarded the same way, with a `ForecastReset` warning

### The forecast setting, from before 0.2.0

Before 0.2.0, `spec.prediction.confidence` defaulted to `75`, so every ManagedWorkload created then has it stored, and a confidence overrides the [forecast setting](../concepts/forecasting.md#forecast-setting). On one created from the label, Napwake drops a stored `75`, balanced's own threshold, so `napwake.dev/forecast` and `defaults.forecast` apply. On one you wrote, remove it for `spec.prediction.mode` to apply:

```bash
kubectl patch managedworkload my-api -n staging --type=json \
  -p '[{"op":"remove","path":"/spec/prediction/confidence"}]'
```

## Wake on request, from before 0.2.0

Before 0.2.0, `spec.wake.onRequest: false`, or `napwake.dev/wake-on-request: "false"` with the label, turned waking on request off for one workload, so requests to it failed while it was paused. That setting is gone: once the new CRD is installed, the field is dropped from every ManagedWorkload stored with it, the annotation is ignored, and those workloads wake on request again.

A manifest that still sets `spec.wake.onRequest` is refused, though. `kubectl apply` checks fields strictly by default, and answers `strict decoding error: unknown field "spec.wake.onRequest"`; a server-side apply, as Flux's and Argo CD's with `ServerSideApply=true` are, answers `.spec.wake.onRequest: field not declared in schema`, so a GitOps sync of it fails too. So take `wake.onRequest` out of the ManagedWorkloads in your manifests, in Git, as you upgrade. Where it was `true`, just remove it: waking on request is the default, `spec.wake.mode: Request`. Where it was `false`, remove it, and put in its place what fits below, usually `mode: Ask`:

```yaml
spec:
  wake:
    mode: Ask   # in place of onRequest: false
```

Instead of turning it off:

- **For a workload on a public URL,** which crawlers and scanners would keep awake, which is what turning it off was usually for, have the doorman ask before waking it: `spec.wake.mode: Ask`, or `napwake.dev/wake: ask` with the label. Only a person's click on the page the doorman shows a browser wakes it, and anything else gets a `503`. See [Asking before waking](../concepts/wake-on-request.md#asking-before-waking).
- **For one port of a Service,** list it in `napwake.dev/doorman-ignore-ports` on the Service. That port fails while the workload is paused, as it did with waking turned off.
- **For the whole cluster,** set the Helm value `doorman.enabled: false`, as before.

To find the ManagedWorkloads that had it, before you upgrade:

```bash
kubectl get managedworkloads -A -o json | jq -r '.items[] | select(.spec.wake.onRequest == false) | "\(.metadata.namespace)/\(.metadata.name)"'
```

and the workloads and namespaces that set the annotation:

```bash
kubectl get deployments,statefulsets,namespaces -A -o json | jq -r '.items[] | select(.metadata.annotations["napwake.dev/wake-on-request"] == "false") | "\(.kind) \(.metadata.namespace // "")/\(.metadata.name)"'
```

The `WakeOnRequest` reason `TurnedOff` is gone with it.

## Rollback

If something goes wrong:

=== "Helm"

    ```bash
    helm rollback napwake -n napwake-system
    ```

=== "kubectl"

    ```bash
    kubectl apply -f https://github.com/napwake/napwake/releases/download/vPREVIOUS/install.yaml
    ```

=== "Kustomize, Argo CD or Flux"

    Put the previous release's `ref`, `targetRevision` or `tag` back, and apply or sync it.

Paused workloads remain paused during rollback. The previous operator version resumes managing them. Rolling back across a breaking API change isn't supported.

## Version History

Check the [GitHub Releases](https://github.com/napwake/napwake/releases) page and the [changelog](https://github.com/napwake/napwake/blob/main/CHANGELOG.md) for every change.
