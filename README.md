<h1 align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/napwake/brand/main/logo/lockup-horizontal-dark.svg">
    <img alt="Napwake" src="https://raw.githubusercontent.com/napwake/brand/main/logo/lockup-horizontal.svg" width="300">
  </picture>
</h1>

<p align="center"><strong>Non-prod environments that sleep when nobody's using them, and wake when someone does.</strong></p>

<p align="center">Pauses idle Kubernetes workloads in staging, preview and dev, and wakes them on the first request.<br>It never deletes a workload or its data.</p>

<p align="center">
  <a href="https://napwake.dev">Website</a> ·
  <a href="https://docs.napwake.dev">Docs</a> ·
  <a href="https://docs.napwake.dev/getting-started/quickstart/">Quickstart</a> ·
  <a href="https://napwake.dev/compare/">How it compares</a> ·
  <a href="https://github.com/napwake/napwake/discussions">Discussions</a>
</p>

<p align="center">
  <a href="https://github.com/napwake/napwake/actions/workflows/test.yml"><img alt="Tests" src="https://github.com/napwake/napwake/actions/workflows/test.yml/badge.svg?branch=main"></a>
  <a href="https://github.com/napwake/napwake/releases"><img alt="Release" src="https://img.shields.io/github/v/release/napwake/napwake"></a>
  <a href="https://artifacthub.io/packages/search?repo=napwake"><img alt="Artifact Hub" src="https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/napwake"></a>
  <a href="LICENSE"><img alt="Licence: Apache 2.0" src="https://img.shields.io/badge/License-Apache_2.0-blue.svg"></a>
  <a href="https://docs.napwake.dev/getting-started/installation/"><img alt="Kubernetes 1.30 and later" src="https://img.shields.io/badge/Kubernetes-1.30+-326CE5?logo=kubernetes&logoColor=white"></a>
</p>

<picture>
  <source media="(max-width: 600px) and (prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/napwake/brand/main/illustrations/scenes/readme-phone-dark.svg">
  <source media="(max-width: 600px)" srcset="https://raw.githubusercontent.com/napwake/brand/main/illustrations/scenes/readme-phone-light.svg">
  <source media="(prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/napwake/brand/main/illustrations/scenes/readme-dark.svg">
  <img alt="A corridor of doors named like environments, some asleep and some awake, with the doorman holding staging's door as it wakes up. Below, the four steps: in use, idle for idleAfter, paused at zero replicas, and woken by a request the doorman holds until a pod is Ready." src="https://raw.githubusercontent.com/napwake/brand/main/illustrations/scenes/readme-light.svg">
</picture>

## See what's idle first

A team that works 10 hours a day, five days a week, uses staging 50 of 168 hours. That leaves 118 hours, 70% of the week, idle. `kubectl napwake scan` shows what's idle in your cluster now, and what it costs.

```bash
brew install napwake/tap/kubectl-napwake
kubectl napwake scan
```

It only reads, with your kubeconfig, and installs nothing in the cluster. It never reads Secrets, and its report stays on your machine.

## Install

You need Kubernetes 1.30 or later, Helm 3.8 or later, and metrics-server. GKE and AKS run metrics-server already. On EKS, kind and minikube, [add it first](https://docs.napwake.dev/getting-started/quickstart/).

Put your users' time zone in `napwake-values.yaml`. It defaults to UTC, which puts the forecast an hour off their mornings for a while after each daylight-saving change.

```yaml title="napwake-values.yaml"
timezone: America/New_York
```

```bash
helm install napwake oci://ghcr.io/napwake/charts/napwake \
  --version 0.2.0 \
  --namespace napwake-system \
  --create-namespace \
  -f napwake-values.yaml
kubectl get pods -n napwake-system
```

The chart installs the operator and the doorman, as the Deployments `napwake` and `napwake-doorman`, and the ManagedWorkload CRD. Keep the values file, and upgrade with it ([Keeping your settings](https://docs.napwake.dev/operations/upgrading/#keeping-your-settings)).

[Installation](https://docs.napwake.dev/getting-started/installation/) also has `install.yaml`, Kustomize, Argo CD and Flux.

<details>
<summary>The plugin without Homebrew, and moving from 0.1.x</summary>

With [krew](https://krew.sigs.k8s.io/docs/user-guide/setup/install/):

```bash
kubectl krew install --manifest-url \
  https://github.com/napwake/napwake/releases/download/v0.2.0/krew-napwake.yaml
```

Or [download it](https://docs.napwake.dev/getting-started/kubectl-plugin/#installation) for Linux, macOS or Windows.

There's no upgrade path from 0.1.x. Uninstall it, then install fresh, as [Moving to Napwake](https://docs.napwake.dev/operations/upgrading/#moving-to-napwake) says.

</details>

## Your first pause

Opt one workload in, in dry-run. Napwake measures it and pauses nothing.

```bash
kubectl annotate deployment my-api -n staging napwake.dev/dry-run=true
kubectl label deployment my-api -n staging napwake.dev/managed=true
kubectl napwake status -n staging
```

The annotation goes first, so it starts in dry-run. `status` shows its state as `running (dry-run)`, with its last activity and when it would pause. When you trust what it measured, end dry-run:

```bash
kubectl napwake enable my-api -n staging
```

After `idleAfter` with no activity, an hour by default, Napwake pauses `my-api`. Its replicas go to zero. The next request to its Service wakes it. So does `kubectl napwake wake my-api -n staging`, which waits until it's Running.

If Argo CD or Flux applies the workload, first have it [leave the replicas to Napwake](https://docs.napwake.dev/guides/gitops/). Otherwise it undoes every pause. The [Quickstart](https://docs.napwake.dev/getting-started/quickstart/) runs the whole loop on a sample app.

## How it works

1. You label a Deployment, StatefulSet or namespace `napwake.dev/managed=true`.
2. Napwake keeps an activity clock for each workload. CPU above `cpuThreshold` (10% of the pods' CPU request by default) resets it. So do deploys, activity annotations from your tools, and Prometheus queries you give it.
3. After `idleAfter` with no activity, Napwake pauses the workload. Its replicas go to zero, and its Services and PVCs stay.
4. While it's paused, the doorman holds the first connection until a pod is Ready. That works for any TCP, such as HTTP, gRPC, Postgres and Redis. Your Ingress doesn't change.
5. A forecast learns each workload's daily and weekly pattern. With `autoResume`, it wakes the workload 15 minutes before an hour it expects to be busy.

Behind a VPN, SSO or an allowlist, the first request wakes a paused workload. Public ones can ask first, with `napwake.dev/wake: ask`. A browser then gets a **Wake it** button, and crawlers, scanners and API calls wake nothing ([Public preview environments](https://docs.napwake.dev/guides/preview-environments/)).

## What it won't do

- Delete anything. A pause sets replicas to zero, and a wake restores them.
- Pause on a timetable. It pauses on idleness.
- Pause anything but Deployments and StatefulSets.
- Replace your autoscaler. While a workload runs, its HPA or KEDA ScaledObject sets the replicas.

It's for non-production. Pausing frees the CPU and memory a workload requested. That's money once your node autoscaler removes nodes, so Napwake reports freed and saved apart.

Napwake can't see requests to an awake workload. One in steady light use, under its `cpuThreshold`, can look idle and be paused while people use it. The next request wakes it, with a cold start. To stop that, give Napwake a [Prometheus query](https://docs.napwake.dev/guides/prometheus-signals/) for its traffic, such as your ingress's request rate. Then every request keeps it awake.

<details>
<summary>What it reads, writes and sends</summary>

- The operator and the doorman talk to the API server. The operator talks to Prometheus only if you set one.
- Nothing leaves the cluster.
- It never reads your Secrets. The doorman reads one of its own, by name, the key it signs the **Wake it** button with.
- With `watchNamespaces`, the operator and the doorman get a Role in each listed namespace in place of a ClusterRole.
- A namespace labelled `napwake.dev/protected=true` is never managed.

[Security](https://docs.napwake.dev/reference/security/) and [Data and access](https://docs.napwake.dev/reference/data-and-access/) have the exact RBAC, and how to verify a release.

</details>

## Docs

The full docs are at [docs.napwake.dev](https://docs.napwake.dev). Start here:

- [Quickstart](https://docs.napwake.dev/getting-started/quickstart/)
- [kubectl plugin](https://docs.napwake.dev/getting-started/kubectl-plugin/)
- [Opting in](https://docs.napwake.dev/guides/opt-in/)
- [Idle detection](https://docs.napwake.dev/concepts/idle-detection/)
- [Wake on request](https://docs.napwake.dev/concepts/wake-on-request/)
- [Forecasting](https://docs.napwake.dev/concepts/forecasting/)
- [Public preview environments](https://docs.napwake.dev/guides/preview-environments/)
- [Cost tracking](https://docs.napwake.dev/concepts/cost-tracking/)
- [Security](https://docs.napwake.dev/reference/security/)
- [How it compares](https://napwake.dev/compare/)

<details>
<summary>Every command</summary>

| Command | What it does | Needs the operator |
|---|---|---|
| `scan` | Shows what's idle, what it costs, and what pausing could free | No |
| `nodes` | Shows why what pausing frees isn't coming off the bill | No |
| `forecast` | Replays the forecast over your Prometheus history, before you turn on `autoResume` | No, but it needs Prometheus history |
| `status` | Shows what Napwake is doing with each workload it manages | Yes |
| `enable` | Ends dry-run for a workload or namespace | Yes |
| `pause` | Pauses a workload now, to wake on its next request | Yes |
| `wake` | Wakes a paused workload and waits until it's Running | Yes |
| `deps` | Shows what Napwake wakes and holds awake with a workload, and what depends on it | Yes |
| `version` | Prints the plugin's version | No |

Every flag is in the [kubectl plugin reference](https://docs.napwake.dev/reference/cli/).

</details>

## How it's built

Napwake is at 0.2.0. Its API is `napwake.dev/v1alpha1`, which may change before 1.0. It has no telemetry, usage reporting or update checks.

Every change lands through a reviewed pull request. Each one runs the unit tests, and end-to-end tests on kind with Kubernetes 1.30 and 1.37. Napwake is also tested by hand on GKE Standard. EKS, AKS and GKE Autopilot aren't tested yet.

Each release signs its image, chart and checksums with Sigstore, keylessly. It carries an SPDX SBOM for each platform, and GitHub build provenance. Then the install smoke test runs every install path in the docs on clean macOS and Ubuntu.

## Help and contributing

- Ask questions in [Discussions](https://github.com/napwake/napwake/discussions). If your team uses Napwake, tell us there too.
- Report bugs in [issues](https://github.com/napwake/napwake/issues).
- Report a vulnerability privately, as [SECURITY.md](SECURITY.md) says.
- Pull requests are welcome. [CONTRIBUTING.md](CONTRIBUTING.md) has the setup and the standards.
- Everyone taking part follows the [Code of Conduct](CODE_OF_CONDUCT.md).

## Licence

Copyright 2026 The Napwake Authors. Licensed under the [Apache License, Version 2.0](LICENSE).
