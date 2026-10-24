# Compatibility

Napwake supports Kubernetes 1.30 and later, and is tested in CI on 1.30 and 1.37.

[Wake on request](../concepts/wake-on-request.md) adds an EndpointSlice to each Service of a paused workload, one per IP family, pointing at the doorman. Anything that finds a Service's backends through its EndpointSlices sends traffic to the doorman without changes. Each project reads them a little differently, so this page records what's been checked.

**Tested** means an end-to-end test runs it on every change. **Tried by hand** means it was run once on a test cluster for the release listed. **Checked against source** means the project's endpoint code was read for the release listed, but nothing runs it yet.

## Service proxies

| Proxy | Status | Notes |
|-------|--------|-------|
| kube-proxy, kind's default mode | Tested | ClusterIP Services, called from pods in the cluster and through ingress-nginx |
| kube-proxy, other modes (ipvs, nftables), and NodePort and LoadBalancer Services | Not tested yet | kube-proxy programs these from the same EndpointSlices as ClusterIP, but no test runs them. Their traffic usually reaches the doorman from a node's address, which `doorman.networkPolicy.ingressFrom` must admit as an `ipBlock`; see [Restricting who can reach the doorman](../concepts/wake-on-request.md#restricting-who-can-reach-the-doorman) |
| NodePort and LoadBalancer Services with `externalTrafficPolicy: Local` | **Partial** | Routed, but kube-proxy sends external traffic only to endpoints on the node it arrives at, so it reaches the doorman only through nodes that run a doorman pod; through any other node it's dropped, and the workload doesn't wake |
| Cilium kube-proxy replacement (v1.20) | Checked against source | |

## Ingress controllers and Gateway API

| Controller | Status | Notes |
|------------|--------|-------|
| ingress-nginx (v1.15) | Tested | nginx picks up endpoint changes on a timer, so for a second or two after a pause it can still send a request to the removed pod. It also closes upstream requests after 60 seconds by default; see [How long a connection is held](../concepts/wake-on-request.md#how-long-a-connection-is-held) |
| Traefik (v3.7): Ingress, IngressRoute, Gateway API | Checked against source | |
| Envoy Gateway (v1.9) | Checked against source | |
| Contour (v1.33) | Checked against source | |
| Kong Ingress Controller (v3.5) | Checked against source | |
| HAProxy Kubernetes Ingress (v3.2) | Checked against source | |
| Cilium Ingress and Gateway API (v1.20) | Checked against source | |

## Cloud load balancers

| Load balancer | Status | Notes |
|---------------|--------|-------|
| AWS Load Balancer Controller, target type `instance` | Checked against source | Traffic reaches the doorman through the NodePort |
| AWS Load Balancer Controller, target type `ip` | Checked against source | Registers the doorman pods as targets. The controller must watch the Napwake namespace, which it does unless `--watch-namespace` is set |
| GKE Ingress (GKE 1.36) on instance groups, its Service with `cloud.google.com/neg: '{"ingress": false}'` | Tried by hand | Traffic reaches the doorman through the NodePort. Google Cloud's load balancers send nothing to a backend that fails its health check, so the doorman answers Google's (`GoogleHC/`) with `200` while the workload is paused, without waking it, and holds the request that follows: the backend stayed healthy while paused, and a request through the Ingress woke the workload. See [On GKE](../concepts/wake-on-request.md#on-gke) |
| HTTPS health checks on the traffic port, any provider | **Partial** | The doorman can't read an encrypted health check, so it can't tell it from a request, and each check wakes the workload: it never stays paused longer than the check interval. AWS target groups that use HTTPS, and Google Cloud backend services that use HTTPS or HTTP/2, check over HTTPS by default. Check over HTTP, or on a port of its own listed in `napwake.dev/doorman-ignore-ports`; see [Health checks the doorman can't recognise](../concepts/wake-on-request.md#health-checks-the-doorman-cant-recognise) |
| Services on GKE (1.36) that no GKE Ingress uses | Tried by hand | Routed like any other: GKE adds `cloud.google.com/neg: '{"ingress": true}'` to every Service of a VPC-native cluster, but no NEG exists until a GKE Ingress uses the Service, so the annotation alone doesn't keep it from the doorman. A request through ingress-nginx woke a paused workload. See [On GKE](../concepts/wake-on-request.md#on-gke) |
| GKE container-native load balancing (NEGs) | **Not supported** by default | A Service whose pods GKE puts in network endpoint groups isn't routed: the NEG controller rejects endpoints whose pods are in another namespace, and can stop syncing the load balancer altogether. That's a Service a GKE Ingress uses while it has `cloud.google.com/neg: '{"ingress": true}'`, one with standalone NEGs (`exposed_ports`, which GKE Gateway sets), and one the NEG controller lists NEGs for in `cloud.google.com/neg-status`. A warning event names them, and the workload reports `WakeOnRequest=False`, reason `UnsupportedLoadBalancer`, when no other Service of its is routed. GKE adds `cloud.google.com/neg: '{"ingress": true}'` to every new Service of a VPC-native cluster, the default, but that asks for NEGs only should a GKE Ingress use the Service, so it doesn't keep a Service from being routed. The optional forwarder, below, routes such a Service; see [On GKE](../concepts/wake-on-request.md#on-gke) |
| GKE Ingress with NEGs (GKE 1.36), with `doorman.negForwarder.enabled` | Tried by hand | Optional, off by default. While a workload is paused, a forwarder Deployment beside it takes the doorman's place in the Service's endpoints, which the NEG controller accepts, and passes connections to the doorman. The NEG held the forwarder's pods, healthy, while the workload was paused; a request through the Ingress woke it, and a browser got the waking-up page; after the wake the forwarder was removed with no request failing. Right after a pause, GKE takes under a minute to swap the NEG's endpoints, and a request then can get a `502`. GKE Gateway, whose Services always use NEGs, is checked against ingress-gce's source only. See [GKE container-native load balancing](../concepts/wake-on-request.md#gke-container-native-load-balancing-negs) |

## Service meshes

| Mesh | Status | Notes |
|------|--------|-------|
| Linkerd (edge-26.9) | Checked against source | Connections to the doorman are plain TCP, without mTLS |
| Istio sidecar mode, PERMISSIVE mTLS (v1.31) | Tried by hand | Istio's default. Callers in the mesh reach the doorman in plain text, and the doorman passes the connection to the woken pod, whose sidecar accepts it |
| Istio sidecar mode, STRICT mTLS (v1.31) | **Partial** | The request wakes the workload, but the woken pod's sidecar refuses the doorman's plain-text connection, so the caller gets a 503. Requests once it's Running succeed |
| Istio ambient mode (v1.31) | **Not supported** | ztunnel builds a Service's backends from its pods, not its EndpointSlices, so callers in the mesh never reach the doorman and the workload doesn't wake. Callers outside the mesh still do |
| Istio AuthorizationPolicies that match on the caller's identity | **Not supported** (not tried) | Connections from the doorman carry no caller identity, so a policy that allows only certain callers denies them |

If you run something that isn't listed, or hit a problem with something that is, please [open an issue](https://github.com/napwake/napwake/issues). For a workload on a public URL, which crawlers would wake, have the doorman [ask before waking](../concepts/wake-on-request.md#asking-before-waking) it with `wake.mode: Ask`; to keep the doorman off a port, list it in `napwake.dev/doorman-ignore-ports` on the Service; to turn waking on request off for the whole cluster, set the Helm value `doorman.enabled: false`.
