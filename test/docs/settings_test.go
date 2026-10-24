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

package docs_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The setting that turned waking on request off for one workload is gone,
// with the WakeOnRequest reason that reported it. Only the upgrade guide,
// which says what replaces it, names them.
func TestDocsDontNameTheWakeSettingThatWent(t *testing.T) {
	gone := []string{"wake.onRequest", "napwake.dev/wake-on-request", "TurnedOff"}
	upgrading := filepath.Join(repoRoot, contentDir, "operations", "upgrading.mdx")
	pages := docsPages(t)
	require.Contains(t, pages, upgrading, "the upgrade guide is where the docs say what replaced them")
	for _, path := range pages {
		if path == upgrading {
			continue
		}
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		for _, name := range gone {
			assert.False(t, strings.Contains(string(data), name), "%s names %s, which is gone", path, name)
		}
	}
}
