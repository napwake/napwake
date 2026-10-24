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

// Command docsite publishes a release's docs into a checkout of the
// gh-pages branch, as one version of the versioned site. A release goes to
// its minor version, such as 0.2, and to latest, where the site's root
// sends readers, when it's the newest release. A release candidate goes to
// next, which the release it rehearses removes. Each folder is its own
// Astro build, since a build writes its base path into every link.
//
//	go run ./hack/docsite -tag v0.2.0 -site ../gh-pages -push
//
// versions.json, at the site's root, lists what's published, in the shape
// the docs' version switcher reads. The site's root and every page of the
// version it sends readers to also answer without a version, such as the
// /guides/cluster-autoscaler/ the plugin prints: each is a small page that
// sends the browser on, anchor kept. The 404 page sends anything else
// without a version to the same page in that version. Versions it isn't
// publishing are left as they are.
//
// It builds with npm in the docs' Astro project, whose dependencies must be
// installed (npm ci), and commits with git in the site's checkout. -dry-run
// says what a tag would publish instead.
package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

const (
	latest = "latest"
	next   = "next"

	// domain is where GitHub Pages serves the site. Its CNAME file, at the
	// root of gh-pages, holds it, and a publish without it would take the
	// site off the domain.
	domain = "docs.napwake.dev"

	// defaultBase is the path the site is served under: the root of the
	// domain.
	defaultBase = "/"

	versionsFile = "versions.json"

	// redirectMark names the pages docsite writes to send an address
	// without a version on, so a later publish can tell them from anything
	// else at the root and replace them.
	redirectMark = `<meta name="generator" content="hack/docsite">`
)

// Building every folder and committing the site takes a few minutes;
// anything near this means a build or git is stuck.
const timeout = 15 * time.Minute

var (
	tagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)

	// versionFolder is a folder at the site's root that holds a version of
	// the docs, published or not, which only a publish of that version
	// changes.
	versionFolder = regexp.MustCompile(`^(latest|next|[0-9]+\.[0-9]+)$`)
)

// rootFiles are the files every build writes that readers and crawlers
// look for at the site's root, copied there from the version the root
// sends readers to.
var rootFiles = []string{"robots.txt", "llms.txt", "llms-full.txt", "llms-small.txt"}

var (
	//go:embed notfound.html
	notFoundTemplate string
	//go:embed redirect.html
	redirectTemplate string

	notFound = template.Must(template.New("404.html").Parse(notFoundTemplate))
	redirect = template.Must(template.New("redirect.html").Parse(redirectTemplate))
)

func main() {
	var o options
	flag.StringVar(&o.tag, "tag", "", "the release tag whose docs to publish, such as v0.2.0 or v0.2.0-rc.1")
	flag.StringVar(&o.site, "site", "", "a checkout of the gh-pages branch to publish into")
	flag.StringVar(&o.docs, "docs", "docs", "the docs' Astro project, with its dependencies installed")
	flag.StringVar(&o.base, "base", defaultBase, "the path the site is served under")
	flag.BoolVar(&o.push, "push", false, "push gh-pages to origin once it's committed")
	flag.BoolVar(&o.dryRun, "dry-run", false, "say what the tag would publish, without building or changing anything")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := run(ctx, o); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type options struct {
	tag, site, docs, base string
	push, dryRun          bool
}

func run(ctx context.Context, o options) error {
	if o.site == "" {
		return errors.New("-site must name the checkout of gh-pages to publish into")
	}
	site, err := readVersions(o.site)
	if err != nil {
		return err
	}
	p, err := planFor(o.tag, site)
	if err != nil {
		return err
	}
	if p.skip != "" {
		fmt.Printf("Not publishing %s's docs: %s\n", o.tag, p.skip)
		return nil
	}
	fmt.Println(p.summary(o.tag))
	if o.dryRun {
		return nil
	}

	// Every folder is built before the site changes, so a build that fails
	// leaves it as it was.
	staging, err := os.MkdirTemp("", "docsite-")
	if err != nil {
		return fmt.Errorf("creating a directory to build in: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }() // best-effort cleanup of a temporary directory
	for _, folder := range p.folders() {
		if err := build(ctx, o.docs, filepath.Join(staging, folder), o.base, folder, p.release); err != nil {
			return err
		}
	}
	if err := publish(o.site, staging, o.base, p, site); err != nil {
		return err
	}
	if err := commit(ctx, o.site, p.summary(o.tag)); err != nil {
		return err
	}
	if o.push {
		return runCommand(ctx, "", nil, "git", "-C", o.site, "push", "origin", "HEAD:refs/heads/gh-pages")
	}
	return nil
}

// deployed is one version in versions.json, in the shape mike wrote, which
// the docs' version switcher reads.
type deployed struct {
	Version    string   `json:"version"`
	Title      string   `json:"title"`
	Aliases    []string `json:"aliases"`
	Properties struct {
		Release string `json:"release"`
	} `json:"properties"`
}

func readVersions(site string) ([]deployed, error) {
	data, err := os.ReadFile(filepath.Join(site, versionsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the published versions: %w", err)
	}
	var versions []deployed
	if err := json.Unmarshal(data, &versions); err != nil {
		return nil, fmt.Errorf("reading %s: %w", versionsFile, err)
	}
	return versions, nil
}

// plan is what publishing a tag's docs changes on gh-pages.
type plan struct {
	version string
	title   string
	aliases []string
	release string
	remove  []string
	// root is where the site's root, and links without a version, go.
	root string
	skip string
}

func planFor(tag string, site []deployed) (plan, error) {
	if !tagPattern.MatchString(tag) || !semver.IsValid(tag) {
		return plan{}, fmt.Errorf("%q isn't a release tag, such as v0.2.0 or v0.2.0-rc.1", tag)
	}
	release := strings.TrimPrefix(tag, "v")
	newest := releaseOf(site, latest)
	if semver.Prerelease(tag) != "" {
		return planCandidate(tag, release, site, newest), nil
	}
	return planRelease(tag, release, site, newest), nil
}

func planCandidate(tag, release string, site []deployed, newest string) plan {
	if newest != "" && semver.Compare(tag, "v"+newest) < 0 {
		return plan{skip: fmt.Sprintf("%s is already released, and next is for the release after the latest", newest)}
	}
	if current := releaseOf(site, next); current != "" && semver.Compare(tag, "v"+current) < 0 {
		return plan{skip: fmt.Sprintf("next has the docs of %s, a later release candidate", current)}
	}
	p := plan{version: next, title: release, release: release, root: latest}
	if newest == "" {
		p.root = next
	}
	return p
}

func planRelease(tag, release string, site []deployed, newest string) plan {
	minor := strings.TrimPrefix(semver.MajorMinor(tag), "v")
	if current := releaseOf(site, minor); current != "" && semver.Compare(tag, "v"+current) < 0 {
		return plan{skip: fmt.Sprintf("%s has the docs of %s, a later release", minor, current)}
	}
	p := plan{version: minor, title: minor, release: release, root: latest}
	if newest == "" || semver.Compare(tag, "v"+newest) >= 0 {
		p.aliases = []string{latest}
	}
	if candidate := releaseOf(site, next); candidate != "" && semver.Compare("v"+candidate, tag) <= 0 {
		p.remove = []string{next}
	}
	return p
}

// releaseOf returns the release whose docs a version or alias has, or ""
// when it isn't published.
func releaseOf(site []deployed, id string) string {
	for _, v := range site {
		if v.Version == id || slices.Contains(v.Aliases, id) {
			return v.Properties.Release
		}
	}
	return ""
}

// folders are the folders a plan builds: its version's, and each alias's,
// since a build's links name the folder it's served from.
func (p plan) folders() []string {
	return append([]string{p.version}, p.aliases...)
}

func (p plan) summary(tag string) string {
	var s strings.Builder
	fmt.Fprintf(&s, "Publish %s's docs to %s", tag, strings.Join(p.folders(), " and "))
	for _, v := range p.remove {
		fmt.Fprintf(&s, ", removing %s", v)
	}
	fmt.Fprintf(&s, ", with the site's root going to %s", p.root)
	return s.String()
}

// apply returns the published versions once the plan is: its version
// listed with its release, its aliases moved to it from any other, and
// what it removes gone. Newest first, next before every release, as the
// version switcher lists them.
func (p plan) apply(site []deployed) []deployed {
	out := make([]deployed, 0, len(site)+1)
	found := false
	for _, v := range site {
		if slices.Contains(p.remove, v.Version) {
			continue
		}
		v.Aliases = slices.DeleteFunc(slices.Clone(v.Aliases), func(a string) bool {
			return slices.Contains(p.aliases, a)
		})
		if v.Version == p.version {
			found = true
			v = p.update(v)
		}
		out = append(out, v)
	}
	if !found {
		out = append(out, p.update(deployed{Version: p.version}))
	}
	for i := range out {
		if out[i].Aliases == nil {
			out[i].Aliases = []string{}
		}
	}
	slices.SortStableFunc(out, func(a, b deployed) int { return versionOrder(b.Version) - versionOrder(a.Version) })
	return out
}

// update is a version's entry once the plan publishes it.
func (p plan) update(v deployed) deployed {
	v.Title = p.title
	v.Properties.Release = p.release
	for _, a := range p.aliases {
		if !slices.Contains(v.Aliases, a) {
			v.Aliases = append(v.Aliases, a)
		}
	}
	return v
}

// versionOrder ranks a version folder for the switcher: next first, then
// minors from the newest, then anything else.
func versionOrder(v string) int {
	if v == next {
		return 1 << 30
	}
	var major, minor int
	if !versionFolder.MatchString(v) {
		return -1
	}
	if _, err := fmt.Sscanf(v, "%d.%d", &major, &minor); err != nil {
		return -1
	}
	return major<<16 | minor
}

// build builds one folder of the site, under its own base path, into out.
func build(ctx context.Context, docs, out, base, folder, release string) error {
	abs, err := filepath.Abs(out)
	if err != nil {
		return fmt.Errorf("finding where to build %s: %w", folder, err)
	}
	env := append(os.Environ(),
		"NAPWAKE_DOCS_BASE="+base+folder+"/",
		"NAPWAKE_DOCS_VERSION="+folder,
		"NAPWAKE_DOCS_RELEASE="+release,
	)
	fmt.Printf("Building %s, %s's docs, at %s%s/\n", folder, release, base, folder)
	return runCommand(ctx, docs, env, "npm", "run", "build", "--", "--outDir", abs)
}

// publish puts a plan's built folders, in staging, into the site's
// checkout, removes the versions it replaces, and writes versions.json and
// the site's root.
func publish(site, staging, base string, p plan, published []deployed) error {
	for _, folder := range p.folders() {
		if err := os.RemoveAll(filepath.Join(site, folder)); err != nil {
			return fmt.Errorf("removing the published %s: %w", folder, err)
		}
		if err := copyDir(filepath.Join(staging, folder), filepath.Join(site, folder)); err != nil {
			return fmt.Errorf("publishing %s: %w", folder, err)
		}
	}
	for _, folder := range p.remove {
		if err := os.RemoveAll(filepath.Join(site, folder)); err != nil {
			return fmt.Errorf("removing %s: %w", folder, err)
		}
	}
	data, err := json.MarshalIndent(p.apply(published), "", "  ")
	if err != nil {
		return fmt.Errorf("writing %s: %w", versionsFile, err)
	}
	if err := os.WriteFile(filepath.Join(site, versionsFile), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", versionsFile, err)
	}
	return writeRoot(site, base, p.root)
}

// writeRoot writes what the site serves outside its versions: a page at
// every address the root version has a page at, sending the browser on to
// it, the 404 page, the root version's robots.txt and llms.txt files, the
// CNAME file that keeps the custom domain, and .nojekyll, without which
// GitHub Pages would leave out Astro's _astro folder.
func writeRoot(site, base, root string) error {
	if err := removeRedirects(site); err != nil {
		return err
	}
	from := filepath.Join(site, root)
	pages, err := pagesOf(from)
	if err != nil {
		return err
	}
	for _, page := range pages {
		if first, _, _ := strings.Cut(page, "/"); versionFolder.MatchString(first) {
			continue // a version's folder, never a redirect
		}
		data, err := redirectPage(base, root, page)
		if err != nil {
			return err
		}
		file := filepath.Join(site, filepath.FromSlash(page), "index.html")
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return fmt.Errorf("writing the redirect for /%s: %w", page, err)
		}
		if err := os.WriteFile(file, data, 0o644); err != nil {
			return fmt.Errorf("writing the redirect for /%s: %w", page, err)
		}
	}

	built, err := os.ReadFile(filepath.Join(from, "404.html"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("reading %s's 404 page: %w", root, err)
	}
	page, err := notFoundPage(base, root, built)
	if err != nil {
		return err
	}
	files := map[string][]byte{"404.html": page, "CNAME": []byte(domain + "\n"), ".nojekyll": nil}
	for _, name := range rootFiles {
		data, err := os.ReadFile(filepath.Join(from, name))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return fmt.Errorf("reading %s's %s: %w", root, name, err)
		}
		files[name] = data
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(site, name), data, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", name, err)
		}
	}
	return nil
}

// pagesOf lists the pages a built version has, as paths from its root
// without slashes at either end: "" for its home, "guides/gitops" for
// /guides/gitops/. A version that isn't there has none.
func pagesOf(dir string) ([]string, error) {
	var pages []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "index.html" {
			return nil
		}
		rel, err := filepath.Rel(dir, filepath.Dir(p))
		if err != nil {
			return err
		}
		pages = append(pages, strings.TrimPrefix(filepath.ToSlash(rel), "."))
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("listing the pages of %s: %w", dir, err)
	}
	if !slices.Contains(pages, "") {
		pages = append(pages, "")
	}
	return pages, nil
}

// removeRedirects removes the redirect pages an earlier publish wrote, so
// a page the root version no longer has stops answering: every folder at
// the site's root that isn't a version's and holds nothing but them.
func removeRedirects(site string) error {
	entries, err := os.ReadDir(site)
	if err != nil {
		return fmt.Errorf("reading the site: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == ".git" || versionFolder.MatchString(e.Name()) {
			continue
		}
		dir := filepath.Join(site, e.Name())
		only, err := onlyRedirects(dir)
		if err != nil {
			return err
		}
		if !only {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("removing the redirects in %s: %w", e.Name(), err)
		}
	}
	return nil
}

func onlyRedirects(dir string) (bool, error) {
	only := true
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if d.Name() != "index.html" || !bytes.Contains(data, []byte(redirectMark)) {
			only = false
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", dir, err)
	}
	return only, nil
}

type redirectData struct {
	Mark      template.HTML
	Target    string
	Canonical string
}

// redirectPage is the page at an address without a version, such as
// /guides/gitops/, that sends the browser to the same page in the root
// version, with its anchor and query. GitHub Pages can't redirect, so it's
// a script, with a meta refresh, which drops the anchor, for a browser
// without scripts. It names the page it goes to as canonical, and isn't
// indexed itself.
func redirectPage(base, root, page string) ([]byte, error) {
	target := path.Join(base, root, page) + "/"
	var b bytes.Buffer
	err := redirect.Execute(&b, redirectData{
		Mark:      template.HTML(redirectMark),
		Target:    target,
		Canonical: "https://" + domain + target,
	})
	if err != nil {
		return nil, fmt.Errorf("rendering the redirect to %s: %w", target, err)
	}
	return b.Bytes(), nil
}

// notFoundPage is the site's 404 page: the root version's own, the
// designed one, with the script that sends links without a version on, or
// a plain page with the script when the root version has none.
func notFoundPage(base, root string, built []byte) ([]byte, error) {
	data := struct{ Base, Root string }{base, root}
	var b bytes.Buffer
	head := bytes.Index(built, []byte("</head>"))
	if head < 0 {
		if err := notFound.Execute(&b, data); err != nil {
			return nil, fmt.Errorf("rendering the 404 page: %w", err)
		}
		return b.Bytes(), nil
	}
	if err := notFound.ExecuteTemplate(&b, "forward", data); err != nil {
		return nil, fmt.Errorf("rendering the 404 page's script: %w", err)
	}
	return slices.Concat(built[:head], b.Bytes(), built[head:]), nil
}

// commit commits the site's checkout, unless the publish changed nothing.
func commit(ctx context.Context, site, message string) error {
	if err := runCommand(ctx, "", nil, "git", "-C", site, "add", "--all"); err != nil {
		return err
	}
	// git diff --quiet exits 1 when there's a difference.
	err := exec.CommandContext(ctx, "git", "-C", site, "diff", "--cached", "--quiet").Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		fmt.Println("Nothing changed on gh-pages")
		return nil
	case !errors.As(err, &exitErr) || exitErr.ExitCode() != 1:
		return fmt.Errorf("checking what the publish changed: %w", err)
	}
	return runCommand(ctx, "", nil, "git", "-C", site, "commit", "--quiet", "-m", message)
}

func copyDir(from, to string) error {
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		return copyFile(p, dst)
	})
}

func copyFile(from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }() // read-only, nothing to report
	dst, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close() // the copy's error is the one to report
		return err
	}
	return dst.Close()
}

func runCommand(ctx context.Context, dir string, env []string, args ...string) error {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running %s: %w", strings.Join(args, " "), err)
	}
	return nil
}
