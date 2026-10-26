#!/usr/bin/env bash
# Checks that every commit after base, up to head, is signed off by its
# author, under the Developer Certificate of Origin: its message has a line
# "Signed-off-by: <author's name> <author's email>", as git commit -s
# writes. The DCO workflow runs it on each pull request. Merge commits are
# left out: GitHub's Update branch button writes one with no sign-off, and
# the commits it brings in are already on the base branch.
#
#   hack/dco/check.sh <base> <head>
set -euo pipefail

base=${1:?usage: check.sh <base> <head>}
head=${2:?usage: check.sh <base> <head>}

unsigned=()
while read -r commit; do
  author=$(git log -1 --format='%an <%ae>' "$commit")
  if ! git log -1 --format=%B "$commit" | grep -qixF -- "Signed-off-by: $author"; then
    unsigned+=("$commit")
  fi
done < <(git rev-list --no-merges "$base..$head")

if [ ${#unsigned[@]} -eq 0 ]; then
  echo "Every commit is signed off by its author."
  exit 0
fi

# Rebasing onto where the branch leaves its base rewrites only the pull
# request's own commits, whatever the contributor's remotes are called.
fork=$(git merge-base "$base" "$head")
echo "::error title=Commits not signed off::${#unsigned[@]} commit(s) have no Signed-off-by line for their author."
echo "These commits have no \"Signed-off-by:\" line naming their author:"
for commit in "${unsigned[@]}"; do
  echo "  ${commit:0:12} $(git log -1 --format='%s (%an <%ae>)' "$commit")"
done
cat <<EOF

Contributions to Napwake are made under the Developer Certificate of Origin,
https://developercertificate.org/. Sign off each commit with git commit -s.
To sign off the commits you've already made, then update the pull request:

  git rebase --signoff ${fork:0:12}
  git push --force-with-lease

${fork:0:12} is where your branch leaves the base branch, so only your own
commits are rewritten. If your origin is napwake/napwake and your branch is
up to date with it, git rebase --signoff origin/main does the same.
EOF
exit 1
