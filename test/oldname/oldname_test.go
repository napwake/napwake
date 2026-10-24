/*
Copyright 2026.

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

// Package oldname_test keeps the product's old name, Hybernate, out of the
// tree. It's allowed only where it's history: the changelog's sections for
// the releases that shipped under it and its entry for the rename, and the
// upgrade guide's section for people moving from it.
package oldname_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/mod/semver"
)

const (
	repoRoot = "../.."
	oldName  = "hybernate"
	thisFile = "test/oldname/oldname_test.go"

	// The changelog's sections for releases before renamedIn, the first
	// under the new name, keep the name they shipped under, and
	// renamedIn's own renamedHeading subsection says what it was.
	changelog      = "CHANGELOG.md"
	renamedIn      = "v0.2.0"
	renamedHeading = "### Renamed"

	// The upgrade guide's section for people moving from the old name,
	// whose heading ends with fromOldName, names it to say what changed.
	upgradeGuide = "docs/operations/upgrading.md"
	fromOldName  = "{ #moving-to-napwake }"
)

// releaseHeading is a changelog section for a release, dated once it has
// shipped.
var (
	releaseHeading = regexp.MustCompile(`^## \[(\d+\.\d+\.\d+)\] - (.+)$`)
	shipped        = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

func TestOldNameIsGone(t *testing.T) {
	if !inWorkTree(repoRoot) {
		t.Skip("listing the tree's files takes a git work tree")
	}
	for _, path := range treeFiles(t) {
		if containsOldName([]byte(path)) {
			t.Errorf("%s: the path names %s", path, oldName)
		}
		if path == thisFile {
			continue
		}
		data, err := os.ReadFile(filepath.Join(repoRoot, path))
		if os.IsNotExist(err) {
			continue // deleted, not yet staged
		}
		require.NoError(t, err)
		lines := strings.Split(string(data), "\n")
		for _, n := range oldNameLines(path, data) {
			t.Errorf("%s:%d names %s: %s", path, n, oldName, strings.TrimSpace(lines[n-1]))
		}
	}
}

// oldNameLines returns the numbers of a file's lines that name the old
// name where it isn't allowed. A binary file, one with a NUL byte, has no
// lines to read.
func oldNameLines(path string, data []byte) []int {
	if !containsOldName(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	allowed := allowedLines(path, lines)
	var out []int
	for i, line := range lines {
		if containsOldName([]byte(line)) && !allowed[i] {
			out = append(out, i+1)
		}
	}
	return out
}

func inWorkTree(dir string) bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = dir
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// treeFiles lists the files in the tree, tracked or not, but not those git
// ignores, such as bin/ and the docs' site/.
func treeFiles(t *testing.T) []string {
	t.Helper()
	cmd := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	require.NoError(t, err)
	files := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	require.NotEmpty(t, files)
	return files
}

func containsOldName(data []byte) bool {
	return bytes.Contains(bytes.ToLower(data), []byte(oldName))
}

// allowedLines marks the lines of a file that may name the old name.
func allowedLines(path string, lines []string) map[int]bool {
	switch path {
	case changelog:
		return changelogHistory(lines)
	case upgradeGuide:
		return sectionFromOldName(lines)
	}
	return nil
}

// changelogHistory marks the changelog's sections for releases shipped
// before renamedIn, and renamedIn's entry for the rename. The link
// references at the end of the file are neither.
func changelogHistory(lines []string) map[int]bool {
	allowed := map[int]bool{}
	var before, renaming, renamed bool
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "## "):
			before, renaming, renamed = false, false, false
			if m := releaseHeading.FindStringSubmatch(line); m != nil {
				version := "v" + m[1]
				before = shipped.MatchString(m[2]) && semver.Compare(version, renamedIn) < 0
				renaming = version == renamedIn
			}
		case strings.HasPrefix(line, "### "):
			renamed = renaming && line == renamedHeading
		case strings.HasPrefix(line, "["):
			before, renaming, renamed = false, false, false
		}
		allowed[i] = before || renamed
	}
	return allowed
}

// sectionFromOldName marks the upgrade guide's section for people moving
// from the old name, from its heading to the next of its level.
func sectionFromOldName(lines []string) map[int]bool {
	allowed := map[int]bool{}
	in, fenced := false, false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
		}
		if !fenced && strings.HasPrefix(line, "## ") {
			in = strings.HasSuffix(line, fromOldName)
		}
		allowed[i] = in
	}
	return allowed
}

func TestOldNameLinesSkipsBinaryFiles(t *testing.T) {
	text := []byte("first\nmade by " + oldName + "\n")
	assert.Equal(t, []int{2}, oldNameLines("notes.md", text))
	assert.Empty(t, oldNameLines("logo.png", append([]byte{0x89, 'P', 'N', 'G', 0}, text...)))
}

func TestOnlyAGitWorkTreeIsListed(t *testing.T) {
	assert.True(t, inWorkTree(repoRoot))
	assert.False(t, inWorkTree(t.TempDir()))
}

// A plugin binary built at the root, under either name, is ignored, so a
// stale one built before the rename doesn't fail the guard.
func TestRootPluginBinariesAreIgnored(t *testing.T) {
	for _, name := range []string{"kubectl-napwake", "kubectl-" + oldName} {
		cmd := exec.Command("git", "check-ignore", "-q", name)
		cmd.Dir = repoRoot
		assert.NoError(t, cmd.Run(), "git doesn't ignore %s at the root", name)
	}
}
