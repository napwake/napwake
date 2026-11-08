# Scan your cluster

> See what's idle in your cluster and what it costs, before you install anything in it.

Source: https://docs.napwake.dev/0.2/getting-started/scan/

## 1. Install the plugin

* Homebrew

  On macOS and Linux:

  ```bash
  brew install napwake/tap/kubectl-napwake
  ```

* Krew

  With [krew](https://krew.sigs.k8s.io/docs/user-guide/setup/install/) installed, on Linux, macOS or Windows:

  ```bash
  kubectl krew install --manifest-url \
    https://github.com/napwake/napwake/releases/download/v0.2.0/krew-napwake.yaml
  ```

  To upgrade it later, see the [plugin page](https://docs.napwake.dev/0.2/getting-started/kubectl-plugin/#installation).

* Download

  On macOS and Linux:

  ```bash
  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  arch=$(uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
  curl -fLO "https://github.com/napwake/napwake/releases/download/v0.2.0/kubectl-napwake-${os}-${arch}.tar.gz"
  tar xzf "kubectl-napwake-${os}-${arch}.tar.gz"
  sudo install "kubectl-napwake-${os}-${arch}" /usr/local/bin/kubectl-napwake
  ```

  Windows, and building it yourself, are on the [plugin page](https://docs.napwake.dev/0.2/getting-started/kubectl-plugin/#installation).

## 2. Run the scan

```bash
kubectl napwake scan
```

It scans your kubeconfig’s current context, in every namespace you can read. It only reads, with your own kubeconfig and permissions. It changes nothing in the cluster, and installs nothing in it.

Checkpoint

You’ll see the cluster, then the headline:

```
staging (EKS us-east-1): 31 workloads in 11 namespaces
  ...
  19 workloads are idle right now, reserving 28.5 vCPU and 77.5 GiB of memory.
  They cost $871/month while running, $1.19 an hour: each hour asleep frees that.
  Replaying the last 7 days, Napwake would have paused 24 of 26 unmanaged workloads for 2,796 hours in all,
  and freed $159: about $691/month.
  ...
```

The workloads follow, idle first, then the report opens in your browser.

What it reads, and why

It reads the cluster of your kubeconfig’s current context, or the one `--context` names.

| It reads                                                                                       | To                                                                                                |
| ---------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------- |
| Namespaces, Deployments and StatefulSets, with their ReplicaSets, ControllerRevisions and pods | Find the workloads, and when each last rolled out                                                 |
| Pod metrics, from metrics-server                                                               | Measure CPU right now                                                                             |
| Prometheus, through the API server                                                             | Replay each workload’s last 7 days                                                                |
| Nodes, and what scales them                                                                    | Price each workload at its node’s list price, and say what would turn freed capacity into savings |
| Services, and the ConfigMaps workloads take variables from                                     | Find what each workload depends on                                                                |
| ManagedWorkloads                                                                               | Show what Napwake does, once it’s installed                                                       |
| Argo CD’s Applications and `argocd-cm`, for a workload Argo CD applies                         | Say whether Argo CD would undo a pause                                                            |

The standard `view` role covers what it reads in each namespace. Nodes, and the `services/proxy` it reads Prometheus through, aren’t in it. Without a read, the scan carries on, and its notes say what it couldn’t see.

The scan finds a Prometheus in the cluster and replays each workload’s last 7 days of CPU and rollouts. It shows how long each would have slept, how often it would have woken, and what that would have freed. Without one, it judges from CPU right now, and can’t say how often a workload would wake.

Limited access, other setups

| If                                                         | The scan                                                                                                            |
| ---------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| You can’t list namespaces                                  | Scans your context’s namespace. Name others with `-n`                                                               |
| You can’t use `services/proxy`                             | Judges from CPU right now, and prints the two commands an admin can run to let you read that one Prometheus Service |
| Prometheus is outside the cluster, such as Thanos or Mimir | Reads it with `--prometheus-url`. See [A Prometheus elsewhere](https://docs.napwake.dev/0.2/reference/cli/#a-prometheus-elsewhere)          |
| There’s no metrics-server, as on EKS by default            | Judges workloads from their Prometheus history                                                                      |
| There’s neither                                            | Can’t measure CPU, and its notes say so                                                                             |

[Permissions](https://docs.napwake.dev/0.2/reference/cli/#permissions) lists every read.

## 3. Read the report

[![The scan report for an example cluster. It leads with 19 of 31 workloads running with no sign of use, then 54% less. Paused when idle, they could cost about $600 a month instead of $1,290.](https://docs.napwake.dev/0.2/home/scan-report-light.webp)](https://napwake.dev/examples/scan-report/)

[![](https://docs.napwake.dev/0.2/home/scan-report-dark.webp)](https://napwake.dev/examples/scan-report/)

The headline is what pausing idle workloads could take off their monthly cost, such as 54% less in this example.

| To share it      | You get                                                      |
| ---------------- | ------------------------------------------------------------ |
| **Share card**   | A PNG of the totals, wide or square, with no workload’s name |
| **Copy summary** | Three plain lines                                            |
| **Save as PDF**  | The whole report                                             |

The card and summary name the cluster only if you tick it. Nothing is sent anywhere.

The file, and other headlines

|                               |                                                                                                                                                 |
| ----------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| The file                      | One file in your temporary directory, `napwake-scan-` and a number, that only you can read. The scan’s last line, `Report:`, gives its path     |
| When it opens                 | When you scan from a terminal on a desktop. Over SSH, piped or with `-o json`, there’s no report unless you pass `--html`                       |
| Saving it                     | `--html scan.html` saves it where you choose                                                                                                    |
| Sending it                    | The file loads nothing from elsewhere, so you can send it as it is                                                                              |
| The example’s headline        | About $600 a month instead of $1,290, 54% less                                                                                                  |
| Without Prometheus history    | The headline is what the idle workloads cost while running, the most pausing could free                                                         |
| Under a day of history        | It asks you to scan again after a few days                                                                                                      |
| Once Napwake pauses workloads | It leads with what Napwake freed this month                                                                                                     |
| Your bill                     | What pausing frees comes off it only as a node autoscaler removes nodes. The `Your bill:` line says what yours does                             |
| The share card                | Your browser draws it. Wide is 1200 × 630, for Slack, LinkedIn and X. Square is 1200 × 1200, for LinkedIn’s feed                                |
| Copy summary                  | Three plain lines, for an email or chat                                                                                                         |
| Save as PDF                   | To print or attach                                                                                                                              |
| Its end                       | Next steps, as the terminal ends. They give the commands to install Napwake and measure, in dry-run, the idle workload that could free the most |

[The web report](https://docs.napwake.dev/0.2/reference/cli/#scan-report) says when each headline shows.

Next: install Napwake and pause one workload

* [Installation →Helm first, then Argo CD, Flux or the Kustomize manifests.](https://docs.napwake.dev/0.2/getting-started/installation/)
* [Quickstart →Try it on a kind cluster first, with a sample workload.](https://docs.napwake.dev/0.2/getting-started/quickstart/)
