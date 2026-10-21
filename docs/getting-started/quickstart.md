# Quickstart

This guide takes one workload from "idle and costing money" to "paused while idle, woken when used", measuring it first so nothing is paused until you say so.

It assumes Hybernate is [installed](installation.md) and you have the [kubectl plugin](kubectl-plugin.md).

Hybernate reads CPU from metrics-server, and while it can't read a workload's CPU it doesn't pause it, since a busy workload would look idle. GKE and AKS run it by default; EKS, kind and a new minikube don't. Check that the Metrics API is there:

```bash
kubectl get apiservice v1beta1.metrics.k8s.io
```

If it's missing or not `Available`, install it. On minikube, run `minikube addons enable metrics-server`; on EKS, add the `metrics-server` add-on, or apply the manifest below. Elsewhere:

```bash
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
```

On kind, the kubelets' certificates aren't signed for their addresses, so also tell metrics-server not to verify them. Do this only on a local test cluster:

```bash
kubectl patch deployment metrics-server -n kube-system --type=json \
  -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
```

`kubectl top pods -A` shows numbers within a minute or so once it's working. If a workload still doesn't pause, see [Troubleshooting](../operations/troubleshooting.md#workload-is-idle-but-not-being-paused).

## 1. A sample workload

If you don't have one to try, create a Deployment:

```bash
kubectl create namespace dev
kubectl create deployment my-api --image=nginx:1.27-alpine --replicas=2 -n dev
kubectl set resources deployment my-api -n dev --requests=cpu=100m,memory=64Mi
kubectl expose deployment my-api -n dev --port=80
kubectl rollout status deployment/my-api -n dev
```

## 2. See what's idle

```bash
kubectl hybernate scan -n dev
```

The scan shows each workload, whether it's idle right now and why, and what it costs while running. A workload deployed in the last hour counts as active, as Hybernate would treat it, so a brand-new one shows as active until then.

On your own cluster, leave out `-n` to scan every namespace. When Argo CD or Flux applies your workloads, the scan's notes list those whose replicas it sets from Git; see [step 5](#5-applied-by-argo-cd-or-flux-leave-the-replicas-to-hybernate) before letting Hybernate pause them.

## 3. Opt it in, measuring first

```bash
kubectl annotate deployment my-api -n dev hybernate.io/dry-run=true hybernate.io/idle-after=5m
kubectl label deployment my-api -n dev hybernate.io/managed=true
```

The annotations are the workload's settings, set first so it starts in dry-run; the label opts it in. Here, dry-run means it's measured and never paused, and `idle-after: 5m` makes the example quick (the default is an hour). In real use, these usually go in the workload's manifest or Helm values, or on its namespace to cover everything in it; see [Opting In](../guides/opt-in.md). With Kustomize, don't use `commonLabels`, which changes the workload's selector: [With Kustomize](../guides/opt-in.md#with-kustomize) gives the ways that work.

Hybernate creates a ManagedWorkload for it:

```bash
kubectl get managedworkloads -n dev
```

```
NAME     PHASE     SAVED   FREED   AGE
my-api   Running   $0.00   $0.00   10s
```

## 4. Watch it measure

```bash
kubectl hybernate status -n dev
```

`status` is one screen of what Hybernate is doing with every workload it manages. For `my-api`, STATE reads `running (dry-run)`, LAST ACTIVITY says when it was last active and from what (`created`, until something else is seen), and NEXT when it would pause. Once there's been no activity for `idle-after`, STATE becomes `idle (dry-run)` and an event says it would have paused. Nothing is scaled down in dry-run:

```bash
kubectl describe managedworkload my-api -n dev
```

Each would-be pause is added up: how many times it would have paused, how long it would have slept, and what that would have freed. Mark it active to end one, as a developer portal or your own tooling would, then look again:

```bash
kubectl annotate deployment my-api -n dev --overwrite hybernate.io/last-activity=$(date -u +%Y-%m-%dT%H:%M:%SZ)
kubectl hybernate status -n dev
kubectl get managedworkloads -n dev
```

`status` leads with what dry-run has measured, and `kubectl get managedworkloads` shows in WOULD FREE what the would-be pauses would have freed. A few minutes of a sample this small free less than a cent, so expect `$0.00` here; left in dry-run for a few days, across a weekend if it's used on weekdays, a real workload's figure is what to judge going live by. `kubectl hybernate scan -n dev` shows the same, with how long it would have slept and how often it would have been woken. For scripts, `kubectl get managedworkload my-api -n dev -o jsonpath='{.status.dryRun}'` gives the summary as data, its hours as Kubernetes quantities; see [Dry Run](../guides/dry-run.md#what-dry-run-measured).

Requests to a running workload don't count as activity by themselves: Hybernate sees its CPU, deploys and activity annotations, and requests only through a [Prometheus query](../guides/prometheus-signals.md) you give it, or once it's paused, through the doorman. An nginx serving the odd request uses almost no CPU, so it would pause. On a workload of your own that doesn't turn idle, LAST ACTIVITY in `status` says what keeps it active, such as `cpu`, and NEXT says why it won't pause when something holds it.

## 5. Applied by Argo CD or Flux? Leave the replicas to Hybernate

Skip this for the sample. Hybernate pauses a workload by setting its replicas to zero, and a GitOps tool that applies `replicas` from Git sets them straight back. Hybernate reports that as a `GitOpsConflict` and, rather than fight the tool, waits an hour before pausing again, so until the tool is told, pausing seems not to work. Tell it once, before step 6:

- **Argo CD:** ignore `/spec/replicas` on Deployments and StatefulSets, in `argocd-cm` for every app or in an Application's `ignoreDifferences`, and add `RespectIgnoreDifferences=true` to the Application's sync options.
- **Flux:** leave `replicas` out of the workload's manifest, or remove it with a post-renderer for a chart that always sets it.

An HPA needs the same, so a workload that already has one under Argo CD or Flux is set. [GitOps tools](../guides/gitops.md) has the YAML, and the settings for Fleet, kapp and Kluctl. The scan's notes from step 2 list the workloads whose replicas are set from Git, and `kubectl hybernate enable` and `pause` warn when it isn't in place.

## 6. Let it pause

When you're happy with what you see:

```bash
kubectl hybernate enable my-api -n dev
```

This sets the workload's `hybernate.io/dry-run` annotation to `"false"`. Once the workload has had no activity for `idle-after`, which NEXT in `kubectl hybernate status -n dev` counts down, Hybernate scales it to zero, remembering it had 2 replicas:

```bash
kubectl get deployment my-api -n dev
# READY: 0/0
```

## 7. Use it again

Send it a request. The [doorman](../concepts/wake-on-request.md) holds the request, wakes the workload, and answers once it's Ready:

```bash
kubectl run curl --rm -it --image=curlimages/curl:8.7.1 -n dev --restart=Never -- curl -s http://my-api
```

A browser opening it gets a waking-up page instead, which loads the app once it's up. You can also wake it from the terminal:

```bash
kubectl hybernate wake my-api -n dev
```

## 8. Stop managing it

```bash
kubectl label deployment my-api -n dev hybernate.io/managed-
```

Removing the label removes the ManagedWorkload. If the workload is paused, it's scaled back to its replicas first, so it's never left switched off.

## What's next?

- [Opting In](../guides/opt-in.md): every setting, namespaces, Kustomize, and GitOps
- [Idle Detection](../concepts/idle-detection.md): how the activity clock decides
- [Wake on Request](../concepts/wake-on-request.md): how requests wake a paused workload
- [ManagedWorkload Guide](../guides/managed-workload.md): writing a ManagedWorkload yourself, for settings annotations don't cover
