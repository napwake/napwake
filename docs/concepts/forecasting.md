# Forecasting

- **It always learns**, for every managed workload, whatever `autoResume` says: from every hour it's awake, and every hour it's paused that the doorman or an activity query watches, the doorman's even while the queries can't be evaluated. With [`wake: ask`](wake-on-request.md), it learns the hours the workload is paused behind the doorman from whether people woke it, since nothing else can. Once it's confident, it holds off pauses before hours it expects to be busy, on its own.
- **With `autoResume`**, it also wakes a paused workload 15 minutes ahead of the demand it expects. Turning `autoResume` on later loses nothing: the forecast has been learning all along, and wakes the workload ahead from then, once it acts.
- **With `pauseEarly`**, off by default, it also pauses an idle workload before `idleAfter` runs out, when it's confident the coming hours are quiet and a request would wake it; see [Pausing Early](#pausing-early).

Hybernate uses a **Holt-Winters double seasonal model** to learn your workload's demand patterns and predict future resource needs. This page covers the mathematical model, how it builds confidence over time, how soon it acts and how to choose that, how it can bring a pause forward, how it starts from your Prometheus history, and how it handles pattern shifts. Every figure on it is measured by the [simulator](#proving-it), which runs the operator's own engine and rules.

## The Idea

Most workloads follow predictable patterns:

- **Daily**: Traffic peaks during business hours, drops overnight
- **Weekly**: Weekdays are busier than weekends

Hybernate learns these two patterns independently by feeding hourly CPU utilization into a Holt-Winters model. Once it has enough data and confidence, it uses the learned patterns to:

- Hold off a pause when it's confident demand is coming in the hour under way or the next
- Wake paused workloads ahead of predicted demand, starting 15 minutes before an hour it expects to be busy
- With [`pauseEarly`](#pausing-early), which is off unless you turn it on, pause an idle workload before `idleAfter` runs out when it's confident the hours ahead are quiet. Otherwise it never causes a pause: the [activity clock](idle-detection.md) decides that

## What an Hour Records

The forecast learns CPU utilization: usage as a percentage of what the workload's own containers request in the pods reporting it, the same measure `cpuThreshold` is set in (see [Idle Detection](idle-detection.md#what-counts-as-activity)). Holding back a pause and waking ahead of demand compare its forecast with `cpuThreshold` directly, so a forecast that keeps an idle workload awake would also wake it once paused.

It's fed each hour once it's over, as the busiest moment seen in it:

- While the workload is awake, its CPU is read every minute, and the hour records the highest utilization. The peak, rather than the mean, is what the forecast's decisions need: the activity clock counts any minute above `cpuThreshold` as activity, so an hour busy only for its last ten minutes is an hour the workload has to be awake for. Usage in the first 15 minutes after a wake, including one by something else scaling it up such as `kubectl scale`, isn't counted: starting up isn't demand, and counted, a wake ahead of a busy hour would teach the forecast that the hour before it is busy too, so that it woke the workload earlier every week. Once it's warmed up, an hour the [activity clock](idle-detection.md#what-counts-as-activity) sees it in use in, from CPU above `cpuThreshold`, an activity annotation or a Prometheus query, counts as at least twice `cpuThreshold`, as an hour a request wakes it in does. A light-use workload's use may show only in an annotation or a query: taught its own light utilization on mornings `autoResume` had it ready, and a request's twice `cpuThreshold` on mornings people woke it, the forecast would have it ready only every other morning. A workload whose use stays under `cpuThreshold` with no other source is idle to Hybernate while it's awake, so `autoResume` can't learn its mornings; lower `cpuThreshold` below its use or add a source that sees it
- While the workload is paused behind the [doorman](wake-on-request.md) (`WakeOnRequest=True`) with `wake: request`, the default, any request would wake it, so a check that finds it still paused finds no demand. A request that wakes it counts as at least twice `cpuThreshold`, however little it then uses, so the hour people arrive in is learned as busy. That stands while its Prometheus activity queries, if it has any, can't be evaluated: a request would still wake it, so the hour is learned, busy if a request woke it or a query found activity, and quiet otherwise
- While the workload is paused with a [Prometheus activity query](../guides/prometheus-signals.md#while-the-workload-is-paused), the query is evaluated every minute, as while it's awake, and wakes it when it finds activity. So a check that finds the query quiet finds no demand, and one that finds activity counts as at least twice `cpuThreshold`, as a request does. That's how a workload no Service routes requests to, such as a queue worker, learns its nights: from a query of its queue's length. Without the doorman, a check where a query can't be evaluated can't see demand, so the hour isn't recorded; behind the doorman, the hour is learned as above. A query that can't be evaluated never wakes the workload
- While the workload is paused with [`wake: ask`](wake-on-request.md), only people wake it: a click on the doorman's asleep page, a [wake request](../guides/pause.md#wake-now) from their tooling or `kubectl hybernate wake`, or an activity annotation. Requests from anything else, such as crawlers, scanners and API clients, are turned away and wake nothing. If someone needed the workload, they'd have woken it, so behind the doorman (`WakeOnRequest=True`) a check that finds it still paused finds no demand, and one that finds a person waking it, or a query finding activity, counts as at least twice `cpuThreshold`, as a request does with `wake: request`. That way `autoResume` learns when people come and has it up before them. A check finds no demand only where people could have woken it, so an outage can't teach the forecast quiet hours: while the doorman doesn't route its Services, such as with the doorman turned off, unavailable, its routing failing or no Service to route, nobody can click Wake it, and while its Prometheus activity queries can't be evaluated, the doorman, which sees only people, can't say there was no other demand. Such a check sees demand only if someone woke the workload, so the hour isn't learned, as without the doorman. A workload no Service routes requests to, such as a queue worker, still learns from its queries while they can be evaluated, as with `wake: request`
- While the workload is paused with neither the doorman nor a query watching it, and `wake: request`, demand can't be seen, and the hour isn't recorded: demand it can't see isn't zero
- An hour is recorded only if it was checked from its start to its end. The hour under way is kept in memory, so after an operator restart or a leader change, the hour it happened in is skipped, unless it was only minutes old, rather than recorded from the part seen after it

## How It Works

The model tracks four components:

| Component | What it represents | Smoothing parameter |
|-----------|-------------------|-------------------|
| **Level** | The mean demand | \( \alpha = 0.01 \) |
| **Trend** | Whether demand is growing or shrinking | \( \beta = 0.001 \) |
| **Daily components** | 24 offsets, one per hour of day: the shape of an average day | \( \gamma_1 = 0.1 \) |
| **Weekly components** | 168 offsets, one per hour of week: how each weekday departs from that shape | \( \gamma_2 = 0.5 \) |

Each hour, the model takes the hour's utilization \( Y(t) \), compares it to what it predicted (for confidence scoring), and updates all four components using exponential smoothing. The model is **additive**: idle workloads spend much of their time at zero demand, where a multiplicative model, which divides by the level and the seasonal factors, diverges.

\[
\begin{aligned}
L(t) &= \max\Bigl(0,\; \alpha \cdot \bigl(Y(t) - D(t) - W(t)\bigr) + (1 - \alpha) \cdot \bigl(L(t{-}k) + k \cdot T(t{-}k)\bigr)\Bigr) \\[6pt]
T(t) &= \beta \cdot \frac{L(t) - L(t{-}k)}{k} + (1 - \beta) \cdot T(t{-}k) \\[6pt]
D(t) &= \gamma_1 \cdot \bigl(Y(t) - L(t) - W(t)\bigr) + (1 - \gamma_1) \cdot D(t - s_1) \\[6pt]
W(t) &= \gamma_2 \cdot \bigl(Y(t) - L(t) - D(t)\bigr) + (1 - \gamma_2) \cdot W(t - s_2)
\end{aligned}
\]

Where \( s_1 = 24 \) (daily season length), \( s_2 = 168 \) (weekly season length), and \( k \) is the number of hours since the previous observation (1, unless observations were missed).

After each update the components are renormalised: the daily components sum to zero, and for each hour of the day the weekly components across the seven days sum to zero. The shift is moved into the component above, which leaves every forecast unchanged and keeps the level the mean demand, so it is never negative.

The forecast for \( h \) hours ahead is never negative either:

\[
F(t+h) = \max\Bigl(0,\; L(t) + h \cdot T(t) + D(t+h) + W(t+h)\Bigr)
\]

A gap in the observations, from the operator being down or a workload paused with neither the doorman nor an activity query watching it, is skipped rather than filled in: there is no honest value to fill it with, and since slots are wall-clock hours (below), skipping an hour doesn't shift what was learned. The trend is carried across a gap of at most a day.

The first week initialises the model, as the first season does in classical Holt-Winters. The first time an hour of the week is seen, its weekly component is set so the forecast for that hour is what was seen, and the level, trend and daily components learn only from hours seen again. So after one week of office hours, the forecast for the next week is the week it saw, weekends quiet. The forecast isn't scored on an hour of the week it's seeing for the first time, since it had nothing to forecast it from, and doesn't act on one it hasn't seen.

## Phase Lifecycle

The engine doesn't start making decisions immediately. It progresses through phases as it collects data and builds confidence:

`Observing` → `DailySuggesting` → `DailyActive` → `WeeklySuggesting` → `FullyActive`

| Phase | Requires | Behavior |
|-------|----------|----------|
| **Observing** | No day of forecasts scored yet | Collecting data, its first week: an hour of the week is scored from the second time it's seen. No predictions yet. |
| **DailySuggesting** | Every hour of the day observed, and a day of forecasts scored | Daily confidence is being scored; predictions aren't acted on yet. |
| **DailyActive** | Daily confidence >= threshold | Predictions drive decisions. Weekly patterns still learning. |
| **WeeklySuggesting** | Every hour of the week observed at least once | Weekly confidence still being earned. |
| **FullyActive** | Weekly confidence >= threshold | Both daily and weekly patterns drive decisions. |

The gates count hours of the calendar, not data points: a workload that is only ever observed from 9 to 5 hasn't shown what happens at night, however many hours it has been watched.

That has a consequence for workloads paused with nothing to watch them: an hour paused without the [doorman](wake-on-request.md) or a [Prometheus activity query](../guides/prometheus-signals.md#while-the-workload-is-paused) isn't observed (see [What an Hour Records](#what-an-hour-records)), so a workload paused every night that way never has its nights observed, and its forecast never reaches `DailySuggesting`. Route it through the doorman if you want `autoResume` and the forecast's veto, with `wake: request`, or with `wake: ask`, whose paused hours behind the doorman are learned from whether people woke it, and for a workload no Service routes requests to, such as a queue worker, give it an activity query that sees its demand, such as its queue's length.

The ManagedWorkload shows the phase per season, in `status.prediction`: `dailyPhase` and `weeklyPhase` are each `Observing`, `Suggesting` or `Active`, next to `dailyConfidence` and `weeklyConfidence`. `DailyActive`, for example, is `dailyPhase: Active` with `weeklyPhase: Observing`. The phase shown, there and in `hybernate_prediction_phase`, is the one the forecast is acting in: after a [regime change](#anomaly-detection) leaves it at `WeeklySuggesting` with nothing scored, it's shown as `DailySuggesting` until a day of its forecasts has been scored and it drives decisions again.

The confidence threshold is the [forecast setting](#forecast-setting)'s, and a change applies at the next reconcile. A phase is earned at the threshold and lost 5 points below it, so confidence hovering at the threshold doesn't switch the forecast on and off every hour.

## Forecast Setting

How soon the forecast acts on what it has learned is one setting, in plain words: `eager`, `balanced` (the default) or `cautious`. Set it with the `hybernate.io/forecast` annotation on an [opted-in](../guides/opt-in.md#settings) workload or its namespace, the Helm value `defaults.forecast` for every opted-in workload, or `spec.prediction.mode` on a ManagedWorkload you write:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: preview-api
  annotations:
    hybernate.io/forecast: eager
```

| Setting | Acts at a confidence of | What it trades |
|---------|------------------------|----------------|
| `eager` | 60% | Acts on less regular patterns `balanced` waits out; wakes a workload for nothing more often |
| `balanced` | 75% | The default: acts on a weekday pattern after 8 days, and wakes a steadily used one for nothing almost never |
| `cautious` | 85% | Acts only on patterns it forecasts well; for workloads that are costly to keep awake for nothing, such as one on GPUs |

A wrong forecast costs a little money, never availability: it only wakes a workload early or keeps it up a while longer, and a request still wakes a paused workload through the doorman whatever the forecast says. So the choice is between mornings ready and hours awake for nothing. Over eight weeks of office hours kept loosely, people arriving up to an hour either side of 09:10 and away one weekday in four, twenty simulated workloads averaged:

| Setting | Mornings ready, of 30 | Hours woken for nothing |
|---------|-----------------------|-------------------------|
| `eager` | 10 | 14 |
| `balanced` | 8 | 8 |
| `cautious` | 4 | 5 |

On office hours kept steadily, every setting acts after 8 days and has every morning after ready, and none wakes the workload on a weekend: the settings differ only on patterns the forecast can't get right every day. Below 60, a threshold gains a morning or so on the most irregular patterns for two more hours woken for nothing; above 85, it loses mornings about as fast as it saves hours.

`spec.prediction.confidence` on the ManagedWorkload, from 50 to 100, overrides the setting's threshold with your own; a label-created ManagedWorkload keeps it when its annotations are applied again.

### How Long It Takes

Learning live from a fresh start, with `autoResume` on, behind the doorman, or, for the queue worker, woken by a Prometheus activity query, and for the public preview, only by people, with `wake: ask`, the simulator measures how many days the forecast takes to act (`DailyActive`) and to be fully active (`FullyActive`), for a workload at 40% of its CPU request while in use and 1% otherwise, over ten weeks (never: not within them):

| Workload | `eager` (60) | `balanced` (75) | `cautious` (85) |
|----------|--------------|-----------------|-----------------|
| Busy on weekdays from 09:10 to 17:30, quiet nights and weekends | 8 / 14 | 8 / 14 | 8 / 14 |
| The same, with people arriving and leaving up to 20 minutes either side and away one weekday in ten (the median of 20) | 8 / 14 | 8 / 14 | 8 / 14 |
| The same, with people arriving and leaving up to an hour either side and away one weekday in four (the median of 20) | 8 / 29 | 9 / never | 10 / never |
| The same hours every day, weekends included | 8 / 14 | 8 / 14 | 8 / 14 |
| Always busy | 8 / 14 | 8 / 14 | 8 / 14 |
| Always idle | 8 / 14 | 8 / 14 | 8 / 14 |
| A queue worker with no Service, which a query of its queue's length wakes, busy every day from 07:00 to 19:00, paused every night | 8 / 14 | 8 / 14 | 8 / 14 |
| A public preview only people wake, with `wake: ask`: someone clicks Wake it at 09:05 on weekdays, and it's quiet nights and weekends | 8 / 14 | 8 / 14 | 8 / 14 |

A test in the repo runs these and fails if they drift from this table.

Every pattern the forecast can get right takes the same time, at every setting: its first week is learned, not scored, since the first time it sees an hour of the week it has nothing to forecast it from, and once it has scored a day of the second week, it acts. On office hours that's the Tuesday of the second week, so the Monday before it is the only morning after the first week that the first request wakes the workload: every morning after is ready, Mondays included, and weekends are left asleep, since the first weekend taught the forecast they're quiet. A setting's threshold matters on patterns the forecast can't get right every day, such as the third row's, where a higher threshold waits for more evidence. An always-busy workload is never paused, and an always-idle one never woken or held awake by its forecast of no demand, so for them, acting changes nothing.

None of this delays idle detection or wake on request, which work from the first hour: until the forecast acts, the first request of the morning wakes the workload, instead of `autoResume` having it ready. And with a Prometheus, a new workload doesn't wait at all: it [learns from its history](#learning-from-history) first, and a workload with a few weeks of a steady pattern acts from the first day.

## When It Acts

`status.prediction` says whether the forecast is acting, and if it isn't, what it waits for and when it's expected to start:

| Field | What it says |
|-------|--------------|
| `acting` | Whether the forecast drives decisions: it holds off pauses before hours it expects to be busy and, with `autoResume`, wakes the workload 15 minutes ahead of them, and with [`pauseEarly`](#pausing-early), brings pauses forward before hours it expects quiet |
| `threshold` | The confidence it acts at: the setting's, or `spec.prediction.confidence` |
| `reason` | `Acting`; `Observing`, it hasn't scored a day of its forecasts yet, as in its first week, or hasn't seen every hour of the day; `EarningConfidence`, its forecasts of the last day aren't yet right enough; `Relearning`, demand broke from what it had learned, after a [regime change](#anomaly-detection); `NotLearning`, the workload is paused with nothing to see its demand: neither the doorman, its message saying why requests don't wake it and how to fix that, nor a Prometheus activity query that can be evaluated. Behind the doorman it learns whatever its queries do, its message ending `its Prometheus activity queries failing` while they can't be evaluated, except with `wake: ask`, where queries that can't be evaluated leave it `NotLearning` too |
| `message` | The same in a sentence, with its confidence and threshold once it has them, such as `learning: it has seen 100 of the 168 hours of the week, and scores its forecast of an hour from the second time it sees it; expected to start acting around Tue 24 Mar 00:00 UTC, if the coming days repeat the last week`. While the workload is paused, it ends with what it learns from then: `requests through the doorman`, `its Prometheus activity queries`, or both, or with `wake: ask` behind the doorman, `people waking it (a click on its asleep page, a wake request or an activity annotation)`, with its queries too if it has any. Not learning, it says why and how to fix it, with a link, such as `not learning while paused: no Service routes requests to it; a request can't wake it, so its demand can't be seen, and it learns only the hours it's awake. To learn the hours it's paused too, give it a Service, or, for a workload no request reaches, such as a queue worker, a Prometheus activity query that sees its demand: https://okedeji.io/hybernate/concepts/wake-on-request/#when-a-workload-isnt-routed` |
| `startsActing` | When it's expected to start acting |
| `history` | What it learned from Prometheus when Hybernate began managing the workload; see [Learning from History](#learning-from-history) |

`startsActing` is projected by running a copy of the forecast on through the demand it last saw in each hour of the week, until it would act: the same phase gates and confidence, on the assumption that the coming week looks like the last one. For an hour of the week it hasn't seen yet, it assumes the same hour on the nearest day it has; an hour of the day it has never seen, as a workload paused every night with nothing watching it leaves them, is never assumed, so such a workload is never projected to act. On office hours the projection is within a day of when the forecast does act from its second day, and within an hour once it has seen a weekend. It's unset while the forecast is acting, and, with the message saying so, when the forecast isn't expected to act within four weeks, as for a pattern too irregular for its setting. It's projected again once an hour, as the forecast learns.

`kubectl get managedworkloads -o wide` shows the reason in a `Forecast` column.

## Pausing Early

The forecast can bring a pause forward. With `pauseEarly` on, a workload that has been idle for 20 minutes is paused without waiting out `idleAfter` when all of these hold:

- **The forecast is acting, and sure the hours ahead are quiet:** every hour until `idleAfter` would run out, and at least the hour under way and the next, is forecast below `cpuThreshold`, judged as the veto and `autoResume` judge them, at the same confidence threshold. An hour of the week it hasn't seen, and the hours after one far quieter than it forecast, as on a holiday, are hours it can't say are quiet, and nothing is brought forward
- **A request would wake it:** its `wake.mode` is `Request`, the default, the [doorman](wake-on-request.md) has a Ready pod, and the workload's last pause found nothing wrong with its routing (`WakeOnRequest`). Every Service that selects the workload's pods is one the doorman would route while it's paused, on every port but those its `hybernate.io/doorman-ignore-ports` annotation lists: it isn't headless, has no UDP or SCTP port, doesn't keep traffic on the caller's node (`internalTrafficPolicy: Local`), has no other workload's Ready pods behind it, and, behind GKE container-native load balancing, has the doorman's [forwarder](wake-on-request.md#gke-container-native-load-balancing-negs) to reach it. At least one of them routes TCP, and their ports fit within the doorman ports the workload, and its namespace, may hold. A wrong forecast then costs a request a wake, a cold start, never its answer. If anything here doesn't hold, or can't be read, the workload waits out `idleAfter` as usual, and the operator logs why. Services without a selector, whose endpoints are written by hand, and `ExternalName` Services aren't checked, since nothing says which pods they reach. A workload whose doorman [asks before waking](wake-on-request.md#asking-before-waking) it never pauses early: a request doesn't wake it, so someone who came while the forecast expected quiet would find it asleep and have to ask, and it waits out `idleAfter` instead
- **Nothing else holds it:** an `active-until` hold, awake [dependents](dependencies.md) and the hour Hybernate waits after a [GitOps tool](../guides/gitops.md#what-hybernate-does) undid its last pause hold an early pause as they hold any. In [dry-run](../guides/dry-run.md), an early pause is a would-be pause, measured like any other

It's off unless you turn it on, with `hybernate.io/pause-early: "true"` on an [opted-in](../guides/opt-in.md#settings) workload or its namespace, the Helm value `defaults.pauseEarly` for every opted-in workload, or `spec.idlePolicy.pauseEarly: true` on a ManagedWorkload you write:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: preview-api
  annotations:
    hybernate.io/auto-resume: "true"
    hybernate.io/pause-early: "true"
```

It's a setting of its own, apart from the [forecast setting](#forecast-setting): that one says how sure the forecast must be before it acts at all, and this one whether, once it is, it brings pauses forward, so a `cautious` forecast can pause early too.

A pause the forecast brings forward has a `PausedEarly` event in place of `IdleDetected`, naming the hours it expects to be quiet: `idle for 20 minutes, since its CPU use was last above the activity threshold, and the forecast expects the hours from 18:00 to 20:00 UTC to be quiet; pausing before idleAfter (1 hour) runs out`. Until the workload wakes, `status.activity.forecastQuietUntil` holds when those hours end, and [`kubectl hybernate status`](../getting-started/kubectl-plugin.md#see-what-hybernate-is-doing) says so in NEXT: `paused early: the forecast expected quiet until Fri 20:00 UTC; wakes on a request or activity`.

Behind the doorman, the hours an early pause sleeps through are learned as any paused hour is (see [What an Hour Records](#what-an-hour-records)): a request would have woken the workload, so a check that finds it still paused finds no demand, and the forecast keeps learning.

### What It Saves and Costs

The hour people leave in is learned as busy, so the forecast can't bring a pause forward before that hour is over: on office hours ending at 17:30, the pause comes at 18:00, where the clock would have paused the workload at 18:29, an `idleAfter` after the last busy minute. The longer `idleAfter`, the more that saves. What it costs is a cold start for anyone who uses the workload in an hour the forecast expected quiet, before `idleAfter` would have run out: their request waits for it to wake, where it would have been up. The simulator measures both at `balanced`, for a workload at 40% of its CPU request while in use, over the weeks after the forecast acts: for a holiday, the week it's in, and for a change of hours, the two weeks after it.

| Workload | Hours awake a week, without → with | Extra cold starts a week |
|----------|------------------------------------|--------------------------|
| Busy on weekdays from 09:10 to 17:30, quiet nights and weekends | 48.7 → 46.3 | 0 |
| The same, with `idleAfter: 2h` | 53.7 → 46.3 | 0 |
| The same hours every day, weekends included | 68.1 → 64.8 | 0 |
| Office hours, people arriving and leaving up to 20 minutes either side and away one weekday in ten (the average of 20) | 45.2 → 43.3 | 0 |
| Office hours, people arriving and leaving up to an hour either side and away one weekday in four (the average of 20) | 38.2 → 37.8 | 0 |
| Office hours, the week of a holiday on a Wednesday | 39.8 → 37.8 | 0 |
| Office hours moving to 13:00 to 21:00, the two weeks after | 56.0 → 54.7 | 0 |
| Office hours, someone coming back for ten minutes 15 to 50 minutes after people left, one weekday in three (the average of 20) | 50.6 → 48.6 | 0.6 |
| Office hours, someone working in the evening too, one weekday in three, in three bursts with up to 25 minutes between them (the average of 20) | 55.4 → 52.2 | 0.5 |
| Always busy | 168.0 → 168.0 | 0 |
| Always idle | 0.0 → 0.0 | 0 |

A test in the repo runs these and fails if they drift from this table.

At the default `idleAfter` of an hour, that's half an hour a weekday, about 5% of the hours awake; at two hours, the same early pause saves an hour and a half a weekday. On every pattern the forecast has learned, holidays and changes of hours included, it costs no cold starts: it brings nothing forward on a holiday, since the forecast doubts what it expects from the holiday's first quiet hour, nor on the first two days of new hours, while it relearns, and nobody comes back in the hours it does pause through. Loosely kept hours save less: there are fewer days people leave to bring a pause forward from, and fewer evenings the forecast is sure of.

The cold starts come from use it can't foresee within `idleAfter` of the last: someone coming back after the others have left, or working in the evening in bursts, where each gap longer than 20 minutes, in an hour the forecast learned as quiet, finds the workload paused. That's what the 20 minutes are for: at 10, evening work like the row above's costs three times the cold starts for a quarter more of the hours saved, while on office hours any wait up to half an hour saves the same. So turn it on for workloads used to a pattern, where an hour after the last use is an hour paid for nothing every day; leave it off for one people come back to at odd hours, or set `active-until` while they do.

## Learning from History

With a Prometheus URL (`prometheus.url`, `--prometheus-url`), a new ManagedWorkload's forecast starts from the workload's history rather than from nothing: the operator reads up to the last four weeks of its CPU and learns them in one go, as it would have learned them live, then carries on live. A workload with a steady pattern acts from the first day, and one whose history is irregular or short goes on learning, as it would have, through the same phase gates and confidence.

Each hour of history is measured as a live hour is: the workload's own containers' usage, sidecars injected into its pods left out, against what they request, at its busiest moment in the hour, as at least twice `cpuThreshold` once above it. Its pods are found by the names a Deployment or StatefulSet gives them, so old revisions' pods are part of its history too. With a Prometheus that holds more than one cluster's series, such as Thanos, Mimir or a shared Prometheus, set `prometheus.selector` (`--prometheus-selector`), such as `cluster="staging"`: its matchers are added to the query, so a same-named workload in another cluster isn't read as this one. It's one range query per workload, a point an hour, of the busiest five-minute rate of `container_cpu_usage_seconds_total` in each hour, so Prometheus works it out and answers with at most 672 points; the operator asks for one workload at a time, waits up to 30 seconds for an answer, and tries a query that fails twice more, two minutes apart, before the forecast learns live only. Reading history never holds up a reconcile: the forecast learns live meanwhile, and takes up what was learned once it's ready.

What history can't tell, it assumes:

- **Hours none of its pods ran in** are learned as quiet, as an hour scaled to zero by something else is live. That includes hours Hybernate itself had it paused, if it managed the workload before: an hour paused behind the doorman or watched by an activity query is quiet anyway, but one paused with neither, whose demand live learning skips, can't be told from an hour nobody used
- **Requests** are today's. If the workload's CPU requests changed over the four weeks, its earlier hours are measured against the new ones
- **Use that shows only in requests or activity annotations** isn't in Prometheus: a light-use workload's history looks quieter than its live hours will
- **Five-minute rates** are smoother than the minute-by-minute readings live learning takes, so a burst of a minute or two counts for less

`status.prediction.history` says what came of it, with an event once:

| `history.reason` | Event | Meaning |
|------------------|-------|---------|
| `Learned` | `ForecastLearnedHistory` | Learned `hours` of history, `from` and `to`; the message says how much, such as `the forecast learned from 4 weeks of the workload's CPU history in Prometheus` |
| `NoHistory` | `ForecastLearningLive` | Prometheus has no CPU for its pods, as for a workload deployed with it; the forecast learns from now |
| `NoCPURequests` | `ForecastHistoryUnavailable` (warning) | Its containers request no CPU, so there's nothing to measure its use against |
| `QueryFailed` | `ForecastHistoryUnavailable` (warning) | Prometheus didn't answer three times; the message has its error |
| `NoPrometheusURL` | none | The operator has no Prometheus URL, so the forecast learns live from the start |

History is read once per ManagedWorkload, when it's new: one that has learned live keeps what it learned. Deleting and creating a ManagedWorkload again starts it afresh, from history again.

To see what the forecast would make of your workloads' history before opting them in, run [`kubectl hybernate forecast`](../getting-started/kubectl-plugin.md#replay-the-forecast).

## Confidence Scoring

Confidence is \( 1 - \text{WAPE} \), the **weighted absolute percentage error**: the total absolute error of the hourly forecasts over a window, divided by the total demand in it.

\[
C = 1 - \frac{\sum_{i} |F(i) - Y(i)|}{\max\bigl(\sum_{i} Y(i),\; n \cdot f\bigr)}
\]

A confidence of 75% means the forecast's total error over the window is 25% of the demand in it. Unlike an average of per-hour percentage errors, it is defined when demand is zero, and an hour of zero demand forecast as zero isn't counted as a perfect hour. The denominator is never less than \( n \cdot f \), where \( f \) is the workload's mean hourly demand over about a week, and at least 1% of the CPU requested: a quiet weekend day is judged against the demand of a typical day, and a workload that idles at a sliver of its request isn't judged on its noise.

- **Daily confidence** is scored over the last 24 scored hours
- **Weekly confidence** is scored over the last 168 scored hours: a whole week of weekdays and weekend must be forecast well, not just the day just gone
- Each needs its full window before it reports a score
- An hour of the week seen for the first time isn't scored, or judged an anomaly: there was nothing to forecast it from

## Anomaly Detection

Traffic patterns change. A marketing campaign, a new feature launch, or a seasonal shift can invalidate what the model has learned.

Hybernate detects these **regime changes** using z-score anomaly detection:

1. Each hour, the signed prediction error \( e(t) = Y(t) - F(t) \) is converted to a two-sided z-score using an exponentially weighted mean \( \mu \) and standard deviation \( \sigma \) of recent errors, which remember about a week:

    \[
    z(t) = \frac{|e(t) - \mu|}{\max(\sigma, f)}
    \]

    A sudden surge and a sudden disappearance of demand both count.

2. If \( z(t) > 3.0 \), the observation is flagged as an anomaly. The model learns from it only up to 3 standard deviations, so a single spike isn't learned as a pattern. An error that large in the same direction as the last one in the same hour of the week isn't an anomaly, though: it's a weekly pattern, such as a Tuesday night batch job, and the model learns it in full
3. If 3 or more anomalies occur within 24 observed hours, the engine declares a **regime change**

What breaks from the learned pattern, an anomaly or any hour in the day after a regime change, isn't learned into the weekly components at once: the change it would make to its hour of the week is held until the same hour the next day. If that hour breaks from its forecast the same way, the change lasted and is made; if it goes as forecast, the day was a one-off, such as a holiday, and the change is dropped. The level and daily components learn it as usual. Learned at once, a weekday holiday had the same weekday a week later forecast quieter than it is.

The hour after one far quieter than forecast, an anomaly, the forecast doubts what it expects: it neither holds off a pause nor wakes the workload until an hour goes as forecast, or a regime change has it relearn. One far busier than forecast casts no doubt, since the workload is in use then anyway.

On a regime change, the engine ends up at least one level below the phase it held before the anomalies began:

- `FullyActive` → `WeeklySuggesting`
- `WeeklySuggesting` or `DailyActive` → `DailySuggesting`
- `DailySuggesting` → `Observing`

If the anomalies have already cost it that level or more, by lowering its confidence, it stays where they left it: one regime change is never a second demotion, and never a promotion.

It then discards the evidence its confidence rested on: the scored errors, the anomaly statistics, and which hours of the week have been observed. Confidence has to be earned again on the new pattern before the forecast drives decisions again: whatever its phase, it neither holds back a pause nor wakes a workload until a full day of its forecasts has been scored again. A `RegimeChange` warning event is emitted once.

A weekday holiday looks like this: `autoResume` wakes the workload for the hours the forecast expects, and its first quiet office hour is an anomaly, so the forecast doubts the rest and the activity clock pauses the workload, an hour and a quarter awake for nothing. A regime change follows once three hours have been quiet. The forecast is off from then until a day has been scored, about 30 hours, so the first request of the next morning wakes the workload, and it's back on during that day. That day is busy again, so the holiday isn't learned into the week: the same weekday a week later, and every morning from the second after the holiday, is ready. Idle detection and wake on request carry on throughout. A single deviant day can't be told from the start of a new pattern until the next day shows which it was, so the forecast doesn't guess; it costs the next morning, and nothing after. A Saturday people come in on is the same: the Saturday after isn't woken, and the Monday after is the morning it costs.

A lasting change, such as a team moving its hours from 09:00 to 13:00, is noticed within the day as a regime change, and learned into the week from its second day. For about a week people wake the workload themselves; at `balanced`, from the second Tuesday every afternoon is ready, and within two weeks the forecast stops waking it for nothing, which until then it does a few times, for the old hours and, on weekends, for the new ones. A workload nobody uses any more is woken for its old hours twice, on its first quiet morning and the same weekday a week later, and then left asleep.

## Persistence

The engine's learned state (the model's components, the scored errors, the anomaly statistics, the weekly changes held for the next day and whether it doubts the hour under way, the last hour observed, and the demand last seen in each hour of the week, which [`startsActing`](#when-it-acts) is projected from) is stored in the ManagedWorkload's status, in `status.prediction.state`: gzipped JSON, base64-encoded, about 2.5 KB. It is written with each hourly observation. This means:

- The engine survives operator restarts and leader changes, and an hour observed before a restart isn't observed twice. The hour under way when the operator restarts is skipped. Restarted every day, at a different hour each day, the operator loses those hours and nothing else: the forecast starts acting within hours of when it would have without the restarts
- A last observed hour more than an hour ahead of the operator's clock, recorded by a clock that was wrong, isn't trusted: the next hour is observed as usual rather than refused until real time catches up
- No external database, persistent volume, or ConfigMap is needed
- Each workload has its own independent engine

State that can't be read, from an older version or a hand edit, is discarded with a `ForecastReset` warning event, and the engine starts learning again.

## Wall-Clock Alignment

Seasonal slots are keyed to wall-clock time, not to a running counter. The hour of day and day of week are counted in the operator's timezone (`--timezone`, the Helm value `timezone`, UTC by default), so business hours stay in their slots when the clocks change for daylight saving. Each workload records it in `status.prediction.timeZone`, and [`kubectl hybernate status`](../getting-started/kubectl-plugin.md#see-what-hybernate-is-doing) shows it. This means:

- If a workload is paused for 6 hours without the doorman, the model doesn't lose alignment. The next observation goes into the correct hour-of-day slot
- An hour paused behind the doorman, or watched by an activity query, is recorded as no demand: that's how the model learns that nights and weekends are quiet
- Monday 9am always maps to the same slot, regardless of gaps. In a timezone whose hours start at half or quarter past the hour in UTC, such as India's, hours, and the 15 minutes `autoResume` wakes ahead of one, are counted on the local clock

With `--timezone` set to its users' zone, a workload in New York or London is up at 08:45 local time every weekday through the clock change. Left at UTC, the operator's clock is an hour off its users' after the change, and the forecast has to learn their mornings again: in the simulator, four mornings the week of the change find the workload paused, and the forecast holds it awake for nothing on others, until about two weeks on.

## Tuning

The [forecast setting](#forecast-setting), or `spec.prediction.confidence` in its place, is the only thing to tune. The smoothing parameters are fixed, the same for every workload, and can't be configured:

| Parameter | Value | What a higher value would do |
|-----------|-------|------------------------------|
| \( \alpha \) | 0.01 | Level reacts faster to demand changes |
| \( \beta \) | 0.001 | Trend detection is more aggressive |
| \( \gamma_1 \) | 0.1 | Daily pattern adapts faster |
| \( \gamma_2 \) | 0.5 | Weekly pattern adapts faster |

Lower values give more stable, smoother forecasts; higher ones a more reactive but noisier model. A weekly component is updated once a week, so \( \gamma_2 \) is high enough for a lasting change in a weekday's hours to be half learned the week after it; the first week sets each one outright (see [How It Works](#how-it-works)). The values were chosen by simulating office-hours, always-on, idle, and noisy workloads.

## Proving It

A forecasting mistake, such as a wrong slot, a late wake or a weekend false wake, would otherwise show only after weeks in a real cluster. The repo's simulator, `internal/forecast/sim`, runs the operator's own forecast engine and decision rules through simulated time a minute at a time, as the operator looks at a ManagedWorkload: the activity clock pausing an idle workload, the forecast holding off a pause, `autoResume` waking it 15 minutes ahead of an hour expected busy, the doorman or an activity query waking it, the warm-up after a wake, each hour's peak fed to the forecast, and operator restarts through the state saved in status. Months of traffic take milliseconds, and a test runs six weeks of office hours through both the reconciler and the simulator and finds the same wakes and pauses at the same minutes.

The tests run in CI on every change, and check, besides [How Long It Takes](#how-long-it-takes):

- **Office hours**: once learned, the workload is up at 08:45 every weekday, 25 minutes before people arrive at 09:10, paused an hour after they leave, and left asleep at weekends; at every setting, every morning from the day the forecast acts is ready, Mondays included, and it never sleeps through a morning it acts on
- **Every day, always busy, always idle, light use** seen only through an activity annotation: each acts as above, and the light-use workload is ready every morning
- **Loosely kept hours**: ready on most mornings once learned, woken for nothing about twice a month, for an hour or two each time; and the settings trade ready mornings for hours woken for nothing as [Forecast Setting](#forecast-setting) says
- **A holiday, a Saturday at work, a permanent change of hours, a workload nobody uses any more, daylight saving** in New York and London, with and without `--timezone`, **daily operator restarts**, and **pauses with neither the doorman nor an activity query**, which never let the forecast act: as described above
- **Pausing early**: on office hours, every weekday's pause comes at 18:00, with mornings as ready and the same hours learned as without it; nothing is brought forward on a holiday or the first two days of new hours, and nobody finds the workload paused who wouldn't have; someone coming back after 18:00 finds the workload paused; and without the doorman nothing is brought forward. The six weeks run through the reconciler bring the same pauses forward at the same minutes. [What It Saves and Costs](#what-it-saves-and-costs) is measured there too
- **A queue worker with no Service**, woken by a query of its queue's length and paused every night: it acts after 8 days, as behind the doorman, and from then autoResume has it up before the morning's messages
- **A public preview only people wake**, with `wake: ask`, paused every night and woken by someone at 09:05 on weekdays: it acts after 8 days, as behind the doorman, and from then autoResume has it up at 08:45, before they come. One nobody wakes is learned as quiet, behind the doorman
- **Learning from history**: a forecast that learned four weeks of office hours has the workload ready from the first morning, and the [projection](#when-it-acts) made in the first week is within a day of when the forecast starts acting

`go run ./hack/forecastsim` runs every scenario at every setting and prints what it measured; `-scenario holiday -setting eager -days` shows one day by day, `-pause-early` has the forecast bring pauses forward, counting the hours that saved and the uses that then found the workload paused, `-wake-on-request=false -wake-on-query` runs a scenario as a queue worker its activity query wakes, and `-wake-on-ask` as a workload only people wake, with `wake: ask`. `-history` replays a workload's recorded CPU instead, as a percentage of its requests: a Prometheus range query's JSON, or CSV of `time,percent` rows, a step of at most seven minutes apart.

On your own cluster, [`kubectl hybernate forecast`](../getting-started/kubectl-plugin.md#replay-the-forecast) replays the forecast over each workload's own Prometheus history: whether it would act opted in today, and over the last two weeks, how many mornings it would have had each workload ready and how often it would have woken one for nothing. Named, as `kubectl hybernate forecast api -n orders`, it shows [one workload's forecast](../getting-started/kubectl-plugin.md#one-workloads-forecast): for one Hybernate manages, what its own forecast is doing, from its status and saved state, whatever history Prometheus has: its phase, the hours of the day and week it has seen, how many hours it has learned from history and live, whether it's acting or when it's expected to, why, and the hours of the next day it expects busy; then the replay of its history, day by day, when there's enough of it.
