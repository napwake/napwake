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

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The scan's install commands are the docs': each page's block that installs
// with napwake-values.yaml runs them, at the chart's release, up to and
// including helm install. A reader gets the same install wherever they copy
// it from, and a copy that drifts, by a flag or a line, fails here.
func TestInstallCommands_AreTheDocs(t *testing.T) {
	root := filepath.Join("..", "..")
	chart, err := os.ReadFile(filepath.Join(root, "charts", "napwake", "Chart.yaml"))
	require.NoError(t, err)
	version := regexp.MustCompile(`(?m)^version: (\S+)$`).FindSubmatch(chart)
	require.NotNil(t, version, "Chart.yaml has a version")
	release := "v" + string(version[1])
	// The CHANGELOG names the release it's for, and a release candidate's
	// bump leaves it alone, so its block installs that release: v0.2.0 at
	// v0.2.0-rc.12. Only that release's section is read: an older one's
	// block installs what it did then. And a release's summary needn't have
	// one, as 0.2.1's links to the upgrade guide instead.
	target, _, _ := strings.Cut(release, "-")

	for _, file := range []string{
		"docs/src/content/docs/getting-started/installation.mdx",
		"docs/src/content/docs/reference/helm-values.mdx",
		"README.md",
		"charts/napwake/README.md",
		"CHANGELOG.md",
	} {
		t.Run(file, func(t *testing.T) {
			want := installCommands(release)
			if file == "CHANGELOG.md" {
				want = installCommands(target)
			}
			page, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
			require.NoError(t, err)
			text := string(page)
			if file == "CHANGELOG.md" {
				text = changelogSection(t, text, strings.TrimPrefix(target, "v"))
			}
			blocks := helmInstallBlocks(text)
			if file == "CHANGELOG.md" && len(blocks) == 0 {
				return
			}
			require.Len(t, blocks, 1, "one block installs with napwake-values.yaml")
			commands := blocks[0]
			if i := slices.IndexFunc(commands, func(c string) bool { return strings.HasPrefix(c, "helm install ") }); i >= 0 {
				commands = commands[:i+1]
			}
			assert.Equal(t, want, commands, "the block's commands, up to helm install")
		})
	}
}

// changelogSection is the text of the CHANGELOG's section for version, from
// its heading to the next version's.
func changelogSection(t *testing.T, changelog, version string) string {
	t.Helper()
	heading := regexp.MustCompile(`(?m)^## \[` + regexp.QuoteMeta(version) + `\] - .*$`).FindStringIndex(changelog)
	require.NotNil(t, heading, "CHANGELOG.md has a section for %s", version)
	section := changelog[heading[1]:]
	if next := regexp.MustCompile(`(?m)^## \[`).FindStringIndex(section); next != nil {
		section = section[:next[0]]
	}
	return section
}

var (
	shellBlock   = regexp.MustCompile("(?ms)^[ \t]*```(?:bash|sh)[^\n]*\n(.*?)^[ \t]*```")
	continuation = regexp.MustCompile(`[ \t]*\\\n[ \t]*`)
)

// helmInstallBlocks are the commands of each shell block on a page that
// installs Napwake with napwake-values.yaml, each continued line joined as
// the shell joins it.
func helmInstallBlocks(page string) [][]string {
	var blocks [][]string
	for _, match := range shellBlock.FindAllStringSubmatch(page, -1) {
		body := match[1]
		if !strings.Contains(body, "helm install napwake oci://") || !strings.Contains(body, "napwake-values.yaml") {
			continue
		}
		var commands []string
		for line := range strings.SplitSeq(continuation.ReplaceAllString(body, " "), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				commands = append(commands, line)
			}
		}
		blocks = append(blocks, commands)
	}
	return blocks
}
