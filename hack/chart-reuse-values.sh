#!/usr/bin/env bash
# Renders the chart as helm upgrade --reuse-values does from an older
# release: with that release's values.yaml as the defaults, rather than
# the chart's own. The first argument is the values.yaml standing in, the
# rest go to helm template.
#
#   hack/chart-reuse-values.sh hack/testdata/chart-values/0.2.0-rc.4.yaml --set timezone=Europe/London
set -euo pipefail

defaults=$1
shift

dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
cp -R charts/hybernate "$dir/"
cp "$defaults" "$dir/hybernate/values.yaml"

helm template hybernate "$dir/hybernate" --namespace hybernate-system "$@"
