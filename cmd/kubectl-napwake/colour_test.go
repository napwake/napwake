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
	"bytes"
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// Colour and doors are for a person at a terminal. Output that isn't a
// terminal's, which scripts read, is plain; NO_COLOR and a dumb terminal
// turn colour off; and doors need a UTF-8 locale in which terminals draw
// them one cell wide.
func TestPaletteFor(t *testing.T) {
	tests := []struct {
		name     string
		terminal bool
		goos     string
		env      map[string]string
		want     palette
	}{
		{name: "a terminal", terminal: true, goos: "darwin", env: map[string]string{"LANG": "en_GB.UTF-8"},
			want: palette{colour: true, doors: true}},
		{name: "piped", goos: "darwin", env: map[string]string{"LANG": "en_GB.UTF-8"}},
		{name: "NO_COLOR", terminal: true, goos: "linux", env: map[string]string{"NO_COLOR": "1", "LC_ALL": "C.UTF-8"},
			want: palette{doors: true}},
		{name: "NO_COLOR empty", terminal: true, goos: "linux", env: map[string]string{"NO_COLOR": "", "LANG": "C.utf8"},
			want: palette{colour: true, doors: true}},
		{name: "a dumb terminal", terminal: true, goos: "linux", env: map[string]string{"TERM": "dumb"}},
		{name: "no locale", terminal: true, goos: "linux", want: palette{colour: true}},
		{name: "a locale that isn't UTF-8", terminal: true, goos: "linux", env: map[string]string{"LANG": "en_US.ISO-8859-1"},
			want: palette{colour: true}},
		{name: "LC_ALL over LANG", terminal: true, goos: "linux",
			env: map[string]string{"LC_ALL": "C", "LANG": "en_US.UTF-8"}, want: palette{colour: true}},
		{name: "Japanese", terminal: true, goos: "darwin", env: map[string]string{"LANG": "ja_JP.UTF-8"},
			want: palette{colour: true}},
		{name: "the old Windows console", terminal: true, goos: "windows"},
		{name: "Windows Terminal", terminal: true, goos: "windows", env: map[string]string{"WT_SESSION": "1f2e"},
			want: palette{colour: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, paletteFor(tt.terminal, tt.goos, func(k string) string { return tt.env[k] }))
		})
	}
}

var sgr = regexp.MustCompile("\x1b\\[[0-9;]*m")

func statusTable(t *testing.T, pl palette) string {
	t.Helper()
	result, err := clusterStatus(context.Background(), newStatusClient(t, interceptor.Funcs{}, statusCluster()...),
		"preview-42", statusOpts())
	require.NoError(t, err)
	result.Cluster = testCluster
	var out bytes.Buffer
	require.NoError(t, writeStatusTable(&out, result, statusNow, pl))
	return out.String()
}

// At a terminal, each state is in its colour beside its door, what needs
// attention is red, and the header and dry-run are grey, and the codes
// leave the columns where they'd be without them.
func TestStatus_PaintedForATerminal(t *testing.T) {
	painted := statusTable(t, palette{colour: true, doors: true})
	doors := statusTable(t, palette{doors: true})

	assert.Equal(t, doors, sgr.ReplaceAllString(painted, ""), "the codes are all that's added")
	assert.Contains(t, painted, "  \x1b[94m1 paused\x1b[0m, \x1b[32m2 running\x1b[0m, \x1b[1;33m1 resuming\x1b[0m; 1 in "+
		"dry-run")
	assert.Contains(t, painted, "\n\x1b[1;31mNeeds attention:\x1b[0m\n")
	assert.Contains(t, painted, "\x1b[31mGitOpsConflict\x1b[0m")
	assert.Contains(t, painted, "\x1b[31mStuck\x1b[0m")
	assert.Contains(t, painted, "\x1b[94m▮ paused\x1b[0m")
	assert.Contains(t, painted, "\x1b[32m▯ running\x1b[0m \x1b[90m(dry-run)\x1b[0m")
	assert.Contains(t, painted, "\x1b[1;33m◧ resuming\x1b[0m")
	assert.Contains(t, painted, "\n\x1b[90m  NAMESPACE ")
	assert.Contains(t, painted, "\x1b[1mRecent pauses and wakes:\x1b[0m")

	lines := strings.Split(doors, "\n")
	header := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "  NAMESPACE") })
	require.GreaterOrEqual(t, header, 0)
	state, forColumn := strings.Index(lines[header], "STATE"), strings.Index(lines[header], "FOR ")
	for _, row := range lines[header+1 : header+5] {
		runes := []rune(row)
		require.Greater(t, len(runes), forColumn, row)
		assert.Regexp(t, `^(▮ paused|▯ running|◧ resuming)`, string(runes[state:]), "under STATE: %q", row)
		assert.Regexp(t, `^\S`, string(runes[forColumn:]), "the next column lines up: %q", row)
	}
}

// Output a script reads, or a terminal without colour, has no codes, and
// nothing but the words in STATE.
func TestStatus_PlainForScripts(t *testing.T) {
	var out bytes.Buffer
	result, err := clusterStatus(context.Background(), newStatusClient(t, interceptor.Funcs{}, statusCluster()...),
		"preview-42", statusOpts())
	require.NoError(t, err)

	require.NoError(t, writeStatus(&out, result, "table", statusNow))

	assert.NotContains(t, out.String(), "\x1b[")
	for _, door := range []string{"▮", "▯", "◧"} {
		assert.NotContains(t, out.String(), door)
	}
	assert.Equal(t, statusTable(t, palette{}), strings.Replace(out.String(), result.Cluster, testCluster, 1))
}
