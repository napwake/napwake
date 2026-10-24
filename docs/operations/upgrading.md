# Upgrading

Napwake upgrades in place from one Napwake release to the next. On 0.1.x or a 0.2.0 release candidate, [move to Napwake](#moving-to-napwake) instead.

## Moving from Hybernate { #moving-to-napwake }

Napwake is Hybernate's new name, from 0.2.0. Hybernate 0.1.x and the 0.2.0 release candidates can't upgrade to it. Every name in the cluster changes:

| Hybernate | Napwake |
|-----------|---------|
| `hybernate.io/...` labels, annotations and finalizer | `napwake.dev/...` |
| `apiVersion: hybernate.io/v1alpha1`, CRD `managedworkloads.hybernate.io` | `apiVersion: napwake.dev/v1alpha1`, CRD `managedworkloads.napwake.dev` |
| Chart `oci://ghcr.io/okedeji/charts/hybernate`, image `ghcr.io/okedeji/hybernate` | Chart `oci://ghcr.io/napwake/charts/napwake`, image `ghcr.io/napwake/napwake` |
| Namespace `hybernate-system`, release `hybernate` | Namespace `napwake-system`, release `napwake` |
| `kubectl hybernate` | `kubectl napwake` |
| Metrics `hybernate_*`, alerts `Hybernate*` | Metrics `napwake_*`, alerts `Napwake*` |

So you hand your workloads back, uninstall Hybernate, rename your labels and install Napwake. Hybernate scales each paused workload back up as it lets it go. None is left at zero with no operator.

### 1. Hand your workloads back

Do this while Hybernate still runs.

=== "0.2.0 release candidate"

    Move the opt-in label to its new name. Hybernate sees its label go and deletes the ManagedWorkload it made, which scales a paused workload back to the replicas it had. Napwake isn't installed yet, so nothing acts on the new label:

    ```bash
    kubectl get deployments,statefulsets,namespaces -A -l hybernate.io/managed -o json | jq -r '.items[] | @sh "kubectl label \(.kind | ascii_downcase)/\(.metadata.name) -n \(.metadata.namespace // "default") napwake.dev/managed=\(.metadata.labels["hybernate.io/managed"]) hybernate.io/managed-"' | sh
    ```

    If the label is in Git, rename it there instead, and sync.

    Then save the ManagedWorkloads you wrote, if they aren't in Git, and delete them. Hybernate hands their workloads back too:

    ```bash
    kubectl get managedworkloads.hybernate.io -A -o yaml > hybernate-managedworkloads.yaml
    kubectl delete managedworkloads.hybernate.io --all -A
    ```

=== "0.1.x"

    Deleting a ManagedWorkload in 0.1.x doesn't wake its workload. So scale each paused one back to the replicas its ManagedWorkload records. Then delete the WorkloadPolicies, which would make ManagedWorkloads again, and the ManagedWorkloads:

    ```bash
    kubectl get managedworkloads.hybernate.io -A -o jsonpath='{range .items[?(@.status.pause)]}{.metadata.namespace} {.spec.target.kind} {.spec.target.name} {.status.pause.previousReplicas}{"\n"}{end}' |
      while read -r ns kind name replicas; do
        kubectl scale "$kind/$name" -n "$ns" --replicas="$replicas"
      done
    kubectl get managedworkloads.hybernate.io -A -o yaml > hybernate-managedworkloads.yaml
    kubectl delete workloadpolicies.hybernate.io --all -A
    kubectl delete managedworkloads.hybernate.io --all -A
    ```

### 2. Uninstall Hybernate

```bash
helm uninstall hybernate -n hybernate-system
kubectl delete crd managedworkloads.hybernate.io
kubectl delete namespace hybernate-system
```

Use your release's name and namespace if they differ. A 0.2.0 release candidate's `helm uninstall` refuses while any ManagedWorkload is left, and names them. Installed from `install.yaml` or Kustomize, delete what you applied instead, with `kubectl delete -f` or `kubectl delete -k`. 0.1.x has two more CRDs:

```bash
kubectl delete crd hybernatereports.hybernate.io workloadpolicies.hybernate.io
```

### 3. Rename the rest

This lists every object that still has a `hybernate.io` label or annotation:

```bash
kubectl api-resources --verbs=list -o name | xargs -n 1 kubectl get -A -o json | jq -r '.items[] | select([(.metadata.labels // {} | to_entries[] | .key, .value), (.metadata.annotations // {} | keys[])] | any(contains("hybernate.io"))) | "\(.kind) \(.metadata.namespace // "-")/\(.metadata.name)"'
```

Change each where it's defined, such as in Git. Every key keeps its name after the prefix, so `hybernate.io/idle-after` becomes `napwake.dev/idle-after`. Two keys don't move:

- `hybernate.io/wake-on-request` has no new name. Where it was `"false"`, set `napwake.dev/wake: ask`. See [Wake on request, from before 0.2.0](#wake-on-request-from-before-020).
- `hybernate.io/paused-from` on a workload means Hybernate never handed it back. Scale the workload to the `replicas` the annotation records, then remove it.

For keys you set by hand, this prints a `kubectl patch` for each object that moves them. Add `| sh` to the end to run them:

```bash
kubectl get deployments,statefulsets,services,namespaces -A -o json | jq -r '
  def moved: with_entries(select(.key | startswith("hybernate.io/")) | select(.key != "hybernate.io/wake-on-request" and .key != "hybernate.io/paused-from"))
    | to_entries | map({(.key): null}, {("napwake.dev/" + (.key | ltrimstr("hybernate.io/"))): .value}) | add;
  .items[] | {labels: (.metadata.labels // {} | moved), annotations: (.metadata.annotations // {} | moved)} as $m
  | select($m.labels or $m.annotations)
  | @sh "kubectl patch \(.kind | ascii_downcase)/\(.metadata.name) -n \(.metadata.namespace // "default") --type=merge -p \({metadata: ($m | with_entries(select(.value)))} | tojson)"'
```

### 4. Install Napwake

Install it as [Installation](../getting-started/installation.md) says. A values file from a 0.2.0 release candidate works as it is, except an `image.repository` of `ghcr.io/okedeji/hybernate`. Remove that. From 0.1.x, start from the chart's defaults.

Apply the ManagedWorkloads you wrote with their new names. Change `apiVersion: hybernate.io/v1alpha1` to `napwake.dev/v1alpha1`, and each `hybernate.io/` annotation:

```bash
sed 's|hybernate\.io/|napwake.dev/|g' my-api.yaml | kubectl apply -f -
```

Apply them from your own manifests. A copy saved with `kubectl get` has Hybernate's status, and may have settings that are gone, as below.

Napwake starts each workload afresh:

- Each forecast learns again. With `prometheus.url`, it first reads up to four weeks of the workload's [CPU history](../concepts/forecasting.md#learning-from-history).
- Savings and cost totals start from zero.
- Dashboards and alert rules need the new metric and alert names. NetworkPolicies and Roles that name `hybernate-system` or the `hybernate.io` API group need the new ones.
- The plugin is `kubectl napwake`. Install it as [the plugin page](../getting-started/kubectl-plugin.md#installation) says, then remove `kubectl-hybernate`.

### The forecast setting, from before 0.2.0

Before 0.2.0, `spec.prediction.confidence` defaulted to `75`, so a ManagedWorkload saved from Hybernate with `kubectl get` has it stored. A confidence overrides the [forecast setting](../concepts/forecasting.md#forecast-setting). Leave it out of what you apply to Napwake, unless you chose it, so `spec.prediction.mode` applies.

### Wake on request, from before 0.2.0

Before 0.2.0, `spec.wake.onRequest: false`, or `hybernate.io/wake-on-request: "false"` with the label, turned waking on request off for one workload. That setting is gone. Napwake ignores the annotation, so the workload wakes on request.

Napwake refuses a ManagedWorkload that sets `spec.wake.onRequest`. `kubectl apply` answers `strict decoding error: unknown field "spec.wake.onRequest"`. A server-side apply, as a GitOps sync can be, answers `.spec.wake.onRequest: field not declared in schema`. So take it out of your manifests. Where it was `true`, remove it, since waking on request is the default. Where it was `false`, put what fits in its place, usually `mode: Ask`:

```yaml
spec:
  wake:
    mode: Ask   # in place of onRequest: false
```

- On a public URL, which crawlers and scanners keep awake, have the doorman ask before waking. Set `spec.wake.mode: Ask`, or `napwake.dev/wake: ask` with the label. Only a person's click on the page the doorman shows wakes it. Anything else gets a `503`. See [Asking before waking](../concepts/wake-on-request.md#asking-before-waking).
- For one port of a Service, list it in `napwake.dev/doorman-ignore-ports` on the Service. That port fails while the workload is paused.
- For the whole cluster, set the Helm value `doorman.enabled: false`.

To find what had it, run these before step 1:

```bash
kubectl get managedworkloads.hybernate.io -A -o json | jq -r '.items[] | select(.spec.wake.onRequest == false) | "\(.metadata.namespace)/\(.metadata.name)"'
kubectl get deployments,statefulsets,namespaces -A -o json | jq -r '.items[] | select(.metadata.annotations["hybernate.io/wake-on-request"] == "false") | "\(.kind) \(.metadata.namespace // "-")/\(.metadata.name)"'
```

The `WakeOnRequest` reason `TurnedOff` is gone with it.

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

Upgrading to a newer version with it, Helm renders with the installed version's defaults rather than the new one's, and those lack every value added since. From 0.2.0 on, the chart fills those in from its own defaults, so upgrading with `--reuse-values` works, in Helm 3 and Helm 4 alike. Releases before 0.2.0 can't be upgraded: [move to Napwake](#moving-to-napwake) instead. What `--reuse-values` still keeps is the installed version's default for each value both versions have, so a default the new version changes isn't picked up. Helm keeps those older defaults with the release, too, and goes on reusing them, which has one more catch: after such an upgrade, a default you remove by setting it to `null` doesn't stay removed. Helm 4 renders the default for the upgrade that sets it to `null`, and removes it from the next; Helm 3 brings it back at every other `--reuse-values` upgrade. To remove a default after such an upgrade, upgrade once with the values file, or with `--reset-then-reuse-values` (Helm 3.14 and later), which renders with the new version's defaults and keeps your values over them. From then on, `--reuse-values` keeps a removed default removed.

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
