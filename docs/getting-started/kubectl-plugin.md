# kubectl Plugin

The `kubectl hybernate` plugin scans a cluster for idle workloads, replays Hybernate's forecast over their history, shows what Hybernate is doing in it and why its nodes aren't scaling down, ends dry-run for workloads you've opted in, pauses workloads on demand, and wakes paused ones.

## Which Command

`scan` and `status` both show idle workloads and money, from opposite ends: `scan` is the survey of a whole cluster, managed or not, of what could be paused and what that could free; `status` is what Hybernate is doing with the workloads it manages. The rest answer narrower questions.

| Command | Use it to | Needs the operator |
|---------|-----------|--------------------|
| [`scan`](#scan-a-cluster) | Find what's idle in a cluster, what it costs, and what pausing it could free, replaying the last week from Prometheus. Before you install Hybernate, or after, to see what's left to opt in | No. Once it's installed, the scan also shows what it has paused and freed |
| [`status`](#see-what-hybernate-is-doing) | See what Hybernate is doing with what it manages: each workload's state, when it pauses next or what holds it awake, what needs attention, what pausing has freed and saved, and the latest pauses and wakes | Yes |
| [`forecast`](#replay-the-forecast) | Check, before you turn on `autoResume`, how the forecast would do on your workloads' own history; name a workload to see [its forecast](#one-workloads-forecast), and for one Hybernate manages, what its own forecast is doing now | No, but it needs Prometheus history. A workload's own forecast needs the operator, and no history |
| [`nodes`](#see-why-nodes-dont-scale-down) | Find out why what pausing frees isn't coming off the bill: the node autoscaler, and what keeps each node from being removed | No. Once it's installed, it also shows what paused workloads would request if they woke |
| [`enable`](#enable-a-workload) | End dry-run for a workload, or a namespace, once you trust what Hybernate measured | Yes |
| [`pause`](#pause-a-workload) | Pause a workload now rather than when its idle clock runs out, such as a preview at the end of the day | Yes |
| [`wake`](#wake-a-workload) | Wake a paused workload now, or keep one awake for a while with `--for`, such as for a demo | Yes |
| [`deps`](#show-a-workloads-dependencies) | See what Hybernate wakes and holds awake with a workload, and what depends on it | Yes |
| `version` | Print the plugin's version | No |

## Namespaces and Connections

`scan`, `forecast` and `status` read every namespace you can, and fall back to your kubeconfig context's namespace, with a note, when your access doesn't allow listing them all; `-n` names namespaces instead, and `-A` (`--all-namespaces`) insists on every one, failing rather than falling back. `pause`, `wake`, `deps` and `enable` work in one namespace: `-n`, or your context's, as kubectl does.

Every command takes kubectl's connection flags: `--kubeconfig`, `--context`, `--cluster`, `--user`, `--as`, `--as-group`, `--as-uid`, `--token`, `--server`, `--certificate-authority`, `--client-certificate`, `--client-key`, `--insecure-skip-tls-verify`, `--tls-server-name`, `--proxy-url`, `--username`, `--password`, `--disable-compression` and `--request-timeout`. Each request to the API server gives up after 30 seconds unless `--request-timeout` sets otherwise (`0` keeps the 30 seconds, unlike kubectl, where it means no limit), so a cluster that stops answering ends a command with an error, not a hang. `kubectl hybernate version` prints the plugin's version.

## Installation

=== "Homebrew"

    On macOS and Linux:

    ```bash
    brew install okedeji/tap/kubectl-hybernate
    kubectl hybernate version
    ```

    Homebrew installs the newest release, or, until the first stable release, the newest release candidate, and `brew upgrade` keeps it current. For this version of the docs exactly, use Krew or a download.

=== "Krew"

    With [krew](https://krew.sigs.k8s.io/docs/user-guide/setup/install/), kubectl's plugin manager, installed:

    ```bash
    kubectl krew install --manifest-url \
      https://github.com/okedeji/hybernate/releases/download/v0.2.0/krew-hybernate.yaml
    kubectl hybernate version
    ```

    Krew installs the plugin on Linux and macOS (amd64 and arm64) and on Windows (amd64). `kubectl krew upgrade` doesn't upgrade a plugin installed from a manifest URL: to move to a later release, run `kubectl krew uninstall hybernate`, then install that release's manifest.

    !!! note
        Once the plugin is accepted into the [krew-index](https://github.com/kubernetes-sigs/krew-index), you'll be able to install it with just `kubectl krew install hybernate`, and upgrade it with `kubectl krew upgrade`.

=== "Download"

    Download the archive for your platform from the release, and put the binary on your `PATH` as `kubectl-hybernate`. On macOS and Linux:

    ```bash
    os=$(uname -s | tr '[:upper:]' '[:lower:]')
    arch=$(uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
    curl -fLO "https://github.com/okedeji/hybernate/releases/download/v0.2.0/kubectl-hybernate-${os}-${arch}.tar.gz"
    tar xzf "kubectl-hybernate-${os}-${arch}.tar.gz"
    sudo install "kubectl-hybernate-${os}-${arch}" /usr/local/bin/kubectl-hybernate
    kubectl hybernate version
    ```

    On Windows, download `kubectl-hybernate-windows-amd64.tar.gz` from the [release](https://github.com/okedeji/hybernate/releases/tag/v0.2.0), extract it with `tar xzf`, and rename `kubectl-hybernate-windows-amd64.exe` to `kubectl-hybernate.exe` in a folder on your `PATH`. To check that an archive came from the release workflow, see [Verifying a release](../reference/security.md#verifying-a-release).

=== "Source"

    ```bash
    git clone https://github.com/okedeji/hybernate.git
    cd hybernate
    make build-plugin
    # Binary is at bin/kubectl-hybernate
    ```

## Scan a Cluster

```bash
kubectl hybernate scan
```

```
staging (EKS us-east-1): 214 workloads in 38 namespaces
Node autoscaler: Karpenter (consolidates when underutilized after 1m)
What pausing frees comes off the bill only as nodes scale down.
History: the last 7 days, from monitoring/prometheus-operated

  Hybernate has 12 workloads paused right now, freeing $1.84 an hour.
  Hybernate has freed $96 this month pausing 14 live workloads, and saved $71 of it.
  3 workloads are in dry-run: measured by Hybernate since starting, they would have slept 312 hours, freeing $41,
  about $140 a month.
  4 workloads are scaled to zero by hand; Hybernate can pause them while idle and wake them on the
  next request instead.
  61 workloads are idle right now, reserving 48.0 vCPU and 190.0 GiB of memory.
  They cost $3,180/month while running, $4.36 an hour: each hour asleep frees that.
  Replaying the last 7 days, Hybernate would have paused 58 of 190 unmanaged workloads for 6,120 hours in all,
  and freed $1,240: about $5,380/month.
  Not counted: 4 scaled to zero by hand and 1 without CPU history to replay.

  NAMESPACE    WORKLOAD               STATE                BECAUSE                                                  COST/MO   FREED THIS MONTH   SAVED   COULD SLEEP   WAKES   COULD FREE/MO
  preview-42   statefulset/postgres   idle (unmanaged)     CPU under 1% of its request; last deployed 23 days ago   $140      -                  -       167h          0       $139
  preview-7    deployment/api         idle (dry-run)       no activity for 5h; measuring since Oct 1                $96       -                  -       41h           4       $40
  preview-3    deployment/web         paused (live)        paused 3h ago                                            $48       $12                $9      -             -       -
  preview-9    deployment/demo        paused (unmanaged)   scaled to zero, not by Hybernate                         $0        -                  -       -             -       -
  payments     deployment/ledger      active (unmanaged)   CPU 64% of its request                                   $210      -                  -       0h            0       $0
  ...

Notes:
  - 2 workloads set no CPU requests, so their use can't be measured: kube-tools/agent, ops/exporter
  - 23 infrastructure workloads are left out of the totals and next steps: monitoring (9), argocd (7), ingress-nginx (3) and 2 more. Hybernate never pauses Kubernetes' own workloads or itself; anything else, infrastructure included, pauses only if you opt it in
  - the history replay sees CPU and rollouts only; requests and activity annotations aren't in Prometheus, so a workload used with little CPU can look like it would sleep more than it would

Costs use the on-demand and spot list prices of the cluster's 4 node types (m6i.xlarge, m6i.2xlarge, r6i.xlarge and 1 more, AWS us-east-1). 6 workloads run on spot nodes, priced at spot.
Pass --cpu-price and --memory-price for yours.
```

`scan` only reads, with your own kubeconfig, and needs nothing installed in the cluster. It works before you install Hybernate, and after: once Hybernate manages a workload, the scan shows what it has paused and reads its activity clock.

**How it decides a workload is idle**, using the same signals Hybernate uses to pause:

- **Workloads Hybernate manages:** the activity clock it keeps for them, which already combines every source configured, including Prometheus queries. Idle means no activity for `idleAfter`; the scan says when Hybernate is holding one awake, for example for its dependents, and, for one whose clock started without activity, what started it: `not idle yet: it woke 3m ago`, `not idle yet: Hybernate began managing it 3m ago`, or `not idle yet: Hybernate wasn't watching until 3m ago`.
- **Other workloads:** what the scan can see without Hybernate. Any of these makes a workload active: CPU above `--cpu-threshold` (10% by default) of what it requests, a rollout within `--idle-after` (1h by default), a `hybernate.io/last-activity` annotation within it, or a `hybernate.io/active-until` in the future. Otherwise, with CPU measured, it's idle.
- **Workloads it can't judge**, because they set no CPU requests or have no metrics yet, are listed in the notes rather than the table, and left out of the headline's totals. Without metrics-server, as on EKS by default, an unmanaged workload with CPU history is judged from it instead: its state is where replaying its history left the activity clock, and BECAUSE says so.
- **Replicas set from Git:** a workload whose replicas a GitOps tool (Argo CD, Flux, Fleet, kapp or Kluctl) last set, from the field managers the API server records, is listed in the notes by tool: its tool would undo every pause until it's told to leave the replicas to Hybernate, a one-time setting given in [GitOps tools](../guides/gitops.md). Once it's told, the workload isn't listed: for Argo CD, the scan reads that from the workload's Application and `argocd-cm`, as [`enable`](#enable-a-workload) does, and when your access doesn't allow reading them, the note says so and lists the workload as one to check. JSON gives the tool in each workload's `replicasFromGit`, with `gitOpsLeavesReplicas` once it's told, and `gitOpsUnchecked` saying why that couldn't be read. The next steps never suggest one, since its tool would revert a label put on by hand too.
- **The cluster's own infrastructure** is left out of the table, the totals and the next steps, and a note says how many workloads in which namespaces. The first line counts only the namespaces the listed workloads are in, so a cluster with 15 namespaces, 12 of them infrastructure, reads `14 workloads in 3 namespaces`; JSON's `namespaces` is every namespace read. JSON and YAML list the infrastructure in `platform`, each with what it is. Two kinds are left out, for different reasons:
    - **What Hybernate never pauses:** Kubernetes' namespaces (`kube-*`, and `openshift-*`, `cattle-*`, `gke-*` and `gmp-*`), the namespace Hybernate runs in, found by its Deployment's `app.kubernetes.io/name: hybernate` label, and, in any namespace, the operator's and the doorman's own Deployments, found by their `control-plane: controller-manager` or `control-plane: doorman` label with `app.kubernetes.io/name: hybernate` or a `hybernate` `helm.sh/chart` label. Apps' dependencies on these aren't listed, as Hybernate never needs to wake them.
    - **What the scan guesses nobody means to pause:** well-known infrastructure such as ingress controllers, cert-manager, Vault, Argo CD, Flux, service meshes, policy engines and monitoring, by their usual namespaces (`ingress-nginx`, `cert-manager`, `vault`, `argocd`, `flux-system`, `istio-system`, `linkerd`, `gatekeeper-system`, `kyverno`, `monitoring` and others) or their `app.kubernetes.io/name`, `app.kubernetes.io/part-of`, `app`, `k8s-app` or `helm.sh/chart` labels. This is only the estimate's exclusion, not an operator rule: opted in, Hybernate pauses these like any app, so apps' dependencies on them are still listed, and none is guessed at in a namespace labelled `hybernate.io/managed=true`, such as a preview environment with its own Vault, or for a workload Hybernate already manages.

    A namespace labelled `hybernate.io/allow-protected=true` is scanned like any other, apart from Hybernate's own Deployments.

**History from Prometheus:** when the cluster has a Prometheus, the scan replays Hybernate's activity clock over the last week of each workload's CPU and rollouts, as if Hybernate had managed it with `--idle-after` and `--cpu-threshold`:

- It finds Prometheus by the Services the Prometheus Operator (and so kube-prometheus-stack) and the community Helm chart create, and queries it through the API server's service proxy, so there's no port-forward or URL to give. The `History:` line under the node autoscaler, and the report's subtitle, name the one it read and how much history it replayed (JSON `history.prometheus` and `history.hours`).
- **With several Prometheus**, it reads the one with the most of the window's CPU history: one with cAdvisor's container CPU from the start of the window, then one with any, then any that answers. Among equals, it prefers one in a monitoring namespace (`monitoring`, `prometheus`, `observability`, `kube-prometheus-stack`, `cattle-monitoring-system`, `openshift-monitoring`, in that order), then one with a usual name (`prometheus-operated`, `kube-prometheus-stack-prometheus`, `prometheus-server`, `prometheus`, `prometheus-k8s`), then the Prometheus Operator's Service over the community chart's, then the first by namespace and name, and it tries five at most. The `History:` line lists the others it found (JSON `history.others`).
- **To choose another**, `--prometheus-service namespace/name` reads that Service through the API server the same way, whether the scan found it or not, on its API port or the one you add, as in `monitoring/thanos-query:http`. `--prometheus-url` reads one outside the cluster, such as Thanos or Mimir; see [A Prometheus elsewhere](#a-prometheus-elsewhere). Either fails the scan if it can't be read, rather than carrying on without history.
- It reads CPU per container from cAdvisor's `container_cpu_usage_seconds_total`, which every common setup scrapes, at five-minute steps (coarser for windows over about 34 days), and matches pods to their workload by name. Sidecars added at pod creation are left out of activity, as for the activity clock.
- A step counts as active when the workload's own CPU is above the threshold of what its running pods request, as the operator counts it, or it rolled out. After `--idle-after` with none, it would be asleep until the next, which is one wake.
- **STATE** is right now: idle means no activity for `--idle-after`, so Hybernate would pause it now. Money is in whole dollars, as `status` shows it: `<$1` for an amount above zero that rounds to nothing, and `$0` only for none. Each money and history cell has one source:
    - **COULD SLEEP** and **WAKES** are what Hybernate measured for a workload in dry-run, since dry-run started (BECAUSE says when), and for an unmanaged workload come from the replay: had Hybernate been managing it, the running hours it would have been paused, after waiting `--idle-after` from each activity, and how many times activity would have arrived while it was asleep, each starting it again and making whoever caused it wait.
    - **FREED THIS MONTH**, for live workloads, is what Hybernate has freed pausing it since the 1st, from the ManagedWorkload's `status.cost.freedThisMonth`, priced at the rates Hybernate uses for the workload, and **SAVED** the part of it that came off the bill as nodes scaled down (`savedThisMonth`). A workload put back in dry-run keeps what it freed and saved while live; dry-run's would-be pauses never add to them. The headline's `pausing 14 live workloads` counts only the live workloads Hybernate pauses, and what they and any put back in dry-run freed; with none live, it says the money came from workloads since put back in dry-run only when there are some. Those it never pauses, in a protected namespace, part of Hybernate, or a StatefulSet that deletes its PVCs when scaled down (`Protected=True` on its ManagedWorkload), are counted on a line of their own, with what they freed this month before they were protected, if anything: `1 more live workload is protected, so Hybernate never pauses it; it freed $12 this month before it was, and saved $5 of that.` (JSON `totals.live`, `totals.liveProtected`, and `totals.protectedFreedThisMonth` and `protectedSavedThisMonth`, which are part of `freedThisMonth` and `savedThisMonth`).
    - **COULD FREE/MO**, for workloads Hybernate doesn't pause yet, is what pausing would free a month: what Hybernate measured for one in dry-run, the same figure its BECAUSE line gives, or what the replay estimates for an unmanaged one. The scan can't know what of it would be saved before Hybernate runs, so it never calls it a saving.
- A live workload isn't replayed: while it's paused, there are no pods for Prometheus to record. Its pauses are what FREED THIS MONTH counts.
- The headline's replay count, such as `0 of 13 unmanaged workloads would have slept`, is of the unmanaged workloads replayed. When the table lists others it doesn't count, a `Not counted:` line says which and why, as the report's summary does: ones that won't pause (`1 that won't pause (PVCs deleted on scale-down)`), in protected namespaces, scaled to zero by hand, or without CPU history to replay.
- **Node autoscaler,** under the first line, is what would turn what pausing frees into savings by removing nodes, found as [`nodes`](#see-why-nodes-dont-scale-down) finds it (JSON `nodeAutoscaler`): Karpenter, Cluster Autoscaler, EKS Auto Mode or GKE Autopilot; `none found`, when nothing in the cluster will remove nodes, so nothing pausing frees comes off the bill; or `unknown`, when your access doesn't allow reading what would tell.
- A Deployment's history is its pods' CPU, including pods of ReplicaSets since deleted, which are known by the names Kubernetes gives them, `<deployment>-<hash>-<suffix>`. A Deployment whose name is long enough for Kubernetes to cut its pods' names short has only its current ReplicaSets' pods replayed.
- Requests and activity annotations aren't recorded in Prometheus, so the replay only knows CPU and rollouts, and a workload used with little CPU can look like it would sleep more than it would. Requests and prices are today's.
- History gets the first 60% of `--timeout`, so a Prometheus or Thanos too slow for the window can't use up the whole scan. Each namespace's reads from the API server come before its history query and share that time, and a namespace reached once it's spent isn't queried at all. A namespace's query may take whatever is left of that time, however long, and isn't cut short by `--request-timeout`. Namespaces whose history isn't read in that time are judged from CPU right now, and the notes name them and say a longer `--timeout` gives history more time, or `--window 0` skips it. When Prometheus, or the API server's proxy in front of it, gives up on a query by itself with time left, a longer `--timeout` wouldn't help: the notes name those namespaces and suggest a shorter `--window`.
- If Prometheus has less history than the window, the replay covers what it has, and says how much wherever it names the window, such as "Replaying the last 30 minutes", with a note that it's shorter than asked. Under a day of it can't show a day's pattern, so the headline says so and to run the scan again after a few days, in the terminal and the report. Without Prometheus, or without permission to reach it, the scan judges from CPU right now, and says why.

**Dependencies:** the scan reads each workload's environment variables, from literal values and ConfigMaps, and lists the workloads they point at:

```
  3 dependencies found, and what Hybernate does with each:
  preview-42   deployment/checkout-api   ->   statefulset/postgres         DATABASE_URL   declared; woken and held with it
  preview-42   deployment/checkout-api   ->   messaging/statefulset/nats   NATS_URL       not managed, so never paused
  preview-42   deployment/worker         ->   statefulset/postgres         PGHOST         woken with it once both are managed
```

- An address counts when it names a Service in a scanned namespace, in any form cluster DNS gives it (`postgres`, `postgres.preview-42`, `postgres.preview-42.svc.cluster.local`, or a headless Service's pod, `postgres-0.postgres-hl`), inside a URL, a `host:port`, or a list of either. The workload is the one the Service's selector picks. A bare word on its own, such as `MODE=postgres`, isn't taken for an address.
- Variable names don't matter, and Service names come from the cluster, not a list the scan keeps.
- An address is shown as its scheme, host and port alone, such as `postgres://postgres:5432`: a user, a password, a path and a query string can each hold a credential, so none of them is. The terminal shows the variable each dependency was found in; only JSON and YAML carry the address itself.
- Hybernate wakes a dependency with the workload that needs it, so the first request doesn't wait for it too, and keeps it up while that workload is, when it manages both; see [Dependencies](../concepts/dependencies.md). The last column says what it does with each: **woken with it once both are managed**, for a workload it doesn't manage yet; **not managed, so never paused**, for a dependency it doesn't manage, which stays up anyway; **woken and held with it**, learned from the workload's environment or a wake; **declared; woken and held with it**, with `hybernate.io/depends-on`; or **not learned yet**, for one Hybernate manages but hasn't learned. Before Hybernate manages any of the workloads that need them, every dependency would say the first, so the scan and the report say it once above the list and leave the column out.
- Secrets are never read, so addresses set there, and ones built in code or read from files, aren't found. The notes say which workloads take variables from Secrets.

**What the numbers mean:**

- Costs are what the workloads' requests cost while running, sidecars included, at the list price of the nodes each one's pods run on, from their instance type and region, spot nodes at their spot price; see [Prices](../concepts/cost-tracking.md#prices). A workload with no pod on a node is priced where Hybernate last saw it run, or at the cluster's most common node type. Nodes Hybernate has no list price for, and every workload when your access doesn't allow listing nodes, use assumed prices from AWS on-demand in us-east-1, and the report says which.
- `--cpu-price` and `--memory-price` set your own prices, which price every workload in place of list prices: use them for discounts, savings plans, or negotiated rates.
- Spot and preemptible nodes are priced at their type's spot price, and the report says how many workloads run on them. A spot node of a type its provider doesn't sell as spot is priced at on-demand, and the report names those workloads' node types and says they cost less than shown. JSON gives each workload's `onSpot` and `spotAtOnDemand`, and each node type in `nodePrices` its `spot` and `atSpot`; see [Spot nodes](../concepts/cost-tracking.md#spot-nodes).
- Without history, the scan shows what idle workloads cost and what each hour asleep frees, not what pausing would free a month: from one moment it can't tell how often a workload would be woken. To measure it once Hybernate is installed, label workloads `hybernate.io/managed=true` and annotate them `hybernate.io/dry-run=true`: Hybernate measures how often it would have paused them, for how long, and what that would have freed, without ever pausing them. The scan then shows it in each one's COULD SLEEP, WAKES, and COULD FREE/MO, with BECAUSE saying since when, a total in the headline, and the `kubectl hybernate enable` command to start pausing. It's from Hybernate's own activity clock, which sees more than the history replay, so it's what to judge going live by.
- An hour asleep frees capacity; it becomes money only when a node autoscaler removes it. `kubectl hybernate nodes` shows what keeps nodes from scaling down.
- BECAUSE gives the evidence for the state, then facts such as when a workload was last deployed, if a week or more ago.
- A StatefulSet whose `persistentVolumeClaimRetentionPolicy.whenScaled` is `Delete` would lose its data to a pause, so Hybernate refuses to pause it: BECAUSE says `won't pause: PVCs deleted on scale-down` (JSON `wontPause`), and it's left out of what pausing could free and of next steps.

**The HTML report:** run in a terminal, the scan also writes the same report as a web page and opens it in your browser. It's one self-contained file, with no scripts, fonts, or images loaded from anywhere (its sorting and filtering are built in), so it can be emailed or attached, and it prints cleanly to PDF. It's written for whoever you send it to:

- the node autoscaler, under the prices
- two headline figures. Until Hybernate has paused anything, they're the opportunity: what pausing could free a month (measured for dry-run workloads, estimated from history for unmanaged ones), as a share of what those workloads cost, "over the same time" when history was replayed, since a replayed workload is measured against what it cost over the history, with the pods it ran then, or, without either, what's idle right now costs while running, the most pausing it could free; and beside it, what the workloads cost a month. A zero says why: with under a day of history, short of the window, that it's too little history yet, so run the scan again after a few days. Once Hybernate has paused workloads, the first is what it has freed this month, with what of it was saved, and the second what pausing could free, while there's something to
- four facts: how many workloads in how many namespaces (what they cost, once that's left the headline), what's idle right now, how many unmanaged workloads would have slept and which listed ones the replay doesn't count (with history) or what idle workloads cost an hour (without), and how many Hybernate manages
- a table by namespace (with a filter), every workload scanned that the scan could judge, with the same columns as the terminal (sortable, with a filter), and the dependencies found
- what the numbers mean, in plain words, and the next steps

Dependency addresses are left out of the page, since it's meant to be passed around; JSON and YAML keep them. The report file is created readable only by you. A scan covers one cluster, the current kubeconfig context or the one you name with `--context`.

The file goes to your temporary directory unless you pass `--html FILE`. It opens only when the table is shown in a terminal on a machine with a desktop; piped output, `-o json`, CI, and SSH sessions (unless X forwarding gives a Linux one a display) get no report unless you ask for one with `--html`. `--open=false` keeps it from opening.

**Cluster names:** EKS and GKE contexts are shortened to the cluster with its provider and region, such as `staging (EKS us-east-1)` for `arn:aws:eks:us-east-1:123456789012:cluster/staging`. Any other context is shown as named. JSON and YAML keep the full context name.

**STATE** is what the workload is doing right now, and the bracket how Hybernate is involved: **unmanaged**, not at all; **dry-run**, it measures but never pauses; **live**, it pauses the workload while idle; **protected**, its namespace is labelled `hybernate.io/protected`, so Hybernate won't manage it, and it's left out of what pausing could free and of the next steps. The operator's `protectedNamespaces` patterns aren't visible to the scan, which only sees the label. A workload someone scaled to zero shows as `paused (unmanaged)`: it costs nothing now, but nothing will wake it on a request, and the headline counts it apart from what Hybernate has paused. Workloads labelled `hybernate.io/ignore=true` aren't listed.

```bash
# Another cluster in your kubeconfig
kubectl hybernate scan --context staging

# Some namespaces, as JSON or YAML, at your own prices
kubectl hybernate scan -n preview-42 -n preview-918 -o json --cpu-price 0.045 --memory-price 0.006

# A month of history from a Thanos that holds many clusters, with a 2h idleAfter
kubectl hybernate scan --window 30d --idle-after 2h --prometheus-url https://thanos.example.com \
  --prometheus-selector 'cluster="staging"'

# CPU right now only
kubectl hybernate scan --window 0

# Save the report to send around
kubectl hybernate scan --html workload-scan.html

# The terminal only
kubectl hybernate scan --open=false
```

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--namespace` | `-n` | all you can read | Namespace to scan; repeat for several |
| `--all-namespaces` | `-A` | | Read every namespace, and fail rather than fall back when your access doesn't allow that. Can't be combined with `-n` |
| `--exclude-namespaces` | | `kube-system`, `kube-public`, `kube-node-lease` | Namespaces to skip |
| `--output` | `-o` | `table` | `table`, `json`, or `yaml` |
| `--limit` | | `25` | Workloads listed in the table, idle first, then paused, then active, each by what pausing could free; `0` for all. Also caps the dependency list. JSON and YAML list everything |
| `--cpu-threshold` | | `10` | CPU use, as a percentage of requests from 1 to 100, above which a workload counts as active, now and in the replay. Workloads Hybernate manages use their own setting |
| `--cpu-price` | | `0.031` | Your price per vCPU-hour, in dollars, zero or more |
| `--memory-price` | | `0.004` | Your price per GiB-hour of memory, in dollars, zero or more |
| `--window` | | `7d` | How much Prometheus history to replay, such as `7d` or `36h`, up to `366d`; `0` judges from CPU right now only |
| `--idle-after` | | `1h` | How long without activity makes a workload idle, now and in the replay, as Hybernate's `idleAfter`. Workloads Hybernate manages use their own setting |
| `--prometheus-service` | | the one found in the cluster with the most history | Prometheus Service in the cluster to read history from, as `namespace/name` or `namespace/name:port`, through the API server |
| `--prometheus-url` | | the one found in the cluster with the most history | Prometheus API to read history from, such as Thanos, Mimir, or Google Cloud Managed Service for Prometheus |
| `--prometheus-selector` | | | Label matchers added to every history query, such as `'cluster="prod"'`, for a Prometheus that holds more than one cluster. Values are double-quoted, as in PromQL |
| `--prometheus-header` | | | Header to send to `--prometheus-url`, as `"Name: value"`, such as `"X-Scope-OrgID: tenant"` for Mimir; repeat for several |
| `--prometheus-bearer-token-file` | | | File holding a bearer token to send to `--prometheus-url` |
| `--prometheus-ca-file` | | | PEM file of CA certificates to trust for `--prometheus-url`, besides the system's |
| `--prometheus-insecure-skip-verify` | | `false` | Don't verify `--prometheus-url`'s certificate |
| `--timeout` | | `5m` | How long the scan may take before it gives up |
| `--html` | | a temporary file | Save the HTML report to this file, to share |
| `--open` | | `true` | Open the HTML report in your browser, when the table is shown in a terminal |

### A Prometheus elsewhere

`--prometheus-url` reads history from a Prometheus API your machine can reach, rather than one found in the cluster, and needs no cluster permission at all. If it doesn't answer like a Prometheus API, the scan fails rather than carrying on without history.

- **Several clusters in one store**, such as Thanos or Mimir: pass `--prometheus-selector` with the label that tells them apart, so the scan reads only this cluster's series. When it finds series from more than one source, the notes suggest it.
- **Authentication**: `--prometheus-header` (for example `X-Scope-OrgID` for a Mimir tenant, or an `Authorization` header), or `--prometheus-bearer-token-file`, but not both for the same header. `--prometheus-ca-file` and `--prometheus-insecure-skip-verify` are for its certificate. These flags only apply with `--prometheus-url`: a Prometheus found in the cluster is reached through the API server with your kubeconfig.
- **Google Cloud Managed Service for Prometheus**, GKE's managed Prometheus, keeps its data in Cloud Monitoring rather than in the cluster: `gmp-system` runs its collectors, which have nothing to query, so the scan doesn't find it there and reads another Prometheus if the cluster has one. To read it instead, give it an access token and name your project and cluster, as the store holds every cluster in the project:

    ```bash
    kubectl hybernate scan \
      --prometheus-url https://monitoring.googleapis.com/v1/projects/PROJECT_ID/location/global/prometheus \
      --prometheus-bearer-token-file <(gcloud auth print-access-token) \
      --prometheus-selector 'cluster="CLUSTER_NAME"'
    ```

    If you've deployed its frontend in the cluster, `--prometheus-service NAMESPACE/frontend` reads it through that instead, with the frontend's own credentials. Either way, its history has the CPU the replay reads only if it collects cAdvisor's metrics, which `kubeletScraping` in its OperatorConfig turns on.
- **Amazon Managed Service for Prometheus** needs requests signed with SigV4, which the scan doesn't do: point `--prometheus-url` at a signing proxy in front of it.

The source is shown in the report with any password in its URL hidden.

### Exit status

The scan exits 0 when it read everything your access allows. Namespaces or resources your access doesn't allow (a `403`) are explained in the notes, and don't change the exit status, unless you named the namespace with `-n`.

It exits 1, after writing the report, when it couldn't read something it should have: a timeout, throttling, a server error, a failed Prometheus query, or a `403` or missing namespace among those you named with `-n`. The notes say what was missed, the error says what to do about it (check a namespace's name, ask for access, or try again with a longer `--timeout`), and JSON and YAML list the namespaces in `incomplete`, by why: `missing`, `denied`, `failed`, or `unscanned`. JSON and YAML also give the number of namespaces read in `namespaces`, and `"workloads": []` for a cluster with none.

A scan that runs out of `--timeout` still reports the namespaces it scanned in time, and lists the rest as `unscanned`; only one that scanned none fails without a report. Scanning every namespace, the scan lists each kind of object across the cluster at once, a page at a time, rather than in each namespace, so a cluster of thousands of namespaces takes a few dozen requests. A kind your access only lets you list namespace by namespace is read that way.

Workloads someone else already scaled to zero count toward nothing the scan says could be freed: they cost nothing now, and the headline counts them apart.

### Permissions

Your user needs to list Deployments, StatefulSets, ReplicaSets, ControllerRevisions, Pods, pod metrics, Services and ManagedWorkloads in the namespaces scanned, and to read the namespaces and the ConfigMaps workloads take variables from; to list namespaces unless you name them with `-n`; and to list nodes, to price workloads at their nodes. `forecast` also lists Deployments, across the cluster or else in the operator's namespace (`hybernate-system` by default), to read the doorman's settings from the operator's own flags; without either it assumes the doorman's defaults, on and without the NEG forwarder, and its notes say so. Naming the node autoscaler reads what [`nodes`](#see-why-nodes-dont-scale-down) does for that. The standard `view` role covers the namespaced reads, ManagedWorkloads too, which Hybernate's chart and manifests add to `view`; it doesn't cover nodes, which are cluster-wide. Without one of these the scan carries on, and the notes say what it couldn't see. Reading history through the service proxy also needs `get` on `services/proxy` for the Prometheus Service, which `view` doesn't include. Without it, the scan judges from CPU right now and prints what an admin can run to let you, for that one Service and nothing else:

```
To replay history, an admin can let you read Prometheus, and nothing else, with:
  kubectl create role hybernate-scan -n monitoring --verb=get --resource=services/proxy --resource-name=prometheus-operated:web
  kubectl create rolebinding hybernate-scan -n monitoring --role=hybernate-scan --user=jane@example.com
Or pass --prometheus-url if Prometheus is reachable from your machine.
```

A Prometheus you name with `--prometheus-service` fails the scan instead, with the same commands in its error; reading it also needs `get` on that Service, to find its port, which `view` covers.

That's only when the API server refuses you the proxy. A `403` from behind it, such as from a `kube-rbac-proxy` in front of Prometheus, is shown as Prometheus's own answer, since a Role for `services/proxy` wouldn't change it.

## Replay the Forecast

```bash
kubectl hybernate forecast -n preview-42
```

```
staging: the forecast replayed over 28 days of CPU history from monitoring/prometheus-operated, times in UTC

NAMESPACE   NAME              FORECAST             READY  COLD  WOKEN FOR NOTHING  WOKEN             WAKES ON
preview-42  api               acts from day one    9/10   1     0                  08:45, 25m ahead  request
preview-42  worker (managed)  acts from Mon 2 Nov  3/10   7     2 (3.5h)           09:45, 20m ahead  query
preview-42  reports           not within 4 weeks   0/6    6     0                  -                 nothing
```

`forecast` shows what Hybernate's [forecast](../concepts/forecasting.md) would make of your workloads, from their own CPU history, before you trust `autoResume` with them. It runs the operator's own forecast and decision rules, through the [simulator](../concepts/forecasting.md#proving-it), over the history scan reads:

- **FORECAST** is when the forecast would act if the workload were opted in today, having [learned all its history](../concepts/forecasting.md#learning-from-history) as the operator does for a new workload: `acts from day one`, from a date it projects, or `not within 4 weeks` for a pattern too irregular for the setting. For a workload Hybernate already manages, marked `(managed)` beside its name, it's that forecast's own, from `status.prediction`: `acting`, `learning`, from a date it projects, `not within 4 weeks` when it's earning its confidence, or relearning, and isn't expected to act within four weeks, or, while it's paused with nothing to see its demand, `not learning while paused:` and why, such as `no Service routes requests to it`. [Name the workload](#one-workloads-forecast) to see what its forecast is doing in full, and the fix, from its `status.prediction.message`.
- **READY**, **COLD**, **WOKEN FOR NOTHING** and **WOKEN** replay the last half of the history as Hybernate with `autoResume` would have managed it, from a forecast that learned only the first half: the mornings it had the workload up before its first use, those whose first use found it paused and woke it, the wakes ahead of demand that didn't come and how long they kept it up, and when it usually woke it. Having learned less, the replay is a little less ready than opting in now would be.
- **WAKES ON** is what wakes the workload once it's paused, in the replay, as it would under Hybernate: `request`, through the [doorman](../concepts/wake-on-request.md), when a Service with a cluster IP selects its pods on a TCP port, keeps traffic cluster-wide, and isn't in GKE NEGs, unless the doorman's [forwarder](../concepts/wake-on-request.md#gke-container-native-load-balancing-negs) is on; `query`, when its ManagedWorkload has a [Prometheus activity query](../guides/prometheus-signals.md#while-the-workload-is-paused), so only for a workload Hybernate manages; `request, query` for both; or `nothing`, so people wake it by hand, and the forecast doesn't learn the hours it's paused. For a workload Hybernate manages, what its operator found of its routing stands, as at its last pause, such as Services served by other workloads' pods. A workload not yet managed is judged by the ManagedWorkload opting it in would make, with `hybernate.io/wake-on-request` from its own annotations, then its namespace's, as opting in reads it. Whether the doorman is on (`doorman.enabled`), and its NEG forwarder, are read from the flags of the operator that manages the namespace, whose Deployment `forecast` finds by its labels across the cluster, or, without access to list Deployments there, in the operator's namespace, `hybernate-system` or the one a ManagedWorkload's `hybernate.io/operator` annotation names; when it can't read one, it assumes Hybernate's defaults, the doorman on and the forwarder off, and says so in its notes, with why and the access that reads it. The JSON has it in `forecast.wakes`: `request`, `query`, and `noRequest`, why a request wouldn't wake it.

A workload counts as used whenever its CPU is above `--cpu-threshold`, as the activity clock counts it; use that shows only in requests or activity annotations isn't in Prometheus.

It reads Prometheus as [scan](#scan-a-cluster) does, found in the cluster or at `--prometheus-url`, with the same [flags](#a-prometheus-elsewhere) and [permissions](#permissions), and fails when there's no history to read. With less than two days of it, a day to learn from and a day to replay, it says so and when there will be enough, and, when Hybernate manages a workload, how to see its own forecast, which needs no history. It leaves out scan's notes on dependencies, GitOps tools and the cluster's infrastructure, which are about what pausing could free. It only reads.

### One Workload's Forecast

Name a workload, as `api` or `statefulset/postgres`, to see its forecast:

```bash
kubectl hybernate forecast api -n preview-42
```

```
staging (EKS us-east-1): the forecast of preview-42/api, times in Europe/London

Hybernate's forecast, from ManagedWorkload api: acting
  acting, on 4 weeks of CPU history and what it has seen since: its forecasts of the last day were 91% right, and it acts from 75%, so it holds off pauses before hours it expects to be busy and wakes the workload 15 minutes ahead of them
  Phase:       daily Active, weekly Active
  Hours seen:  24 of the 24 hours of the day, 168 of the 168 hours of the week
  Learned:     790 hours: 672 from its CPU history in Prometheus, and 118 live in the 5 days Hybernate has managed it
  Confidence:  91% right over the last day, 88% over the last week; it acts from 75%
  Acts from:   now
  Busy hours:  Thu 22 Oct 09:00–18:00, up to 64% of its CPU requests
  autoResume:  on: it holds off pauses before the hours it expects busy, and wakes the workload 15 minutes ahead of them

Replayed over its last 28 days of CPU history, from monitoring/prometheus-operated:
  Opted in today: acts from day one, at a confidence of 75%.

From Thu 8 Oct to Thu 22 Oct, with autoResume, from a forecast that learned only the history before; while it's paused, a request through the doorman wakes it:
DAY         FIRST USE  FOUND IT  WOKEN AT  FORECAST  WOKEN FOR NOTHING
Thu 8 Oct   09:12      ready     08:45     acting    -
Fri 9 Oct   09:20      ready     08:45     acting    -
Sat 10 Oct  -          -         -         acting    -
...

Ready on 9 of 10 mornings, woken 25m ahead; it never woke it for nothing.
```

For a workload Hybernate manages, it first shows what Hybernate's own forecast of it is doing, from its ManagedWorkload's `status.prediction` and the state the forecast saves there, as the operator restores it:

- **The first line** says whether it's acting, as `status` shows it in FORECAST: `acting`, `learning`, `relearning` or `not learning`. Under it is the operator's own message, as it wrote it in `status.prediction.message`: what it's waiting for and why.
- **Phase** is the phase of each season, daily and weekly: `Observing`, `Suggesting` or `Active`. See [Phase Lifecycle](../concepts/forecasting.md#phase-lifecycle).
- **Hours seen** are the hours of the day it has seen since it started or last relearned, all 24 of which it needs before it can act, and the hours of the week it has seen.
- **Learned** is how many hours of demand it has learned, how many of them came from the workload's CPU history in Prometheus when Hybernate began managing it (`status.prediction.history`), and how many it has learned live since, out of how long Hybernate has managed it. When it learned no history, the **History** line gives the operator's reason.
- **Confidence** is how right its forecasts of the last day and the last week were, and the confidence it acts from.
- **Acts from** is `now`, when it's acting; the time it's expected to start (`status.prediction.startsActing`), if the coming days repeat the last week; `not known until it has seen every hour of the day`, or `not known until it has scored a day of its forecasts` while it's otherwise still observing; or `not expected within 4 weeks`, for a pattern too irregular for its setting.
- **Busy hours** are the hours of the next 24 it expects demand at or above the workload's `cpuThreshold` in, with the most it expects as a share of the CPU it requests. It holds off a pause before each, and with `autoResume` wakes the workload 15 minutes ahead of it. It forecasts none until it's acting.
- **autoResume** says whether it's on, what that changes, and when it's off, how to turn it on: the workload's `hybernate.io/auto-resume: "true"` annotation for a workload opted in with the label, or `spec.idlePolicy.autoResume: true` for a ManagedWorkload written by hand, which annotations don't change.
- **Its state** says why the plugin couldn't read the saved state, such as a plugin of another version than the operator; the lines read from it are left out. A workload the operator hasn't recorded a forecast for yet says so, and what to check. One its ManagedWorkload says Hybernate leaves alone (`TargetAvailable=False`), labelled `hybernate.io/ignore=true` or gone, says that instead of what the forecast last recorded (JSON `operator.stopped`), as `status` does in NEXT.

Then, for any workload, managed or not, comes the replay of its history: when the forecast would act were the workload opted in today, and the last half of the history day by day, with each day's first use, how it found the workload, when the forecast woke it, and whether the forecast was acting. When it can't be replayed, the line that takes its place says why and what to do: too little history, a day to learn from and a day to replay, with when there will be enough; no Prometheus to read, or one it couldn't; no CPU requests to measure use against; no CPU history of its pods; or a workload scaled to zero outside Hybernate. A workload Hybernate manages shows its own forecast whatever its history, and exits 0. One it doesn't manage, with nothing else to show, exits 1 when there's no Prometheus it can read; with too little history, it says so and exits 0.

NAME is looked for in every namespace `forecast` reads, or those given with `-n`. It matches a Deployment or StatefulSet, and a ManagedWorkload's own name, for one written under another name. When it matches more than one, such as `api` in two namespaces, `forecast` names them and asks for `-n` or `kind/name`. A name that matches nothing says so; in a namespace named with `-n`, a workload labelled `hybernate.io/ignore=true`, which the forecast leaves out as Hybernate does, is named as such. Times are in the operator's time zone, as the workload's ManagedWorkload records it, unless `--timezone` says otherwise. `-o json` and `-o yaml` give the same in each workload's `operator` (with `learned`: `hours`, `fromHistory`, `hoursOfDay`, `hoursOfWeek`, and `busy`, each span's `from`, `to` and `peak`), `forecast` and `noReplay`. Reading the ManagedWorkload takes `list` on `managedworkloads`, which `view` has; without it, the workload is shown as one Hybernate doesn't manage.

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--namespace` | `-n` | all you can read | Namespace to replay; repeat for several |
| `--all-namespaces` | `-A` | `false` | Every namespace, failing rather than falling back when your access doesn't allow it |
| `--exclude-namespaces` | | system namespaces | Namespaces to skip |
| `--window` | | `28d` | How much history to read, from `2d` to `34d`; the operator learns up to four weeks |
| `--forecast` | | `balanced` | How soon the forecast acts, `eager`, `balanced` or `cautious`, for workloads Hybernate doesn't manage; see [Forecast setting](../concepts/forecasting.md#forecast-setting) |
| `--timezone` | | the operator's | The time zone whose hours and weekdays the forecast learns, and that times are shown in: by default the operator's `timezone`, as its ManagedWorkloads record it in `status.prediction.timeZone`, or `UTC` when you can read none |
| `--cpu-threshold` | | `10` | CPU use, as a percentage of requests, above which a workload is in use; managed workloads use their own |
| `--idle-after` | | `1h` | How long without use before a pause; managed workloads use their own |
| `--output` | `-o` | `table` | `table`, `json`, or `yaml`; JSON and YAML give every workload's days |
| `--timeout` | | `5m` | How long the replay may take before it gives up |
| `--prometheus-url` and the other `--prometheus-*` flags | | | As for scan |

## See What Hybernate Is Doing

```bash
kubectl hybernate status
```

```
staging (EKS us-east-1): Hybernate manages 4 workloads.
  1 paused, 2 running, 1 resuming; 1 in dry-run, never paused: pausing it would have freed $7 since Oct 1
  Node autoscaler: Karpenter (consolidates when underutilized after 1m)
  Forecast time zone: Europe/London
  Freed $42 this month, and saved $9 of it.

Needs attention:
  Pausing freed $42 this month, but only $9 of it came off the bill: nodes aren't scaling down.
  kubectl hybernate nodes shows why.
  preview-7/worker   GitOpsConflict   Argo CD set the replicas from Git, undoing the pause; ...
  preview-7/worker   Stuck            resuming for 25m: waiting for preview-42/statefulset/postgres

  NAMESPACE    WORKLOAD               STATE               FOR   LAST ACTIVITY      NEXT                                        FORECAST                    FREED THIS MONTH   SAVED   WOULD FREE
  preview-42   deployment/api         paused              3h    request, 4h ago    wakes on a request or activity              acting                      $12                $9      -
  preview-42   statefulset/postgres   running             5h    cpu, 2h ago        held awake while preview-7/worker uses it   -                           $30                $0      -
  preview-7    deployment/web         running (dry-run)   2h    rollout, 15m ago   would pause in 45m                          learning, acts from Oct 8   -                  -       $7
  preview-7    deployment/worker      resuming            25m   -                  -                                           -                           <$1                $0      -

Recent pauses and wakes:
  30m ago   preview-7/worker   scaled up to 2 replicas by argocd-controller outside Hybernate; counted as activity
  3h ago    preview-42/api     paused
  5h ago    preview-42/api     request on Service api, waking
```

`status` is one screen of every workload Hybernate manages, read from their ManagedWorkloads and events:

- **The summary:** how many workloads are in each phase, and how many are in dry-run, with what pausing them would have freed since the first started measuring, by what dry-run measured (`status.dryRun.estimatedFreed`, the would-be pause under way included); the node autoscaler, found as [`nodes`](#see-why-nodes-dont-scale-down) finds it, or `none found`, or `unknown` when your access doesn't allow reading what would tell; and what pausing live workloads has freed this month, its requests at list price, and how much of that has been saved, come off the bill as nodes scaled down, in whole dollars, as `scan` shows them: `<$1` for an amount above zero that rounds to nothing, and `$0` only for none. Freed capacity is only money once a node autoscaler removes the nodes it was on. When the operator can't measure what's saved for some workloads, the summary says why, once for each reason. It also says which time zone the [forecast](../concepts/forecasting.md#wall-clock-alignment) counts hours in, the operator's `timezone` as each workload's `status.prediction.timeZone` records it, and, when that's UTC, the default, how to set it to your users' zone, so `autoResume` follows their clock through daylight saving.
- **Needs attention:** when pausing freed at least a dollar this month and less than half of it was saved, counting only workloads whose savings are measured, a line saying nodes aren't scaling down, pointing at `kubectl hybernate nodes`. Then what keeps Hybernate from doing its job, from each workload's conditions: a GitOps tool undoing pauses (`GitOpsConflict`), a dependency that's missing, in a cycle, or in a namespace that doesn't allow it (`DependencyNotAllowed`), two ManagedWorkloads for one target, a protected namespace, a target that's gone, metrics or Prometheus that can't be read, a Prometheus query that has never matched a series (`PrometheusQueryEmpty`), a workload whose requests can't wake it while it's paused (`WakeOnRequest`), which for one that's awake now is what its last pause found with its Services, to fix before the next, but not its being awake (`NotPaused`) or the doorman being turned off, a resume that stopped with replicas not Ready (`ResumeIncomplete`), a pause that stopped waiting with pods still running (`PauseIncomplete`), and a pause or wake taking over 10 minutes (`Stuck`), with what it waits for. `kubectl describe managedworkload` has the rest.
- **The table:** each workload as `kind/name`, with its ManagedWorkload's name in brackets when you wrote one under another name; its state and for how long; its last activity and where it came from, such as `cpu, 2h ago`, or, while there's been none since its clock started, what started it, which isn't activity: `none since Hybernate began managing it 3m ago`, `none since it woke 3m ago`, or, after a gap in watching, `unobserved until 3m ago`; what pausing it has freed this month and what of that it has saved, in whole dollars or `<$1`, as `scan` shows them, `-` for a workload only ever in dry-run, or `unknown` when what's saved can't be measured, where totals last brought up to date in an earlier month count as none, and one in dry-run now keeps what its pauses freed and saved before dry-run was turned on; when any workload is in dry-run, **WOULD FREE**, what pausing it would have freed since dry-run started measuring it, `$0` until a would-be pause has, and `-` for one that isn't in dry-run; and **NEXT**:
    - when it pauses: `pauses in 45m`, `pauses now` once it's `Idle`, with or without an `idlePolicy`, as after a pause request, and `would pause …` in dry-run; `pauses now, early: the forecast expects quiet until Fri 20:00 UTC` once the forecast has [brought the pause forward](../concepts/forecasting.md#pausing-early), or `expected` once that time has passed, as for a would-be pause in dry-run, which stays `Idle` until activity. After a GitOps tool undid its last pause, Hybernate waits an hour before pausing it again, which reads `pauses at 14:20 UTC (after GitOps conflict)`; see [GitOps tools](../guides/gitops.md#what-hybernate-does). Status is written every few minutes, so for up to 7 minutes after the time it showed, a workload still running reads `pauses soon`; after that, `overdue to pause`, which means something the table can't see holds it, and `kubectl describe managedworkload` says what
    - why it won't pause yet, and until when: `held awake while preview-7/worker uses it` (its dependents, until they pause), `held awake for 2h (active-until)` (`hybernate.io/active-until` on the ManagedWorkload or the workload, as `wake --for` sets), or `held awake by the forecast: busy from 13:00 UTC` (the hour the forecast expects demand in)
    - why it won't pause at all: `never pauses: no idlePolicy`, `never pauses: namespace protected`, `never pauses: part of Hybernate` (the operator's or the doorman's own Deployment), `never pauses: PVCs deleted on scale-down` (a StatefulSet whose `persistentVolumeClaimRetentionPolicy.whenScaled` is `Delete`), `nothing: labelled hybernate.io/ignore`, `nothing: workload not found`, `nothing: another ManagedWorkload has it`, `won't pause: dependsOn cycle`, `won't pause: no CPU metrics`, or `won't pause: Prometheus failing`
    - for a paused one, what wakes it: `wakes on a request or activity`, `wakes when a person asks, or on activity` for one whose doorman [asks before waking](../concepts/wake-on-request.md#asking-before-waking) it, or `wakes on activity` without the doorman. When the forecast [brought the pause forward](../concepts/forecasting.md#pausing-early), before `idleAfter` ran out, it says so first, and until when the forecast expected quiet: `paused early: the forecast expected quiet until Fri 20:00 UTC; wakes on a request or activity`
- **FORECAST:** whether the workload's [forecast](../concepts/forecasting.md#when-it-acts) acts: `acting`; `learning`, with `acts from Oct 8` when the operator expects it to start, if the coming days repeat the last week it saw; `relearning`, after demand broke from what it had learned; or `not learning while paused:` and why, such as `no Service routes requests to it`, while it's paused with neither the doorman nor a [Prometheus activity query](../guides/prometheus-signals.md#while-the-workload-is-paused) to see its demand; its `status.prediction.message` says how to fix it. `-` until the operator has recorded one. JSON gives the reason from `status.prediction.reason` in `forecast`, and `forecastStartsActing`.
- **Turned away while asleep,** under the table, when a workload whose doorman asks before waking it turned requests away during its current or last pause: `preview-42/web   312 requests during the pause from 3h ago`. A person shown the page that asks isn't counted, so many mean something automated calls it, such as a monitor, a webhook or another service. `-o json` has them as `turnedAway`, from the ManagedWorkload's `status.turnedAway`; see [What it turns away](../concepts/wake-on-request.md#what-it-turns-away).
- **Recent pauses and wakes,** newest first, as far back as the cluster keeps events, or `--since`: each pause, and each wake with its cause, a request on a Service, an activity annotation (including `kubectl hybernate wake` and a dependent waking), the forecast, or a scale-up from outside Hybernate. Requests the doorman closed without reaching the workload are listed too, and a wake whose cause wasn't recorded, such as a hand-back when a namespace becomes protected, is listed by its `resumed` event alone. The table shows the latest 10; `-o json` has every one. The API server keeps events for an hour unless its `--event-ttl` says otherwise, so on most clusters that's what `status` can show.

```bash
kubectl hybernate status --context staging -n preview-42
kubectl hybernate status --since 1h -o json
```

### Share it as a web page

```bash
kubectl hybernate status --html              # opens it in your browser
kubectl hybernate status --html status.html  # saves it, to send around
```

`--html` writes the same status as a web page, for a team or a manager: the summary, with the node autoscaler and the forecast's time zone; what pausing freed this month, what of that was saved, and what dry-run measured pausing would have freed; what needs attention; every workload, with its state and for how long, its last activity, what happens to it next, and what it freed or would free, in a table you can sort and filter; and every pause and wake since `--since`, with what caused it, at its time in UTC. Given a file, it writes it there; given none, to a new temporary file. Either is readable only by you, since it names the cluster's workloads. In a terminal, it opens the page in your browser, as [`scan`](#scan-a-cluster) does with its report, unless `--open=false`; otherwise it prints where the page is. The terminal table, or `-o json`, is printed as well. The page loads nothing from elsewhere, so it works offline and attached to an email.

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--namespace` | `-n` | all you can read | Namespace to show; repeat for several |
| `--all-namespaces` | `-A` | | Read every namespace, and fail rather than fall back when your access doesn't allow that. Can't be combined with `-n` |
| `--output` | `-o` | `table` | `table`, `json`, or `yaml` |
| `--since` | | `24h` | How far back to list pauses and wakes |
| `--html` | | | Also write the status as a web page to this file; given without one, to a temporary file |
| `--open` | | `true` | Open the `--html` page in your browser, when run in a terminal |
| `--timeout` | | `1m` | How long to wait for the cluster before giving up |

Your user needs `list` on `managedworkloads` and `events`, in every namespace or the ones passed with `-n`. Without access to events, it shows the rest and says so. To name the node autoscaler, it reads what [`nodes`](#see-why-nodes-dont-scale-down) does for that: Karpenter's NodePools, nodes' metadata, the `cluster-autoscaler-status` ConfigMap and Deployments' metadata in `kube-system`; without enough of them to tell, it says the autoscaler is unknown. It also lists `deployments` and `statefulsets`, reading only their metadata, for a `hybernate.io/active-until` annotation on the workload itself, and goes without it when it can't. A namespace named with `-n` that doesn't exist is an error, which takes `get` on `namespaces` to tell; without it, the namespace shows as one Hybernate manages nothing in.

## See Why Nodes Don't Scale Down

Pausing a workload frees what its pods requested, but that comes off the bill only once a node autoscaler removes the node they ran on. `nodes` shows what's in the way:

```bash
kubectl hybernate nodes
```

```
staging (EKS us-east-1): 3 nodes
Node autoscaler: Karpenter (NodePool batch consolidates only empty nodes after 1m; NodePool default consolidates when underutilized after 1m)
Hybernate's 2 paused workloads would request 2.0 vCPU and 4.0 GiB of memory if awake.
  in pool batch: 0.5 vCPU and 1.0 GiB
  in pool default: 1.5 vCPU and 3.0 GiB

  NODE      POOL      INSTANCE TYPE   CPU REQUESTED     MEMORY REQUESTED   UTILISATION   BLOCKED BY
  batch-1   batch     c5.large        0.1 of 1.9 vCPU   0.3 of 3.0 GiB     10%           NodePool consolidates only empty nodes
  quiet-1   default   m5.large        0.2 of 1.9 vCPU   1.0 of 7.0 GiB     14%           PDB allows no disruption (shop/db)
  busy-1    default   m5.xlarge       3.0 of 3.9 vCPU   8.0 of 14.0 GiB    77%           -

Next steps:
  1. A NodePool consolidates only empty nodes, which a node running anything never is. Set spec.disruption.consolidationPolicy: WhenEmptyOrUnderutilized.
     For example: NodePool batch consolidates only empty nodes, and this one runs 1 pod
  2. A PodDisruptionBudget allows no disruptions, so its pods can't be moved. Raise maxUnavailable, lower minAvailable, or run more replicas than it requires.
     For example: shop/db: PodDisruptionBudget db allows no disruptions
  See https://okedeji.io/hybernate/guides/cluster-autoscaler/
  To have pausing empty whole nodes, put non-production workloads on a node pool of their own: https://okedeji.io/hybernate/guides/node-pools/
```

- **Node autoscaler:** what scales the cluster's nodes, which `status` and `scan` show too:
    - **Karpenter**, from the `nodepools.karpenter.sh` CRD or nodes labelled `karpenter.sh/nodepool`, with each NodePool's `spec.disruption.consolidationPolicy` and `consolidateAfter`
    - **EKS Auto Mode**, from nodes labelled `eks.amazonaws.com/compute-type: auto`; its NodePools are Karpenter's, and read the same way
    - **Cluster Autoscaler**, self-managed or the one GKE Standard and AKS run for you, from the `cluster-autoscaler-status` ConfigMap it writes in `kube-system`, with the cloud its nodes are on. Without the ConfigMap, a Deployment in `kube-system` whose name ends in `cluster-autoscaler` is named as a hint that it may not be running
    - **GKE Autopilot**, from that ConfigMap and nodes all named `gk3-`, as Autopilot names them; it scales nodes itself
    - `none found`, when every place was read and none of them showed one: freed capacity won't come off the bill until something removes nodes
    - `unknown`, when your access didn't allow reading what would tell, which it names. It never says none when it couldn't look
- **The table:** each node, least used first, with its pool (Karpenter NodePool, EKS node group, GKE node pool or AKS agent pool), its instance type, what its running pods request of what it can allocate, and its utilisation: the larger of the two, as Cluster Autoscaler measures it against its scale-down threshold. DaemonSet pods count towards what's requested, as they do for the autoscaler.
- **BLOCKED BY:** why the autoscaler won't remove the node, however little of it is used, the first two in the table and every one in `-o json`. On a node a Karpenter NodePool owns, Karpenter's reasons: the node or a pod annotated `karpenter.sh/do-not-disrupt: "true"`, a pod whose PodDisruptionBudget allows no disruptions (`status.disruptionsAllowed` is 0), a NodePool with `consolidationPolicy: WhenEmpty` and anything but DaemonSet pods on the node, or `consolidateAfter: Never`. On any other node, Cluster Autoscaler's: the node annotated `cluster-autoscaler.kubernetes.io/scale-down-disabled: "true"`, a pod annotated `cluster-autoscaler.kubernetes.io/safe-to-evict: "false"`, a pod with an `emptyDir` not in memory or a `hostPath` volume it doesn't name in `cluster-autoscaler.kubernetes.io/safe-to-evict-local-volumes`, a pod no controller owns, a `kube-system` pod without a PodDisruptionBudget, and the PodDisruptionBudget rule. DaemonSet and static pods never block, and a pod annotated `safe-to-evict: "true"` blocks only by its PodDisruptionBudget, as with Cluster Autoscaler. Local storage and `kube-system` pods block only while Cluster Autoscaler's `--skip-nodes-with-local-storage` and `--skip-nodes-with-system-pods` are on, as they are by default upstream; AKS turns the first off. While the autoscaler is unknown, nodes Karpenter doesn't own are judged by Cluster Autoscaler's rules. A node no autoscaler found scales is `not autoscaled`.
- **Next steps:** how to clear each kind of reason the table shows, with one example of it, the [node autoscaler guide](../guides/cluster-autoscaler.md), and the [node pools guide](../guides/node-pools.md).

A node with nothing in BLOCKED BY that stays is still in use: paused workloads share it with busy ones. [Node Pools for Real Savings](../guides/node-pools.md) shows how to give non-prod workloads nodes that pausing them empties.

What paused workloads would request is what Hybernate recorded their pause freed, in all and by the node pool their pods ran in, as each pause recorded it: the pools that would need room for them if they woke. A workload whose pods ran in several pools is shared between them by how many ran in each. Pauses that recorded no pool are counted in all but not by pool.

```bash
kubectl hybernate nodes --context staging -o json
```

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--output` | `-o` | `table` | `table`, `json`, or `yaml` |
| `--timeout` | | `1m` | How long to wait for the cluster before giving up |

Your user needs `list` on `nodes`, and on `pods` and `poddisruptionbudgets` in every namespace; `list` on `nodepools.karpenter.sh`; `get` on the `cluster-autoscaler-status` ConfigMap and `list` on `deployments` in `kube-system`; and `list` on `managedworkloads`. It reads what it can, and the notes say what it couldn't: without pods, what each node runs and the reasons that come from its pods are left out; without nodes, there's no table.

## Pause a Workload

```bash
kubectl hybernate pause my-api -n staging
```

```
pausing staging/my-api...
staging/my-api is Paused after 4s; a request to it, or kubectl hybernate wake, wakes it
```

`pause` asks Hybernate to pause a workload now, instead of when its idle clock runs out: a preview environment at the end of the day, say. It's an ordinary pause, the same as an idle one: a request to the workload through the [doorman](../concepts/wake-on-request.md), [activity](../concepts/idle-detection.md#activity-annotations), `autoResume` or [`wake`](#wake-a-workload) wakes it. It works whether or not the workload has an `idlePolicy`. See [Pause and Resume](../guides/pause.md#pause-now) for exactly what Hybernate does with the request.

```bash
# Pause it even if the forecast expects it to be busy soon, without asking
kubectl hybernate pause my-api -n staging --yes

# Request the pause and return straight away
kubectl hybernate pause my-api -n staging --wait=false
```

NAME is found as for [`wake`](#wake-a-workload).

What it says depends on the phase:

| Phase | What happens |
|-------|--------------|
| `Running` | It pauses, and `pause` waits until it's `Paused` |
| `Idle` | It pauses now |
| `Resuming` | It pauses once it's up |
| `Pausing` | Already pausing; `pause` waits until it's `Paused` |
| `Paused` | Already paused; `pause` returns at once |
| dry-run | Never paused: Hybernate counts a would-be pause, and `pause` says so |

Hybernate pauses it by the same rules as an idle pause, so `pause` exits 1 with Hybernate's reason when it won't: workloads that are awake depend on it, a `dependsOn` cycle runs through it, its namespace is protected, or the workload doesn't exist, is labelled `hybernate.io/ignore` or is managed by another ManagedWorkload. A workload scaled to zero outside Hybernate is off already, which `pause` says, exiting 0. Asking does override the hour Hybernate waits after a GitOps tool undid its last pause.

When a confident forecast expects the workload to be busy within the hour, Hybernate declines the pause and says when, since it would likely be woken straight back, and `pause` asks:

```
preview-42/api wasn't paused: the forecast expects demand at 80% of requests in the hour from 09:00 UTC
Pause it anyway? [y/N]
```

Answering `y` asks again overriding the forecast, and it pauses; anything else leaves it running, exiting 0. The question waits for you as long as you take: `--timeout` counts only the time spent on the cluster, and starts again after your answer. `--yes` overrides the forecast from the start, without asking. With no terminal to ask on, such as in a script, `pause` exits 1 with the reason and says to pass `--yes`.

When a `hybernate.io/active-until` annotation holds it awake, on the ManagedWorkload, as `wake --for` sets, or on the workload itself, Hybernate would refuse the pause, so `pause` asks first, naming which holds it:

```
preview-42/api wasn't paused: the hybernate.io/active-until annotation on its ManagedWorkload keeps it awake until 2026-10-17T20:00:00Z
Clear it and pause now? [y/N]
```

Answering `y` removes the annotation, from both when both hold it, and pauses it; anything else leaves it running, exiting 0. `--clear-hold` clears it without asking; with no terminal to ask on and without `--clear-hold`, `pause` exits 1 and says to pass it. `--yes` doesn't clear a hold, so a nightly script that pauses with `--yes` doesn't end one someone set for a demo.

When a GitOps tool sets the workload's replicas from Git and isn't told to leave them to Hybernate, `pause` warns before asking, with the one-time fix: the tool sets them again on its next sync, which undoes the pause. See [GitOps tools](../guides/gitops.md#checking-the-fix-is-in) for how it tells:

```
Warning: Argo CD application argocd/shop sets preview-42/api's replicas from Git, as argocd-controller, so its next sync will likely scale it back up, undoing this pause. To have Argo CD leave replicas to Hybernate, once:
  Add RespectIgnoreDifferences=true to the syncPolicy.syncOptions of Argo CD application argocd/shop, so syncs leave the replicas it ignores.
See https://okedeji.io/hybernate/guides/gitops/#argo-cd
```

When it can't tell, such as without access to Argo CD's namespace, the warning says why, and to make the fix if you haven't already.

If the workload isn't `Paused`, and Hybernate hasn't said why, within `--timeout`, `pause` exits 1 with its phase and the `kubectl describe` command that says why.

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--namespace` | `-n` | kubeconfig context's | Namespace of the workload |
| `--wait` | | `true` | Wait until the workload is Paused, or Hybernate says why it isn't |
| `--yes` | `-y` | `false` | Pause it even if the forecast expects it to be busy within the hour, without asking |
| `--clear-hold` | | `false` | Clear a `hybernate.io/active-until` hold on its ManagedWorkload, as `wake --for` sets, or on the workload, without asking |
| `--timeout` | | `5m` | How long to wait, cluster calls included; it starts again after you answer a question |

Your user needs `get`, `list` and `patch` on `managedworkloads` in the namespace, and `get` on the workload for the GitOps warning and its own hold, which are left out without it. Clearing a hold on the workload takes `patch` on it. For a workload Argo CD applies, telling whether Argo CD leaves its replicas takes `list` on `applications.argoproj.io` in every namespace, or `get` in `argocd`, and `get` on the `argocd-cm` ConfigMap; without them, the warning says it couldn't check.

## Wake a Workload

```bash
kubectl hybernate wake my-api -n staging
```

```
waking staging/my-api...
staging/my-api is Running after 23s
```

`wake` marks the ManagedWorkload as active now by setting its `hybernate.io/last-activity` annotation, the same [activity annotation](../concepts/idle-detection.md#activity-annotations) a developer portal would set. A paused workload wakes, along with the workloads it [depends on](../concepts/dependencies.md); a running one has its idle clock restarted. It then waits until the workload is Running.

```bash
# Keep it awake for the next two hours, e.g. for a demo
kubectl hybernate wake my-api -n staging --for 2h

# Request the wake and return straight away
kubectl hybernate wake my-api -n staging --wait=false
```

NAME is the workload, as `status` shows it, such as `api` or `statefulset/postgres`, or its ManagedWorkload's name. When a bare name answers to more than one ManagedWorkload, such as when a Deployment and a StatefulSet share it, or one ManagedWorkload is named after a workload another one manages, it lists them and asks for the workload as `kind/name`, which only matches workloads.

What it says depends on the phase:

| Phase | What happens |
|-------|--------------|
| `Paused` | It wakes, and `wake` waits until it's `Running` |
| `Pausing` | It wakes once the pause finishes |
| `Resuming` | It's already waking |
| `Idle` | Marked active, so it stays up |
| `Running` | Already running; its idle clock restarts, and `wake` returns at once |
| dry-run, not paused | Never paused; its idle clock restarts, and `wake` returns at once |

It refuses, and says why, when activity can't wake the workload: the workload doesn't exist or is labelled `hybernate.io/ignore`, or another ManagedWorkload manages it.

If the workload isn't `Running` within `--timeout`, `wake` exits 1 with its phase and the `kubectl describe` command that says why.

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--namespace` | `-n` | kubeconfig context's | Namespace of the workload |
| `--for` | | | Also keep the workload awake for this long, by setting `hybernate.io/active-until` on its ManagedWorkload. A hold there that ends later stays, and `wake` says so |
| `--wait` | | `true` | Wait until the workload is Running |
| `--timeout` | | `5m` | How long to wait, cluster calls included |

Your user needs `get`, `list` and `patch` on `managedworkloads` in the namespace.

## Show a Workload's Dependencies

```bash
kubectl hybernate deps postgres -n preview-42
```

```
preview-42/postgres (StatefulSet, Paused)
Depends on:
  nothing
Depended on by:
  preview-42/api      learned from PGHOST                   Running
  preview-42/worker   declared in dependsOn, waitForReady   Paused
Learned links come from the dependent's environment or its requests; hybernate.io/ignore-dependencies drops one.
```

`deps` shows what Hybernate holds awake and wakes with a ManagedWorkload, in both directions: what it depends on, and what depends on it, in any namespace. Each link says where it comes from, a `dependsOn` or what Hybernate [learned](../concepts/dependencies.md#learned-dependencies) from the dependent's environment or from a wake, and what the other workload is doing now; one Hybernate doesn't manage shows as `not managed`. It only reads ManagedWorkloads. Without access to them in every namespace, it reads the workload's own and says that dependents elsewhere aren't shown; a dependency in another namespace then shows as `unknown (can't read <namespace>)`. NAME is found as for [`wake`](#wake-a-workload).

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--namespace` | `-n` | kubeconfig context's | Namespace of the workload |
| `--timeout` | | `1m` | How long to wait for the cluster before giving up |

## Enable a Workload

A workload in dry-run, whether from its own `hybernate.io/dry-run: "true"`, its namespace's, the cluster default, or `spec.dryRun` on a ManagedWorkload you wrote, is measured but never paused. `enable` ends that, so Hybernate starts pausing it while idle:

```bash
kubectl hybernate enable checkout-api -n preview-42
kubectl hybernate enable statefulset/postgres -n preview-42
kubectl hybernate enable --all -n preview-42
```

```
deployment/checkout-api: dry-run ended, Hybernate will pause it while idle
```

For a workload opted in with the `hybernate.io/managed` label, it sets the workload's own `hybernate.io/dry-run` annotation to `"false"`, which wins over its namespace's annotation and the cluster's default. It reads the workload's ManagedWorkload to tell whether it's in dry-run, so a dry-run from the cluster default counts too. For a ManagedWorkload you wrote, it sets `spec.dryRun: false`. It doesn't wait: Hybernate pauses the workload once its clock runs out.

`enable` only ends dry-run for a workload Hybernate manages. Otherwise it changes nothing and exits non-zero with the reason: the namespace is protected (labelled `hybernate.io/protected=true`, or a `kube-*` namespace, unless labelled `hybernate.io/allow-protected=true`); the workload is labelled `hybernate.io/ignore=true`, or its own `hybernate.io/managed` is anything but `"true"`, which opts it out of a managed namespace; no ManagedWorkload manages it, because the operator isn't running, doesn't watch the namespace, or protects it by its own settings; or its ManagedWorkload says Hybernate never pauses it: `Protected=True` (such as by a `protectedNamespaces` pattern, or for Hybernate's own operator and doorman), a target labelled `hybernate.io/ignore`, or `DuplicateTarget=True`. Right after you label a workload, the operator may not have made its ManagedWorkload yet; run `enable` again a few seconds later. Without the ManagedWorkload CRD, it says Hybernate isn't installed.

`--all` ends dry-run for every workload in the namespace. When the namespace's own `hybernate.io/dry-run` annotation, or the cluster default, keeps any of them in dry-run, it sets the namespace's annotation to `"false"` and drops the workloads' own annotations, so the namespace decides for all of them. When each is in dry-run by its own annotation, it leaves the namespace alone and sets each workload's annotation to `"false"`. Either way, it sets `spec.dryRun: false` on ManagedWorkloads written by hand in the namespace. When Git owns the namespace, its annotation is left for you to change there, which also ends dry-run for the workloads that take it, and workloads with their own annotation get `"false"`. When your access doesn't allow changing the namespace, as with `edit` or `admin` bound in it, `enable` leaves its annotation alone, says so, and sets every workload's own annotation to `"false"`, which wins over it; with nothing but the namespace's annotation to change, it exits non-zero. Labelled workloads no ManagedWorkload manages are left alone. When `enable` just set the namespace's annotation to `"false"`, that ends their dry-run too once Hybernate picks them up, unless they have an annotation of their own, so they're covered. The rest are listed, and `enable` exits non-zero. When none of the namespace's workloads has been picked up yet, `enable` changes nothing, the namespace's annotation included, and exits non-zero, since the operator may not be running. Workloads Hybernate never pauses, by their ManagedWorkload, are left alone and listed too.

When a GitOps tool (Argo CD, Flux, Fleet, kapp or Kluctl) applies the setting `enable` would change, the `hybernate.io/dry-run` annotation on the workload or its namespace, or a ManagedWorkload's `spec.dryRun`, `enable` changes nothing there, because the tool would put the old value straight back. It tells by the field managers the API server records as owning that setting, as `pause` does for replicas, so it doesn't matter how the tool tracks what it applies; for an object with no managed fields recorded, it goes by the tracking label or annotation Argo CD or Flux adds. A tool that applies the workload but not the annotation, such as when Git doesn't have it, leaves a change to it alone. It names the tool, prints the change to make in Git, and exits non-zero:

```
deployment/checkout-api is managed by Argo CD application shop, which would undo a change made here. In its manifest:
  set the annotation  hybernate.io/dry-run: "false"
Or pass --force to change the cluster anyway.
```

When Helm set it, `enable` changes it, since Helm only applies a chart when you run it, and warns that the next `helm upgrade` puts the chart's value back, so set it in the chart too. See [GitOps tools](../guides/gitops.md#ending-dry-run).

Ending dry-run lets Hybernate pause the workload, and a GitOps tool that sets its replicas from Git scales it back up on its next sync after each pause. So whatever `enable` does, ends dry-run, finds it ended already, or leaves the change to Git, it warns as [`pause`](#pause-a-workload) does, with the one-time fix, unless the tool is told to leave the replicas to Hybernate. With `--all`, workloads a tool treats alike, such as those of one Argo CD application, get one warning between them.

A bare name is looked up as a Deployment and as a StatefulSet. When both have it, `enable` lists them and asks for the kind; prefix the name with `deployment/` or `statefulset/` to be exact.

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--namespace` | `-n` | kubeconfig context's | Namespace of the workload |
| `--all` | | `false` | Enable every workload in the namespace |
| `--force` | | `false` | Change the cluster even when a GitOps tool applies the setting from Git |
| `--timeout` | | `1m` | How long to wait for the cluster before giving up |

Your user needs `get` and `patch` on the workload, `get` on its namespace, and `list` on `managedworkloads` in it. Without `list` on ManagedWorkloads, `enable` goes by the annotations alone, so it can't check that Hybernate manages the workload or see a dry-run from the cluster default, and it says so. With `--all`, it also needs `list` on Deployments and StatefulSets, and `patch` on the namespace to end its dry-run there; without it, each workload gets its own annotation instead. The GitOps warning reads Argo CD as `pause`'s does. See [Opting In](../guides/opt-in.md) for the label and every setting.
