# Upgrading

## General Upgrade Process

1. **Read the release notes** in the [changelog](https://github.com/okedeji/hybernate/blob/main/CHANGELOG.md) for the target version. Check for breaking changes, CRD schema changes, or required steps.

2. **Upgrade.** The CRD is upgraded with the operator:

    === "Helm"

        ```bash
        helm upgrade hybernate oci://ghcr.io/okedeji/charts/hybernate \
          --version X.Y.Z \
          --namespace hybernate-system
        ```

        The chart is only published as an OCI artifact: there's no `helm repo` to update. Its version has no `v`. The CRD is a template in the chart, so `helm upgrade` upgrades it too, unless you installed with `crds.install: false` and manage it yourself.

    === "kubectl"

        ```bash
        kubectl apply -f https://github.com/okedeji/hybernate/releases/download/vX.Y.Z/install.yaml
        ```

    === "Kustomize"

        Change each Hybernate base's `ref` in your kustomization to `vX.Y.Z`, then:

        ```bash
        kubectl apply -k .
        ```

    === "Argo CD or Flux"

        Change the Application's `targetRevision`, or the `OCIRepository`'s `tag`, to `X.Y.Z` in Git, and let it sync.

3. **Verify the upgrade:**

    ```bash
    kubectl get pods -n hybernate-system
    kubectl logs -n hybernate-system -l control-plane=controller-manager --tail=20
    ```

4. **Check workloads:** Verify ManagedWorkloads are reconciling correctly:

    ```bash
    kubectl get managedworkloads --all-namespaces
    ```

## Narrowing watchNamespaces

Upgrading to a `watchNamespaces` that leaves out a namespace, or setting it where it was empty, removes the operator's and the doorman's Roles in the namespaces left out, and stops them watching there. Workloads paused in those namespaces would stay at zero replicas, their Services still routed to doorman ports nothing serves, and their ManagedWorkloads' finalizers would never run.

So the chart refuses it. A pre-upgrade hook, the `<fullname>-upgrade-guard` Job, lists the ManagedWorkloads the running operator took on, with its `hybernate.io/cleanup` finalizer, in namespaces the new `watchNamespaces` leaves out, and fails if there are any, before anything changes: `helm upgrade` stops with `job hybernate-upgrade-guard failed`, or with Helm 4 `resource Job/hybernate-system/hybernate-upgrade-guard not ready`, and Helm 3.18 and later print them in a message whose first sentence, `Hybernate refuses this upgrade: ...`, names them and what to do, followed by the steps below; `kubectl logs job/hybernate-upgrade-guard -n hybernate-system` shows the same. It waits two and a half minutes for any being deleted first. `helm rollback` to a revision with a narrower `watchNamespaces` is guarded the same way.

To narrow it, stop managing the workloads in the namespaces you're dropping first, while the operator still watches them, so it scales each one back up:

1. Remove the `hybernate.io/managed` label from the workloads and namespaces there.
2. Delete the ManagedWorkloads you wrote yourself there:

    ```bash
    kubectl delete managedworkloads --all -n preview-1
    ```

3. Upgrade.

The hook reads ManagedWorkloads' metadata cluster-wide, through a ClusterRole that, like its ServiceAccount, exists only while it runs. It runs only when `watchNamespaces` is set: widening to every namespace strands nothing. Argo CD runs it as a PreSync hook, before every sync. `helm upgrade --no-hooks` skips it, and `guard.upgrade: false` drops it; then restore the workloads left behind by hand, as in [Restoring workloads after the operator is gone](../getting-started/installation.md#restoring-workloads-after-the-operator-is-gone), for those namespaces.

A refused upgrade is recorded as a failed revision, and the release keeps running the previous one. `helm uninstall` is still guarded after it, though it runs with the failed revision's values: it also counts ManagedWorkloads the operator took on outside them.

## CRD Compatibility

Hybernate follows these CRD versioning rules:

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

Before 0.2.0, `spec.prediction.confidence` defaulted to `75`, so every ManagedWorkload created then has it stored, and a confidence overrides the [forecast setting](../concepts/forecasting.md#forecast-setting). On one created from the label, Hybernate drops a stored `75`, balanced's own threshold, so `hybernate.io/forecast` and `defaults.forecast` apply. On one you wrote, remove it for `spec.prediction.mode` to apply:

```bash
kubectl patch managedworkload my-api -n staging --type=json \
  -p '[{"op":"remove","path":"/spec/prediction/confidence"}]'
```

## Rollback

If something goes wrong:

=== "Helm"

    ```bash
    helm rollback hybernate -n hybernate-system
    ```

=== "kubectl"

    ```bash
    kubectl apply -f https://github.com/okedeji/hybernate/releases/download/vPREVIOUS/install.yaml
    ```

=== "Kustomize, Argo CD or Flux"

    Put the previous release's `ref`, `targetRevision` or `tag` back, and apply or sync it.

Paused workloads remain paused during rollback. The previous operator version resumes managing them. Rolling back across a breaking API change isn't supported.

## Version History

Check the [GitHub Releases](https://github.com/okedeji/hybernate/releases) page and the [changelog](https://github.com/okedeji/hybernate/blob/main/CHANGELOG.md) for every change.
