# GitOps tools

Hybernate pauses a workload by setting its replicas to zero. If a GitOps tool such as Argo CD, Flux, Fleet, kapp or Kluctl applies that workload from Git with `replicas` in its manifest, it sees the cluster no longer matches Git and sets them back, undoing the pause. Each tool has a one-time setting that leaves the replicas to Hybernate; this page gives it.

Teams that already run an HPA or KEDA under Argo CD or Flux usually have it in place, since an autoscaler changes replicas too.

## What Hybernate does

Hybernate never fights Git. When a paused workload is scaled up by anything other than Hybernate, it counts as activity: the workload goes back to Running, and `status.lastScaledUp` records when, how many replicas, and what did it, by the field manager the API server recorded (`argocd-controller`, `kustomize-controller`, `kubectl-client-side-apply`, ...). A scale through the scale subresource, as `kubectl scale`, an HPA and KEDA do, records no field manager, so it shows as `unknown`.

When that was Argo CD, Flux, Rancher Fleet, kapp or Kluctl, the workload also reports it, with the fix:

```
GitOpsConflict=True   Argo CD set the replicas from Git, undoing the pause; Hybernate pauses it again from 14:20 UTC. ...
```

along with a `GitOpsConflict` warning event. Hybernate waits an hour before pausing again, so it doesn't restart the workload in a loop with the tool; [`kubectl hybernate status`](../getting-started/kubectl-plugin.md#see-what-hybernate-is-doing) shows when, as `pauses at 14:20 UTC (after GitOps conflict)`. Pausing again is also how it finds out the fix is in: once a pause holds for an hour, the condition clears with a `GitOpsConflictResolved` event. A pause Hybernate wakes sooner doesn't show the tool has stopped, so the condition stays until a later pause holds for the hour.

Before you opt anything in, [`kubectl hybernate scan`](../getting-started/kubectl-plugin.md#scan-a-cluster) lists the workloads whose replicas are set from Git, by tool.

### Checking the fix is in

[`kubectl hybernate enable`](../getting-started/kubectl-plugin.md#enable-a-workload) and [`pause`](../getting-started/kubectl-plugin.md#pause-a-workload) warn when a GitOps tool will undo pauses, and say nothing once it's told not to:

- **Argo CD** keeps owning the replicas it applied even once it ignores them, so they read the workload's Application, found by its `argocd.argoproj.io/tracking-id` annotation, or the `argocd.argoproj.io/instance` or `app.kubernetes.io/instance` label it tracks by, in the namespace an `<namespace>_<name>` value gives, or in any namespace, or `argocd`. Argo CD leaves the replicas when the Application has `RespectIgnoreDifferences=true` in its sync options and ignores `/spec/replicas` (or `.spec.replicas`, or all of `spec`) for the workload's kind, in its own `spec.ignoreDifferences` or in `argocd-cm`, beside the Application or in `argocd`: a `resource.customizations.ignoreDifferences.apps_Deployment` (or `apps_StatefulSet`, or `all`) key, or the older `resource.customizations` block. The warning gives only the step that's missing. When the Application or `argocd-cm` can't be read, such as without access to Argo CD's namespace, or with Argo CD running in another cluster, it says why, and to make the fix if you haven't already.
- **Flux, Fleet and Kluctl** set only the fields their manifests have, so they set the replicas exactly when the API server records them as owning `spec.replicas`.
- **kapp** keeps the replicas a rebase rule tells it to as its own, and the rule isn't on the workload, so the warning says it can't tell.

## Argo CD

Have Argo CD ignore the replicas of Deployments and StatefulSets, once for every app, in the `argocd-cm` ConfigMap:

```yaml title="argocd-cm" linenums="1"
data:
  resource.customizations.ignoreDifferences.apps_Deployment: |
    jsonPointers:
      - /spec/replicas
  resource.customizations.ignoreDifferences.apps_StatefulSet: |
    jsonPointers:
      - /spec/replicas
```

Then add `RespectIgnoreDifferences=true` to each app's sync options, so a sync leaves them too and not only the diff:

```yaml title="application.yaml" linenums="1"
spec:
  syncPolicy:
    syncOptions:
      - RespectIgnoreDifferences=true
```

This is the setting Argo CD's own docs give for an HPA, so apps that already have it need nothing more. To keep it to some apps, put the same `jsonPointers` in their `spec.ignoreDifferences` instead.

Argo CD can also ignore fields by the field manager that set them, but not for this: when a server-side-applied object is scaled through its scale subresource, Kubernetes drops the field's owner without recording a new one, so the replicas Hybernate sets aren't attributed to any manager.

## Flux

Flux only sets the fields a manifest has. Leave `replicas` out of the workload's manifest in Git, and Flux leaves them to Hybernate; Kubernetes starts a new Deployment at one replica.

For a Helm chart that always renders `replicas`, remove it in the HelmRelease with a post-renderer:

```yaml title="helmrelease.yaml" linenums="1"
spec:
  postRenderers:
    - kustomize:
        patches:
          - target: {kind: Deployment, name: my-api}
            patch: |
              - op: remove
                path: /spec/replicas
```

## Rancher Fleet

Fleet applies what its bundle renders. Leave `replicas` out of the workload's manifest in Git and Fleet leaves them to Hybernate. For a Helm chart that always renders them, remove them with a kustomize patch in the bundle's `fleet.yaml`.

## kapp

Have kapp keep the replicas it finds in the cluster, as its docs give for an HPA, with a rebase rule in a kapp `Config`:

```yaml title="kapp-config.yaml" linenums="1"
apiVersion: kapp.k14s.io/v1alpha1
kind: Config
rebaseRules:
  - path: [spec, replicas]
    type: copy
    sources: [existing, new]
    resourceMatchers:
      - apiVersionKindMatcher: {apiVersion: apps/v1, kind: Deployment}
      - apiVersionKindMatcher: {apiVersion: apps/v1, kind: StatefulSet}
```

## Kluctl

Leave `replicas` out of the workload's manifest in Git. For a Helm chart that always sets them, remove them with a patch in the deployment item's `kustomization.yaml`. Kluctl's conflict settings, such as `kluctl.io/ignore-conflicts-manager`, can't help: a scale through the scale subresource leaves the replicas with no field manager, so Kluctl's next deploy sets them back without a conflict to ignore.

## Ending dry-run

A workload's dry-run lives in its `hybernate.io/dry-run` annotation, its namespace's, or a ManagedWorkload's `spec.dryRun`. When Git has that setting, change it there: a change made in the cluster is put back on the tool's next sync. [`kubectl hybernate enable`](../getting-started/kubectl-plugin.md#enable-a-workload) knows this. It reads who owns the setting from the field managers the API server records, so it finds Argo CD whether it tracks apps by label (`app.kubernetes.io/instance`, its default) or by annotation, Flux whether it applies a Kustomization or a HelmRelease, and Fleet, kapp and Kluctl. For those it changes nothing, prints the change to make in Git, and exits non-zero; `--force` changes the cluster anyway. When the tool applies the workload but Git doesn't have the annotation, nothing would put it back, so `enable` sets it.

A chart installed with plain `helm` is different: Helm only applies it when you run it, so `enable` sets the annotation and warns that the next `helm upgrade` restores the chart's value. Set it to `"false"` in the chart's values too.
