# Napwake

> Pauses idle Kubernetes workloads in staging, preview and dev, and wakes them on the first request. It never deletes a workload or its data.

Source: https://docs.napwake.dev/next/

Napwake docs

# Non-prod environments that sleep when nobody's using them, and wake when someone does.

Pauses idle Kubernetes workloads in staging, preview and dev, and wakes them on the first request.

It never deletes a workload or its data.

[Install](https://docs.napwake.dev/next/getting-started/installation/)[Quickstart](https://docs.napwake.dev/next/getting-started/quickstart/)

A request has reached staging while it was asleep. The doorman holds it at the door while staging wakes, then lets it through. Dev stays asleep. Preview is already awake.

## See what’s idle first

A team that works 10 hours a day, five days a week, uses staging 50 of 168 hours. The other 70%, it’s idle. `kubectl napwake scan` shows your cluster’s own number before you install anything.

[![The scan report for an example cluster. It leads with 19 of 31 workloads running for nobody and $691 that could be freed a month. Beside it, the cluster drawn as a hotel at night, twice: now, with most rooms lit, and with Napwake, the rooms lit for nobody dark.](https://docs.napwake.dev/next/home/scan-report-light.webp)](https://napwake.dev/examples/scan-report/)

[![](https://docs.napwake.dev/next/home/scan-report-dark.webp)](https://napwake.dev/examples/scan-report/)

```sh
kubectl napwake scan
```

* It runs with your kubeconfig and installs nothing in the cluster.
* It shows what's idle right now as rooms lit for nobody, and what each costs at your nodes' list prices.
* With Prometheus, it replays the last week of each workload's CPU and rollouts, and says what pausing would have freed.

One file to send your team. It loads nothing from elsewhere and prints to PDF.

[Open the example report ↗](https://napwake.dev/examples/scan-report/) [Scan, in the plugin's docs](https://docs.napwake.dev/next/getting-started/kubectl-plugin/#scan-a-cluster)

## Four doors

Asleep · paused

Scaled to zero. Its Services are routed to the doorman, which wakes it on the first request.

Waking up · resuming

A request, an activity annotation, or `autoResume` ahead of demand woke it. Its replicas are restored and its dependencies woken.

Awake · running

Its pods are Ready and held requests passed through. With no activity for `idleAfter`, and no forecast of demand, it pauses again.

Needs you · needs attention

A note pinned on the door. Something stops Napwake doing its job, and `kubectl napwake status` says what to fix.

## How it works

Label a Deployment, StatefulSet or namespace `napwake.dev/managed: "true"` and Napwake creates a ManagedWorkload for it, with its settings from annotations. Then it keeps an activity clock: CPU, deploys, activity annotations, and Prometheus.

1. Awake · running

   Running

   Activity clock: CPU, deploys, activity annotations, Prometheus

2. No activity for `idleAfter`, and no forecast of demand

3. Asleep · paused

   Paused

   Scaled to zero, its Services routed to the doorman

4. A request, an activity annotation, or `autoResume` ahead of demand

5. Waking up · resuming

   Resuming

   Replicas restored, dependencies woken

Pods Ready, held requests passed through

Private or public

Napwake is for non-production environments on your own clusters. Behind a VPN, SSO or an allowlist, the first request wakes a paused workload.

Public ones ask first. A browser gets a **Wake it** button, so people wake them and robots don’t. [Public preview environments](https://docs.napwake.dev/next/guides/preview-environments/) says how.

## What it does

* ### Opt in with a label

  Label a workload or namespace `napwake.dev/managed: "true"`. Settings are annotations in your Git, so Argo CD and Flux work as today.

* ### Activity clock

  Napwake records each workload's last activity: CPU over `cpuThreshold`, a deploy, an activity annotation or a Prometheus query. After `idleAfter` with none, it pauses. No learning period.

* ### Demand forecasting

  A Holt-Winters double seasonal model learns each workload's day and week from up to four weeks of Prometheus. It scores itself from week two and acts when accurate enough: `eager`, `balanced` or `cautious`. It re-learns after drift, never from a holiday.

* ### Safe by default

  Napwake never pauses what it can't measure. A confident forecast can hold a pause off. `dryRun` pauses nothing and shows what would. Anyone else's scale-up is a wake. If a GitOps tool undoes a pause, Napwake tells you how to fix it.

* ### Cost tracking

  Napwake tracks each workload's cost, what pausing frees, and what came off the bill, from each node pool's spare capacity. Freed capacity is money once an autoscaler removes nodes. `kubectl napwake nodes` shows what keeps them. [How to let them go](https://docs.napwake.dev/next/guides/node-pools/).

* ### Metrics and alerts

  Prometheus metrics for operator health, lifecycle transitions, wakes on request and forecast confidence, with alerting rules.

## Quick example

deployment.yaml

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-api
  namespace: dev
  labels:
    napwake.dev/managed: "true"
  annotations:
    napwake.dev/dry-run: "true"
    napwake.dev/idle-after: "1h"
```

Napwake watches `my-api` and records each time it’s active: CPU above `cpuThreshold` (10% of its requests by default), a deploy, or an activity annotation, and, with a [Prometheus query](https://docs.napwake.dev/next/guides/prometheus-signals/), its request rate. In dry-run it only measures. Once you run `kubectl napwake enable my-api -n dev`, it pauses the Deployment after an hour with no activity, and wakes it when a request reaches its Service.

For every setting, see [Opting in](https://docs.napwake.dev/next/guides/opt-in/). For settings annotations don’t cover, write a [ManagedWorkload](https://docs.napwake.dev/next/guides/managed-workload/) yourself.

Get started

* [Installation →Helm or the Kustomize manifests, with Argo CD or Flux.](https://docs.napwake.dev/next/getting-started/installation/)
* [Quickstart →One workload from idle to paused while idle, woken when used.](https://docs.napwake.dev/next/getting-started/quickstart/)
* [kubectl plugin →`scan`, `status`, `pause`, `wake` and the rest.](https://docs.napwake.dev/next/getting-started/kubectl-plugin/)
