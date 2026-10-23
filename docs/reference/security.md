# Security

What a security review of Hybernate needs: what it can do in a cluster, what it talks to, and how to check that what you install is what was released.

## What it can do

- **It never deletes your resources.** Its one action is to scale a Deployment or StatefulSet to zero, through the scale subresource, and back. The only other change it makes to a workload is its own `hybernate.io/paused-from` annotation while the workload is paused, though its role may patch workloads, and the only things it deletes are its own ManagedWorkloads and EndpointSlices, and with the optional forwarder below, its own forwarder Deployments.
- **It never reads your Secrets.** The doorman reads one Secret of its own, by name: the key it signs the [Wake it button](../concepts/wake-on-request.md#the-wake-it-button) with, which the chart creates empty and the doorman fills. Its Role names that Secret alone, so it can't read or list any other. The operator has no RBAC on Secrets. Like any role that may patch Deployments and StatefulSets, though, whoever holds its token could change a pod template to mount one, and read it from the pod that runs.
- **What the doorman shows the internet names nothing inside the cluster.** For a workload that [asks before waking](../concepts/wake-on-request.md#asking-before-waking), its pages and its `503`s name no namespace, workload or Service. The asleep page runs no script, and no other site may frame it, so none can trick someone into clicking **Wake it**. Its button carries a token signed for that workload and pause, good for 10 minutes and checked in constant time. Only a post of the page's token, still fresh, wakes the workload, from a browser's `User-Agent` that says the click came from the page: with `Sec-Fetch-Site: same-origin` where browsers send it, over HTTPS, and `Sec-Fetch-User: ?1` from Chromium and Firefox; with an `Origin` that's the page's own address where they don't, over plain HTTP. The page's `Referrer-Policy: same-origin` lets a browser send that `Origin`, and sends nothing to other sites. A browser from before about 2020, which may say neither, is asked to wake it another way. A robot that forges those headers on purpose, or drives a real browser, can still click it, at most once a minute.
- **It can write any EndpointSlice in the namespaces it works in.** It only ever creates, changes and deletes its own, labelled `endpointslice.kubernetes.io/managed-by: doorman.hybernate.io`, and never overwrites one without that label, but RBAC can't limit a role by label, so its ServiceAccount is allowed the rest too. Whoever holds its token could redirect any Service there.
- **The optional GKE NEG forwarder needs more.** With `doorman.negForwarder.enabled` (off by default), the operator may also create and delete Deployments in the namespaces it works in, to run a [forwarder](../concepts/wake-on-request.md#gke-container-native-load-balancing-negs) beside each paused workload behind GKE container-native load balancing. RBAC can't limit that to its own, so whoever holds its token could create Deployments there, as well as patch them, which it can already. It only ever deletes a Deployment of the forwarder's name that its ManagedWorkload owns. The forwarders run as their namespace's `default` ServiceAccount without its token, with no access to the API server, at the Pod Security Standards' restricted level.
- **It can be confined to namespaces.** With [`watchNamespaces`](helm-values.md#namespaces), the operator and doorman get a Role in each of those namespaces instead of a ClusterRole, so Kubernetes itself refuses them anything elsewhere. The operator keeps one ClusterRole, read-only, on Namespaces, Nodes and Karpenter NodePools, and a Role in its own namespace to read the doorman's EndpointSlices. With secure metrics (the default), both also keep a ClusterRoleBinding to `system:auth-delegator`, which only lets them check the tokens of whoever scrapes their metrics.
- **It can be kept out of namespaces.** A namespace labelled `hybernate.io/protected=true`, or matching [`protectedNamespaces`](helm-values.md#namespaces), is never managed; see [Protected namespaces](../guides/opt-in.md#protected-namespaces).
- **A namespace's workloads are only kept awake for another namespace that it allows.** A dependency in another namespace, declared in `dependsOn` or learned, is held awake, woken and waited for only when its namespace's `hybernate.io/dependents-from` annotation lists the dependent's namespace. Otherwise it's ignored and reported as `DependencyNotAllowed` with the same message whether it exists or not, so a dependency can't probe another namespace. A dependency learned from a request that woke a workload is only learned from a `hybernate.io/last-request-from` the doorman wrote, which the chart's `<fullname>-request-stamps` admission policy lets no one else set, and which the operator also checks by the field manager the API server records, and only for a sender in a namespace the woken workload's namespace allows. See [Across namespaces](../concepts/dependencies.md#across-namespaces).
- **It doesn't fight people or tools.** A paused workload scaled up by anything else wakes; a GitOps tool undoing a pause is reported, not fought; see [GitOps tools](../guides/gitops.md).

Everything it reads, writes, and sends is listed in [Data and Access](data-and-access.md), and the exact RBAC in [Helm values](helm-values.md#rbac).

## What it talks to

| From | To | Why |
|---|---|---|
| Operator, doorman | The Kubernetes API server | Everything it does |
| Operator | Prometheus, at `prometheus.url` | Only with `prometheus.url` set: the Prometheus activity queries workloads configure, and, once for each new ManagedWorkload, one range query for up to four weeks of its containers' CPU, to start its forecast from |
| Any client of a paused workload's Service: a pod, an ingress controller, or a load balancer through a NodePort | Doorman | The doorman holds the connection, wakes the workload, and forwards it |
| Doorman | The woken workload's pods, in any routed namespace, on their Service ports | Passing the held connection through |
| Google's load balancers, and any other client of a paused workload's Service behind GKE NEGs | Forwarder, in the workload's namespace, with `doorman.negForwarder.enabled` | The forwarder stands in for the workload's pods |
| Forwarder | Doorman, on its routing ports | Passing connections on, untouched |
| Prometheus | Operator and doorman metrics | Only with a token allowed to get `/metrics`, unless `metrics.secure` is off |

Nothing leaves the cluster: no telemetry, no usage reporting, no update checks. The images run as a non-root user on a distroless base, with a read-only root filesystem. So do the chart's guards, hook Jobs that run only during `helm uninstall`, `helm upgrade` and `helm rollback`, from the operator's own image, each with a ServiceAccount that may only list ManagedWorkloads.

### The doorman is a way in to paused workloads

Any client that can reach a doorman pod can reach every routed, paused workload, in any namespace, on its Service ports, and a workload's own NetworkPolicy sees the doorman, not the client, as the source. The doorman listens on all of its pod's interfaces: binding to the pod IP would only shut out containers in its own pod, so a NetworkPolicy on the doorman is the control that matters. The chart's NetworkPolicy (`networkPolicy.enabled`, off by default) leaves the doorman's routing ports open to every source unless you restrict them. On any cluster where untrusted pods share the network, enable `networkPolicy` and set `doorman.networkPolicy.ingressFrom`. Where the default is too wide:

- Admit only the callers your paused workloads really have, such as your ingress controller's namespace and the namespaces whose apps call paused services, with `doorman.networkPolicy.ingressFrom`. See [Wake on Request](../concepts/wake-on-request.md#restricting-who-can-reach-the-doorman).
- Give them an egress NetworkPolicy allowing only your pod CIDRs and the API server: set `doorman.networkPolicy.egress` (off by default; see [Helm values](helm-values.md#network-policy)). The doorman only dials Pod-backed IP endpoints of EndpointSlices the EndpointSlice controller manages, and refuses loopback, link-local, and cloud metadata addresses (AWS's `fd00:ec2::254` and Alibaba Cloud's `100.100.100.200` included). Anyone who can write EndpointSlices in a namespace can forge those labels, so the doorman also reads the pod each endpoint names and dials the address only if that pod is in the Service's namespace and has it: a forged slice can send it only to that namespace's own pods, never past a NetworkPolicy into another namespace. The operator checks the same way before it leaves a Service to pods it says are already serving it, so a forged slice can't keep a paused workload's Service from waking it either. The policy enforces the same bounds in the network.
- With `doorman.negForwarder.enabled`, the chart's policy also admits the forwarders, from any namespace, by their labels: `app.kubernetes.io/name: hybernate-forwarder`, and `hybernate.io/operator` naming Hybernate's namespace. Anyone who can create pods can give them those labels, and so reach the doorman's routing ports as a caller in `ingressFrom` can. A forwarder passes on only its own workload's ports, but the doorman can't tell its connections from those of a pod that only claims to be one.
- Its rate limits (see [Limits](../concepts/wake-on-request.md#limits)) slow a port scan through the doorman; they don't stop one. One workload can hold at most 256 of a doorman pod's 2048 places, so a workload that never becomes Ready can't stop other workloads' requests from being held.

The doorman's ServiceAccount can patch ManagedWorkloads, to set `hybernate.io/last-request` and `hybernate.io/last-request-from`, the caller's IP address, and `hybernate.io/turned-away`, where it counts the requests it [turned away](../concepts/wake-on-request.md#what-it-turns-away) from a workload that asks before waking. The chart's `<fullname>-doorman` ValidatingAdmissionPolicy (`doorman.admissionPolicy.enabled`, on by default) holds it to those three annotations: the API server refuses a patch from it that changes a ManagedWorkload's spec, labels, finalizers, owners or any other annotation. The Kustomize manifests include it too, taking the doorman's ServiceAccount as its parameter, so it follows the `namespace` and `namePrefix` set in `config/default` or in an overlay on top of it.

The operator learns which workload depends on which from `hybernate.io/last-request-from`, so the doorman must be the only one who sets it. With the same `doorman.admissionPolicy.enabled`, the chart's `<fullname>-request-stamps` ValidatingAdmissionPolicy refuses anyone but the doorman's ServiceAccount, the operator and cluster admins included, a create or change that sets `hybernate.io/last-request` or `hybernate.io/last-request-from` on a ManagedWorkload; removing one is allowed. To wake a workload, set `hybernate.io/wake-requested`, as `kubectl hybernate wake` does. With `watchNamespaces`, it applies in those namespaces only. Without it, the operator still ignores an address that wasn't written under Hybernate's field manager, but anyone who can patch a ManagedWorkload can write under that name with `--field-manager=hybernate`. The Kustomize manifests include it too, named `hybernate-request-stamps`, taking the doorman's ServiceAccount as its parameter like the doorman's policy, so it follows any `namespace` and `namePrefix`. If that ServiceAccount is deleted, both of the Kustomize manifests' policies let every write through, leaving stamps to the operator's field-manager check: the API server looks a policy's parameter up before anything else, so refusing without it would refuse every write to every ManagedWorkload, the operator's too.

## Verifying a release

Every release is built by the [release workflow](https://github.com/okedeji/hybernate/blob/main/.github/workflows/release.yml) from a tag, and signed keylessly with [Sigstore](https://www.sigstore.dev/): the signature is bound to that workflow and tag, so there's no key to steal. Each command below fails unless the artifact came from that workflow. The signatures are made with cosign 3, so verify them with cosign 3 or later.

**The image**, its signature, an SBOM for each platform, and build provenance:

```bash
IMAGE=ghcr.io/okedeji/hybernate:v0.2.0
cosign verify $IMAGE \
  --certificate-identity-regexp '^https://github.com/okedeji/hybernate/.github/workflows/release.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation $IMAGE --type spdxjson \
  --certificate-identity-regexp '^https://github.com/okedeji/hybernate/.github/workflows/release.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
gh attestation verify oci://$IMAGE --repo okedeji/hybernate
```

**The Helm chart:**

```bash
cosign verify ghcr.io/okedeji/charts/hybernate:0.2.0 \
  --certificate-identity-regexp '^https://github.com/okedeji/hybernate/.github/workflows/release.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

**The plugin, `install.yaml`, and the other release files**, through their signed checksums:

```bash
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/okedeji/hybernate/.github/workflows/release.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --check --ignore-missing checksums.txt
gh attestation verify kubectl-hybernate-linux-amd64.tar.gz --repo okedeji/hybernate
```

## Vulnerabilities

- **Every pull request** runs [govulncheck](https://go.dev/doc/security/vuln/) on the Go code, which fails on any known vulnerability the code can reach, and [Trivy](https://trivy.dev/) on the built image, which fails on critical and high vulnerabilities that have a fix. A vulnerability with no fix yet is reported, not blocking.
- **Every release** runs both again before anything is published, and attaches Trivy's full report for each platform to the GitHub release as `vulnerabilities-linux-amd64.txt` and `vulnerabilities-linux-arm64.txt`, alongside the SBOMs, `sbom-linux-amd64.spdx.json` and `sbom-linux-arm64.spdx.json`.
- **Every day**, both run against `main`, and Trivy against the released image, `ghcr.io/okedeji/hybernate:latest`, for both platforms, so a vulnerability disclosed after a release is reported then rather than at the next pull request.
- **Dependencies** (Go modules, GitHub Actions, base images) are kept current by Dependabot, weekly. Every Action is pinned to a commit, and the base images to a digest.

To report a vulnerability, see [SECURITY.md](https://github.com/okedeji/hybernate/blob/main/SECURITY.md): privately, through a GitHub security advisory.
