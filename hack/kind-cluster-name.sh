#!/usr/bin/env bash
# Prints the name of a checkout's own kind cluster for a test, such as
# napwake-e2e-fix-p30-p28-3f9a1c: the test, the checkout's directory, made
# safe for kind, and a hash of its path. Each worktree or clone gets its own
# clusters, and two whose directories share a name don't share one. The
# checkout is the one this script is in, or the directory given.
#
# Usage: hack/kind-cluster-name.sh <test> [<checkout>]
set -euo pipefail

test=${1:?usage: kind-cluster-name.sh <test> [<checkout>]}
checkout=$(cd "${2:-$(dirname "$0")/..}" && pwd -P)

# kind takes lowercase letters, digits, dots and dashes, and warns past 50
# characters; the directory's part is cut short to stay under it.
dir=$(basename "$checkout" | tr '[:upper:]' '[:lower:]' | tr -cs 'a-z0-9' '-' | cut -c1-20 | sed 's/^-*//; s/-*$//')
hash=$(printf '%s' "$checkout" | cksum | awk '{printf "%06x", $1 % 16777216}')
echo "napwake-$test-${dir:+$dir-}$hash"
