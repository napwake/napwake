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
	"path"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// emDash is written as its escape, so this file doesn't hold one.
const emDash = "\u2014"

var (
	// emDashFiles may hold an em dash: Ref.astro's code turns those
	// Markdown's smart punctuation makes back into hyphens.
	emDashFiles = map[string]bool{"docs/src/components/Ref.astro": true}

	// lockfiles, by name, are written by npm and go from what packages
	// publish.
	lockfiles = map[string]bool{"package-lock.json": true, "go.sum": true}
)

// No em dash anywhere in the product, in code, comments, strings, docs or
// the chart, as docs/STYLE.md asks. Every file git lists is read, tracked
// or new, but not those it ignores.
func TestNoEmDashes(t *testing.T) {
	cmd := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	require.NoError(t, err, "listing the tree's files takes a git work tree")
	files := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	require.NotEmpty(t, files)

	for _, file := range files {
		if emDashFiles[file] || lockfiles[path.Base(file)] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(repoRoot, file))
		if os.IsNotExist(err) {
			continue // deleted, not yet staged
		}
		require.NoError(t, err)
		// An image's bytes can spell an em dash's by chance, and aren't
		// UTF-8 text.
		if !utf8.Valid(data) {
			continue
		}
		n := 0
		for line := range strings.Lines(string(data)) {
			n++
			if strings.Contains(line, emDash) {
				t.Errorf("%s:%d has an em dash: write a full stop or a comma instead, as docs/STYLE.md asks", file, n)
			}
		}
	}
}
