---
name: verify
description: Run the checks Napwake's CI runs on a pull request, fastest first, and read what a failure says. Use before pushing a change, or when a CI check fails.
---

# Verify a change

Run from the repository's root. Stop at the first failure, fix it, then run that step again. AGENTS.md says which CI job runs what.

Once per shell, point envtest at its binaries, as an absolute path:

```bash
export KUBEBUILDER_ASSETS="$(make -s setup-envtest | tail -n 1)"
```

| Step | Command | Takes | Needs |
|---|---|---|---|
| 1 | `make fmt vet` | seconds | |
| 2 | `go test -race -count=1 ./internal/<package>/...`, for each package you changed | under a minute | envtest, for some |
| 3 | `make verify-generated` | seconds | your changes committed |
| 4 | `make check-chart` | under a minute | Helm 3 |
| 5 | `make test-doorman-policy` | under a minute | envtest |
| 6 | `make test-alerts` | seconds, once the image is pulled | Docker, Helm |
| 7 | The docs build and address check, below | about a minute | Node |
| 8 | `make lint` | about a minute | |
| 9 | `make vulncheck` | under a minute | the network |
| 10 | `make test` | up to 20 minutes | |
| 11 | `make test-helm-smoke`, then `make test-e2e` | tens of minutes each | Docker, kind, kubectl. See the `e2e` skill |

The docs, step 7:

```bash
(cd docs && npm ci && npm run build)
NAPWAKE_DOCS_DIST=$PWD/docs/dist go test -count=1 ./test/docs ./hack/docsite \
  -run 'TestDocsAddressesLandInTheBuiltSite|TestBuiltSiteRedirectsKeepTheAnchor'
```

`make test` runs `make manifests generate fmt vet` first. A diff it leaves is generated code: commit it on its own, as `chore(crd): regenerate CRD manifests`.

## Reading a failure

- **A Go test.** Find the first `--- FAIL:` and the `Error Trace:` file and line under it. Run that test alone: `go test -race -count=1 ./internal/controller -run 'TestName$' -v`. A test that starts an operator against envtest prints its log and events only when it fails: they say how it got there.
- **envtest doesn't start,** with `unable to start control plane itself` and `etcd: no such file or directory`: `KUBEBUILDER_ASSETS` is unset or relative. Set it as above.
- **A test that fails now and then** is a bug, in the test or the code. One that depends on load or the time of day breaks the time rules in AGENTS.md.
- **`make verify-generated`.** "Generated files are out of date": run `make manifests generate` and commit the result. "Generated files are untracked": commit them. A diff from `go mod tidy -diff`: run `go mod tidy`. "doesn't use golang:...": the `Dockerfile`, `.devcontainer/devcontainer.json` and `go.mod`'s toolchain move together.
- **`make check-chart`.** Each check prints one sentence saying what's wrong. Find it in the Makefile, `grep -n 'a few of its words' Makefile`, to see what it renders. "isn't a copy of values.yaml": run `make manifests`. A difference from `hack/rbaccheck`: the chart's RBAC templates and the `+kubebuilder:rbac` markers disagree.
- **`make lint`.** Each issue is `file:line:col: message (linter)`. `make lint-fix` fixes formatting and imports.
- **The docs build.** The links validator lists each broken link or anchor under the page that has it.
- **`TestDocsAddressesLandInTheBuiltSite`.** It names the file, the address, and whether the page or the anchor is missing.
- **`TestInstallSmokeRunsTheDocsCommands`.** A docs install command and `.github/workflows/install-smoke.yml` differ. Change both together.
- **`make test-alerts`.** promtool names the test file, the alert, and what it expected.
- **The e2e or Helm smoke tests.** See the `e2e` skill.
