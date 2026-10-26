/*
Copyright 2026 The Napwake Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package dco tests check.sh, which the DCO workflow runs on every pull
// request, against commits made in a repository of the test's own.
package dco

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	ada   = "Ada Lovelace <ada@example.com>"
	grace = "Grace Hopper <grace@example.com>"
)

// repo is a git repository in a temporary directory, with git's global and
// system settings out of reach, so the machine's hooks and signing don't
// apply.
type repo struct {
	t   *testing.T
	dir string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	r := &repo{t: t, dir: t.TempDir()}
	r.git("init", "--quiet", "--initial-branch=main")
	r.commit(ada, "chore: the first commit\n\nSigned-off-by: "+ada)
	return r
}

func (r *repo) env() []string {
	return append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "HOME="+r.dir)
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = r.env()
	out, err := cmd.CombinedOutput()
	require.NoError(r.t, err, "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

// commit makes an empty commit by author, with message, and returns its
// hash.
func (r *repo) commit(author, message string) string {
	r.t.Helper()
	name, email, _ := strings.Cut(strings.TrimSuffix(author, ">"), " <")
	r.git("-c", "user.name="+name, "-c", "user.email="+email,
		"commit", "--quiet", "--allow-empty", "--no-verify", "-m", message)
	return r.git("rev-parse", "HEAD")
}

// check runs check.sh on the commits after base, up to head.
func (r *repo) check(base, head string) (string, error) {
	r.t.Helper()
	script, err := filepath.Abs("check.sh")
	require.NoError(r.t, err)
	cmd := exec.Command(script, base, head)
	cmd.Dir = r.dir
	cmd.Env = r.env()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name     string
		commits  []struct{ author, message string }
		unsigned []int // the commits, by index, check.sh names
	}{
		{
			name: "every commit signed off by its author",
			commits: []struct{ author, message string }{
				{ada, "fix(doorman): one\n\nSigned-off-by: " + ada},
				{grace, "fix(doorman): two\n\nCo-Authored-By: " + ada + "\nSigned-off-by: " + grace},
			},
		},
		{
			name: "a sign-off in another case, as some tools write it",
			commits: []struct{ author, message string }{
				{ada, "fix(doorman): one\n\nsigned-off-by: Ada Lovelace <ADA@example.com>"},
			},
		},
		{
			name: "one commit not signed off",
			commits: []struct{ author, message string }{
				{ada, "fix(doorman): one\n\nSigned-off-by: " + ada},
				{ada, "fix(doorman): two"},
			},
			unsigned: []int{1},
		},
		{
			name: "signed off by someone else, not its author",
			commits: []struct{ author, message string }{
				{grace, "fix(doorman): one\n\nSigned-off-by: " + ada},
			},
			unsigned: []int{0},
		},
		{
			name: "a sign-off that isn't a line of its own",
			commits: []struct{ author, message string }{
				{ada, "fix(doorman): one, Signed-off-by: " + ada},
			},
			unsigned: []int{0},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRepo(t)
			base := r.git("rev-parse", "HEAD")
			hashes := make([]string, 0, len(tt.commits))
			for _, c := range tt.commits {
				hashes = append(hashes, r.commit(c.author, c.message))
			}

			out, err := r.check(base, hashes[len(hashes)-1])

			if len(tt.unsigned) == 0 {
				assert.NoError(t, err, out)
				return
			}
			assert.Error(t, err, "check.sh passed commits that aren't signed off:\n%s", out)
			for i, h := range hashes {
				named := strings.Contains(out, h[:12])
				assert.Equal(t, slices.Contains(tt.unsigned, i), named, "commit %d, %s, named in:\n%s", i, h[:12], out)
			}
			assert.Contains(t, out, "git rebase --signoff "+base[:12], "says how to sign them off")
			assert.Contains(t, out, "git push --force-with-lease")
		})
	}
}

// A merge commit, such as the one GitHub's Update branch button writes,
// isn't signed off, and brings in commits already on the base branch.
func TestCheckSkipsMerges(t *testing.T) {
	r := newRepo(t)
	base := r.git("rev-parse", "HEAD")
	r.git("switch", "--quiet", "-c", "feature")
	r.commit(ada, "fix(doorman): one\n\nSigned-off-by: "+ada)
	r.git("switch", "--quiet", "main")
	r.commit(grace, "fix(plugin): on main\n\nSigned-off-by: "+grace)
	r.git("switch", "--quiet", "feature")
	r.git("-c", "user.name=GitHub", "-c", "user.email=noreply@github.com",
		"merge", "--quiet", "--no-ff", "--no-verify", "-m", "Merge branch 'main' into feature", "main")

	out, err := r.check(base, r.git("rev-parse", "HEAD"))
	assert.NoError(t, err, out)
}

func TestCheckNeedsABase(t *testing.T) {
	r := newRepo(t)
	head := r.git("rev-parse", "HEAD")
	_, err := r.check("", head)
	assert.Error(t, err, "check.sh ran without a base")
}
