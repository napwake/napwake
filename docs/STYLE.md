# Napwake docs style

How every page of Napwake's docs is written. Kede (CMO) owns the words. Ara (CTO) owns every fact. Read it before writing a line.

## The one rule
**Every fact, half the words.** Cut words, never facts. Defaults, flags, field names, limits and caveats all stay. What goes is everything around them: preamble, repeats, hedges, and examples the point doesn't need.

## Write like a person who's busy
- **Short sentences.** Most under 12 words, none over 20. One idea each.
- **Short paragraphs.** Three sentences at most. Then a list, a table, a code block or a new heading.
- **Lead with what the reader does or gets.** The why comes after, if it's needed at all.
- **Show, then say.** The command or the YAML first, one line under it to say what it does.
- **Plain words.** "use", not "utilize"; "about", not "approximately"; "so", not "therefore".
- **Talk to the reader.** "You", "your cluster". Active voice.
- **Say it once.** If another page explains it, link there.

## Never (the tells of machine writing)
- Em dashes. Use a full stop or a comma.
- "Not X, but Y" and "It's not just X, it's Y".
- A colon that sets up a reveal ("Here's the thing: …", "The result:"). Don't trade em dashes and semicolons for colons. A colon goes only before a list, a code block or a table. Anywhere else, write two sentences.
- Lists of three for rhythm. Use as many items as there are.
- "Note that", "It's worth noting", "Importantly", "Simply", "Just", "In short", "Essentially", "In other words".
- Rhetorical questions.
- A bold phrase at the start of every bullet.
- Semicolons in prose. Parentheses inside parentheses.
- "Seamless", "powerful", "robust", "effortless", "leverage", "ensure" (say "makes sure" or nothing).
- Restating the heading in the first sentence.

## Page shape
1. **Title** that names the thing or the task ("Pause and resume", "Wake on request").
2. **One sentence** saying what the page is for. No "This page covers…".
3. **The answer, the steps or the table.** The common case first.
4. **Details** that most readers don't need go in a collapsible `<details>` or the reference, never deleted.
5. **Next:** two or three links to where the reader goes after.

## Kinds of page
- **Guides** (do a task): numbered steps, one command each, what you'll see after it. A checkpoint at the end ("You'll see `paused` in `kubectl napwake status`").
- **Concepts** (how it works): one picture of the idea first (a scene with the doors, a diagram), then the rules, short. Edge cases in a table.
- **Reference** (look it up): tables. One line per row. No prose between tables except a sentence that says what the table is.
- **Operations** (run it): symptoms first, then the fix.

## The doorman and the doors
- The doors carry the states: asleep, waking up, awake, needs you (plain words in scenes; `paused`, `resuming`, `running` in the CLI's words).
- The doorman appears where he earns it: a concept's opening scene, a guide's start or finish, empty states. One per page at most, never decoration.
- When the text names him, he only holds the door or wakes a workload on the first request ([decision 0013](https://github.com/napwake/brand/blob/main/guidelines/doorman.md)). Everything else Napwake does is "Napwake …".

## Before and after
> **Before (96 words):** The doorman passes a held request on as soon as one of the workload's pods is Ready, and Kubernetes decides Ready from the pod's readiness probe. A pod without one is Ready as soon as its containers start, which is often before the app inside can answer: while it loads its configuration or schema, connects to its database, or warms a cache. The request that woke it then gets whatever the app answers while it starts, such as a `503`, instead of waiting for it. An app that isn't listening yet is fine: the doorman tries again every second until `maxWait`.
>
> **After (71 words, every fact kept):** The doorman lets the request through once a pod is Ready. Ready comes from the readiness probe. Without one, a pod is Ready when its containers start, often before the app can serve. The request then gets whatever the app answers, such as a `503`. Give the app a probe that passes only once it can serve. If the app isn't listening yet, the doorman retries every second until `maxWait`.

> **Before:** Napwake never pauses a workload it can't measure, and a confident forecast can hold off a pause.
>
> **After:** If Napwake can't measure a workload, it never pauses it. A confident forecast can also hold a pause off.

## Checks before a page is done
- Prose (everything but code, tables and component props) down by half. Tables don't excuse prose. Moving a fact into a table is fine, but the page's prose still has to halve.
- Every fact in the old page is in the new one, or listed in the page's facts ledger as moved (to `<details>` or the reference) with where.
- No line from the "Never" list. Search for em dashes, "not just", "Note that", ";".
- The build passes with no broken links.
