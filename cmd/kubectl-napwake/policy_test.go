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
	"crypto/sha256"
	"encoding/base64"
	"html"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The scan report and the status page run only their own style and
// scripts, which their policy names by hash, and load nothing: every
// source is inline, so opening one reaches nothing, and a link sends no
// referrer.
func TestPages_LoadNothingFromElsewhere(t *testing.T) {
	var status bytes.Buffer
	require.NoError(t, writeStatusHTML(&status, statusResult{Cluster: "staging"}, 24*time.Hour, statusNow))
	pages := map[string]string{"scan": renderHTML(t, historyResult()), "status": status.String()}

	for name, body := range pages {
		t.Run(name, func(t *testing.T) {
			m := regexp.MustCompile(`<meta http-equiv="Content-Security-Policy" content="([^"]*)">`).
				FindStringSubmatch(body)
			require.Len(t, m, 2, "the page has a policy")
			policy := html.UnescapeString(m[1])
			for _, directive := range []string{"default-src 'none'", "img-src data:", "font-src data:",
				"base-uri 'none'", "form-action 'none'"} {
				assert.Contains(t, policy, directive)
			}
			assert.NotContains(t, policy, "http", "nothing loads from a host")
			assert.Equal(t, 1, strings.Count(policy, "unsafe"), "only style attributes are inline")
			assert.Contains(t, policy, "style-src-attr 'unsafe-inline'")
			blocks := inlineBlock.FindAllStringSubmatch(body, -1)
			require.Len(t, blocks, 3, "a style and two scripts")
			for _, b := range blocks {
				sum := sha256.Sum256([]byte(b[2]))
				assert.Contains(t, policy, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'",
					"the policy names the page's %s", b[1])
			}
			assert.Contains(t, body, `<meta name="referrer" content="no-referrer">`)

			sources := regexp.MustCompile(`\s(?:src|srcset|poster)="([^"]*)"|url\(["']?([^"')]*)`).
				FindAllStringSubmatch(body, -1)
			require.NotEmpty(t, sources)
			for _, s := range sources {
				src := s[1] + s[2]
				assert.True(t, src == "" || strings.HasPrefix(src, "data:") || strings.HasPrefix(src, "#"),
					"only what's inline loads: %.60q", src)
			}
			for _, link := range regexp.MustCompile(`<link [^>]*href="([^"]*)"`).FindAllStringSubmatch(body, -1) {
				assert.True(t, strings.HasPrefix(link[1], "data:"), "a <link> loads only what's inline: %.60q", link[1])
			}
			assert.NotRegexp(t, `@import|<iframe|<object|<embed`, body)
		})
	}
}
