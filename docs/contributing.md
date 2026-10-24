# Contributing

## Development Setup

### Prerequisites

- Go 1.26+ (`go.mod` pins the toolchain)
- Docker (`make test-alerts` runs promtool in a container, so you don't need it installed)
- kubectl
- [Kind](https://kind.sigs.k8s.io/) (for the end-to-end and Helm tests)
- Helm, only for `make check-chart` and `make test-alerts`

`make` downloads controller-gen, kustomize, setup-envtest and golangci-lint into `bin/` as it needs them.

### Clone and Build

```bash
git clone https://github.com/napwake/napwake.git
cd napwake
make build
```

### Run Tests

```bash
# Unit and integration tests (uses envtest for the K8s API)
make test

# Lint
make lint

# E2E tests: creates a Kind cluster, runs, and deletes it (KIND_NODE_IMAGE picks the Kubernetes version)
make test-e2e

# Install the Helm chart into Kind, and pause and wake a workload through the doorman
make test-helm-smoke
```

### Run Locally

```bash
# Install CRDs into your current cluster
make install

# Run the operator locally (outside the cluster)
make run
```

### Build the kubectl Plugin

```bash
make build-plugin
# Binary at bin/kubectl-napwake
```

## Project Structure

```
cmd/main.go                    # Operator entrypoint
cmd/kubectl-napwake/main.go  # kubectl plugin
api/v1alpha1/                  # CRD type definitions
internal/controller/           # Reconcilers
internal/forecast/             # Holt-Winters engine
internal/signal/               # Prometheus activity queries
internal/lifecycle/            # Pause and resume
internal/autoscaler/           # HPA and KEDA
internal/gitops/               # GitOps tool detection
internal/discovery/            # Cluster scan for kubectl napwake scan
internal/doorman/              # Wake on request
internal/cost/                 # Cost rates and node list prices
internal/metrics/              # Prometheus metrics, and the Metrics API reader
charts/napwake/              # Helm chart
config/                        # CRD, RBAC, kustomize manifests
test/e2e/                      # End-to-end tests, in Kind
test/docs/                     # Validates the docs' ManagedWorkloads against the CRD, and Helm values against the chart
tests/prometheus/              # promtool tests for the alert rules
docs/                          # This site (mkdocs)
overrides/                     # The site's theme overrides: the banner on every version but the latest
hack/docsite/                  # Publishes a release's docs as a version of the site
hack/brewformula/              # Writes a release's Homebrew formula
```

## Code Standards

Key coding standards:

- **No comments that restate what the code does.** Comments are for *why*, not *what*.
- **Wrap errors with context:** `fmt.Errorf("doing X: %w", err)`
- **Return early on errors.** Happy path flows straight down.
- **Accept interfaces, return structs.** Define interfaces at the point of consumption.
- **Test behavior, not implementation.** Table-driven tests with `testify`.

## Commit Conventions

Use [Conventional Commits](https://www.conventionalcommits.org/):

```
feat(idle): add idle detection with detector and webhook signal
fix(idle): reset idle timer on active signal
refactor(forecast): extract confidence scoring into separate type
chore(crd): regenerate CRD manifests
docs(guides): add Prometheus signals guide
test(policy): add table tests for scale constraints
```

One logical change per commit. Generated code gets its own commit.

## Pull Requests

1. Fork the repository
2. Create a feature branch from `main`
3. Make your changes with tests
4. Run `make lint test` and ensure everything passes
5. Open a PR with a clear description of what and why

### PR Checklist

- [ ] Tests pass (`make test`)
- [ ] Lint passes (`make lint`)
- [ ] CRD manifests regenerated if types changed (`make manifests`), and generated code is current (`make verify-generated`)
- [ ] DeepCopy regenerated if types changed (`make generate`)
- [ ] The chart still lints and renders, with RBAC matching `config/rbac` (`make check-chart`), if you changed it or RBAC
- [ ] Alert rules still pass their tests (`make test-alerts`), if you changed them
- [ ] Documentation updated if user-facing behavior changed

CI also runs the end-to-end tests on Kubernetes 1.30 and 1.37, the Helm smoke test, and `make vulncheck`.

## Documentation

The docs are built with mkdocs: `pip install --require-hashes -r docs/requirements.txt`, then `mkdocs serve`. `make test` creates every ManagedWorkload shown in a `yaml` block in `README.md` and `docs/`, and in `config/samples/`, against the real CRD, so a doc example that doesn't validate fails the build. A partial one, a `spec:` block or spec fields such as `dependsOn` on their own, is wrapped into a whole ManagedWorkload first; title it `managedworkload.yaml` or leave it untitled, since a block titled as another file, such as `application.yaml`, isn't read as one. Helm values blocks, titled `napwake-values.yaml`, `values.yaml` or `values-<name>.yaml`, or untitled with only the chart's keys, are checked against `charts/napwake/values.schema.json`, and every value they set must be one the chart has. Put `<!-- snippet -->` on the line before a block that's deliberately partial and shouldn't be checked. Links to `https://docs.napwake.dev` in the code must point at a page in `docs/`.

## Releasing

A release is a tag on `main`, `vX.Y.Z`. `make release VERSION=X.Y.Z` bumps the chart's version and appVersion, the kustomize image, and the install and verify commands and download links in the docs (the pages in the Makefile's `VERSIONED_DOCS`), dates the version's section of `CHANGELOG.md`, commits that, and tags it. Pushing the tag starts the release workflow:

1. Checks the chart, the kustomize image, the docs and the dated changelog name the tag's version, and that no page's install link is to `releases/latest` or another release (`make verify-version`), then runs every check a pull request does, including Trivy on the image.
2. Builds the plugin archives, the krew manifest, `install.yaml` and the chart; pushes the image by digest, untagged, and signs it and attaches its SBOMs and provenance; and drafts the GitHub release with every file, its signed checksums, and the changelog section, with a link to the release's docs, as its notes.
3. Tags the image, pushes and signs the chart, tags the image `latest`, and publishes the GitHub release, which moves `releases/latest`.
4. Writes the Homebrew formula from the published archives and pushes it to [the tap](#the-homebrew-tap): for a release, and for a release candidate until the first stable release.

Once the workflow succeeds, the docs workflow publishes the tag's docs, as [the docs site](#the-docs-site) describes.

Until step 3 nothing users install has changed, so a run that fails earlier can be run again: it replaces its draft. One that fails in step 3 can be too.

**Rehearse a release with a release candidate first.** A tag with a hyphen, such as `v0.2.0-rc.1`, runs the whole workflow as a pre-release: everything is published but `latest`, so the image, the chart and the GitHub release exist only for those who ask for that version, and its docs are published under `next`, with a banner saying it's a release candidate. Its notes are its version's changelog section, which needn't be dated yet. From a throwaway branch, so `main` keeps the release's version:

```bash
git switch -c rehearse-v0.2.0-rc.1 main
make release VERSION=0.2.0-rc.1
git push origin v0.2.0-rc.1
```

Once it succeeds, install the release candidate as the docs describe, with `--version 0.2.0-rc.1`, and check its signatures with the commands in [Security](reference/security.md#verifying-a-release). Then release from `main`:

```bash
git switch main
make release VERSION=0.2.0
git push origin main v0.2.0
```

### The Homebrew tap

`brew install napwake/tap/kubectl-napwake` installs the plugin from [napwake/homebrew-tap](https://github.com/napwake/homebrew-tap). For each release, the release workflow's last job writes the tap's `Formula/kubectl-napwake.rb` with [`hack/brewformula`](https://github.com/napwake/napwake/tree/main/hack/brewformula), from the published macOS and Linux archives, and commits it as `kubectl-napwake X.Y.Z`. A release candidate is brewed only while the tap has no stable release, so people trying it before 0.2.0 can install it with Homebrew; once a stable release is there, a release candidate never replaces it, since `brew install` and `brew upgrade` take whatever the formula is.

It pushes with the `HOMEBREW_TAP_TOKEN` secret: a fine-grained personal access token whose only permission is Contents read and write on `napwake/homebrew-tap`. Without the secret, or if the push fails, the job warns and the tap stays on the last release, while the release, and its docs, go ahead; once the token is fixed, re-run the job. Fine-grained tokens expire, so renew it before its expiry date.

To check a change to the formula, render it from a release's archives and audit and install it from a local tap:

```bash
gh release download v0.2.0 -R napwake/napwake -D /tmp/release \
  -p 'kubectl-napwake-darwin-*.tar.gz' -p 'kubectl-napwake-linux-*.tar.gz'
go run ./hack/brewformula -tag v0.2.0 -dir /tmp/release > kubectl-napwake.rb
brew tap-new napwake/local --no-git
cp kubectl-napwake.rb "$(brew --repository napwake/local)/Formula/"
brew audit --strict --online --formula napwake/local/kubectl-napwake
brew install napwake/local/kubectl-napwake
brew test napwake/local/kubectl-napwake
brew uninstall napwake/local/kubectl-napwake
brew untap napwake/local
```

### Krew

Each release carries a krew manifest, `krew-napwake.yaml`, which the release workflow writes with `hack/krew-manifest.sh` and checks with krew's `validate-krew-manifest`. Until the plugin is in [krew-index](https://github.com/kubernetes-sigs/krew-index), the docs install it from that manifest's URL.

**Once 0.2.0 is released, submit the plugin to krew-index,** a pull request to that repository:

1. Check the release's manifest installs on every platform. This needs [krew](https://krew.sigs.k8s.io/docs/user-guide/setup/install/) installed:

    ```bash
    gh release download v0.2.0 -R napwake/napwake -p krew-napwake.yaml
    go install sigs.k8s.io/krew/cmd/validate-krew-manifest@v0.5.0
    validate-krew-manifest -manifest krew-napwake.yaml
    ```

2. Fork [kubernetes-sigs/krew-index](https://github.com/kubernetes-sigs/krew-index), add the manifest as `plugins/napwake.yaml`, and open a pull request. Krew's maintainers review a new plugin by hand, against their [naming guide](https://krew.sigs.k8s.io/docs/developer-guide/develop/naming-guide/) and [best practices](https://krew.sigs.k8s.io/docs/developer-guide/develop/best-practices/).
3. Once it's merged, change the README's and the [plugin page's](getting-started/kubectl-plugin.md#installation) Krew instructions to `kubectl krew install napwake`, without a manifest URL.

**For each later release,** open a pull request to krew-index that sets `plugins/napwake.yaml`'s `version`, `uri`s and `sha256`s to those in the release's `krew-napwake.yaml`. One that changes only those is tested and merged automatically. [krew-release-bot](https://github.com/rajatjindal/krew-release-bot) can open it from the release workflow.

### The docs site

The site keeps a version of the docs for each minor release, `0.2`, `0.3` and so on, each built from its newest patch's tag, so the install commands on every page name the release that page documents. The docs workflow publishes them once the release workflow succeeds, with [mike](https://github.com/jimporter/mike), to the `gh-pages` branch, which GitHub Pages serves:

- A release's docs go to its minor version, and to `latest` when it's the newest release. The site's root sends readers to `latest`, or to `next` until there's a release.
- A release candidate's docs go to `next`, with a banner saying they're for a release candidate. The release it rehearses removes `next`.
- A link without a version, such as `https://docs.napwake.dev/guides/gitops/`, which the operator and the plugin print, goes to the same page in the version the root sends readers to, through the `404.html` at the branch's root.

[`hack/docsite`](https://github.com/napwake/napwake/tree/main/hack/docsite) decides what a tag publishes, and never replaces a version's docs with an older release's. To republish a release's docs, run the docs workflow by hand with its tag. To see what a tag would publish, run it with `-dry-run` from a checkout with the docs requirements installed and the `gh-pages` branch fetched from `origin`:

```bash
go run ./hack/docsite -tag v0.2.0 -dry-run
```

`mkdocs serve` previews the docs you're editing, without versions; `mike serve` serves the versions on your local `gh-pages`.

The site has no domain of its own: `okedeji.io` belongs to the owner's user site, `okedeji/okedeji.github.io`, and GitHub serves this repository's Pages under it, at `/napwake/`. So `gh-pages` has no `CNAME` file, and mustn't get one: it would claim `okedeji.io` for this repository.

Moving the site from the old Actions deployment to `gh-pages` takes two settings, once the docs workflow has published the first release or release candidate since versioning, which creates the branch:

1. **Settings, Environments, `github-pages`:** add `gh-pages` to the deployment branches. Only `main` is allowed today, and GitHub's own Pages workflow deploys from the branch it serves.
2. **Settings, Pages, Build and deployment:** set the source to *Deploy from a branch*, branch `gh-pages`, folder `/ (root)`.

Until then the old site stays as it is. Tags from before versioning can't be published this way: their docs have no mike configuration.

## Regenerating Generated Code

After changing CRD types in `api/v1alpha1/`:

```bash
make generate   # DeepCopy methods
make manifests  # CRD YAML, RBAC
```

Commit generated code separately: `chore(crd): regenerate CRD manifests`
