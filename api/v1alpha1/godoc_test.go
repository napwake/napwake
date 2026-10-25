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

package v1alpha1

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every type of the API that has a doc comment has its own, starting with
// its name, as godoc and the generated reference read it: one type put
// between another's comment and its declaration takes that comment over.
func TestTypeDocsNameTheirType(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	checked := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") || strings.HasPrefix(file, "zz_generated") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ParseComments)
		require.NoError(t, err)
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts := spec.(*ast.TypeSpec)
				doc := gen.Doc
				if len(gen.Specs) > 1 || ts.Doc != nil {
					doc = ts.Doc
				}
				if text := prose(doc); text != "" {
					checked++
					assert.True(t, strings.HasPrefix(text, ts.Name.Name+" "),
						"%s: the doc comment before %s is about something else: %q", file, ts.Name.Name, text)
				}
			}
		}
	}
	assert.Positive(t, checked)
}

// prose is a doc comment without its kubebuilder markers.
func prose(doc *ast.CommentGroup) string {
	if doc == nil {
		return ""
	}
	var lines []string
	for line := range strings.SplitSeq(doc.Text(), "\n") {
		if !strings.HasPrefix(line, "+") {
			lines = append(lines, line)
		}
	}
	return strings.TrimSpace(strings.Join(lines, " "))
}
