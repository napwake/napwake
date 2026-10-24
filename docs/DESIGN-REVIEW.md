# Napwake docs: design checkpoint (phase 1)

For Kede and the CEO, 2026-10-22. The theme and six pages, designed in full, in light, dark and at phone width, to look at before the design goes out to every page. Nothing is pushed or published.

**To look:** the screenshots in `screens/` (below), or run it: `npm ci && npm run build && npm run preview`, then open http://localhost:4321. `npm run site:local` serves the versioned site (0.2 as latest, and next) on :4400.

| Page | What it is | Screens (`screens/…`) |
|---|---|---|
| Home | the docs' front door | `home-{desktop,phone}-{light,dark}.jpg` |
| Wake on request | a concept page | `concept-wake-on-request-…` |
| Quickstart | a guide | `guide-quickstart-…` |
| Labels and annotations | a reference page | `reference-labels-annotations-…` |
| Forecasting | the maths page | `maths-forecasting-…` |
| 404 | No room by that name | `404-…` |
| Versions | next's banner, the switcher | `versions-next-banner.jpg`, `versions-latest-switcher.jpg` |

Lighthouse (mobile, its default): performance 97 to 100 and accessibility 100 on all six (`screens/lighthouse.txt`). The build fails on any broken link or anchor; all pass.

## The idea, everywhere

The docs are a hotel at night, with the doorman at its door: **a livery header and quiet corridors**. The brand appears where it carries meaning, never as decoration.

- **The header is his livery:** Coat teal (Coat deep in dark), with the Lamp piping of his collar as a 2px line along its foot. The one-colour lockup sits on it with the eyes and hat band cut in the header's colour (the brand's `symbol-on-livery`), then "DOCS" and the version.
- **Type:** Big Shoulders Display for page titles only; everything else is Atkinson Hyperlegible Next, at 17px with room to breathe; commands and labels in Atkinson Hyperlegible Mono. Section labels (sidebar groups, eyebrows, table headers) use the brand's state-label style: mono capitals with 0.06em tracking.
- **Colour:** only the tokens. Links and the current page take `action`: Coat in light, Lamp in dark, like light from a door. The state colours stay for states. Flat surfaces separated by hairlines, no shadows, except the one window mock-up.
- **Doors mean states, and only states.** Every door glyph sits beside its word, so colour is never the only signal. Doors are drawn by the brand's rig from state, never by hand.
- **Code reads like a terminal:** a muted `$` before each command (copying skips it, and continuation lines get none); a command's output is joined under it, with a dashed rule and an OUTPUT tag. The syntax colours are three calm inks: teal keys and commands, amber strings, muted comments.
- **Callouts** are the brand's: a surface with a coloured edge and a plain title. "Needs you" (danger) carries the door with the pinned note, never an alarm. Starlight's own icons are gone: the brand allows no other icon set.
- **Maths** in cards (KaTeX, built ahead, nothing loaded from elsewhere). **Mermaid** drawn at build time in the theme's colours, following light and dark.
- **Every page** opens with its section as an eyebrow, the title, and a lede when it has one. The site footer gives the mark, the licence and the illustrations' credit line.

## Each page, and why

**Home.** It answers "what is this, and is it for me" in one screen, then shows the proof.
- **The hero:** the headline (CMO decision 0002) runs full width, over the plain line, "It never deletes a workload or its data.", Install (primary) and Quickstart. Beside them, the corridor: dev asleep, staging waking with the doorman's hand on the door, preview awake. The caption says what's happening in plain words.
- **"See what's idle first":** the brand's number, then `kubectl napwake scan` with an extract of the plugin docs' real output, lines and columns trimmed, labelled as an extract.
- **"Three states, one door each":** door cards, plain word first ("ASLEEP · paused").
- **"How it works":** the lifecycle as three doors, in the old Mermaid diagram's own words, with the loop back.
- **A note on who it's for:** behind a VPN, SSO or an allowlist (decision 0002, rule 4).
- **What it does:** the old Key Features as cards, with the brand's UI icon where one exists. "Scan before you install" is left out because the section above shows it.
- **The quick example**, and Get started, with the doorman's "This way".

Left out, and why: "Intelligent Kubernetes workload lifecycle management" (decision 0001); "Why Napwake?" (the number says it with a source); "under 5 minutes" (no source; MIGRATION.md, doubt 2).

**Wake on request (a concept).** Readers come to learn what happens to a request that reaches a paused workload. The page shows that before it explains it:
- The corridor scene, with staging waking.
- Its facts at a glance: on by default, any TCP protocol, held up to `wake.maxWait`, a waking-up page for browsers.
- The wake as three doors (routed to the doorman, held and woken, passed through), with what moves the workload between them, above the four numbered steps.
- In "What wakes a workload, and what doesn't", a door beside every outcome: ajar where it wakes, shut where nothing does. The long table scans in a second.
- The waking-up page's screenshot in a browser frame.

The rest is the page as it was, in the theme.

**Quickstart (a guide).** The reader is about to run commands in a real cluster.
- The lede is the promise.
- A map of the eight steps shows the sample workload's door after each: awake until step 6 pauses it, awake again after step 7, and step 5 marked "skip for the sample". The reader sees the whole path and jumps to any step.
- The prerequisites sit apart in "Before you start".
- Every command has its `$`, and every output is joined to its command.
- "What's next" ends the page as cards with the doorman pointing.

**Labels and annotations (a reference).** Readers arrive looking for one name.
- An index of all 31 names, by group, at the top; each row has its own anchor (`#label-napwake-dev-managed`), so one label can be linked to.
- The settings' order of precedence drawn as a chain.
- Wider tables (the index stands in for "On this page"), with room for the description column.
- A soft edge on any table wider than the screen shows it scrolls.

**Forecasting (the maths).** A dense page that needs the idea before the model.
- The morning list opens it, labelled by the doorman's rule: Napwake, not the doorman, wakes workloads before the team arrives.
- The three opening points become three cards: always learns, `autoResume`, `pauseEarly`.
- "A weekday of office hours" draws the page's own measured times as three rows of states:
  - the activity clock alone: the first request wakes it at 09:10;
  - with `autoResume`: up at 08:45;
  - with `pauseEarly` too: paused at 18:00 instead of 18:29.
- The phase lifecycle is drawn as a track.
- The formulas are set in cards.

**404.** The doorman shrugs: "No room by that name". The text is `hack/docsite`'s own words ("These docs have no such page"), then the docs home, search, and four ways in.

## Built for AI readers (CMO decision 0004), on every page

- **Under each page's title** (on the home, above the footer), a quiet row labelled "For your assistant": Copy as Markdown, View .md, Ask Claude ↗ and Ask ChatGPT ↗. They're words on hairline buttons, with no borrowed icons, and they wrap at phone width. The assistant links open with "Read <the page's .md>, a page of Napwake's docs, and answer my questions about it.", with no tracking and no referrer. The 404 has none.
- **Every page as Markdown** at its address plus `.md`, with absolute links and a `text/markdown` link in its head. `llms.txt` says what Napwake is, who it's for and how it works, in the brand README's words, with the key pages as Markdown. `llms-full.txt` and `llms-small.txt` carry every page. All of these work under each version's base.
- **`robots.txt`** welcomes every crawler and names latest's sitemap.
- **A social card per page** in the launch card's style: the wordmark, the page's section and title, its lede or description, and the doorman in a pose for the page's job (`screens/cards/`). The home's title is now "Napwake docs".

## New words, for Ara to check (beyond the voice edit)

| Where | Words | From |
|---|---|---|
| Home, hero | The headline; the plain line; "It never deletes a workload or its data." | CMO decision 0002; the old home |
| Home, hero caption | "A request has reached staging while it was asleep. The doorman holds it at the door while staging wakes, then lets it through. …" | Wake on request, How it works 2 and 3 |
| Home, scan | The number (10 hours a day, 50 of 168, 70%); the extract and its note | CMO decision 0001; kubectl plugin, Scan a cluster and History from Prometheus |
| Home, states and flow | Door cards and the lifecycle | The old home's Mermaid labels, word for word |
| Home, note | "On the public internet, crawlers and scanners wake a paused workload every few minutes, so it frees little." | Wake on request, Environments on the public internet |
| Home, Get started | "Helm or the Kustomize manifests, with Argo CD or Flux." and two more | Installation; Quickstart; kubectl plugin |
| Wake on request | The caption; the four glance values; the three flow texts; "The page a browser sees while the workload starts." | That page: How it works, How long a connection is held, Browsers |
| Quickstart | The map's short titles and notes ("dry-run", "skip for the sample", "not managed"); "Before you start" | That page's headings and steps 3 to 8 |
| Forecasting | The caption; the timeline's rows, notes and caption, with waking drawn as a few minutes | Proving it; What it saves and costs; "until the forecast acts, the first request of the morning wakes the workload" |
| Labels and annotations | The lede; the four precedence steps | That page's sections; Settings annotations |
| 404 | "It may have moved, or the link may be from another version of the docs." | New. The rest is `notfound.html`'s |
| `llms.txt` | Its three paragraphs and the key pages' one-liners | The brand README; Quickstart; Idle detection; Wake on request; Forecasting; each page linked |
| Every page | "For your assistant"; the assistant prompt; the cards' alt text | New |

## What I'd still change, and limits

- **Screenshots:** desktop shots used headless Chrome as the brief says. Phone widths and full pages used the same Chrome through Playwright, because Chrome's `--window-size` won't go below about 500px.
- **The waking-page screenshot** is the product's current, older page, and it contradicts its own page (MIGRATION.md, doubt 1). Once Ara ships cmo-002's waking page, the docs can show the brand's `WakingPage` live.
- **On a phone, wide reference tables scroll sideways.** Phase 2 turns each row into a stacked card at phone width (a build plugin that labels each cell with its column), keeping the table for screen readers.
- **The doorman is the brand's rig,** not an illustrator's hand (as the design system says).
- **Pages not designed yet** are converted and themed, but keep Title Case headings inside the page. Their titles and the sidebar are in sentence case already.

## Phase 2, after the CEO's yes: every page, one at a time

Each page gets its own design pass, then its own voice edit in a separate commit so Ara can check it for truth. Every page gets a lede (what it's for, in a sentence), its section eyebrow, and a "Next" at the end where a reader goes on. Scenes go on pages, never inside steps; doors only where a state is meant.

| # | Page | Reader, and what it's for | What it shows | Components |
|---|---|---|---|---|
| 1 | Home | done in phase 1 | | |
| 2 | Getting started / Installation | A platform engineer installing for the first time | A choice up front: Helm, Kustomize, Argo CD, Flux (tabs that remember the choice across the page); prerequisites as a checklist; "Verify installation" as what you'll see; uninstall and restore as a calm needs-you section; next steps | Panel, Tabs with a shared key, TerminalBlock, Callout, NextSteps, Doorman "This way" |
| 3 | Getting started / Quickstart | done in phase 1 | | |
| 4 | Getting started / kubectl plugin | Someone running one command, back for its flags | The "which command" table as cards, one per command, with its UI icon (scan, status, pause, wake, forecast, nodes) linking to its section; each command's real output as a TerminalBlock; flags as reference tables with row anchors | Card grid with Icon, TerminalBlock, Ref, RefIndex |
| 5 | Concepts / Architecture | An engineer judging what runs in their cluster | The Mermaid diagram redrawn as a brand diagram: operator, doorman, API server, metrics-server, Prometheus, with the doorman at the Service; the reconciler's nine checks as a numbered rail | a new `ComponentMap`, StateFlow (down) |
| 6 | Concepts / Lifecycle | Someone reading a ManagedWorkload's status | Every phase as a door, with the triggers between them (automatic and on request) as a StateFlow loop; conditions and events as anchored reference rows | StateFlow (across), StateBadge (product words), Ref |
| 7 | Concepts / Forecasting | done in phase 1 | | |
| 8 | Concepts / Idle detection | Someone asking why a workload did or didn't pause | The activity clock as a timeline: sources of activity on one axis, `idleAfter` counting down to the pause; "when a source can't be read" as needs-you callouts; the watch pose at the top, labelled as Napwake's activity clock | Doorman (watch), DayTimeline, Callout |
| 9 | Concepts / Dependencies | Someone with a web app and its database | A corridor of two rooms waking together; declared and learned dependencies side by side; across namespaces as a permission chain | Scene, Cards, Precedence |
| 10 | Concepts / Wake on request | done in phase 1 | | |
| 11 | Concepts / Cost tracking | Someone asked "what did this save?" | Freed and saved as a bar of what pausing frees and what came off the bill, from the page's own example; how saved is measured, step by step; status fields as reference rows | a small data chart (brand palette), StateFlow, Ref |
| 12 | Guides / Opting in | Someone labelling their first namespace | The label and its annotations on one manifest; "which setting wins" as the Precedence chain; Kustomize and GitOps as tabs; protected namespaces as a callout | Precedence, Tabs, Callout |
| 13 | Guides / ManagedWorkload | Someone writing the CR by hand | The full example with each field linking to its row; spec reference as anchored rows | Ref, RefIndex, annotated code |
| 14 | Guides / Pause and resume | Someone pausing on purpose | Pause and resume as two doors with what happens to replicas, Services and the finalizer; "workloads nobody comes back to" with the doorman at rest | StateFlow, Doorman (lights, rest) |
| 15 | Guides / Cluster Autoscaler | Someone whose nodes don't scale down | Why freed isn't saved until nodes go; recommended settings for each autoscaler as tabs; "what keeps a node" as a checklist; verifying it works, with the real `kubectl napwake nodes` output | Tabs, Panel, TerminalBlock |
| 16 | Guides / Node pools for real savings | The same reader, one step further | A pool of its own drawn as nodes emptying; Karpenter, GKE, AKS and EKS as tabs; "what waking costs" plainly | a small diagram, Tabs, Callout |
| 17 | Guides / Dry run | Someone not ready to let it pause | Measuring, then pausing, as a door that stays awake with a clipboard (the "rounds" pose); what dry run measured as joined command and output; the recommended workflow as a track | Doorman (rounds), PhaseTrack, TerminalBlock |
| 18 | Guides / Prometheus activity | Someone whose traffic isn't CPU | How a query becomes activity; while paused, the query wakes it; example queries with PromQL highlighting; "writing good queries" as do and don't pairs | StateFlow, Cards, Callout |
| 19 | Guides / GitOps tools | An Argo CD or Flux user whose pause gets undone | The conflict drawn: Git sets replicas, Napwake waits an hour; each tool's fix as tabs with the YAML; "checking the fix is in" as what you'll see | Tabs (Argo CD, Flux, Fleet, kapp, Kluctl), TerminalBlock |
| 20 | Guides / HPA and KEDA | Someone who already scales to zero with KEDA | A short page: what each does, and when KEDA alone fits better (voice rule 5) | Cards, Callout |
| 21 | Reference / API | Someone reading the CRD | Spec and status as anchored rows with a name index; WorkloadPhase values as state badges in product words | Ref, RefIndex, StateBadge |
| 22 | Reference / Helm values | Someone writing values.yaml | A name index by group; every value an anchored row with its default; a search-friendly page | Ref, RefIndex |
| 23 | Reference / Metrics | Someone building a dashboard | Tiers as sections with anchored metric rows; useful queries with PromQL highlighting and a copy button | Ref, RefIndex, code |
| 24 | Reference / Labels and annotations | done in phase 1 | | |
| 25 | Reference / Data and access | A security reviewer | What it reads, writes and sends as three panels; RBAC as anchored rows; what the plugin can see | Panel, Ref |
| 26 | Reference / Security | The same reviewer | "The doorman is a way in to paused workloads" as a needs-you callout; verifying a release as joined command and output; how to report a vulnerability | Callout, TerminalBlock |
| 27 | Reference / Configuration | Someone running the binary by hand | Flags as anchored rows with defaults; TLS and leader election as panels | Ref, RefIndex, Panel |
| 28 | Reference / Compatibility | Someone with a particular ingress or mesh | A compatibility table with a door per row: works, works with a change, doesn't (needs you), with the change in words | Outcome, Ref |
| 29 | Operations / Monitoring | An on-call engineer | Key metrics as cards with what each means; alerting rules as anchored rows; health checks | Cards, Ref |
| 30 | Operations / Troubleshooting | Someone with a problem now | A symptom index at the top; each issue as a needs-you callout ("what you see, why, the fix") with the command to check, and the note pose only at the top | Doorman (note), RefIndex, Callout, TerminalBlock |
| 31 | Operations / Upgrading | Someone moving between releases | The upgrade path as steps; per-install method tabs; rollback as a calm callout; version history as a timeline | Tabs, PhaseTrack, Callout |
| 32 | Contributing | A first-time contributor | Setup as a checklist; project structure as a file tree; commit and PR conventions as a checklist; how the docs build, rewritten for Astro (with Ara) | Starlight FileTree, Panel |
