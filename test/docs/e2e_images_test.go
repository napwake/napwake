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
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pinnedImage is an image by tag and digest, such as
// curlimages/curl:8.7.1@sha256:..., so the tag says what it is and the
// digest holds it to that.
var pinnedImage = regexp.MustCompile(`^[a-z0-9./-]+:[A-Za-z0-9._-]+@sha256:[0-9a-f]{64}$`)

// The end-to-end tests run against the images make load-test-e2e-images
// loads, so each is pinned by digest: a retagged image would change what
// they test, unseen.
func TestE2EImagesArePinnedByDigest(t *testing.T) {
	makefile, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	require.NoError(t, err)
	images := makeVariable(t, string(makefile), "E2E_IMAGES")
	require.NotEmpty(t, images, "the Makefile sets E2E_IMAGES")
	for _, image := range images {
		assert.Regexp(t, pinnedImage, image, "E2E_IMAGES has an image that isn't tag@sha256:digest")
	}
}

// makeVariable returns the words a Makefile sets a variable to, with ?=
// or =, across its continued lines.
func makeVariable(t *testing.T, makefile, name string) []string {
	t.Helper()
	var value strings.Builder
	in := false
	for line := range strings.Lines(makefile) {
		line = strings.TrimRight(line, "\n")
		if !in {
			rest, ok := strings.CutPrefix(line, name+" ")
			if !ok {
				continue
			}
			_, rest, ok = strings.Cut(rest, "=")
			if !ok {
				continue
			}
			line, in = rest, true
		}
		continued := strings.HasSuffix(line, `\`)
		value.WriteString(strings.TrimSuffix(line, `\`) + " ")
		if !continued {
			break
		}
	}
	return strings.Fields(value.String())
}
