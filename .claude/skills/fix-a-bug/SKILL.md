---
name: fix-a-bug
description: Fix a bug in Napwake as the project does, with a failing test first, then the fix, a check that the test catches it, the docs, the changelog line and the commit. Use when fixing a reported bug, a wrong behaviour, or a defect found in review.
---

# Fix a bug

1. **Find where it lives.** AGENTS.md's map says which package owns the behaviour. Read the code and the docs page that describe it.

2. **Write a failing test,** in that package, of what a user or the API server would see. Use envtest if the API server's behaviour matters, the fake client otherwise. Run it and check it fails for the bug's reason, not another:

   ```bash
   export KUBEBUILDER_ASSETS="$(make -s setup-envtest | tail -n 1)"
   go test -race -count=1 ./internal/<package> -run 'TestName$' -v
   ```

   If the bug only shows on a real cluster, write an e2e spec instead (see the `e2e` skill).

3. **Fix it,** the smallest change that makes the test pass. Run the test again.

4. **Break the fix on purpose** (undo a line, flip a condition) and check the test fails. Put the fix back.

5. **Run the package's tests,** then the `verify` skill's steps.

6. **The docs.** If the docs, the README, the chart's values comments or `NOTES.txt`, or a help string describe the old behaviour, change them. See the `write-docs` skill.

7. **The changelog.** If a user could notice the bug, add a line to `CHANGELOG.md`, in the section of the version being prepared (`## [x.y.z] - Unreleased`), under `### Fixed`. Say what users saw, and what happens now.

8. **Commit,** signed off:

   ```bash
   git commit -s -m "fix(<scope>): <what works now>"
   ```

   The scope is the package or area, such as `controller`, `doorman` or `plugin`. If the fix changed API types or RBAC markers, commit what `make manifests generate` wrote on its own, as `chore(crd): regenerate CRD manifests`.

9. **The pull request.** It's squash-merged, so its title is the commit on `main`: write it like the commit. Say what was wrong, why, and how the test proves the fix.
