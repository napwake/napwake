#!/usr/bin/env bash
# Writes the krew manifest for a release to stdout, from the plugin archives
# in the current directory, which the release workflow builds.
#
#   hack/krew-manifest.sh v0.2.0 napwake/napwake > napwake.yaml
set -euo pipefail

tag=$1
repo=$2
platforms=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64)

cat <<EOF
apiVersion: krew.googlecontainertools.github.com/v1alpha2
kind: Plugin
metadata:
  name: napwake
spec:
  version: ${tag}
  homepage: https://github.com/${repo}
  shortDescription: Find idle workloads, and pause and wake them
  description: |
    Napwake pauses idle Kubernetes workloads in staging, preview and dev
    clusters, and wakes them when they're used. It wakes a workload on a
    request, and holds the request until the workload is ready. It also
    wakes on activity, and ahead of the hours its forecast expects to be
    busy. This plugin is Napwake's command line. With nothing installed,
    it scans any cluster for idle Deployments and StatefulSets and what
    they cost. With the operator installed, it also shows and controls
    what Napwake pauses and wakes.
  caveats: |
    scan, nodes and forecast work on any cluster. status, enable, pause,
    wake and deps need the Napwake operator installed in it:
    https://docs.napwake.dev/getting-started/installation/
  platforms:
EOF

for platform in "${platforms[@]}"; do
  os=${platform%/*}
  arch=${platform#*/}
  ext=""
  if [ "$os" = windows ]; then
    ext=.exe
  fi
  archive=kubectl-napwake-${os}-${arch}.tar.gz
  sha=$(sha256sum "$archive" | cut -d' ' -f1)
  cat <<EOF
    - selector:
        matchLabels:
          os: ${os}
          arch: ${arch}
      uri: https://github.com/${repo}/releases/download/${tag}/${archive}
      sha256: "${sha}"
      files:
        - from: kubectl-napwake-${os}-${arch}${ext}
          to: kubectl-napwake${ext}
        - from: LICENSE
          to: .
        - from: NOTICE
          to: .
      bin: kubectl-napwake${ext}
EOF
done
