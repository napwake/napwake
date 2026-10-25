---
name: add-a-setting
description: Add a setting to Napwake end to end, as a ManagedWorkload spec field, an opt-in annotation, an operator flag or a Helm value, with the generated code, the chart's schema and defaults, the reference pages and the tests. Use when adding or changing any setting a user can set.
---

# Add a setting

A setting reaches a workload through up to four layers. An opt-in setting goes through all four, as `pauseEarly` does:

| Layer | Where | `pauseEarly`'s |
|---|---|---|
| Spec field | `api/v1alpha1/managedworkload_types.go` | `spec.idlePolicy.pauseEarly` |
| Annotation, for workloads opted in with the label | `api/v1alpha1/labels.go`, read in `internal/controller/optin_settings.go` | `napwake.dev/pause-early` |
| Operator flag, the cluster's default | `cmd/main.go`, into `controller.OptInDefaults` | `--default-pause-early` |
| Helm value, which sets the flag | `charts/napwake/values.yaml` | `defaults.pauseEarly` |

`git grep -n -i 'pause-early\|pauseEarly'` lists every file one setting touches. Run the same for yours when you're done, and compare.

Start with a failing test of the behaviour, as AGENTS.md says.

## 1. The spec field

1. Add the field to its type in `managedworkload_types.go`:
   - a doc comment that starts with the Go name and says what it does, and its default;
   - `// +optional`, and validation markers, such as `// +kubebuilder:validation:Minimum=50`, an `Enum`, or an `XValidation` rule with a `message` a user can act on;
   - `// +kubebuilder:default=...` if it has a default;
   - `json:"name,omitempty"`.
2. Regenerate, and commit what it writes on its own, as `chore(crd): regenerate CRD manifests`:

   ```bash
   make manifests generate
   ```

   That writes the CRD in `config/crd/bases/` and the chart's `templates/crds/`, and the deepcopy code. Never edit those by hand.
3. Read the field where the behaviour lives, in `internal/controller` or the package that owns it.
4. Test what the CRD accepts and refuses against envtest, as `internal/controller/crd_envtest_test.go` does, and the behaviour where it lives.

## 2. The annotation

1. Add `Annotation<Name> = "napwake.dev/<name>"` to the block of settings in `labels.go`. `TestDocsAnnotationsAreReadable` learns the annotations Napwake reads from these constants.
2. Read it in `optInSpec`, in `optin_settings.go`, with the settings readers (`s.boolean`, `s.duration`, `s.durationUpTo`, `s.forecastMode`). The workload's annotation wins over its namespace's, which wins over the cluster's default. An invalid value falls back, with an `InvalidSetting` warning event on the workload.
3. A spec field with no annotation goes in `keepUserSettings` instead, so an edit to a label-created ManagedWorkload isn't undone.
4. Test in `optin_settings_test.go`: the workload's value, the namespace's, the default, and an invalid value.

## 3. The operator flag

1. Add a field to `OptInDefaults` and its value to `DefaultOptInDefaults`, in `optin_settings.go`.
2. Add the flag in `cmd/main.go`, beside the other `--default-*` flags, writing into `optIn`. Its help says what it sets, as the others' do.
3. Hold it to the annotation's rules in `validateOptInDefaults`, so the operator refuses to start with a default a workload couldn't have. Test that in `cmd/main_test.go`.
4. The kustomize install runs with the flag's default. Set it in `config/manager/manager.yaml` only if that install needs another value.

## 4. The Helm value

1. Add it to `charts/napwake/values.yaml`, under `defaults:` for an opt-in default, with a comment above it saying what it does. People read these comments.
2. Add it to `charts/napwake/values.schema.json`, with the type and limits the operator holds it to. The schema refuses keys it doesn't list.
3. Pass it to the operator in `templates/deployment.yaml`, quoted, as its neighbours are:

   ```yaml
   - {{ printf "--default-pause-early=%v" .Values.defaults.pauseEarly | quote }}
   ```

4. Run `make manifests`. It copies `values.yaml` to `files/defaults.yaml`, which fills the value in for a `helm upgrade --reuse-values` from an older release.
5. If the operator refuses some values, add what the chart must refuse and accept to `check-chart` in the `Makefile`, as `defaults.idleAfter` and `defaults.cpuThreshold` have.
6. Run `make check-chart`. Through `hack/chartflags`, it also checks the operator takes every flag the chart renders, with today's values and with older releases'.

## 5. The docs

Each reference page is a table of `<Ref id="...">` rows. Add a row, with an id like its neighbours', and an entry in the page's `RefIndex` where they have one.

| Layer | Page, in `docs/src/content/docs/` |
|---|---|
| Spec field | `reference/api.mdx` |
| Annotation | `reference/labels-annotations.mdx`, and the settings table in `guides/opt-in.mdx` |
| Operator flag | `reference/configuration.mdx` |
| Helm value | `reference/helm-values.mdx`, and the lists of settings with a Helm default in `guides/opt-in.mdx` and `reference/labels-annotations.mdx` |

Then the concept or guide page for what the setting does. See the `write-docs` skill. Add a line to `CHANGELOG.md`, under `### Added` in the section of the version being prepared.

## 6. Check

```bash
make manifests generate
export KUBEBUILDER_ASSETS="$(make -s setup-envtest | tail -n 1)"
go test -race -count=1 ./api/... ./cmd/... ./internal/controller/...
make check-chart
```

Then the `verify` skill's steps, and the commits: the generated files in a `chore(crd)` commit, the rest as `feat(<scope>): ...`.
