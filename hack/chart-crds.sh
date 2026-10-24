#!/usr/bin/env bash
# Writes the Helm chart's CRD templates from controller-gen's CRDs.
#
# The chart ships its CRDs as templates rather than in crds/, because Helm
# installs crds/ once and never upgrades it: an upgrade would leave the old
# schema in place and the API server would prune every new field. As
# templates they're upgraded with the release, guarded by crds.install, and
# kept on uninstall, since deleting a CRD deletes every ManagedWorkload.
# Argo CD renders the chart with helm template and prunes what it rendered,
# ignoring Helm's resource policy, so the CRD tells it not to delete it too.
set -euo pipefail

src=${1:-config/crd/bases}
dst=${2:-charts/napwake/templates/crds}

mkdir -p "$dst"
rm -f "$dst"/*.yaml

for crd in "$src"/*.yaml; do
  if grep -q '{{' "$crd"; then
    echo "$crd contains {{, which Helm would read as a template" >&2
    exit 1
  fi
  if ! grep -q '^  annotations:$' "$crd"; then
    echo "$crd has no metadata.annotations to add Helm's to" >&2
    exit 1
  fi
  {
    echo '{{- include "napwake.defaults" . -}}'
    echo '{{- if .Values.crds.install }}'
    awk '
      /^---$/ && NR == 1 { next }
      { print }
      /^metadata:$/ && !labelled {
        print "  labels:"
        print "    {{- include \"napwake.labels\" . | nindent 4 }}"
        labelled = 1
      }
      /^  annotations:$/ && !annotated {
        print "    helm.sh/resource-policy: keep"
        print "    argocd.argoproj.io/sync-options: Delete=false"
        annotated = 1
      }
    ' "$crd"
    echo '{{- end }}'
  } >"$dst/$(basename "$crd")"
done
