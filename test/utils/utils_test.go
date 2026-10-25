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

package utils

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Each checkout's kind clusters, and the kubeconfig, image and plugin named
// after them, are its own, so tests run from two worktrees at once never
// share one. Two checkouts whose directories share a name don't either.
func TestKindClusterName_IsEachCheckoutsOwn(t *testing.T) {
	script, err := filepath.Abs(filepath.Join("..", "..", "hack", "kind-cluster-name.sh"))
	require.NoError(t, err)
	name := func(test, checkout string) string {
		t.Helper()
		out, err := exec.Command(script, test, checkout).Output()
		require.NoError(t, err)
		return strings.TrimSpace(string(out))
	}
	checkout := func(dir string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), dir)
		require.NoError(t, os.Mkdir(path, 0o750))
		return path
	}
	first, second := checkout("napwake"), checkout("napwake")
	odd := checkout("Fix_P30 & P28 (a longer name than most)")

	names := []string{name("e2e", first), name("e2e", second), name("e2e", odd), name("smoke", first)}

	kindName := regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)
	seen := map[string]bool{}
	for _, n := range names {
		assert.Regexp(t, kindName, n)
		assert.LessOrEqual(t, len(n), 50, "kind warns of a name past 50 characters: %s", n)
		assert.False(t, seen[n], "%s is another checkout's, or another test's", n)
		seen[n] = true
	}
	assert.Equal(t, names[0], name("e2e", first), "a checkout gets the same cluster each run")
	assert.True(t, strings.HasPrefix(names[0], "napwake-e2e-napwake-"), "%s names its checkout", names[0])
}
