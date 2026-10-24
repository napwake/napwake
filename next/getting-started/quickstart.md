# Quickstart

> Take one workload from idle and costing money to paused while idle and woken when used. You measure it first, so nothing pauses until you say so.

Source: https://docs.napwake.dev/next/getting-started/quickstart/

1. [1A sample workload](https://docs.napwake.dev/next/getting-started/quickstart/#1-a-sample-workload)
2. [2See what's idle](https://docs.napwake.dev/next/getting-started/quickstart/#2-see-whats-idle)
3. [3Opt it in, measuring firstdry-run](https://docs.napwake.dev/next/getting-started/quickstart/#3-opt-it-in-measuring-first)
4. [4Watch it measuredry-run](https://docs.napwake.dev/next/getting-started/quickstart/#4-watch-it-measure)
5. [5Let it pause](https://docs.napwake.dev/next/getting-started/quickstart/#5-let-it-pause)
6. [6Wake it with a request](https://docs.napwake.dev/next/getting-started/quickstart/#6-wake-it-with-a-request)
7. [7Stop managing itnot managed](https://docs.napwake.dev/next/getting-started/quickstart/#7-stop-managing-it)

Before you start

You need Napwake [installed](https://docs.napwake.dev/next/getting-started/installation/), the [kubectl plugin](https://docs.napwake.dev/next/getting-started/kubectl-plugin/) and metrics-server. Napwake reads CPU from it, and won’t pause a workload whose CPU it can’t read.

```bash
kubectl get apiservice v1beta1.metrics.k8s.io
```

If the Metrics API is missing or not `Available`, install metrics-server:

| Cluster  | metrics-server                                        |
| -------- | ----------------------------------------------------- |
| GKE, AKS | Runs by default                                       |
| minikube | Off when new. `minikube addons enable metrics-server` |
| EKS      | Off. The `metrics-server` add-on, or the manifest     |
| kind     | Off. The manifest, then the patch                     |
| Others   | The manifest                                          |

```bash
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
```

On kind, kubelet certificates aren’t signed for their addresses. The patch tells metrics-server not to verify them. Local test clusters only.

```bash
kubectl patch deployment metrics-server -n kube-system --type=json \
  -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
```

`kubectl top pods -A` shows numbers within a minute or so once it works. If a workload still won’t pause, see [Troubleshooting](https://docs.napwake.dev/next/operations/troubleshooting/#workload-is-idle-but-not-being-paused).

## 1. A sample workload

Skip it to use your own.

```bash
kubectl create namespace dev
kubectl create deployment my-api --image=nginx:1.27-alpine --replicas=2 -n dev
kubectl set resources deployment my-api -n dev --requests=cpu=100m,memory=64Mi
kubectl expose deployment my-api -n dev --port=80
kubectl rollout status deployment/my-api -n dev
```

CheckpointAwake

`my-api` runs 2 replicas, with a Service on port 80.

## 2. See what’s idle

```bash
kubectl napwake scan -n dev
```

The scan shows whether each workload is idle and why, and what it costs while it runs. Leave out `-n` to scan every namespace.

CheckpointAwake

`my-api` is active, as Napwake would treat any workload deployed in the last hour.

## 3. Opt it in, measuring first

```bash
kubectl annotate deployment my-api -n dev napwake.dev/dry-run=true napwake.dev/idle-after=5m
kubectl label deployment my-api -n dev napwake.dev/managed=true
```

The annotations are its settings, set first so it starts in dry-run, measured and never paused. `idle-after=5m` keeps the example quick. The default is an hour. The label opts it in.

In real use, they go in the manifest or Helm values, or on the namespace for everything in it. See [Opting in](https://docs.napwake.dev/next/guides/opt-in/), Kustomize included.

CheckpointAwake · dry-run

Napwake creates a ManagedWorkload for it:

```bash
kubectl get managedworkloads -n dev
```

```plaintext
NAME     PHASE     FREED   SAVED   WOULD FREE   AGE
my-api   Running   $0.00   $0.00   $0.00        10s
```

## 4. Watch it measure

```bash
kubectl napwake status -n dev
```

For `my-api`, STATE reads `running (dry-run)`. LAST ACTIVITY says when it was last active and from what. Until there’s been any, it reads `none since Napwake began managing it`. NEXT says when it would pause.

```bash
kubectl describe managedworkload my-api -n dev
```

After `idle-after` with no activity, STATE reads `idle (dry-run)` and an event says it would have paused. Nothing is scaled down. Mark it active, as a developer portal or your tooling would, to end the would-be pause:

```bash
kubectl annotate deployment my-api -n dev --overwrite napwake.dev/last-activity=$(date -u +%Y-%m-%dT%H:%M:%SZ)
kubectl napwake status -n dev
kubectl get managedworkloads -n dev
```

`status` now leads with what dry-run measured. WOULD FREE is what the would-be pauses would have freed.

With a workload of your own

Measure it a few days, across a weekend if it’s used on weekdays, before going live. See [Dry run](https://docs.napwake.dev/next/guides/dry-run/#what-dry-run-measured).

Requests to a running workload count only through a [Prometheus query](https://docs.napwake.dev/next/guides/prometheus-signals/). So an nginx serving the odd request on almost no CPU would pause. If yours won’t turn idle, LAST ACTIVITY and NEXT say why.

CheckpointAwake · dry-run

WOULD FREE reads `$0.00`. A few minutes of a sample this small free less than a cent.

## 5. Let it pause

Applied by Argo CD or Flux?

Skip this for the sample. A tool that applies `replicas` from Git undoes every pause, and Napwake waits an hour to pause again (`GitOpsConflict`). Before you enable, have it leave replicas to Napwake, as in [GitOps tools](https://docs.napwake.dev/next/guides/gitops/). The scan’s notes list affected workloads. Those with an HPA are already set.

```bash
kubectl napwake enable my-api -n dev
```

It sets `napwake.dev/dry-run` to `"false"`. After `idle-after` with no activity, which NEXT counts down, Napwake scales `my-api` to zero and remembers its 2 replicas.

CheckpointAsleep

```bash
kubectl get deployment my-api -n dev
# READY: 0/0
```

## 6. Wake it with a request

```bash
kubectl run curl --rm -it --image=curlimages/curl:8.7.1 -n dev --restart=Never -- curl -s http://my-api
```

The [doorman](https://docs.napwake.dev/next/concepts/wake-on-request/) holds the request and wakes `my-api`. A browser coming through an Ingress or a Gateway gets a waking-up page, which loads the app once it’s up.

`kubectl port-forward` goes straight to a pod, so it can’t reach a paused workload. Wake it first:

```bash
kubectl napwake wake my-api -n dev
```

CheckpointAwake

curl gets its answer once `my-api` is Ready.

## 7. Stop managing it

```bash
kubectl label deployment my-api -n dev napwake.dev/managed-
```

CheckpointAwake · not managed

`my-api` has no ManagedWorkload. Had it been paused, it would be scaled back to its replicas first, never left switched off.

Next

* [Opting in →Every setting, namespaces, Kustomize and GitOps.](https://docs.napwake.dev/next/guides/opt-in/)
* [Idle detection →How the activity clock decides.](https://docs.napwake.dev/next/concepts/idle-detection/)
* [Wake on request →How a request wakes a paused workload.](https://docs.napwake.dev/next/concepts/wake-on-request/)
