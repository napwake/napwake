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
	"go/ast"
	"go/parser"
	"go/token"
	"html"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// builtSiteEnv names the built docs site the links are checked against.
// Set, the site must be there; unset, the test reads docs/dist, Astro's
// own output, when a build has left one.
const builtSiteEnv = "NAPWAKE_DOCS_DIST"

var (
	// docsAddress is an address on the docs site, as code, a chart, a
	// changelog or a README writes one: bare, quoted, in a Markdown link or
	// bold. A sentence's full stop after it isn't part of it.
	docsAddress = regexp.MustCompile("https://docs\\.napwake\\.dev(/[^\\s\"'`<>()\\[\\]\\\\*]*)?")

	// chartAddress is an address the chart's NOTES build from the docs of
	// the chart's own version, {{ $docs }}.
	chartAddress = regexp.MustCompile(`\{\{-?\s*\$docs\s*-?\}\}([^\s"'` + "`" + `<>()]*)`)

	// versioned is the version folder an address may start with, which the
	// built site, one version of the docs, is served under.
	versioned = regexp.MustCompile(`^/(latest|next|\d+\.\d+)(/|$)`)

	elementID = regexp.MustCompile(`\sid="([^"]+)"`)
)

// Every address on the docs site that Napwake shows a person or links to
// from outside the docs, in its code, its chart, its manifests, the README
// or the changelog, is a page of the built site, and its anchor, if it has
// one, is on that page. A version's folder, such as next in the chart's
// NOTES for a release candidate, is taken off first, since the built site
// is one version. The docs' own links are checked by the build.
//
// Go code also builds addresses from its constants, such as gitOpsGuide +
// "#argo-cd", so each + of strings is read as the code joins it.
func TestDocsAddressesLandInTheBuiltSite(t *testing.T) {
	site := openBuiltSite(t)
	found := 0
	check := func(file string, addresses []string) {
		for _, address := range addresses {
			found++
			if problem := site.resolve(address); problem != "" {
				t.Errorf("%s links to https://docs.napwake.dev%s, %s", file, address, problem)
			}
		}
	}
	goFiles := map[string][]string{}
	for _, file := range filesOutsideDocs(t) {
		data, err := os.ReadFile(filepath.Join(repoRoot, file))
		if os.IsNotExist(err) {
			continue // deleted, not yet staged
		}
		require.NoError(t, err)
		check(file, docsAddresses(file, string(data)))
		if filepath.Ext(file) == ".go" {
			goFiles[path.Dir(file)] = append(goFiles[path.Dir(file)], file)
		}
	}
	for _, files := range goFiles {
		for file, joined := range joinedStrings(t, repoRoot, files) {
			for _, s := range joined {
				check(file, docsAddresses(file, s))
			}
		}
	}
	t.Logf("%d addresses checked against %s", found, site.dir)
	assert.Greater(t, found, 50, "the scan finds the addresses the product prints")
}

// joinedStrings returns, for each of a package's files, the strings its +
// expressions build from string literals, the package's string constants
// and variables, and the values of its maps of strings, every value a map
// holds standing in for an index into it. Whatever else they join, such as
// a call, ends an address, as a space would.
func joinedStrings(t *testing.T, root string, files []string) map[string][]string {
	t.Helper()
	fset := token.NewFileSet()
	parsed := make(map[string]*ast.File, len(files))
	for _, file := range files {
		f, err := parser.ParseFile(fset, filepath.Join(root, file), nil, parser.SkipObjectResolution)
		require.NoError(t, err)
		parsed[file] = f
	}

	known := map[string][]string{}
	// A constant may be built from another, declared later or in another
	// file, so the package's are read until no more are learned.
	for learned := true; learned; {
		learned = false
		for _, f := range parsed {
			for _, decl := range f.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
					continue
				}
				for _, spec := range gen.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, name := range vs.Names {
						if _, done := known[name.Name]; done || i >= len(vs.Values) {
							continue
						}
						if values, ok := stringValues(vs.Values[i], known); ok {
							known[name.Name] = values
							learned = true
						}
					}
				}
			}
		}
	}

	out := map[string][]string{}
	for file, f := range parsed {
		var visit func(ast.Node) bool
		visit = func(n ast.Node) bool {
			b, ok := n.(*ast.BinaryExpr)
			if !ok || b.Op != token.ADD {
				return true
			}
			operands := flattenAdd(b)
			joined := []string{""}
			for _, operand := range operands {
				values, ok := stringValues(operand, known)
				if !ok {
					values = []string{" "}
					ast.Inspect(operand, visit)
				}
				joined = product(joined, values)
			}
			out[file] = append(out[file], joined...)
			return false
		}
		ast.Inspect(f, visit)
	}
	return out
}

// stringValues returns the strings an expression can be, from what's
// known of the package's constants, variables and maps.
func stringValues(e ast.Expr, known map[string][]string) ([]string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return nil, false
		}
		s, err := strconv.Unquote(e.Value)
		return []string{s}, err == nil
	case *ast.Ident:
		values, ok := known[e.Name]
		return values, ok
	case *ast.ParenExpr:
		return stringValues(e.X, known)
	case *ast.IndexExpr:
		return stringValues(e.X, known)
	case *ast.CompositeLit:
		if _, ok := e.Type.(*ast.MapType); !ok {
			return nil, false
		}
		var values []string
		for _, elt := range e.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				return nil, false
			}
			v, ok := stringValues(kv.Value, known)
			if !ok {
				return nil, false
			}
			values = append(values, v...)
		}
		return values, len(values) > 0
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return nil, false
		}
		joined := []string{""}
		for _, operand := range flattenAdd(e) {
			values, ok := stringValues(operand, known)
			if !ok {
				return nil, false
			}
			joined = product(joined, values)
		}
		return joined, true
	}
	return nil, false
}

func flattenAdd(e ast.Expr) []ast.Expr {
	if b, ok := e.(*ast.BinaryExpr); ok && b.Op == token.ADD {
		return append(flattenAdd(b.X), flattenAdd(b.Y)...)
	}
	return []ast.Expr{e}
}

func product(prefixes, values []string) []string {
	out := make([]string, 0, len(prefixes)*len(values))
	for _, p := range prefixes {
		for _, v := range values {
			out = append(out, p+v)
		}
	}
	return out
}

// docsAddresses returns the path, query left out and anchor kept, of each
// docs address in a file. Those with a placeholder, such as the %d.%d of a
// version a program fills in, are left out: their forms are checked where
// they're filled in.
func docsAddresses(file, data string) []string {
	var out []string
	add := func(p string) {
		p = strings.TrimRight(p, ".,;:!?")
		if strings.ContainsAny(p, "%{}$") {
			return
		}
		if before, after, ok := strings.Cut(p, "?"); ok {
			_, anchor, _ := strings.Cut(after, "#")
			p = before
			if anchor != "" {
				p += "#" + anchor
			}
		}
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		out = append(out, p)
	}
	for _, m := range docsAddress.FindAllStringSubmatch(data, -1) {
		add(m[1])
	}
	if strings.HasPrefix(file, "charts/") {
		for _, m := range chartAddress.FindAllStringSubmatch(data, -1) {
			add(m[1])
		}
	}
	return out
}

// filesOutsideDocs lists the files in the tree outside docs/, tracked or
// not, but not those git ignores.
func filesOutsideDocs(t *testing.T) []string {
	t.Helper()
	cmd := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", ".", ":!docs")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	require.NoError(t, err, "listing the tree's files takes a git work tree")
	files := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	require.NotEmpty(t, files)
	return files
}

// builtSite is a docs build's output folder, a version of the site.
type builtSite struct {
	dir string
	ids map[string]map[string]bool
}

func openBuiltSite(t *testing.T) *builtSite {
	t.Helper()
	dir, set := os.LookupEnv(builtSiteEnv)
	if !set {
		dir = filepath.Join(repoRoot, "docs", "dist")
	}
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		if set {
			t.Fatalf("%s=%s has no built docs site: %v", builtSiteEnv, dir, err)
		}
		t.Skipf("no built docs site in %s: run npm run build in docs/, or set %s", dir, builtSiteEnv)
	}
	return &builtSite{dir: dir, ids: map[string]map[string]bool{}}
}

// resolve says what's wrong with an address on the site, or "" when it
// lands: a page, a directory's index.html, or another file the build
// wrote, such as a page's .md copy, and an anchor that's an element's id
// on that page.
func (s *builtSite) resolve(address string) string {
	page, anchor, _ := strings.Cut(address, "#")
	if m := versioned.FindStringSubmatch(page); m != nil {
		page = "/" + strings.TrimPrefix(page, m[0])
	}
	file := filepath.Join(s.dir, filepath.FromSlash(page))
	if path.Ext(page) == "" {
		file = filepath.Join(file, "index.html")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "a page the built site doesn't have"
	}
	if anchor == "" {
		return ""
	}
	ids, ok := s.ids[file]
	if !ok {
		ids = map[string]bool{}
		for _, m := range elementID.FindAllStringSubmatch(string(data), -1) {
			ids[html.UnescapeString(m[1])] = true
		}
		s.ids[file] = ids
	}
	if !ids[anchor] {
		return "an anchor its page doesn't have"
	}
	return ""
}

func TestDocsAddresses(t *testing.T) {
	tests := []struct {
		name string
		file string
		data string
		want []string
	}{
		{
			name: "in Go, with an anchor",
			file: "internal/doorman/page.go",
			data: `docs = "https://docs.napwake.dev/concepts/wake-on-request/#asking-before-waking"`,
			want: []string{"/concepts/wake-on-request/#asking-before-waking"},
		},
		{
			name: "the site's root, bare and in a Markdown link",
			file: "README.md",
			data: "See https://docs.napwake.dev. Or [the docs](https://docs.napwake.dev/).",
			want: []string{"/", "/"},
		},
		{
			name: "at the end of a sentence, and in bold",
			file: "CHANGELOG.md",
			data: "See https://docs.napwake.dev/guides/gitops/. **https://docs.napwake.dev/guides/pause/**",
			want: []string{"/guides/gitops/", "/guides/pause/"},
		},
		{
			name: "a placeholder a program fills in",
			file: ".github/workflows/release.yml",
			data: `printf 'https://docs.napwake.dev/%s/\n' "$docs"`,
		},
		{
			name: "the chart's NOTES, for its version",
			file: "charts/napwake/templates/NOTES.txt",
			data: `{{- $docs := printf "https://docs.napwake.dev/%d.%d/" 0 3 -}}` + "\n" +
				`{{- if $chart.Prerelease }}{{ $docs = "https://docs.napwake.dev/next/" }}{{ end -}}` + "\n" +
				"instead: {{ $docs }}operations/upgrading/#keeping-your-settings\n{{ $docs }}\n",
			want: []string{"/next/", "/operations/upgrading/#keeping-your-settings", "/"},
		},
		{
			name: "a query, left out",
			file: "README.md",
			data: "https://docs.napwake.dev/guides/opt-in/?from=readme#with-kustomize",
			want: []string{"/guides/opt-in/#with-kustomize"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, docsAddresses(tt.file, tt.data))
		})
	}
}

func TestJoinedStrings(t *testing.T) {
	dir := t.TempDir()
	write := func(name, data string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644))
	}
	write("a.go", `package a

const guide = base + "guides/gitops/"

var anchors = map[int]string{1: "argo-cd", 2: "flux"}

func warn(tool int, name string) string {
	return "leave replicas alone: " + guide + "#" + anchors[tool] + " for " + name
}

func gap(name string) string { return fmt.Sprint(guide + name, guide+"#kapp") }
`)
	write("b.go", `package a

const base = "https://docs.napwake.dev/"
`)
	joined := joinedStrings(t, dir, []string{"a.go", "b.go"})
	addresses := make([]string, 0, len(joined["a.go"]))
	for _, s := range joined["a.go"] {
		addresses = append(addresses, docsAddresses("a.go", s)...)
	}
	assert.ElementsMatch(t, []string{
		"/guides/gitops/", "/guides/gitops/#argo-cd", "/guides/gitops/#flux", "/guides/gitops/", "/guides/gitops/#kapp",
	}, addresses)
}

func TestBuiltSiteResolve(t *testing.T) {
	dir := t.TempDir()
	write := func(name, data string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644))
	}
	write("index.html", `<h1 id="_top">Napwake</h1>`)
	write("guides/gitops/index.html", `<h2 id="argo-cd">Argo CD</h2><span class="x" id="it&#39;s">`)
	write("concepts/forecasting.md", "# Forecasting")
	site := &builtSite{dir: dir, ids: map[string]map[string]bool{}}

	for address, want := range map[string]string{
		"/":                          "",
		"/guides/gitops/":            "",
		"/guides/gitops":             "",
		"/guides/gitops/#argo-cd":    "",
		"/guides/gitops/#it's":       "",
		"/next/guides/gitops/":       "",
		"/0.3/guides/gitops/":        "",
		"/latest/":                   "",
		"/concepts/forecasting.md":   "",
		"/guides/gitops/#flux":       "an anchor its page doesn't have",
		"/guides/pause/":             "a page the built site doesn't have",
		"/nextgen/guides/gitops/":    "a page the built site doesn't have",
		"/concepts/forecasting.html": "a page the built site doesn't have",
	} {
		assert.Equal(t, want, site.resolve(address), address)
	}
}
