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

package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "rewrite testdata/kubectl-napwake.rb from what the formula renders")

// releaseDir writes stand-ins for a release's plugin archives, each holding
// its own platform's name, so each has a checksum of its own.
func releaseDir(t *testing.T, platforms ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, p := range platforms {
		name := "kubectl-napwake-" + p + ".tar.gz"
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(p), 0o644))
	}
	return dir
}

func TestFormula(t *testing.T) {
	dir := releaseDir(t, "darwin-arm64", "darwin-amd64", "linux-arm64", "linux-amd64", "windows-amd64")

	var out bytes.Buffer
	require.NoError(t, run(&out, "v0.2.0", "napwake/napwake", dir))

	golden := filepath.Join("testdata", "kubectl-napwake.rb")
	if *update {
		require.NoError(t, os.WriteFile(golden, out.Bytes(), 0o644))
	}
	want, err := os.ReadFile(golden)
	require.NoError(t, err)
	assert.Equal(t, string(want), out.String(), "run go test ./hack/brewformula -update to accept a deliberate change")
}

func TestFormulaRefuses(t *testing.T) {
	complete := releaseDir(t, "darwin-arm64", "darwin-amd64", "linux-arm64", "linux-amd64")
	tests := []struct {
		name    string
		tag     string
		repo    string
		dir     string
		wantErr string
	}{
		{
			name:    "a tag that isn't a release's",
			tag:     "main",
			repo:    "napwake/napwake",
			dir:     complete,
			wantErr: `"main" isn't a release tag`,
		},
		{
			name:    "a repository that isn't one",
			tag:     "v0.2.0",
			repo:    "napwake/napwake\" do evil",
			dir:     complete,
			wantErr: "isn't a GitHub repository",
		},
		{
			name:    "a missing archive",
			tag:     "v0.2.0",
			repo:    "napwake/napwake",
			dir:     releaseDir(t, "darwin-arm64", "darwin-amd64", "linux-amd64"),
			wantErr: "kubectl-napwake-linux-arm64.tar.gz",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := run(&out, tt.tag, tt.repo, tt.dir)
			assert.ErrorContains(t, err, tt.wantErr)
			assert.Empty(t, out.String(), "nothing is written for a release that can't be described")
		})
	}
}
