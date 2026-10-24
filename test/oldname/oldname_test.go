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
// the releases that shipped under it, and the upgrade guide's section for
// people moving from it.
package oldname_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/mod/semver"
)

const (
	repoRoot = "../.."
	oldName  = "hybernate"
	thisFile = "test/oldname/oldname_test.go"

	// The changelog's sections for releases before renamedIn, the first
	// under the new name, keep the name they shipped under.
	changelog = "CHANGELOG.md"
	renamedIn = "v0.2.0"

	// The upgrade guide's section for people moving from the old name,
	// whose heading ends with fromOldName, names it to say what changed.
	upgradeGuide = "docs/operations/upgrading.md"
	fromOldName  = "{ #moving-to-napwake }"
)

// releaseHeading is a changelog section for a release that has shipped.
var releaseHeading = regexp.MustCompile(`^## \[(\d+\.\d+\.\d+)\] - \d{4}-\d{2}-\d{2}$`)

func TestOldNameIsGone(t *testing.T) {
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
		if !containsOldName(data) {
			continue
		}
		lines := strings.Split(string(data), "\n")
		allowed := allowedLines(path, lines)
		for i, line := range lines {
			if containsOldName([]byte(line)) && !allowed[i] {
				t.Errorf("%s:%d names %s: %s", path, i+1, oldName, strings.TrimSpace(line))
			}
		}
	}
}

// treeFiles lists the files in the tree, tracked or not, but not those git
// ignores, such as bin/ and the docs' site/.
func treeFiles(t *testing.T) []string {
	t.Helper()
	cmd := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	require.NoError(t, err, "listing the tree's files needs git and a clone")
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
		return releasedBeforeRename(lines)
	case upgradeGuide:
		return sectionFromOldName(lines)
	}
	return nil
}

// releasedBeforeRename marks the changelog's sections for releases before
// renamedIn. The link references at the end of the file aren't theirs.
func releasedBeforeRename(lines []string) map[int]bool {
	allowed := map[int]bool{}
	history := false
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "## "):
			m := releaseHeading.FindStringSubmatch(line)
			history = m != nil && semver.Compare("v"+m[1], renamedIn) < 0
		case strings.HasPrefix(line, "["):
			history = false
		}
		allowed[i] = history
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
