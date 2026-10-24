#!/usr/bin/env bash
# Prints the chart's NOTES.txt as helm install would, for a chart version
# and values, which helm template can't: it leaves NOTES out. A copy of the
# chart renders it as the value of an ordinary template. The first argument
# is the chart's version; the rest go to helm template.
#
#   hack/chart-notes.sh 0.3.0-rc.1 --set timezone=Europe/London
set -euo pipefail

version=$1
shift

dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
cp -R charts/napwake "$dir/"
chart="$dir/napwake"
mv "$chart/templates/NOTES.txt" "$chart/notes.txt"
VERSION=$version perl -pi -e 's/^version:.*/version: $ENV{VERSION}/' "$chart/Chart.yaml"
cat > "$chart/templates/notes.yaml" <<'EOF'
notes: |
{{ tpl (.Files.Get "notes.txt") . | indent 2 }}
EOF

helm template napwake "$chart" --namespace napwake-system --show-only templates/notes.yaml "$@" |
  sed -n '/^notes: |$/,$p' | tail -n +2 | sed 's/^  //'
