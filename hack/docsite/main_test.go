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
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func published(version, release string, aliases ...string) deployed {
	d := deployed{Version: version, Title: version, Aliases: aliases}
	if version == next {
		d.Title = release
	}
	if d.Aliases == nil {
		d.Aliases = []string{}
	}
	d.Properties.Release = release
	return d
}

func TestPlanFor(t *testing.T) {
	type want struct {
		folders, remove []string
		title, release  string
		root            string
	}
	tests := []struct {
		name string
		tag  string
		site []deployed
		want want
		skip string
	}{
		{
			name: "the first release candidate, before any release",
			tag:  "v0.2.0-rc.4",
			want: want{folders: []string{"next"}, title: "0.2.0-rc.4", release: "0.2.0-rc.4", root: "next"},
		},
		{
			name: "the first release",
			tag:  "v0.2.0",
			site: []deployed{published("next", "0.2.0-rc.4")},
			want: want{folders: []string{"0.2", "latest"}, remove: []string{"next"}, title: "0.2", release: "0.2.0",
				root: "latest"},
		},
		{
			name: "a patch release",
			tag:  "v0.2.1",
			site: []deployed{published("0.2", "0.2.0", "latest")},
			want: want{folders: []string{"0.2", "latest"}, title: "0.2", release: "0.2.1", root: "latest"},
		},
		{
			name: "a release candidate for the next minor",
			tag:  "v0.3.0-rc.1",
			site: []deployed{published("0.2", "0.2.1", "latest")},
			want: want{folders: []string{"next"}, title: "0.3.0-rc.1", release: "0.3.0-rc.1", root: "latest"},
		},
		{
			name: "a later release candidate replaces next",
			tag:  "v0.3.0-rc.2",
			site: []deployed{published("next", "0.3.0-rc.1"), published("0.2", "0.2.1", "latest")},
			want: want{folders: []string{"next"}, title: "0.3.0-rc.2", release: "0.3.0-rc.2", root: "latest"},
		},
		{
			name: "a patch to an older minor keeps latest and next",
			tag:  "v0.2.2",
			site: []deployed{
				published("next", "0.4.0-rc.1"),
				published("0.3", "0.3.0", "latest"),
				published("0.2", "0.2.1"),
			},
			want: want{folders: []string{"0.2"}, title: "0.2", release: "0.2.2", root: "latest"},
		},
		{
			name: "the release a candidate rehearsed removes it",
			tag:  "v0.3.0",
			site: []deployed{published("next", "0.3.0-rc.2"), published("0.2", "0.2.1", "latest")},
			want: want{folders: []string{"0.3", "latest"}, remove: []string{"next"}, title: "0.3", release: "0.3.0",
				root: "latest"},
		},
		{
			name: "republishing a release",
			tag:  "v0.2.1",
			site: []deployed{published("0.2", "0.2.1", "latest")},
			want: want{folders: []string{"0.2", "latest"}, title: "0.2", release: "0.2.1", root: "latest"},
		},
		{
			name: "an earlier patch than the minor has",
			tag:  "v0.2.0",
			site: []deployed{published("0.2", "0.2.1", "latest")},
			skip: "0.2 has the docs of 0.2.1, a later release",
		},
		{
			name: "an earlier release candidate than next has",
			tag:  "v0.3.0-rc.1",
			site: []deployed{published("next", "0.3.0-rc.2"), published("0.2", "0.2.1", "latest")},
			skip: "next has the docs of 0.3.0-rc.2, a later release candidate",
		},
		{
			name: "a release candidate of a release already out",
			tag:  "v0.2.0-rc.4",
			site: []deployed{published("0.2", "0.2.0", "latest")},
			skip: "0.2.0 is already released, and next is for the release after the latest",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := planFor(tt.tag, tt.site)
			require.NoError(t, err)
			assert.Equal(t, tt.skip, p.skip)
			if tt.skip != "" {
				return
			}
			got := want{folders: p.folders(), remove: p.remove, title: p.title, release: p.release, root: p.root}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPlanForRefusesWhatIsntATag(t *testing.T) {
	for _, tag := range []string{"", "0.2.0", "v0.2", "v0.2.0+build", "v0.2.0; rm -rf /", "main"} {
		_, err := planFor(tag, nil)
		assert.ErrorContains(t, err, "isn't a release tag", tag)
	}
}

// versions.json after each publish is what mike would have written: a
// version per minor and next, each with its release, latest on the newest
// release only, and next gone once its release is out.
func TestPlanApply(t *testing.T) {
	var site []deployed
	steps := []struct {
		tag  string
		want []deployed
	}{
		{"v0.2.0-rc.8", []deployed{published("next", "0.2.0-rc.8")}},
		{"v0.2.0", []deployed{published("0.2", "0.2.0", "latest")}},
		{"v0.3.0-rc.1", []deployed{published("next", "0.3.0-rc.1"), published("0.2", "0.2.0", "latest")}},
		{"v0.2.1", []deployed{published("next", "0.3.0-rc.1"), published("0.2", "0.2.1", "latest")}},
		{"v0.3.0", []deployed{published("0.3", "0.3.0", "latest"), published("0.2", "0.2.1")}},
		{"v0.2.2", []deployed{published("0.3", "0.3.0", "latest"), published("0.2", "0.2.2")}},
		{"v0.10.0", []deployed{published("0.10", "0.10.0", "latest"), published("0.3", "0.3.0"), published("0.2", "0.2.2")}},
	}
	for _, step := range steps {
		p, err := planFor(step.tag, site)
		require.NoError(t, err)
		require.Empty(t, p.skip, step.tag)
		site = p.apply(site)
		assert.Equal(t, step.want, site, step.tag)
	}

	data, err := json.Marshal(site[1:])
	require.NoError(t, err)
	assert.JSONEq(t, `[{"version": "0.3", "title": "0.3", "aliases": [], "properties": {"release": "0.3.0"}},
		{"version": "0.2", "title": "0.2", "aliases": [], "properties": {"release": "0.2.2"}}]`, string(data),
		"a version without aliases has an empty list, as the switcher reads it")
}

// fakeBuild writes what a docs build of folder writes that publishing
// reads: pages, the 404 page, and the files the root gets a copy of.
func fakeBuild(t *testing.T, staging, folder, release string) {
	t.Helper()
	for name, data := range map[string]string{
		"index.html":                      "home of " + release,
		"guides/gitops/index.html":        "gitops of " + release,
		"concepts/forecasting/index.html": "forecasting of " + release,
		"concepts/forecasting.md":         "# Forecasting",
		"_astro/page.css":                 "body{}",
		"404.html":                        "<html><head><title>No room by that name</title></head><body></body></html>",
		"robots.txt":                      "robots of " + release,
		"llms.txt":                        "llms of " + release,
	} {
		write(t, filepath.Join(staging, folder, name), data)
	}
}

func write(t *testing.T, file, data string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
	require.NoError(t, os.WriteFile(file, []byte(data), 0o644))
}

func read(t *testing.T, file string) string {
	t.Helper()
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	return string(data)
}

// A release candidate, then a release, then the next release candidate,
// onto a site with an older version and files of its own: each publishes
// its folders, keeps the custom domain, answers at the root and at every
// page without a version, and leaves the rest alone.
func TestPublish(t *testing.T) {
	site := t.TempDir()
	write(t, filepath.Join(site, "0.1", "index.html"), "0.1's docs, built with MkDocs")
	write(t, filepath.Join(site, "next", "requirements.txt"), "mkdocs, from an earlier publish")
	write(t, filepath.Join(site, "google1234.html"), "a site verification")
	write(t, filepath.Join(site, "keep", "notes.txt"), "not a redirect")
	old, err := redirectPage("/", "next", "guides/gone")
	require.NoError(t, err)
	write(t, filepath.Join(site, "guides", "gone", "index.html"), string(old))
	versions := []deployed{published("0.1", "0.1.3")}
	data, err := json.Marshal(versions)
	require.NoError(t, err)
	write(t, filepath.Join(site, versionsFile), string(data))

	for _, step := range []struct {
		tag, root string
		folders   []string
		gone      []string
	}{
		{tag: "v0.2.0-rc.8", root: "next", folders: []string{"next"}, gone: []string{"next/requirements.txt", "guides/gone"}},
		{tag: "v0.2.0", root: "latest", folders: []string{"0.2", "latest"}, gone: []string{"next"}},
		{tag: "v0.3.0-rc.1", root: "latest", folders: []string{"next"}},
	} {
		t.Run(step.tag, func(t *testing.T) {
			published, err := readVersions(site)
			require.NoError(t, err)
			p, err := planFor(step.tag, published)
			require.NoError(t, err)
			require.Equal(t, step.folders, p.folders())
			staging := t.TempDir()
			for _, folder := range p.folders() {
				fakeBuild(t, staging, folder, p.release)
			}

			require.NoError(t, publish(site, staging, "/", p, published))

			for _, folder := range p.folders() {
				assert.Equal(t, "gitops of "+p.release, read(t, filepath.Join(site, folder, "guides", "gitops", "index.html")))
				assert.FileExists(t, filepath.Join(site, folder, "_astro", "page.css"))
			}
			for _, gone := range step.gone {
				assert.NoFileExists(t, filepath.Join(site, gone))
				assert.NoDirExists(t, filepath.Join(site, gone))
			}
			assert.Equal(t, "0.1's docs, built with MkDocs", read(t, filepath.Join(site, "0.1", "index.html")))
			assert.Equal(t, "a site verification", read(t, filepath.Join(site, "google1234.html")))
			assert.Equal(t, "not a redirect", read(t, filepath.Join(site, "keep", "notes.txt")))
			assert.Equal(t, "docs.napwake.dev\n", read(t, filepath.Join(site, "CNAME")))
			assert.FileExists(t, filepath.Join(site, ".nojekyll"))

			for page, target := range map[string]string{
				"":                     "/" + step.root + "/",
				"guides/gitops":        "/" + step.root + "/guides/gitops/",
				"concepts/forecasting": "/" + step.root + "/concepts/forecasting/",
			} {
				redirect := read(t, filepath.Join(site, page, "index.html"))
				assert.Contains(t, redirect, `<meta http-equiv="refresh" content="0; url=`+target+`">`, page)
				assert.Contains(t, redirect, `<link rel="canonical" href="https://docs.napwake.dev`+target+`">`, page)
			}
			assert.NoFileExists(t, filepath.Join(site, "concepts", "forecasting.md"), "only pages answer without a version")
			assert.NoDirExists(t, filepath.Join(site, "_astro"))

			notFound := read(t, filepath.Join(site, "404.html"))
			assert.Contains(t, notFound, "<title>No room by that name</title>", "the root version's own 404 page")
			assert.Contains(t, notFound, `var root = "`+step.root+`";`)
			assert.Less(t, strings.Index(notFound, "var root"), strings.Index(notFound, "</head>"))
			assert.Equal(t, "robots of "+releaseOf(p.apply(published), step.root), read(t, filepath.Join(site, "robots.txt")))
			assert.FileExists(t, filepath.Join(site, "llms.txt"))
		})
	}

	got, err := readVersions(site)
	require.NoError(t, err)
	assert.Equal(t, []deployed{
		published("next", "0.3.0-rc.1"), published("0.2", "0.2.0", "latest"), published("0.1", "0.1.3"),
	}, got)
}

// The page at an address without a version sends the browser on with the
// address's anchor, which a meta refresh would drop, and its query. The
// meta refresh is there for a browser without scripts.
func TestRedirectPageKeepsTheAnchor(t *testing.T) {
	page, err := redirectPage("/", "latest", "concepts/forecasting")
	require.NoError(t, err)
	assertRedirects(t, string(page), "/latest/concepts/forecasting/")
}

// The same, for the redirects written from a real build of the docs, which
// the docs workflow makes before it runs this: the site's root and
// /concepts/forecasting/, which the website links to with an anchor.
func TestBuiltSiteRedirectsKeepTheAnchor(t *testing.T) {
	dist, set := os.LookupEnv("NAPWAKE_DOCS_DIST")
	if !set {
		dist = filepath.Join("..", "..", "docs", "dist")
	}
	if _, err := os.Stat(filepath.Join(dist, "concepts", "forecasting", "index.html")); err != nil {
		if set {
			t.Fatalf("NAPWAKE_DOCS_DIST=%s has no built docs site: %v", dist, err)
		}
		t.Skipf("no built docs site in %s: run npm run build in docs/, or set NAPWAKE_DOCS_DIST", dist)
	}
	staging := t.TempDir()
	for _, folder := range []string{"0.2", "latest"} {
		require.NoError(t, copyDir(dist, filepath.Join(staging, folder)))
	}
	site := t.TempDir()
	p, err := planFor("v0.2.0", nil)
	require.NoError(t, err)

	require.NoError(t, publish(site, staging, "/", p, nil))

	forecasting := read(t, filepath.Join(site, "concepts", "forecasting", "index.html"))
	assertRedirects(t, forecasting, "/latest/concepts/forecasting/")
	assertRedirects(t, read(t, filepath.Join(site, "index.html")), "/latest/")
	assert.Contains(t, read(t, filepath.Join(site, "404.html")), "No room by that name", "the docs' own 404 page")
}

// assertRedirects checks a redirect page sends a browser to target with the
// anchor it was opened with, by running its script where node is
// installed, and otherwise by reading it.
func assertRedirects(t *testing.T, page, target string) {
	t.Helper()
	assert.Contains(t, page, `<meta name="robots" content="noindex">`)
	assert.Contains(t, page, `<link rel="canonical" href="https://docs.napwake.dev`+target+`">`)
	assert.Contains(t, page, `<noscript><meta http-equiv="refresh" content="0; url=`+target+`"></noscript>`)
	scripts := regexp.MustCompile(`<script>(.*)</script>`).FindAllStringSubmatch(page, -1)
	require.Len(t, scripts, 1, "one line of script")
	assert.Contains(t, scripts[0][1], "location.hash")

	node, err := exec.LookPath("node")
	if err != nil {
		t.Log("node isn't installed, so the script isn't run")
		return
	}
	run := `var location = {search: "?q=1", hash: "#when-it-acts", replace: function (u) { console.log(u); }};` +
		scripts[0][1]
	out, err := exec.Command(node, "-e", run).CombinedOutput()
	require.NoError(t, err, "%s", out)
	assert.Equal(t, target+"?q=1#when-it-acts", strings.TrimSpace(string(out)))
}

func TestNotFoundPage(t *testing.T) {
	page, err := notFoundPage("/", "latest", nil)
	require.NoError(t, err)
	assert.Contains(t, string(page), `var base = "/";`)
	assert.Contains(t, string(page), `var root = "latest";`)
	assert.Contains(t, string(page), `<a href="/">`)

	page, err = notFoundPage("/", "next", []byte("<html><head><title>No room</title></head><body></body></html>"))
	require.NoError(t, err)
	assert.Regexp(t,
		`^<html><head><title>No room</title><script>(?s:.*)var root = "next";(?s:.*)</script>\s*</head><body></body></html>$`,
		string(page))
}

// Links without a version are sent on only under the path the site is
// served from, which is the docs' default base, in docs/astro.config.mjs,
// and the CNAME file names the domain the docs are built for.
func TestDefaultsAreWhereTheSiteIsServed(t *testing.T) {
	config, err := os.ReadFile(filepath.Join("..", "..", "docs", "astro.config.mjs"))
	require.NoError(t, err)
	m := regexp.MustCompile(`env\('NAPWAKE_DOCS_BASE', '([^']*)'\)`).FindSubmatch(config)
	require.NotNil(t, m, "astro.config.mjs has a default base")
	assert.Equal(t, defaultBase, string(m[1]))

	m = regexp.MustCompile(`env\('NAPWAKE_DOCS_SITE', '([^']*)'\)`).FindSubmatch(config)
	require.NotNil(t, m, "astro.config.mjs has a default site")
	site, err := url.Parse(string(m[1]))
	require.NoError(t, err)
	assert.Equal(t, domain, site.Host)
}

// Each build names the version search engines should send readers to:
// latest once a stable release is out, and until then the version itself,
// since latest doesn't exist yet. Once latest does, next isn't indexed, so
// searches find the release, not a candidate.
func TestBuildEnv_NamesTheVersionThatExists(t *testing.T) {
	tests := []struct {
		name, folder, root string
		want, notWant      []string
	}{
		{name: "a candidate before any release", folder: "next", root: "next",
			want: []string{"NAPWAKE_DOCS_CANONICAL=next"}, notWant: []string{"NAPWAKE_DOCS_NOINDEX=true"}},
		{name: "a candidate after a release", folder: "next", root: "latest",
			want: []string{"NAPWAKE_DOCS_CANONICAL=latest", "NAPWAKE_DOCS_NOINDEX=true"}},
		{name: "a release's minor", folder: "0.2", root: "latest",
			want: []string{"NAPWAKE_DOCS_CANONICAL=latest"}, notWant: []string{"NAPWAKE_DOCS_NOINDEX=true"}},
		{name: "latest", folder: "latest", root: "latest",
			want: []string{"NAPWAKE_DOCS_CANONICAL=latest"}, notWant: []string{"NAPWAKE_DOCS_NOINDEX=true"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := buildEnv("/", tt.folder, "0.2.0", tt.root)
			for _, w := range tt.want {
				assert.Contains(t, env, w)
			}
			for _, w := range tt.notWant {
				assert.NotContains(t, env, w)
			}
			assert.Contains(t, env, "NAPWAKE_DOCS_BASE=/"+tt.folder+"/")
		})
	}
}
