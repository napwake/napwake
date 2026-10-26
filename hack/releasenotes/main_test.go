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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const changelogHead = `# Changelog

## [Unreleased]

`

const olderRelease = `## [0.1.0] - 2026-03-17

### Added

- The first release
`

func TestNotes(t *testing.T) {
	tests := []struct {
		name      string
		changelog string
		tag       string
		want      string
	}{
		{
			name: "a section that opens with a summary gives the summary and a link to the whole section",
			changelog: changelogHead + `## [0.2.0] - 2026-10-30

Napwake pauses idle workloads.

Install it new.

### Added

- A long list
- of changes

` + olderRelease,
			tag: "v0.2.0",
			want: `Napwake pauses idle workloads.

Install it new.

All of 0.2.0's changes are listed in [CHANGELOG.md](https://github.com/o/r/blob/v0.2.0/CHANGELOG.md#020---2026-10-30).

**Docs for this release:** https://docs.napwake.dev/0.2/
`,
		},
		{
			name: "a section without one gives the whole section",
			changelog: changelogHead + `## [0.2.1] - 2026-11-02

### Fixed

- One fix

` + olderRelease,
			tag: "v0.2.1",
			want: `
### Fixed

- One fix


**Docs for this release:** https://docs.napwake.dev/0.2/
`,
		},
		{
			name: "a release candidate takes its version's section, and next's docs",
			changelog: changelogHead + `## [0.3.0] - Unreleased

The summary.

### Added

- A change
`,
			tag: "v0.3.0-rc.2",
			want: `The summary.

All of 0.3.0's changes are listed in [CHANGELOG.md](https://github.com/o/r/blob/v0.3.0-rc.2/CHANGELOG.md#030---unreleased).

**Docs for this release:** https://docs.napwake.dev/next/
`,
		},
		{
			name: "a section with no ### heading is all summary, so it's given whole",
			changelog: changelogHead + `## [0.2.2] - 2026-11-09

Only the operator's image is rebuilt.

` + olderRelease,
			tag: "v0.2.2",
			want: `
Only the operator's image is rebuilt.


**Docs for this release:** https://docs.napwake.dev/0.2/
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := notes(tt.changelog, tt.tag, "o/r")
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// A section without a summary that's over GitHub's limit is cut at the
// last whole line that fits, and links to the rest.
func TestNotesCutsALongSectionWithoutASummary(t *testing.T) {
	const change = "- A change that users notice, one of many in this release\n"
	var list strings.Builder
	for list.Len() <= maxSection {
		list.WriteString(change)
	}
	changelog := changelogHead + "## [0.2.0] - 2026-10-30\n\n### Added\n\n" + list.String() + "\n" + olderRelease

	got, err := notes(changelog, "v0.2.0", "o/r")
	require.NoError(t, err)

	const rest = "\nThe list goes on in [CHANGELOG.md](https://github.com/o/r/blob/v0.2.0/CHANGELOG.md).\n"
	body, docs, found := strings.Cut(got, rest)
	require.True(t, found, "the cut notes link to the rest:\n%s", got[len(got)-300:])
	assert.LessOrEqual(t, len(body), maxSection)
	assert.True(t, strings.HasSuffix(body, change), "cut at a whole line")
	assert.Equal(t, "\n**Docs for this release:** https://docs.napwake.dev/0.2/\n", docs)
}

func TestNotesRefuses(t *testing.T) {
	const missing = "CHANGELOG.md's section for 0.2.0 is empty or missing"
	tests := []struct {
		name, changelog, tag, want string
	}{
		{"a missing section", changelogHead + olderRelease, "v0.2.0", missing},
		{"an empty section", changelogHead + "## [0.2.0] - Unreleased\n\n" + olderRelease, "v0.2.0", missing},
		{"a tag that isn't a version", changelogHead + olderRelease, "v0.1", "isn't a release's tag"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := notes(tt.changelog, tt.tag, "o/r")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

// The release being prepared, the chart's version, gets notes GitHub takes
// whole: its section opens with a summary, or is short enough without one.
func TestTheNextReleasesNotesFit(t *testing.T) {
	chart, err := os.ReadFile(filepath.Join("..", "..", "charts", "napwake", "Chart.yaml"))
	require.NoError(t, err)
	m := regexp.MustCompile(`(?m)^version: (\S+)$`).FindSubmatch(chart)
	require.NotNil(t, m, "Chart.yaml has a version")
	changelog, err := os.ReadFile(filepath.Join("..", "..", "CHANGELOG.md"))
	require.NoError(t, err)

	got, err := notes(string(changelog), "v"+string(m[1]), "napwake/napwake")
	require.NoError(t, err)
	assert.NotContains(t, got, "The list goes on in",
		"the release's notes would be cut: open its CHANGELOG.md section with a summary, above its first ###")
}
