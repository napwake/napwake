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

package docs_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Every Go file starts with the licence header in hack/boilerplate.go.txt,
// after its build constraint if it has one, as controller-gen writes it.
// Every Go file git lists is read, tracked or new, but not those it
// ignores.
func TestGoFilesHaveTheLicenceHeader(t *testing.T) {
	header, err := os.ReadFile(filepath.Join(repoRoot, "hack", "boilerplate.go.txt"))
	require.NoError(t, err)
	cmd := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "*.go")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	require.NoError(t, err, "listing the tree's files takes a git work tree")
	files := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	require.NotEmpty(t, files)

	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(repoRoot, file))
		if os.IsNotExist(err) {
			continue // deleted, not yet staged
		}
		require.NoError(t, err)
		if !strings.HasPrefix(afterBuildConstraint(string(data)), string(header)) {
			t.Errorf("%s doesn't start with the licence header in hack/boilerplate.go.txt", file)
		}
	}
}

// afterBuildConstraint returns a Go file's source after its //go:build
// line and the blank lines after it, or the whole source if it has none.
func afterBuildConstraint(src string) string {
	rest, ok := strings.CutPrefix(src, "//go:build ")
	if !ok {
		return src
	}
	_, rest, _ = strings.Cut(rest, "\n")
	return strings.TrimLeft(rest, "\n")
}
