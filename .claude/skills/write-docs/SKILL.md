---
name: write-docs
description: Write or change Napwake's docs, with where pages live, the voice, links that work in every version, and the tests that hold the docs to the code. Use for any change under docs/, to the README, the chart's NOTES or values comments, or a docs link in code.
---

# Write the docs

## Where things are

- Pages are `docs/src/content/docs/<section>/<page>.mdx`, in `getting-started`, `concepts`, `guides`, `operations` and `reference`.
- `docs/src/sidebar.json` lists every page in the sidebar. Add a new page there.
- Components are in `docs/src/components/`, imported at the top of a page as its neighbours do.
- A reference page is a table of `<Ref id="...">` rows, with a `RefIndex` of them at the top.

## The voice

[docs/STYLE.md](../../../docs/STYLE.md) is the rule. Read it before writing a line. Its core:

- Every fact, half the words. Cut words, never facts: defaults, flags, limits and caveats stay.
- Short sentences, short paragraphs. The command or the YAML first, then one line on what it does.
- "You". Plain words. Say it once, and link to where it's explained.
- Never an em dash, "Note that", a rhetorical question, or a semicolon in prose. A colon only before a list, a code block or a table.
- A guide is numbered steps with a checkpoint at the end. A concept starts with one picture. A reference page is tables. An operations page starts with the symptom.

## Links and versions

- Each minor release has its own copy of the site, such as `/0.2/`, and a release candidate has `/next/`. `hack/docsite` builds each from its tag.
- Between pages, link from the root, without a version: `[GitOps tools](/guides/gitops/#argo-cd)`. The build adds the version. A relative link, such as `../gitops/`, fails the build.
- From code, the chart's values or the README, write the whole address without a version, such as `https://docs.napwake.dev/guides/gitops/#argo-cd`. It lands on the newest release's page, anchor kept. The chart's `NOTES.txt` links to its own version's docs with `{{ $docs }}`.
- The install commands name a release. The pages in the Makefile's `VERSIONED_DOCS` name the chart's current version, and `make bump` rewrites it. Never change a version by hand, or link to `releases/latest`.

## What the tests hold the docs to

| You write | Checked by |
|---|---|
| A ManagedWorkload in a `yaml` block, whole or a `spec:` part | `TestDocsManifestsAreValid` in `test/docs` creates it against the CRD |
| `napwake.dev/` annotations on a Deployment, StatefulSet or Namespace | `TestDocsAnnotationsAreReadable` in `internal/controller`: each is one Napwake reads, with a value it takes |
| Helm values, in a block titled `napwake-values.yaml`, `values.yaml` or `values-<name>.yaml` | `TestDocsHelmValuesAreValid` in `test/docs`, against `charts/napwake/values.schema.json` |
| A block that's partial on purpose | Nothing, after a `{/* snippet */}` line in `.mdx`, or `<!-- snippet -->` in `.md` |
| An install or verify command on `installation`, `kubectl-plugin` or `security` | `TestInstallSmokeRunsTheDocsCommands` in `test/docs`: `.github/workflows/install-smoke.yml` runs the same lines. Change both together |
| The forecasting page's tables: "How long it takes", "Forecast setting", and pausing early's "What it saves and costs" | `TestDocs_HowLongItTakes`, `TestDocs_WhatEachSettingTrades` and `TestDocs_PausingEarly` in `internal/forecast/sim`. Change the numbers only to what they measure |
| A link between pages | `npm run build` |
| A docs address outside `docs/` | `TestDocsAddressesLandInTheBuiltSite` in `test/docs`, against a build. `TestDocsLinksExist` in `cmd/kubectl-napwake`, for `cmd/` and `internal/` |
| An em dash, or the product's old name | `TestNoEmDashes` in `test/docs`, `TestOldNameIsGone` in `test/oldname` |

## Check

Preview, in `docs/`, at the address it prints:

```bash
cd docs && npm ci && npm run dev
```

Then, from the repository's root:

```bash
(cd docs && npm run build)
NAPWAKE_DOCS_DIST=$PWD/docs/dist go test -count=1 ./test/docs ./hack/docsite \
  -run 'TestDocsAddressesLandInTheBuiltSite|TestBuiltSiteRedirectsKeepTheAnchor'
export KUBEBUILDER_ASSETS="$(make -s setup-envtest | tail -n 1)"
go test -count=1 ./test/docs ./internal/controller ./internal/forecast/sim -run 'TestDocs|TestInstallSmoke'
```

Every docs change ships with the code it describes, in the same pull request. Never write a page that says something the code doesn't do.
