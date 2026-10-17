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

3. **Verify the upgrade:**

    ```bash
    kubectl get pods -n hybernate-system
    kubectl logs -n hybernate-system -l control-plane=controller-manager --tail=20
    ```

4. **Check workloads:** Verify ManagedWorkloads are reconciling correctly:

    ```bash
    kubectl get managedworkloads --all-namespaces
    ```

## CRD Compatibility

Hybernate follows these CRD versioning rules:

- **v1alpha1**: Breaking changes may occur between minor versions. Always read release notes.
- Field additions are non-breaking (new optional fields with defaults).
- Field removals or type changes are breaking and will be called out in release notes.

The CRD is kept when the chart is uninstalled, so uninstalling and reinstalling never deletes your ManagedWorkloads.

### Values a newer CRD rejects

A newer CRD can reject values an older one stored. `idleAfter` and `wake.maxWait` are now checked as Go parses durations, and `maxWait` is at most `1h`. If you wrote one that isn't a Go duration, such as `1d` or `2w`, the operator and the doorman can't read any ManagedWorkload (`time: unknown unit "d" in duration "1d"`), so they can't sync and don't start, and every write to that ManagedWorkload, its status too, is rejected until it's fixed. A valid duration that's only out of range, such as a `maxWait` of `2h`, keeps working: CRD validation ratcheting, on by default from Kubernetes 1.30, lets writes through while the value is unchanged, and the doorman holds connections for `1h` at most anyway. Fix it the next time you edit it.

To find them, list every value:

```bash
kubectl get managedworkloads -A -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}{"\t"}{.spec.idlePolicy.idleAfter}{"\t"}{.spec.wake.maxWait}{"\n"}{end}'
```

And fix each one with a value in hours, minutes or seconds:

```bash
kubectl patch managedworkload my-api -n staging --type merge -p '{"spec":{"idlePolicy":{"idleAfter":"24h"}}}'
```

## Forecast Engine State

The forecast engine state is serialized in each ManagedWorkload's status, in `status.prediction.state`. On upgrade:

- Compatible state versions are imported automatically
- State that can't be read, from an incompatible version or a hand edit, is discarded, and the engine re-learns from scratch, with a `ForecastReset` warning event

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

Paused workloads remain paused during rollback. The previous operator version resumes managing them. Rolling back across a breaking API change isn't supported.

## Version History

Check the [GitHub Releases](https://github.com/okedeji/hybernate/releases) page and the [changelog](https://github.com/okedeji/hybernate/blob/main/CHANGELOG.md) for every change.
