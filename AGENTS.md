# Working on Napwake

How Napwake is built, tested and changed. For contributors and the AI assistants they work with. [CONTRIBUTING.md](CONTRIBUTING.md) and [the contributing page](docs/src/content/docs/contributing.mdx) have the setup and the release process.

## What Napwake is

A Kubernetes operator that pauses idle workloads in non-production clusters, scaling them to zero, and wakes them on the next request, an activity annotation, or ahead of the demand its forecast expects. It ships as one Go binary, a `kubectl napwake` plugin, a Helm chart and a docs site.

## The repository

| Path | What it owns |
|---|---|
| `cmd/main.go` | The one binary, `/manager`: the operator by default; with `--doorman`, `--guard` or `--forwarder`, the doorman, the chart's uninstall and upgrade guard, or a NEG forwarder |
| `cmd/kubectl-napwake/` | The plugin (Cobra): `scan`, `status`, `forecast`, `nodes`, `pause`, `wake`, `deps`, `enable`, `version`, and the HTML pages `scan` and `status` write |
| `api/v1alpha1/` | The ManagedWorkload CRD's types and markers, and every label, annotation and finalizer Napwake reads or writes (`labels.go`) |
| `internal/controller/` | The reconcilers: label opt-in (annotations to a ManagedWorkload's spec), the ManagedWorkload's lifecycle (activity clock, pause and wake, dependencies, cost, forecast), doorman routing through EndpointSlices, and NEG forwarders |
| `internal/lifecycle/` | Pausing and resuming a Deployment or StatefulSet through its scale subresource, with a pause record so every step can be repeated and undone |
| `internal/doorman/` | Holds connections to a paused workload, wakes it, passes them through once it's Ready, and serves the asleep and waking-up pages |
| `internal/forwarder/` | Passes connections, bytes untouched, from a paused workload's namespace to the doorman, for GKE's NEGs |
| `internal/forecast/` | The per-workload Holt-Winters model (daily and weekly seasonality), its confidence and phases, and the rules that act on it |
| `internal/forecast/sim/` | Runs the forecast and the operator's rules through simulated time. The forecasting page's tables come from it |
| `internal/signal/` | Prometheus activity queries |
| `internal/metrics/` | Napwake's Prometheus metrics, and the reader for the Metrics API and pods |
| `internal/cost/` | Cost rates, the month's totals, and node list prices (`prices.csv.gz`, written by `make prices`) |
| `internal/capacity/` | What pausing took off the bill, from the node capacity paused workloads would need back |
| `internal/nodescale/` | Finds the node autoscaler, and what keeps each node from being scaled down |
| `internal/autoscaler/` | Finds a workload's HPA or KEDA ScaledObject, and pauses and resumes a KEDA one through KEDA |
| `internal/gitops/` | Tells who sets a workload's replicas, from its field managers, and whether a GitOps tool leaves them to Napwake |
| `internal/discovery/` | The scan behind `kubectl napwake scan`: workloads, their use, history from Prometheus, dependencies and prices |
| `internal/guard/` | Refuses a Helm uninstall or upgrade that would leave ManagedWorkloads with no operator |
| `internal/human/` | Writes values the way people say them, for events and condition messages |
| `charts/napwake/` | The Helm chart: `values.yaml`, `values.schema.json`, the templates, and `files/defaults.yaml` |
| `config/` | The kustomize install (`config/default`, which `install.yaml` is built from), generated CRD and RBAC, and alert rules in `config/prometheus/` |
| `hack/` | Tools for the checks and the release: `chartflags`, `rbaccheck`, `promrules`, `doormanpolicy`, `docsite`, `brewformula`, `prices`, `forecastsim`, `dco`, and the `chart-*.sh` and `helm-smoke.sh` scripts |
| `test/docs/` | Drift tests: the docs' manifests and values against the CRD and the chart's schema, their addresses against the built site, their install commands against the install smoke test |
| `test/oldname/` | Keeps the product's old name out of the tree, except where it's history |
| `test/e2e/`, `test/utils/` | The end-to-end suite on kind (Ginkgo, build tag `e2e`), and its helpers |
| `tests/prometheus/` | promtool tests for the alert rules |
| `docs/` | The docs site, Astro Starlight |
| `.github/workflows/` | CI, below, and the release |

## Build, test and check

You need Go (the toolchain `go.mod` names), Docker, Helm, kind, kubectl, and Node (the version in `docs/.nvmrc`) for the docs. `make` downloads controller-gen, kustomize, setup-envtest and golangci-lint into `bin/` as it needs them.

Tests that need a real API server use envtest. `make setup-envtest` downloads its etcd and kube-apiserver, for the Kubernetes version `go.mod`'s `k8s.io/api` matches, into `bin/k8s/`, and prints their folder. `make test` sets `KUBEBUILDER_ASSETS` itself. For `go test` on its own, set it, as an absolute path:

```bash
export KUBEBUILDER_ASSETS="$(make -s setup-envtest | tail -n 1)"
go test -race -count=1 ./internal/controller -run 'TestOptIn'
```

The files in `bin/k8s/` are read-only. Run `chmod -R u+w bin/k8s` before deleting them, or a worktree that holds them.

What CI runs on every pull request:

| Workflow, job | Command |
|---|---|
| Tests, Test | `make test` |
| Tests, Generated files and go.mod | `make verify-generated` |
| Tests, Helm chart | `make check-chart` and `make test-doorman-policy`, with Helm 3 |
| Tests, Alert rules | `make test-alerts` |
| Lint | `make lint-config`, then `make lint` |
| docs, build | The docs build and the address check, below |
| E2E | `make test-e2e KIND_NODE_IMAGE=<kindest/node image>`, on Kubernetes 1.30 and 1.37 |
| Helm Smoke Test | `make test-helm-smoke`, with Helm 4 |
| Security | `make vulncheck`, and Trivy on the image the `Dockerfile` builds |
| DCO, Sign-off | `hack/dco/check.sh`, on the pull request's commits |

The docs job builds the site, then checks every docs address outside `docs/` against it, from the repository's root:

```bash
(cd docs && npm ci && npm run build)
NAPWAKE_DOCS_DIST=$PWD/docs/dist go test -count=1 ./test/docs ./hack/docsite \
  -run 'TestDocsAddressesLandInTheBuiltSite|TestBuiltSiteRedirectsKeepTheAnchor'
```

`make test` runs every package but `test/e2e`, with `-race`, after `make manifests generate fmt vet`. The `verify` skill in `.claude/skills/` runs these fastest first.

## How a change is made

1. **A failing test first.** Write the test that shows the bug, or the behaviour that's missing, and watch it fail for that reason.
2. **Make the change,** and watch the test pass.
3. **Break the change on purpose** (undo a line, flip a condition) and check the test fails. Then put it back. A test that passes either way proves nothing.
4. **Docs for every behaviour you change,** in the same pull request: the pages in `docs/`, the README, the chart's `values.yaml` comments and `NOTES.txt`, and the plugin's and the flags' help. Never document what the code doesn't do.
5. **Generated code in its own commit.** After `make manifests generate`, commit what they wrote on its own, as `chore(crd): regenerate CRD manifests`.
6. **Run what CI runs** (above) before you push.

## Code standards

### Comments
- Comments say why, never what. Name things so the code needs none.
- A comment earns its place for a decision that would surprise a reader, a workaround (with the issue's link), or an algorithm that isn't obvious.
- Doc comments on exported names start with the name and say what the signature can't. In `api/v1alpha1`, a type's doc comment must start with its name.
- No banners, no commented-out code, no TODOs. Do it now, or open an issue.
- Every Go file starts with the licence header in `hack/boilerplate.go.txt`.

### Errors
- Wrap errors with context, `fmt.Errorf("reading the pause record: %w", err)`, so the message traces the path.
- Return early. The happy path runs straight down.
- Never drop an error silently. If you ignore one on purpose, say why on that line: `_ = conn.Close() // best effort, nothing to do if it fails`.
- A sentinel error (`var ErrNoPodMetrics = errors.New(...)`) for callers to check with `errors.Is`; an error type for those that need its data, with `errors.As`.
- Log an error where it's handled, not where it's returned.
- No `panic`, except for a programmer's error that can't happen at run time, such as an embedded template that doesn't parse.

### Naming
- Go's conventions. Short names in small scopes, longer ones in larger scopes.
- Receivers are one or two letters, the same on every method of a type.
- Acronyms keep one case: `ID`, `URL`, `HTTP`, `userID`.
- Packages are short, lowercase and singular. No `util`, `helpers` or `common` packages: a function lives in the package that uses it.
- Don't stutter: `doorman.Server`, not `doorman.DoormanServer`.
- Export only what another package uses.

### Packages and interfaces
- Each package under `internal/` has one job you can say in a sentence.
- Accept interfaces, return structs.
- Define an interface where it's used, not where it's implemented, as `internal/controller` does for its `forecaster` and `lifecyclePauser`. Keep it small, and add it only once there are two implementations or a test needs a fake.

### Concurrency
- Every goroutine has an owner and a way to stop: a `context.Context` it watches, and a `sync.WaitGroup` or `errgroup.Group` its owner waits on.
- Never start a goroutine in a constructor. A `Start` or `Run` method that takes a context does.
- Channels to pass work on, mutexes to guard state, whichever is simpler.
- Never wait by sleeping.

### Tests
- Test behaviour, not implementation: what a user or the API server would see.
- Table-driven tests with `t.Run` where there are several cases.
- testify: `require` for what must hold to go on, `assert` for the checks. The e2e suite, and the Ginkgo suite in `internal/controller`, use Ginkgo and Gomega.
- envtest where the API server's behaviour matters (CRD validation, admission policies, server-side apply, field managers), the fake client elsewhere.
- A helper that can fail takes `t`, calls `t.Helper()`, and fails the test. It never swallows an error.
- **Time is read as production reads it.** A test clock carries a monotonic reading, as `time.Now()` does: build it with `monotonic` in `internal/controller/helpers_test.go`, never with `time.Date` alone. A test that needs the real clock pins the minute it runs in. Prove "doesn't wait" or "doesn't hang" by a signal, an order or a count, never by elapsed time. Where a bound can't be avoided, make it generous and name it.
- A safety property, such as when a workload may pause, gets a property test across its whole range, then the cases it assumes away.
- `kubectl napwake scan` and `status` open their page in a browser from a terminal. Pass `--open=false` in tests and scripts.

### Kubernetes
- Reconciles are idempotent: the same state twice gives the same result. A pause writes what it will undo to status before it changes anything, so every step can be repeated after an interruption.
- Status conditions (`metav1.Condition`) say why a workload isn't paused or woken. Events report each change a user would want to see in `kubectl describe`. Both are listed in `docs/src/content/docs/reference/conditions-and-events.mdx`: add a row for each new one.
- Finalizers for cleanup before a delete. Owner references on what the operator creates, such as the doorman's EndpointSlices.
- Write with Napwake's field manager, `v1alpha1.FieldManager`.
- `RequeueAfter` for an expected wait. Return the error for a transient failure, so controller-runtime backs off.
- Log with logr from the context (`log.FromContext(ctx)`), in key-value pairs, which the `logcheck` linter checks. Name the workload and namespace in a line about one.
- Every call that leaves the process (the API server, the Metrics API, Prometheus, HTTP) has a context with a deadline.
- RBAC comes from the `+kubebuilder:rbac` markers in `internal/controller`. The chart keeps its own copy in its templates. Change both, and `make check-chart` compares them.

### Dependencies
- The standard library first. controller-runtime and client-go are used directly, not wrapped.
- A new module needs a reason in the pull request: it's maintained, its licence works with Apache 2.0, and what it pulls in is worth it.
- `go.mod` and `go.sum` stay tidy (`go mod tidy`).
- The operator and the plugin are static Go binaries, with no other runtime. The docs site's Node lives in `docs/` only.
- Pin what you add: tools by version in the `Makefile`, Actions by commit SHA, the `Dockerfile`'s images by digest.

### Performance
- Clear code first. Measure before optimizing, and keep allocations down only where it shows, such as the reconcile loop and the doorman's request path.

## What the repository enforces

| Rule | Checked by |
|---|---|
| Tests read time as production does | The `monotonic` helper in `internal/controller/helpers_test.go`, and `TestHourStart_CarriesNoMonotonicReading` in `internal/forecast`. Reviews check new tests |
| The docs' install and verify commands are the ones the install smoke test runs | `TestInstallSmokeRunsTheDocsCommands` in `test/docs`, against `.github/workflows/install-smoke.yml`. Change both together |
| Versions change only through `make bump VERSION=x.y.z` | `make verify-version`, which the release workflow runs on the tag. `make release` stops if `bump` changed a file it doesn't commit |
| `charts/napwake/files/defaults.yaml` is a copy of `values.yaml`, and the chart fills in values an older release's `--reuse-values` lacks | `make check-chart`, which also checks every template starts with `{{- include "napwake.defaults" . -}}`. `make manifests` writes the copy |
| Every docs link answers | `npm run build` in `docs/` (links and anchors between pages), `TestDocsAddressesLandInTheBuiltSite` in `test/docs` (every docs address outside `docs/`, after a build), and `TestDocsLinksExist` in `cmd/kubectl-napwake` |
| No em dash anywhere, in code, comments, strings, docs or commit messages: write a full stop or a comma | `TestNoEmDashes` in `test/docs` |
| Generated files are current, and `go.mod` is tidy | `make verify-generated` |
| Every commit in a pull request is signed off by its author | The DCO workflow, `hack/dco/check.sh` |
| The docs' ManagedWorkloads, annotations and Helm values are valid | `TestDocsManifestsAreValid` and `TestDocsHelmValuesAreValid` in `test/docs`, `TestDocsAnnotationsAreReadable` in `internal/controller` |
| The chart grants what `config/rbac/role.yaml` does, and passes flags the operator takes | `make check-chart` (`hack/rbaccheck`, `hack/chartflags`) |
| The forecasting page's tables are what the simulator measures | `TestDocs_HowLongItTakes`, `TestDocs_WhatEachSettingTrades` and `TestDocs_PausingEarly` in `internal/forecast/sim` |
| The product's old name appears only in history | `TestOldNameIsGone` in `test/oldname` |

## Docs

- Astro Starlight, in `docs/`. Pages are `docs/src/content/docs/<section>/<page>.mdx`, and `docs/src/sidebar.json` lists them.
- The voice is in [docs/STYLE.md](docs/STYLE.md). Read it before writing a line.
- Versioned per release: each minor release gets its own copy of the site, and a release candidate gets `next`, built from its tag by `hack/docsite`. So links between pages are written from the root without a version, `/guides/gitops/#argo-cd`, and the build adds it.
- Preview with `npm ci` then `npm run dev` in `docs/`. `npm run build` must pass.
- The `write-docs` skill has the details.

## Commits and pull requests

- [Conventional Commits](https://www.conventionalcommits.org/): `type(scope): summary`, in lower case. Types: `feat`, `fix`, `docs`, `test`, `refactor`, `chore`. The scope is the package or area, such as `controller`, `doorman`, `plugin`, `api`, `crd`, `chart`, `forecast`, `e2e` or `docs`. For example, `fix(doorman): count requests turned away while a workload is still pausing`.
- One logical change per commit.
- Anything a user notices gets a line in `CHANGELOG.md`, in the section of the version being prepared (`## [x.y.z] - Unreleased`), under Added, Changed, Fixed or Security. Keep each line self-contained: they're where branches meet.
- Pull requests are squash-merged, so the title becomes the commit on `main`. Write it as a Conventional Commit, and say in the description what changed and why.
- Contributions are made under the [Developer Certificate of Origin](https://developercertificate.org/). Sign off each commit with `git commit -s`. The DCO check fails a pull request with a commit that has no `Signed-off-by:` line for its author, and says how to sign it off.

## Reviewing a change

- Look for real defects, not style. An em dash counts as one.
- Give each finding its file and line, a severity (blocker, major or minor), what goes wrong (the input or state, then the wrong outcome), why you're sure, and a one-line fix.
- Prove it before you report it: trace the path end to end and, where it's cheap, write a throwaway test. Mark what you couldn't prove as plausible.
- Try to break the safety property the change relies on, with the cases its own tests assume away.
- Check the docs against the code after the docs change, not only before.
- Review each round of fixes too. A fix is new code.
- Five proven findings beat thirty guesses. End with a line on what you did and didn't review.
- Don't push to the branch you review. To run something, check its commit out in a worktree of your own.

## Never

- Edit generated files by hand: `config/crd/bases/`, `charts/napwake/templates/crds/`, `config/rbac/role.yaml`, `**/zz_generated.*.go`, `charts/napwake/files/defaults.yaml`, `internal/cost/prices.csv.gz` and `PROJECT`. Run the target that writes them.
- Remove a `// +kubebuilder:scaffold:*` marker.
- Change a version by hand. `make bump` does it.
- Add a dependency without saying why.
- Put a secret anywhere: code, tests, docs, fixtures, logs or commit messages. Use a placeholder.
- Run a test against a cluster you didn't create for it, or change kubectl's current context. Each kind cluster gets a kubeconfig of its own.
