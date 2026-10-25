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
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The install smoke test runs the docs' own install and verify commands, so
// it proves what a reader runs. Each block a reader copies is found on its
// page by a command in it, and each of its lines, with the release's version
// written as ${VERSION}, must be a line of the workflow.
func TestInstallSmokeRunsTheDocsCommands(t *testing.T) {
	workflow, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "install-smoke.yml"))
	require.NoError(t, err, "the install smoke test runs the docs' install and verify commands")
	lines := map[string]bool{}
	for line := range strings.SplitSeq(string(workflow), "\n") {
		lines[strings.TrimSpace(line)] = true
	}
	version := chartVersion(t)

	for _, c := range []struct{ page, command string }{
		{"getting-started/kubectl-plugin.mdx", "brew install napwake/tap/kubectl-napwake"},
		{"getting-started/kubectl-plugin.mdx", "kubectl krew install --manifest-url"},
		{"getting-started/installation.mdx", "helm install napwake oci://"},
		{"getting-started/installation.mdx", "kubectl apply -f https://github.com/napwake/napwake/releases/download/"},
		{"reference/security.mdx", "cosign verify $IMAGE"},
		{"reference/security.mdx", "cosign verify ghcr.io/napwake/charts/napwake"},
		{"reference/security.mdx", "cosign verify-blob checksums.txt"},
	} {
		data, err := os.ReadFile(filepath.Join(repoRoot, contentDir, c.page))
		require.NoError(t, err)
		var found []string
		for _, b := range shellBlocks(string(data)) {
			if strings.Contains(b, c.command) {
				found = append(found, b)
			}
		}
		require.Len(t, found, 1, "one block on %s runs %q", c.page, c.command)
		for line := range strings.SplitSeq(found[0], "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			line = strings.ReplaceAll(strings.ReplaceAll(line, "v"+version, "v${VERSION}"), version, "${VERSION}")
			assert.True(t, lines[line], "install-smoke.yml doesn't run %s's line %q", c.page, line)
		}
	}
}

var chartVersionLine = regexp.MustCompile(`(?m)^version: (\S+)$`)

// chartVersion is the release the docs name: main's target, or a release
// commit's own version, which make bump writes into both.
func chartVersion(t *testing.T) string {
	t.Helper()
	chart, err := os.ReadFile(filepath.Join(repoRoot, "charts", "napwake", "Chart.yaml"))
	require.NoError(t, err)
	match := chartVersionLine.FindSubmatch(chart)
	require.NotNil(t, match, "Chart.yaml has a version")
	return string(match[1])
}

// shellBlocks returns the bodies of a page's fenced bash and sh blocks,
// dedented by their fence's indentation, as a reader copies them.
func shellBlocks(page string) []string {
	var (
		blocks  []string
		current *strings.Builder
		indent  string
	)
	scanner := bufio.NewScanner(strings.NewReader(page))
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		lang, _, _ := strings.Cut(trimmed, " ")
		switch {
		case current == nil && (lang == "```bash" || lang == "```sh"):
			current = &strings.Builder{}
			indent = line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		case current != nil && trimmed == "```":
			blocks = append(blocks, current.String())
			current = nil
		case current != nil:
			current.WriteString(strings.TrimPrefix(line, indent))
			current.WriteByte('\n')
		}
	}
	return blocks
}
