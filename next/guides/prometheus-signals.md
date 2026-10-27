# Prometheus activity

> Count requests, connections or queue messages as activity, so a workload stays awake while it's used and wakes when it's needed.

Source: https://docs.napwake.dev/next/guides/prometheus-signals/

CPU tells Napwake whether a workload is working. A Prometheus query tells it whether anyone uses it. That’s requests through an ingress, open connections or messages on a queue.

Any result above zero keeps the workload awake, and [wakes it](https://docs.napwake.dev/next/guides/prometheus-signals/#while-the-workload-is-paused) once it’s paused.

1. Give the operator your Prometheus’s address in the Helm value `prometheus.url`, where you keep your other values.

   * Helm

     napwake-values.yaml

     ```yaml
     timezone: Europe/London
     prometheus:
       url: http://prometheus.monitoring.svc.cluster.local:9090
     ```

     Upgrade with the file, at the chart version you run (`helm list -n napwake-system`), or Helm upgrades the chart too:

     ```bash
     helm upgrade napwake oci://ghcr.io/napwake/charts/napwake --version X.Y.Z \
       --namespace napwake-system -f napwake-values.yaml
     ```

     Installed without a values file, see [Keeping your settings](https://docs.napwake.dev/next/operations/upgrading/#keeping-your-settings).

   * Argo CD

     Add it to the Application’s `valuesObject`, as on [Installation](https://docs.napwake.dev/next/getting-started/installation/), and commit. Don’t run `helm upgrade`, since the next sync undoes it.

     Argo CD Application

     ```yaml
     spec:
       source:
         helm:
           valuesObject:
             timezone: Europe/London
             prometheus:
               url: http://prometheus.monitoring.svc.cluster.local:9090
     ```

   * Flux

     Add it to the HelmRelease’s `values`, as on [Installation](https://docs.napwake.dev/next/getting-started/installation/), and commit. Don’t run `helm upgrade`, since the next reconcile undoes it.

     Flux HelmRelease

     ```yaml
     spec:
       values:
         timezone: Europe/London
         prometheus:
           url: http://prometheus.monitoring.svc.cluster.local:9090
     ```

   The operator restarts with it. From the manifests, add the operator flag `--prometheus-url`, as [Installation](https://docs.napwake.dev/next/getting-started/installation/) adds `--timezone`. This prints the URL it runs with:

   ```bash
   kubectl get deployments -n napwake-system -o yaml | grep -- '- --prometheus-url'
   ```

2. Add a query to the ManagedWorkload.

   managedworkload.yaml

   ```yaml
   spec:
     idlePolicy:
       idleAfter: 1h
       activity:
         prometheus:
           - promQL: 'sum(rate(nginx_ingress_controller_requests{exported_namespace="staging", exported_service="my-api"}[5m]))'
   ```

   Queries add to CPU, deploys and activity annotations, the other activity sources. Any one keeps the workload awake.

   There’s no annotation for queries. On a ManagedWorkload created from the `napwake.dev/managed` label, add them to its `spec.idlePolicy.activity.prometheus`. The opt-in controller keeps them when it updates the rest from the annotations.

3. Test the same query in Prometheus.

   ```bash
   curl -s 'http://prometheus:9090/api/v1/query' \
     --data-urlencode 'query=sum(rate(nginx_ingress_controller_requests{exported_namespace="staging", exported_service="my-api"}[5m]))'
   ```

   A result above zero is activity ([How a result counts](https://docs.napwake.dev/next/guides/prometheus-signals/#how-a-result-counts)).

4. Once the workload has had requests, check what kept it awake.

   ```bash
   kubectl get managedworkload my-api -n staging -o jsonpath='{.status.activity.lastActivitySource}'
   ```

CheckpointAwake

`prometheus` means a query was the most recent source of activity.

## The Prometheus address

| URL   | Rule                                                                                                       |
| ----- | ---------------------------------------------------------------------------------------------------------- |
| Is    | The base of the Prometheus HTTP API, `http` or `https`. The operator refuses to start with any other       |
| Gets  | `/api/v1/query` appended, with any path prefix kept, so `https://mimir.example.com/prometheus` works as-is |
| Sends | No credentials or headers. Reach a Prometheus that needs them through a proxy that adds them               |

With a URL, each new workload’s forecast also starts from up to four weeks of CPU history. So `autoResume` can act from day one ([Learning from history](https://docs.napwake.dev/next/concepts/forecasting/#learning-from-history)).

Where Prometheus holds several clusters’ series, `prometheus.selector` (`--prometheus-selector`), such as `cluster="staging"`, narrows that history query to this cluster. Activity queries run as written, so narrow those in the query.

## How a result counts

Every check, about once a minute, awake or paused, runs each query in turn as an instant query (`/api/v1/query`). It stops at the first that shows activity. Each has 2 seconds, and all of a workload’s 5 seconds together.

| The result                                              | Means                                                                                                                                                                      |
| ------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Any series above zero                                   | Activity. The [activity clock](https://docs.napwake.dev/next/concepts/idle-detection/) resets. With a series per pod, one busy pod is enough                                                       |
| Zero, or empty                                          | No activity from this source. The clock keeps running                                                                                                                      |
| `NaN` or infinite                                       | Ignored, as no evidence either way. `NaN` is what a ratio of two idle counters gives (0/0). A result with nothing else in it counts as empty, and the other sources decide |
| A range vector (`metric[5m]` on its own)                | `QueryFailed`. Return an instant vector or a scalar                                                                                                                        |
| Over 4 MiB, such as a series per pod across the cluster | Refused too. Aggregate with `sum` or `max`                                                                                                                                 |

## While the workload is paused

1. Asleep · paused

   Paused

   Its queries still run every minute, not every 5.

2. Activity shows in Prometheus

3. Waking up · resuming

   A query shows activity

   A `WokeByActivity` event says `a Prometheus activity query shows activity, waking`. Its dependencies wake with it.

4. Within a minute

5. Awake · running

   Awake

   `lastActivitySource: prometheus`

That’s how a workload no request reaches wakes on its own. No Service routes requests to a queue worker, so query its queue’s length:

managedworkload.yaml

```yaml
spec:
  idlePolicy:
    idleAfter: 30m
    activity:
      prometheus:
        - promQL: 'sum(rabbitmq_queue_messages{queue="tasks"})'
```

| While paused                                                                                    | What happens                                                                                                                                                                                   |
| ----------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| What to query                                                                                   | Something that exists while it’s paused, such as the queue’s broker. The workload’s own metrics have no series while it has no pods                                                            |
| A query already showing activity as a [pause you ask for](https://docs.napwake.dev/next/guides/pause/#pause-now) begins | It doesn’t undo the pause, such as with messages still queued. It wakes the workload only after a check finds it quiet, then active again. An idle pause begins only once every query is quiet |
| A query that can’t be evaluated                                                                 | Never wakes it. `PrometheusAvailable` is `False`, as while awake. Its message says the queries can’t wake it until Prometheus answers, and whether a request through the doorman still does    |
| The [forecast](https://docs.napwake.dev/next/concepts/forecasting/#what-an-hour-records)                                | Learns the hours the queries watch, quiet or busy. So a worker paused every night still learns whole days. With `autoResume`, it wakes ahead of its first messages once the forecast acts      |

## When a query can’t be evaluated

Napwake can’t see that activity. So it doesn’t pause the workload, and a failing query doesn’t wake a paused one. The time isn’t counted as idle either ([When a source can’t be read](https://docs.napwake.dev/next/concepts/idle-detection/#when-a-source-cant-be-read)).

`PrometheusAvailable`, only on workloads with queries, says why:

| Reason                  | Meaning                                                                                                                                                                           |
| ----------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `EndpointNotConfigured` | Queries are configured, but the operator has no `--prometheus-url`                                                                                                                |
| `QueryFailed`           | Prometheus returned an error, a range vector or an oversized result. Or it didn’t answer a query within 2 seconds, or all of the workload’s within 5. The message has the details |

```bash
kubectl get managedworkload my-api -n staging \
  -o jsonpath='{.status.conditions[?(@.type=="PrometheusAvailable")]}'
```

### A query that matches nothing

A query that answers but matches no series looks like a quiet workload, as does a misspelt metric or label. Napwake can’t tell them apart, so the workload pauses on its other sources as usual.

| `PrometheusQueryEmpty`    |                                                                                                                                                                     |
| ------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Set to `True`             | Reason `NoSeries`, naming the queries, for a query that has matched nothing since its first evaluation. A `PrometheusQueryEmpty` warning event marks when it begins |
| Cleared                   | Once the query matches a series, even one at zero. A counter created on the first request, as many client libraries’ labelled counters are, matches once it exists  |
| After an operator restart | Checked from each query’s first evaluation again, since matches are kept in memory                                                                                  |

### When Prometheus is down

When Prometheus fails, every workload’s queries fail at once. When one query fails, only the workloads that use it do.

| When                                                                                                    | What happens                                                                                                                                                                                                                                                         |
| ------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Prometheus doesn’t answer, answers with a server error or `429`, or times out on three queries in a row | The operator doesn’t ask it again for a minute. Every workload’s queries fail at once, as `QueryFailed` with the error. None waits out a timeout and holds up queued wakes. The message stays the same for the whole outage, whether or not a check asked Prometheus |
| It’s out for longer than a couple of checks                                                             | The clock restarts (`lastActivitySource: unobserved`)                                                                                                                                                                                                                |
| Prometheus rejects a query, such as one that doesn’t parse                                              | It fails only for the workloads that use it                                                                                                                                                                                                                          |
| A query times out while others answer                                                                   | It fails only for the workloads that use it, so one tenant’s slow query doesn’t hold off everyone’s                                                                                                                                                                  |

## Example queries

| Use case                       | PromQL                                                                                                      |
| ------------------------------ | ----------------------------------------------------------------------------------------------------------- |
| Requests through ingress-nginx | `sum(rate(nginx_ingress_controller_requests{exported_namespace="staging", exported_service="my-api"}[5m]))` |
| Requests seen by the app       | `sum(rate(http_requests_total{service="my-api"}[5m]))`                                                      |
| Open WebSocket connections     | `sum(websocket_connections{service="my-api"})`                                                              |
| Messages waiting on a queue    | `sum(rabbitmq_queue_messages{queue="tasks"})`                                                               |
| Kafka consumer lag             | `sum(kafka_consumer_lag{group="worker"})`                                                                   |

ingress-nginx labels each request with its namespace and Service. Scraped by the Prometheus Operator, which keeps `namespace` and `service` for the scrape target, they become `exported_namespace` and `exported_service`.

Give both. Preview environments often share Service names, and another’s traffic would keep this one awake.

ingress-nginx is retired upstream, its repository archived, and it’s still the controller Napwake’s tests run on.

In [dry-run](https://docs.napwake.dev/next/guides/dry-run/#what-dry-run-measured), nothing goes through the doorman, so a request-rate query is how it sees requests. Without one, a request counts only through the CPU it uses.

## Writing good queries

* Count activity, such as requests per second. A result above zero means use, so a query for “no requests” reads the wrong way round.
* `rate(...[5m])` stays above zero for about five minutes after the last request. That’s usually right. One request keeps the workload awake, and the clock starts once traffic stops.
* Keep queries cheap. Aggregate, and use recording rules for anything heavy.
* Leave out traffic that isn’t people. Health checks, uptime monitors and scrapers counted by the query keep a workload awake, and wake it once paused. Filter them by path or user agent where your metrics allow:

```promql
sum(rate(nginx_ingress_controller_requests{exported_namespace="staging", exported_service="my-api", path!~"/healthz|/metrics"}[5m]))
```

Next

* [Idle detection →Every source of activity, and when a workload pauses.](https://docs.napwake.dev/next/concepts/idle-detection/)
* [Dry run →Measure a workload before it pauses.](https://docs.napwake.dev/next/guides/dry-run/)
