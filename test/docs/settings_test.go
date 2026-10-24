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
	"io/fs"
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
	root := filepath.Join("..", "..", "docs")
	gone := []string{"wake.onRequest", "hybernate.io/wake-on-request", "TurnedOff"}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".md" ||
			path == filepath.Join(root, "operations", "upgrading.md") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, name := range gone {
			assert.False(t, strings.Contains(string(data), name), "%s names %s, which is gone", path, name)
		}
		return nil
	})
	require.NoError(t, err)
}
