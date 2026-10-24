#!/usr/bin/env bash
# Prints, as JSON, what the chart's napwake.fill helper makes of a set of
# values and a set of defaults, both given as JSON: the values, with every
# key they lack taken from the defaults.
#
#   hack/chart-fill.sh '{"enabled": false}' '{"enabled": true, "port": 80}'
set -euo pipefail

dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
cp -R charts/napwake "$dir/"
chart="$dir/napwake"
# The test values aren't the chart's, which its schema would refuse.
rm "$chart/values.schema.json"
cat > "$chart/templates/fill.yaml" <<'TEMPLATE'
{{- $values := .Values.fillTest.values | fromJson -}}
{{- include "napwake.fill" (list $values (.Values.fillTest.defaults | fromJson)) -}}
filled: {{ toJson $values | quote }}
TEMPLATE

printf '%s' "$1" > "$dir/values.json"
printf '%s' "$2" > "$dir/defaults.json"
helm template napwake "$chart" --show-only templates/fill.yaml \
  --set-file fillTest.values="$dir/values.json" --set-file fillTest.defaults="$dir/defaults.json" |
  sed -n 's/^filled: "\(.*\)"$/\1/p' | sed 's/\\"/"/g'
