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
	"html/template"
	"io"
	"regexp"
	"strings"
)

// inlineBlock is a page's own style or script, between bare tags.
var inlineBlock = regexp.MustCompile(`(?s)<(script|style)>(.*?)</(?:script|style)>`)

// policyPlaceholder is where a page's Content-Security-Policy goes.
const policyPlaceholder = `content="POLICY"`

// executeWithPolicy writes t's page for data with its policy filled in.
// The policy is read from the page as rendered: html/template drops the
// comments in a style or a script, so their source hashes differently.
func executeWithPolicy(w io.Writer, t *template.Template, data any) error {
	var page bytes.Buffer
	if err := t.Execute(&page, data); err != nil {
		return err
	}
	_, err := io.WriteString(w, withPolicy(page.String()))
	return err
}

// withPolicy fills in page's Content-Security-Policy: it runs only the
// page's own style and scripts, named by their hashes, and loads nothing
// that isn't inline, so opening a report reaches nothing. Style attributes
// are allowed, as the pages' scenes use them: with every fetch 'none' or
// data:, a style can't load or send anything.
func withPolicy(page string) string {
	if strings.Count(page, policyPlaceholder) != 1 {
		panic("a page needs one " + policyPlaceholder)
	}
	var scripts, styles []string
	for _, m := range inlineBlock.FindAllStringSubmatch(page, -1) {
		sum := sha256.Sum256([]byte(m[2]))
		hash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
		if m[1] == "script" {
			scripts = append(scripts, hash)
		} else {
			styles = append(styles, hash)
		}
	}
	policy := strings.Join([]string{
		"default-src 'none'",
		"script-src " + strings.Join(scripts, " "),
		"style-src " + strings.Join(styles, " "),
		"style-src-attr 'unsafe-inline'",
		"img-src data:",
		"font-src data:",
		"base-uri 'none'",
		"form-action 'none'",
	}, "; ")
	return strings.Replace(page, policyPlaceholder, `content="`+policy+`"`, 1)
}
