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

// Command docsite publishes a release's docs to the gh-pages branch with
// mike, as one version of the versioned site. A release goes to its minor
// version, such as 0.2, and moves the latest alias, where the site's root
// sends readers, when it's the newest release. A release candidate goes to
// next, which the release it rehearses removes. Links without a version,
// such as those the operator and the plugin print, are sent to the same
// page in latest by the 404 page it writes at the branch's root.
//
//	go run ./hack/docsite -tag v0.2.0 -push
//
// It runs mike and git in the current directory, the docs' checkout, so
// mkdocs and mike, which docs/requirements.txt installs, must be on the
// PATH. -dry-run prints what it would run instead.
package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"text/template"
	"time"

	"golang.org/x/mod/semver"
)

const (
	branch = "gh-pages"
	latest = "latest"
	next   = "next"

	// releaseEnv names the release being built, which mkdocs.yml reads
	// into extra.release for the banner on pages that aren't latest's.
	releaseEnv = "NAPWAKE_DOCS_RELEASE"

	// defaultBase is the path the site is served under: the root of
	// docs.napwake.dev.
	defaultBase = "/"
)

// Building and committing the docs takes a minute or two; anything near
// this means mkdocs or git is stuck.
const timeout = 15 * time.Minute

var tagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)

//go:embed notfound.html
var notFoundTemplate string

func main() {
	tag := flag.String("tag", "", "the release tag whose docs to publish, such as v0.2.0 or v0.2.0-rc.1")
	push := flag.Bool("push", false, "push gh-pages to origin once it's committed")
	dryRun := flag.Bool("dry-run", false, "print the commands instead of running them")
	base := flag.String("base", defaultBase, "the path the site is served under")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := run(ctx, *tag, *base, *push, *dryRun); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, tag, base string, push, dryRun bool) error {
	out, err := exec.CommandContext(ctx, "mike", "list", "--json").Output()
	if err != nil {
		return fmt.Errorf("listing the published versions: %w%s", err, stderrOf(err))
	}
	var site []deployed
	if err := json.Unmarshal(out, &site); err != nil {
		return fmt.Errorf("reading mike list's versions: %w", err)
	}
	p, err := planFor(tag, site)
	if err != nil {
		return err
	}
	if p.skip != "" {
		fmt.Printf("Not publishing %s's docs: %s\n", tag, p.skip)
		return nil
	}
	page, err := notFoundPage(base, p.root)
	if err != nil {
		return err
	}

	env := append(os.Environ(), releaseEnv+"="+p.release)
	if dryRun {
		fmt.Printf("export %s=%s\n", releaseEnv, p.release)
	}
	for _, args := range p.commands() {
		if err := runCommand(ctx, env, dryRun, args...); err != nil {
			return err
		}
	}
	if err := commitNotFoundPage(ctx, page, p.root, dryRun); err != nil {
		return err
	}
	if push {
		return runCommand(ctx, env, dryRun, "git", "push", "origin", branch)
	}
	return nil
}

// deployed is one version in mike's versions.json.
type deployed struct {
	Version    string   `json:"version"`
	Aliases    []string `json:"aliases"`
	Properties struct {
		Release string `json:"release"`
	} `json:"properties"`
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

func (p plan) commands() [][]string {
	deploy := []string{"mike", "deploy", "--title", p.title, "--prop-set-string", "release=" + p.release}
	if len(p.aliases) > 0 {
		deploy = append(deploy, "--update-aliases")
	}
	deploy = append(deploy, p.version)
	deploy = append(deploy, p.aliases...)

	cmds := make([][]string, 0, len(p.remove)+2)
	cmds = append(cmds, deploy)
	for _, v := range p.remove {
		cmds = append(cmds, []string{"mike", "delete", v})
	}
	return append(cmds, []string{"mike", "set-default", p.root})
}

func notFoundPage(base, root string) ([]byte, error) {
	t, err := template.New("404.html").Parse(notFoundTemplate)
	if err != nil {
		return nil, fmt.Errorf("parsing the 404 page: %w", err)
	}
	var b bytes.Buffer
	if err := t.Execute(&b, struct{ Base, Root string }{base, root}); err != nil {
		return nil, fmt.Errorf("rendering the 404 page: %w", err)
	}
	return b.Bytes(), nil
}

// commitNotFoundPage commits the 404 page to gh-pages, beside the versions
// mike manages, which leaves it alone. GitHub Pages serves it for every
// path the site doesn't have.
func commitNotFoundPage(ctx context.Context, page []byte, root string, dryRun bool) error {
	if dryRun {
		fmt.Printf("# commit 404.html to the root of %s, sending links without a version to %s\n", branch, root)
		return nil
	}
	dir, err := os.MkdirTemp("", "gh-pages-")
	if err != nil {
		return fmt.Errorf("creating a directory for %s: %w", branch, err)
	}
	defer func() { _ = os.RemoveAll(dir) }() // best-effort cleanup of a temporary directory

	if err := runCommand(ctx, nil, false, "git", "worktree", "add", "--quiet", dir, branch); err != nil {
		return err
	}
	defer func() {
		_ = exec.CommandContext(ctx, "git", "worktree", "remove", "--force", dir).Run() // the directory is removed anyway
	}()

	if err := os.WriteFile(filepath.Join(dir, "404.html"), page, 0o644); err != nil {
		return fmt.Errorf("writing the 404 page: %w", err)
	}
	if err := runCommand(ctx, nil, false, "git", "-C", dir, "add", "404.html"); err != nil {
		return err
	}
	// git diff --quiet exits 1 when there's a difference.
	err = exec.CommandContext(ctx, "git", "-C", dir, "diff", "--cached", "--quiet").Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return nil
	case !errors.As(err, &exitErr) || exitErr.ExitCode() != 1:
		return fmt.Errorf("checking whether the 404 page changed: %w", err)
	}
	return runCommand(ctx, nil, false, "git", "-C", dir, "commit", "--quiet", "-m",
		"Send links without a version to "+root)
}

func runCommand(ctx context.Context, env []string, dryRun bool, args ...string) error {
	if dryRun {
		fmt.Println(strings.Join(args, " "))
		return nil
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func stderrOf(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
		return ": " + strings.TrimSpace(string(exitErr.Stderr))
	}
	return ""
}
