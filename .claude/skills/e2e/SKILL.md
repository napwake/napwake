---
name: e2e
description: Run Napwake's end-to-end tests and its Helm smoke test on kind, each with a kubeconfig of its own, run one spec, and read a failure from the operator's and the doorman's logs the suite prints. Use when a change needs a real cluster to prove, or when the E2E or Helm smoke check fails.
---

# End-to-end tests on kind

The suite, in `test/e2e/` (Ginkgo, build tag `e2e`), builds the image and the plugin from your tree. It deploys Napwake with kustomize into a kind cluster, with metrics-server, ingress-nginx and KEDA, and runs its specs four at a time. It needs Docker, kind and kubectl.

## The whole suite

```bash
make test-e2e
```

- The cluster is this checkout's own, named by `hack/kind-cluster-name.sh e2e`: `napwake-e2e-<directory>-<hash of its path>`. So are its kubeconfig, `bin/e2e/<cluster>/kubeconfig`, the image, `napwake:<cluster>`, and the plugin, `bin/e2e/<cluster>/kubectl-napwake`. Two worktrees can run at once.
- The run reaches its cluster only through that kubeconfig. Your own kubeconfig and its current context are left alone. The suite stops before it changes anything if kubectl's current context isn't `kind-<cluster>`.
- `KIND_NODE_IMAGE` picks the Kubernetes version. CI runs 1.30 and 1.37, with the images in `.github/workflows/test-e2e.yml`.
- A second run from the same checkout needs another name: `make test-e2e KIND_CLUSTER=napwake-e2e-second`.
- A run that passes deletes its cluster. One that fails keeps it, and its last lines say how to reach it and delete it.

## One spec

```bash
cluster=$(hack/kind-cluster-name.sh e2e)
make setup-test-e2e load-test-e2e-images
KIND_CLUSTER=$cluster KUBECONFIG="$PWD/bin/e2e/$cluster/kubeconfig" E2E_IMG=napwake:$cluster \
  E2E_PLUGIN="$PWD/bin/e2e/$cluster/kubectl-napwake" \
  go run github.com/onsi/ginkgo/v2/ginkgo --tags=e2e --timeout=60m -v \
  --focus='wakes a paused app on a request to its Service' ./test/e2e/
make cleanup-test-e2e
```

`--focus` is a regular expression matched against each spec's full name. Add `--dry-run` to list what it picks without a cluster. The suite still builds, deploys and removes Napwake around the spec.

## The Helm smoke test

```bash
make test-helm-smoke
```

It installs the chart with `watchNamespaces` into a kind cluster of its own, `napwake-smoke-...`, through a temporary kubeconfig. It pauses and wakes a workload through the doorman, has an upgrade and an uninstall refused, and prints the ManagedWorkloads, EndpointSlices, pods, events and logs if a step fails. `KEEP_CLUSTER=true make test-helm-smoke` keeps the cluster and prints its kubeconfig.

## Reading a failure

1. **Find the spec.** Ginkgo prints `[FAILED]` with the spec's name, the file and line of the expectation that failed, and its message. The `STEP:` lines above it show how far it got.
2. **Read what the suite printed after it.** When a spec fails, the suite prints, in the same output:
   - `Controller logs:`, the operator's last 500 lines, and `Previous controller logs:`, from before a restart, which is where a crash shows;
   - `Doorman logs:`;
   - `Napwake's events:` and `Napwake's pods:`;
   - `ManagedWorkloads:` and `Doorman EndpointSlices:`, across the cluster;
   - `Events in the specs' namespaces:`.

   In the operator's log, find the workload's name and the minute of the failure. Lines logged in a reconcile carry its ManagedWorkload's name and namespace. Look for errors, and for `phase transition` lines, which say from, to and why.
3. **Look at the cluster it kept,** through its own kubeconfig:

   ```bash
   cluster=$(hack/kind-cluster-name.sh e2e)
   export KUBECONFIG=$PWD/bin/e2e/$cluster/kubeconfig
   kubectl get managedworkloads -A -o wide
   kubectl describe managedworkload <name> -n <namespace>
   kubectl logs -n napwake-system -l control-plane=controller-manager --tail=-1
   kubectl logs -n napwake-system -l control-plane=doorman --prefix --tail=-1
   ```

   `kubectl describe` shows the ManagedWorkload's conditions, which say why it isn't paused or woken, and its events.
4. **Delete it** when you're done: `make cleanup-test-e2e`.

In CI the cluster is gone with the runner, so steps 1 and 2 are what there is: open the failed job's log and search for `[FAILED]`.
