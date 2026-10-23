# Public Preview Environments

For a team with an environment for each pull request, on a public URL: Hybernate pauses each preview while nobody uses it, and only people wake it. Someone who opens it in a browser gets a page with a **Wake it** button, and their click wakes it; a pull request workflow, a portal or a chat command wakes it for them. Crawlers and scanners, which reach every public address every few minutes, wake nothing.

Opt in each preview's namespace, with the doorman asking before it wakes anything in it:

```yaml title="namespace.yaml"
apiVersion: v1
kind: Namespace
metadata:
  name: preview-42
  labels:
    hybernate.io/managed: "true"
  annotations:
    hybernate.io/wake: ask
```

## Why ask before waking

With the default, `wake: request`, any request to a paused workload wakes it, except from the probes and health checks the doorman recognises. On the public internet, most requests come from robots: search engines' crawlers, link previewers, and scanners that try every address for known weaknesses. Many present an ordinary browser's `User-Agent` on purpose, so no list of them can keep up. A preview anyone can reach is woken by them every few minutes, and is hardly ever paused.

With `wake: ask`, the doorman asks first. A person's browser gets a page saying the preview is asleep, with a **Wake it** button, and only a click on it wakes the preview. Everything else, such as `curl`, API clients, crawlers and scanners, gets a `503` saying how to wake it, and wakes nothing. Your tooling wakes it with an annotation.

A preview behind a VPN, single sign-on at the ingress, or an allowlist of your offices' addresses is out of the robots' reach. Keep `request` there: the first request wakes it, with no button to click. See [Environments on the public internet](../concepts/wake-on-request.md#environments-on-the-public-internet).

## Turning it on

Put the `hybernate.io/managed` label and `hybernate.io/wake: ask` on each preview's namespace, so that every Deployment and StatefulSet in it is managed and asks before waking. Where they go depends on what creates the namespace:

=== "Namespace"

    When the namespace is in your manifests, label and annotate it there:

    ```yaml title="namespace.yaml"
    apiVersion: v1
    kind: Namespace
    metadata:
      name: preview-42
      labels:
        hybernate.io/managed: "true"
      annotations:
        hybernate.io/wake: ask
    ```

=== "Argo CD"

    An Application that creates the preview's namespace, with `CreateNamespace=true`, sets them in `managedNamespaceMetadata`. It also has to leave the replicas to Hybernate, or it scales the preview back up after every pause:

    ```yaml title="application.yaml"
    apiVersion: argoproj.io/v1alpha1
    kind: Application
    metadata:
      name: preview-42
      namespace: argocd
    spec:
      project: default
      source:
        repoURL: https://github.com/your-org/previews.git
        targetRevision: main
        path: previews/preview-42
      destination:
        server: https://kubernetes.default.svc
        namespace: preview-42
      ignoreDifferences:
        - group: apps
          kind: Deployment
          jsonPointers:
            - /spec/replicas
      syncPolicy:
        automated: {}
        managedNamespaceMetadata:
          labels:
            hybernate.io/managed: "true"
          annotations:
            hybernate.io/wake: ask
        syncOptions:
          - CreateNamespace=true
          - RespectIgnoreDifferences=true
    ```

    Argo CD sets `managedNamespaceMetadata` only in a sync with `CreateNamespace=true`; see the Argo CD tab of [A whole namespace](opt-in.md#a-whole-namespace) for a namespace it created before, and [With GitOps](opt-in.md#with-gitops) for StatefulSets.

=== "Kustomize"

    With Flux, or anything else that applies a kustomization, put the Namespace in the kustomization, saved as `namespace.yaml` beside it, as in the first tab:

    ```yaml title="kustomization.yaml"
    apiVersion: kustomize.config.k8s.io/v1beta1
    kind: Kustomization
    namespace: preview-42
    resources:
      - namespace.yaml
      - ../base
    ```

To have every workload opted in on the cluster ask, whatever its namespace, set the Helm value `defaults.wake`:

```yaml title="hybernate-values.yaml"
defaults:
  wake: ask
```

A workload's own `hybernate.io/wake` wins over its namespace's, and the namespace's over the Helm default; see [Which setting wins](opt-in.md#which-setting-wins). A value other than `request` or `ask` is read as `ask`, with an `InvalidSetting` warning event, so a typo never lets robots wake a preview.

The doorman reads each request to tell a person's browser from a robot, so it needs plain HTTP: TLS ended at the ingress or load balancer in front of the preview, as is usual. A workload that ends TLS itself is never woken by a click. People, though, need to open the preview over HTTPS: a browser says a click came from the page only to an HTTPS address, and a click that doesn't say wakes nothing; see [What browsers, robots and API clients see](../concepts/wake-on-request.md#what-browsers-robots-and-api-clients-see). Behind a GKE Ingress or Gateway, it also needs the doorman's [forwarder](../concepts/wake-on-request.md#gke-container-native-load-balancing-negs).

### A preview of more than one workload

Every workload in the namespace asks, which suits what people open in a browser. What only other workloads call, such as an API the frontend calls inside the cluster, a database or a queue worker, is woken with what people open when it's a [dependency](../concepts/dependencies.md) of it. Hybernate learns that from the frontend's environment, such as an `API_URL` that names the API's Service, or you declare it with `hybernate.io/depends-on`. One click then wakes the whole preview, and the dependencies stay up as long as the frontend does. To see what wakes with a workload:

```bash
kubectl hybernate deps web -n preview-42
```

```
preview-42/web (Deployment, Paused)
Depends on:
  preview-42/api   learned from API_URL   Paused
Depended on by:
  nothing
Learned links come from the dependent's environment or its requests; hybernate.io/ignore-dependencies drops one.
```

A workload that's nobody's dependency is woken only by tooling while it asks: its callers are other workloads, which are turned away, and a database's protocol isn't HTTP, so its connections are closed. Have it wake on any request instead, with the annotation on the workload, which wins over the namespace's. No robot reaches a Service that no Ingress exposes, so that wakes it only for its real callers:

```yaml title="deployment.yaml"
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  annotations:
    hybernate.io/wake: request
```

See [When to keep waking on request](../concepts/wake-on-request.md#when-to-keep-waking-on-request).

### Checking it

Once the preview has paused, after an hour with no activity by default, each workload says what wakes it, and its Services are routed to the doorman:

```bash
kubectl get managedworkloads -n preview-42 \
  -o custom-columns='NAME:.metadata.name,WAKE:.spec.wake.mode,PHASE:.status.phase,DOORMAN:.status.conditions[?(@.type=="WakeOnRequest")].reason'
```

```
NAME   WAKE      PHASE    DOORMAN
api    Request   Paused   DoormanRouted
web    Ask       Paused   DoormanRouted
```

Any other reason under DOORMAN says why requests can't reach the doorman; see [When a workload isn't routed](../concepts/wake-on-request.md#when-a-workload-isnt-routed).

## What people see

1. **The asleep page.** Someone opens the preview, say `https://pr-42.preview.example.com/orders/7`, and gets a page saying it's asleep, with a **Wake it** button. It's sent as `200 OK` with `Cache-Control: no-store` and `X-Robots-Tag: noindex`, runs no script, can't be shown in another site's frame, and names no namespace, workload or Service. Nothing wakes.
2. **The click.** The button posts back to the same address, with a token the doorman signed for this preview and this pause, good for 10 minutes. From a browser that says the click came from the page, as every browser from 2023 on does over HTTPS, the preview wakes, with what it depends on, and a `WokenByRequest` event says a person asked.
3. **The waking-up page,** which says the preview is starting, and for how long, and reloads every 3 seconds. Once one of its pods is Ready, the next reload is the page they opened, `/orders/7`.

A page left open longer than 10 minutes says it had expired, and offers the button again. Once someone has clicked, others who open the preview get the waking-up page, then the app, without clicking. The preview pauses again once it has had no activity for `idleAfter`, an hour by default. See [The Wake it button](../concepts/wake-on-request.md#the-wake-it-button) for the token, and for what a click from another site gets.

A browser from before 2023, or any on a page opened over plain HTTP, doesn't say where a click came from, so its click wakes nothing: the page comes back saying so, and that `kubectl hybernate wake`, a [wake request](#waking-it-from-tooling) or a current browser over HTTPS wakes the preview instead. A browser posting a form of the app's own after the preview paused, such as its login form, gets the asleep page too, and nothing wakes until someone clicks.

## What robots and API clients see

Anything but a person's browser opening a page or posting a small form, such as `curl`, scripts and API clients, crawlers and link previewers such as `Googlebot`, headless browsers, scanners, or a page's own `fetch`, gets a `503` saying the preview is asleep and how to wake it. Nothing wakes:

```bash
curl -i https://pr-42.preview.example.com/
```

```
HTTP/1.1 503 Service Unavailable
Content-Type: text/plain; charset=utf-8
Cache-Control: no-store
Retry-After: 300
X-Robots-Tag: noindex

This environment is asleep. A request doesn't wake it: only a person who opens it in a browser and clicks Wake it, or their tooling, can.
Open it in a browser and click Wake it; set the hybernate.io/wake-requested annotation on its ManagedWorkload; or run kubectl hybernate wake: https://okedeji.io/hybernate/guides/pause/#wake-now
https://okedeji.io/hybernate/concepts/wake-on-request/#asking-before-waking
```

A client that sends `Accept: application/json` gets the same in JSON. Probes and health checks the doorman recognises are answered as for any workload, and a connection that isn't HTTP is closed. Once someone has woken the preview, every caller is served as usual. See [What browsers, robots and API clients see](../concepts/wake-on-request.md#what-browsers-robots-and-api-clients-see).

## Waking it from tooling

Tooling wakes a preview by setting the `hybernate.io/wake-requested` annotation on its ManagedWorkload to a new value, whatever its wake setting. Hybernate acts on each value once, records it in `status.lastWakeRequest`, and says what came of it in the `WakeRequest` condition, so the tool can tell its request was taken up; see [Wake Now](pause.md#wake-now). A ManagedWorkload created from the label is named after its workload, such as `web`. Waking it wakes what it depends on, too.

### When a pull request is pushed

A deploy to a paused preview doesn't wake it: its new pods start at its next wake. To have it up for whoever reviews each push, wake it from the workflow that deploys it, and wait until it's Running. In GitHub Actions:

```yaml title=".github/workflows/preview.yaml"
name: Preview
on:
  pull_request:

jobs:
  preview:
    runs-on: ubuntu-latest
    steps:
      # ...the steps that deploy the preview and give kubectl access to the cluster
      - name: Wake the preview
        env:
          NS: preview-${{ github.event.pull_request.number }}
          TOKEN: ${{ github.run_id }}-${{ github.run_attempt }}
        run: |
          kubectl wait managedworkload/web -n "$NS" --for=create --timeout=1m
          kubectl annotate managedworkload web -n "$NS" --overwrite hybernate.io/wake-requested="$TOKEN"
          kubectl wait managedworkload web -n "$NS" --for=jsonpath='{.status.lastWakeRequest}'="$TOKEN" --timeout=10m
          answer=$(kubectl get managedworkload web -n "$NS" \
            -o jsonpath='{.status.conditions[?(@.type=="WakeRequest")].status} {.status.conditions[?(@.type=="WakeRequest")].message}')
          case "$answer" in
            True*) ;;
            *) echo "::error::${answer#* }"; exit 1 ;;
          esac
          kubectl wait managedworkload web -n "$NS" --for=jsonpath='{.status.phase}'=Running --timeout=10m
```

On a pull request's first push, Hybernate makes the ManagedWorkload from the label moments after the deploy, so the first `kubectl wait` waits for it to exist; `--for=create` needs kubectl 1.31 or later. A request set before Hybernate has taken the ManagedWorkload on is answered as for any awake preview, `already awake`. The token is the run's ID and attempt, so each run asks afresh, a re-run included. The next `kubectl wait` waits for Hybernate to take the request up. A request made while a pause or a wake is under way is answered once that finishes, as [Wake Now](pause.md#wake-now) says, so while the preview is starting this wait lasts as long as its start does, and has the same 10 minutes as the last. When Hybernate won't wake the preview, the step fails with its reason, such as `not woken at 2026-10-23 21:01 UTC: it's scaled to zero outside Hybernate, which leaves it off until its replicas are set above zero`. The last waits until the preview is Running. Its dependencies wake with it; to wait for them too, add a `kubectl wait` for each.

### From a terminal

```bash
kubectl hybernate wake web -n preview-42 --wait
```

```
waking preview-42/web...
preview-42/web is Running after 2s
```

`--wait`, the default, waits for Hybernate's answer and then until the workload is Running, both within `--timeout`, 5 minutes by default. A request made while a pause or a wake is under way is answered once that finishes, and the same 5 minutes covers that wait too; for a preview that takes longer to start, give it more, such as `--timeout 10m`. See [Wake a Workload](../getting-started/kubectl-plugin.md#wake-a-workload).

### From a portal or a chat bot

Anything that talks to the Kubernetes API can ask. A portal that has just made the environment gets `404 Not Found` until Hybernate has made its ManagedWorkload from the label, a moment after the workload, so it retries until the ManagedWorkload exists. With `curl`, a `PATCH` sets the annotation, printing nothing unless it fails, with the API server's address in `$APISERVER`, its CA certificate in `ca.crt`, and a token for the bot's ServiceAccount in `$KUBE_TOKEN`:

```bash
NS=preview-42
REQUEST=chat-$(date -u +%Y%m%dT%H%M%SZ)
curl --fail-with-body -sS -o /dev/null --cacert ca.crt -X PATCH \
  -H "Authorization: Bearer $KUBE_TOKEN" \
  -H 'Content-Type: application/merge-patch+json' \
  -d '{"metadata":{"annotations":{"hybernate.io/wake-requested":"'"$REQUEST"'"}}}' \
  "$APISERVER/apis/hybernate.io/v1alpha1/namespaces/$NS/managedworkloads/web"
```

Then read the ManagedWorkload until Hybernate has answered that request, every second or two. A request made while the preview is pausing or waking is answered once that finishes, so keep reading for as long as the preview takes to start:

```bash
curl --fail-with-body -sS --cacert ca.crt -H "Authorization: Bearer $KUBE_TOKEN" \
  "$APISERVER/apis/hybernate.io/v1alpha1/namespaces/$NS/managedworkloads/web" |
  jq -r --arg request "$REQUEST" 'select(.status.lastWakeRequest == $request)
    | .status.conditions[] | select(.type == "WakeRequest") | "\(.status) \(.message)"'
```

```
True woken as requested at 2026-10-23 21:03 UTC
```

It prints nothing until then. `False` gives the reason in the message, and once it's `True`, `.status.phase` says when the preview is `Running`.

### The access tooling needs

A Role in the preview's namespace, bound to the ServiceAccount your tooling runs as, such as one in a `ci` namespace:

```yaml title="preview-waker.yaml"
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: preview-waker
  namespace: preview-42
rules:
  - apiGroups: [hybernate.io]
    resources: [managedworkloads]
    verbs: [get, list, watch, patch]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: preview-waker
  namespace: preview-42
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: preview-waker
subjects:
  - kind: ServiceAccount
    name: preview-waker
    namespace: ci
```

- **`kubectl annotate`** takes `get` and `patch`.
- **`kubectl wait`** takes `get`, `list` and `watch`: it lists the one ManagedWorkload by name before it watches it, and without `list` it waits out its timeout even once the condition is met.
- **`kubectl hybernate wake`** takes `get`, `list` and `patch`.
- **An HTTP client,** such as a portal's, takes `patch` to ask, and `get`, or `watch`, to read the answer.

People with Kubernetes' built-in `edit` or `admin` role in the namespace have all of these once Hybernate is installed.

## Up before people come

With `wake: ask`, only people wake a paused preview, so behind the doorman the forecast learns from every hour it's paused: busy if someone woke it, with a click, a wake request or an activity annotation, and quiet otherwise. If someone had needed it, they'd have woken it. An hour the doorman isn't routing it, or its activity queries can't be evaluated, isn't learned unless someone woke it, so an outage can't teach the forecast quiet hours; see [What an Hour Records](../concepts/forecasting.md#what-an-hour-records). Turn on `autoResume`, and once the forecast is confident, it wakes the preview 15 minutes ahead of the hour people usually come:

```yaml title="namespace.yaml"
apiVersion: v1
kind: Namespace
metadata:
  name: preview-42
  labels:
    hybernate.io/managed: "true"
  annotations:
    hybernate.io/wake: ask
    hybernate.io/auto-resume: "true"
```

On a weekday pattern, it acts after 8 days and is fully active after 14. From then, someone who clicks **Wake it** at about 09:05 on weekdays finds the preview up from 08:45, and weekends are left asleep. See [How Long It Takes](../concepts/forecasting.md#how-long-it-takes). A new preview starts from nothing, since its workloads have no history to [learn from](../concepts/forecasting.md#learning-from-history), so one for a pull request merged within a week is gone before the forecast acts. It pays off for previews that last, such as one for each team or long-lived branch, or a demo environment.

Wakes from tooling count as people's, so a workflow that wakes the preview on every push teaches the forecast the hours your team pushes in. A preview that asks never [pauses early](../concepts/forecasting.md#pausing-early): someone who came while the forecast expected quiet would find it asleep.

The forecast says what it learns from:

```bash
kubectl get managedworkload web -n preview-42 -o jsonpath='{.status.prediction.message}'
```

```
learning: it has seen 0 of the 24 hours of the day, and can't act until it has seen them all; while it's paused, it learns from people waking it (a click on its asleep page, a wake request or an activity annotation)
```

## What it turned away

The doorman counts the requests it turns away during each pause: the `503`s and closed connections, not people shown the asleep page, nor probes and health checks. It's a count only, with nothing of the requests themselves. It's in the ManagedWorkload's status, brought up to date about once a minute, for the current pause or, once the preview is awake, the last one:

```bash
kubectl get managedworkload web -n preview-42 -o jsonpath='{.status.turnedAway}'
```

```
{"pausedAt":"2026-10-23T20:41:44Z","requests":3}
```

[`kubectl hybernate status`](../getting-started/kubectl-plugin.md#see-what-hybernate-is-doing) lists the workloads that turned requests away:

```
Turned away while asleep, by a doorman that asks before waking: something automated may call these.
  preview-42/web   3 requests during the pause from 6m ago
```

On a public URL, crawlers and scanners account for some of them: that's what asking is for. More than you'd expect can mean something of yours calls the preview: an uptime monitor, a webhook, or another service. A person is never turned away, so it's worth finding out what it is:

- **An uptime monitor or health checker:** add its `User-Agent` to the Helm value `doorman.healthCheckUserAgents`. It's then answered as a health check, and no longer counted.
- **Something that needs the preview awake,** such as a test suite or a webhook that exercises it: have it set a [wake request](#waking-it-from-tooling) first.

`hybernate_doorman_wakes_total{result="turned_away"}` counts them as they happen; see [Metrics](../reference/metrics.md#doorman) and [What it turns away](../concepts/wake-on-request.md#what-it-turns-away).

## What it can't do

- **Stop a robot that clicks.** A robot that loads the page and clicks **Wake it** wakes the preview as a person does, at most once a minute, whichever doorman pod it reaches, and the preview then stays up for `idleAfter`. That's rare, and costs a wake.
- **Keep a preview private.** Asking keeps robots from waking a preview, not people from using it: anyone with its URL can click and see what's on it. For anything that must stay private, put it behind a VPN, single sign-on at the ingress, or an allowlist, and keep `wake: request`.
