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

// Command brewformula writes the Homebrew formula for a release of the
// kubectl plugin to stdout, from the release's macOS and Linux archives in
// a directory. The release workflow downloads them from the published
// release, so the checksums are those of the files brew downloads, and
// pushes the formula to the napwake/homebrew-tap repository.
//
//	go run ./hack/brewformula -tag v0.2.0 -repo napwake/napwake -dir release > kubectl-napwake.rb
package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

var (
	tagPattern  = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)
	repoPattern = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)
)

//go:embed formula.rb.tmpl
var formulaTemplate string

func main() {
	tag := flag.String("tag", "", "the release's tag, such as v0.2.0")
	repo := flag.String("repo", "napwake/napwake", "the GitHub repository the release is in")
	dir := flag.String("dir", ".", "the directory holding the release's plugin archives")
	flag.Parse()

	if err := run(os.Stdout, *tag, *repo, *dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// archive is where brew downloads one platform's plugin archive from, and
// the checksum it holds the download to.
type archive struct {
	URL    string
	SHA256 string
}

type formula struct {
	Version    string
	Homepage   string
	MacARM     archive
	MacIntel   archive
	LinuxARM   archive
	LinuxIntel archive
}

func run(out io.Writer, tag, repo, dir string) error {
	if !tagPattern.MatchString(tag) {
		return fmt.Errorf("%q isn't a release tag, such as v0.2.0", tag)
	}
	if !repoPattern.MatchString(repo) {
		return fmt.Errorf("%q isn't a GitHub repository, such as napwake/napwake", repo)
	}
	f := formula{
		Version:  strings.TrimPrefix(tag, "v"),
		Homepage: "https://github.com/" + repo,
	}
	for platform, a := range map[string]*archive{
		"darwin-arm64": &f.MacARM,
		"darwin-amd64": &f.MacIntel,
		"linux-arm64":  &f.LinuxARM,
		"linux-amd64":  &f.LinuxIntel,
	} {
		name := "kubectl-napwake-" + platform + ".tar.gz"
		sum, err := sha256File(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		*a = archive{
			URL:    fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, tag, name),
			SHA256: sum,
		}
	}

	t, err := template.New("formula").Parse(formulaTemplate)
	if err != nil {
		return fmt.Errorf("parsing the formula's template: %w", err)
	}
	if err := t.Execute(out, f); err != nil {
		return fmt.Errorf("writing the formula: %w", err)
	}
	return nil
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("reading the plugin archive: %w", err)
	}
	defer func() { _ = file.Close() }() // read-only, so closing can't lose anything

	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", fmt.Errorf("hashing %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
