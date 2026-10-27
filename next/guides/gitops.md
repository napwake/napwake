# GitOps tools

> Tell your GitOps tool once to leave the replicas to Napwake, so its syncs don't undo pauses.

Source: https://docs.napwake.dev/next/guides/gitops/

Napwake pauses by setting replicas to zero. A GitOps tool that applies `replicas` from Git sees the drift and sets them back.

Under Argo CD or Flux with an HPA or KEDA, you likely have the fix already. Autoscalers change replicas too.

Before you start

```bash
kubectl napwake scan
```

[`scan`](https://docs.napwake.dev/next/getting-started/kubectl-plugin/#scan-a-cluster) lists the workloads whose replicas Git sets, by tool.

## Leave the replicas to Napwake

* Argo CD

  1. Ignore Deployment and StatefulSet replicas for every app, in `argocd-cm`:

     argocd-cm

     ```yaml
     data:
       resource.customizations.ignoreDifferences.apps_Deployment: |
         jsonPointers:
           - /spec/replicas
       resource.customizations.ignoreDifferences.apps_StatefulSet: |
         jsonPointers:
           - /spec/replicas
     ```

     With Argo CD’s Helm chart, which writes `argocd-cm` from its values, use `configs.cm`:

     argo-cd-values.yaml

     ```yaml
     configs:
       cm:
         resource.customizations.ignoreDifferences.apps_Deployment: |
           jsonPointers:
             - /spec/replicas
         resource.customizations.ignoreDifferences.apps_StatefulSet: |
           jsonPointers:
             - /spec/replicas
     ```

  2. Add `RespectIgnoreDifferences=true` to each app’s sync options, so a sync leaves them alone too, as the diff already does:

     application.yaml

     ```yaml
     spec:
       syncPolicy:
         syncOptions:
           - RespectIgnoreDifferences=true
     ```

  It’s Argo CD’s HPA setting, so apps with it need nothing more. For some apps only, put the `jsonPointers` in their `spec.ignoreDifferences`.

  Why not ignore by field manager

  Scaling a server-side-applied object through its scale subresource drops the field’s owner and records none. So no manager owns the replicas Napwake sets.

* Flux

  Leave `replicas` out of the manifest in Git, since Flux sets only the fields a manifest has. A new Deployment starts at one replica.

  For a Helm chart that always renders them, use a post-renderer:

  helmrelease.yaml

  ```yaml
  spec:
    postRenderers:
      - kustomize:
          patches:
            - target: {kind: Deployment, name: my-api}
              patch: |
                - op: remove
                  path: /spec/replicas
  ```

* Fleet

  Leave `replicas` out of the manifest in Git, since Fleet applies what its bundle renders. For a chart that always renders them, use a kustomize patch in the bundle’s `fleet.yaml`.

* kapp

  Keep the replicas kapp finds in the cluster with a rebase rule, as its docs give for an HPA:

  kapp-config.yaml

  ```yaml
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

* Kluctl

  Leave `replicas` out of the manifest in Git. For a chart that always sets them, patch them out in the deployment item’s `kustomization.yaml`.

  Conflict settings such as `kluctl.io/ignore-conflicts-manager` can’t help. Scaled through the scale subresource, the replicas have no field manager. So Kluctl’s next deploy sets them back without a conflict.

## Checking the fix is in

Checkpoint

[`kubectl napwake enable`](https://docs.napwake.dev/next/getting-started/kubectl-plugin/#enable-a-workload) and [`pause`](https://docs.napwake.dev/next/getting-started/kubectl-plugin/#pause-a-workload) warn while a GitOps tool will undo pauses. Once it’s told, they print nothing about it.

| Tool                | How the warning tells                                                                                          |
| ------------------- | -------------------------------------------------------------------------------------------------------------- |
| Argo CD             | Reads the Application, since Argo CD keeps owning the replicas it ignores. Names only the missing step         |
| Flux, Fleet, Kluctl | Checks whether the API server records the tool owning `spec.replicas`                                          |
| kapp                | Says it can’t tell. kapp keeps as its own the replicas a rebase rule names, and the rule isn’t on the workload |

Without access to Argo CD’s namespace, or with Argo CD in another cluster, they can’t read the Application or `argocd-cm`. They say why, and to make the fix if you haven’t.

Where they look in Argo CD

| It looks for                                                                              | In                                                                                                                                                                                                                                       |
| ----------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| The Application                                                                           | By the workload’s `argocd.argoproj.io/tracking-id` annotation, or the `argocd.argoproj.io/instance` or `app.kubernetes.io/instance` label it tracks by. In the namespace an `<namespace>_<name>` value gives, any namespace, or `argocd` |
| `RespectIgnoreDifferences=true`                                                           | The Application’s sync options                                                                                                                                                                                                           |
| An ignore of `/spec/replicas`, `.spec.replicas` or all of `spec`, for the workload’s kind | The Application’s `spec.ignoreDifferences`. Or `argocd-cm`, beside it or in `argocd`, as a `resource.customizations.ignoreDifferences.apps_Deployment`, `apps_StatefulSet` or `all` key, or the older `resource.customizations` block    |

## What Napwake does

Napwake never fights Git.

1. Asleep · paused

   Napwake pauses it

2. Counted as activity

3. Awake · running

   A sync sets the replicas from Git

   A `GitOpsConflict` condition and warning event say what to change.

4. An hour later, so they don't loop

5. Asleep · paused

   Napwake pauses it again

   If it holds for an hour, the condition clears.

`status.lastScaledUp` records when, how many replicas, and the field manager, such as `argocd-controller`, `kustomize-controller` or `kubectl-client-side-apply`. A scale through the scale subresource, as by `kubectl scale`, an HPA or KEDA, shows as `unknown`.

For Argo CD, Flux, Rancher Fleet, kapp and Kluctl, the condition links to the tool’s tab:

```text
GitOpsConflict=True   Argo CD set the replicas from Git, undoing the pause; Napwake pauses it again from 14:20 UTC. To stop it, have Argo CD ignore replicas, as for an HPA: https://docs.napwake.dev/guides/gitops/#argo-cd
```

[`kubectl napwake status`](https://docs.napwake.dev/next/getting-started/kubectl-plugin/#see-what-napwake-is-doing) shows `pauses at 14:20 UTC (after GitOps conflict)`. A pause woken within the hour proves nothing, so the condition stays until one holds. Then it clears with a `GitOpsConflictResolved` event.

## Opting in a whole namespace

The opt-in label is on the Namespace, which isn’t in Git when the tool creates it. See [A whole namespace](https://docs.napwake.dev/next/guides/opt-in/#a-whole-namespace).

| The tool creates it with                       | Put the label in                             |
| ---------------------------------------------- | -------------------------------------------- |
| Argo CD’s `CreateNamespace=true`               | The Application’s `managedNamespaceMetadata` |
| A Flux HelmRelease’s `install.createNamespace` | A Namespace you add to the kustomization     |

## Ending dry-run

Dry-run is the workload’s `napwake.dev/dry-run` annotation, its namespace’s, or a ManagedWorkload’s `spec.dryRun`. If Git has it, change it there, or the tool’s next sync puts it back.

[`kubectl napwake enable`](https://docs.napwake.dev/next/getting-started/kubectl-plugin/#enable-a-workload) finds the tool from what set `replicas`. It finds the Argo CD application by its tracking annotation, Argo CD’s default since 3.0, or its label.

| Who set it              | `enable`                                                                                                             |
| ----------------------- | -------------------------------------------------------------------------------------------------------------------- |
| A GitOps tool, from Git | Changes nothing, prints the change to make in Git, and exits non-zero. `--force` changes the cluster anyway          |
| Plain `helm`            | Changes the cluster, but the next `helm upgrade` restores the chart’s value. Set `"false"` in the chart’s values too |

Next

* [Opting in →Label a workload, or a whole namespace.](https://docs.napwake.dev/next/guides/opt-in/)
* [Dry run →Measure a workload before it pauses.](https://docs.napwake.dev/next/guides/dry-run/)
* [kubectl plugin →`scan`, `status`, `enable` and `pause`.](https://docs.napwake.dev/next/getting-started/kubectl-plugin/)
