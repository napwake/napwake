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

// Command releasenotes writes a release's notes to stdout, from its
// version's section of CHANGELOG.md. The release workflow drafts the GitHub
// release with them. A section that opens with a summary, text above its
// first ### heading, gives only the summary and a link to the whole
// section, so a long list of changes stays in the changelog. One without
// gives the whole section, cut at the last line that fits GitHub's limit.
// Either way the notes end with a link to the release's docs: its minor
// version's, or next's for a release candidate, whose notes are those of
// the version it rehearses.
//
//	go run ./hack/releasenotes -tag v0.2.0 -repo napwake/napwake > release-notes.md
package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode"
)

// maxSection is where a section without a summary is cut. GitHub refuses
// notes over 125,000 characters, and the links after it need the rest.
const maxSection = 120000

var (
	tagPattern  = regexp.MustCompile(`^v([0-9]+)\.([0-9]+)\.([0-9]+)(-[0-9A-Za-z.]+)?$`)
	repoPattern = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)
)

func main() {
	tag := flag.String("tag", "", "the release's tag, such as v0.2.0")
	repo := flag.String("repo", "napwake/napwake", "the GitHub repository the release is in")
	changelog := flag.String("changelog", "CHANGELOG.md", "the changelog to read")
	flag.Parse()

	data, err := os.ReadFile(*changelog)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if !repoPattern.MatchString(*repo) {
		fmt.Fprintf(os.Stderr, "%q isn't a GitHub repository, such as napwake/napwake\n", *repo)
		os.Exit(1)
	}
	out, err := notes(string(data), *tag, *repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Print(out)
}

// notes returns the release notes for tag, from changelog.
func notes(changelog, tag, repo string) (string, error) {
	m := tagPattern.FindStringSubmatch(tag)
	if m == nil {
		return "", fmt.Errorf("%q isn't a release's tag, such as v0.2.0 or v0.2.0-rc.1", tag)
	}
	version := m[1] + "." + m[2] + "." + m[3]
	heading, section := sectionOf(changelog, version)
	if strings.TrimSpace(section) == "" {
		return "", fmt.Errorf("CHANGELOG.md's section for %s is empty or missing", version)
	}

	var b strings.Builder
	if summary, ok := summaryOf(section); ok {
		fmt.Fprintf(&b, "%s\n\nAll of %s's changes are listed in [CHANGELOG.md](https://github.com/%s/blob/%s/CHANGELOG.md#%s).\n",
			summary, version, repo, tag, anchor(heading))
	} else if len(section) > maxSection {
		cut := section[:maxSection]
		b.WriteString(cut[:strings.LastIndex(cut, "\n")+1])
		fmt.Fprintf(&b, "\nThe list goes on in [CHANGELOG.md](https://github.com/%s/blob/%s/CHANGELOG.md).\n", repo, tag)
	} else {
		b.WriteString(section)
	}

	docs := m[1] + "." + m[2]
	if m[4] != "" {
		docs = "next"
	}
	fmt.Fprintf(&b, "\n**Docs for this release:** https://docs.napwake.dev/%s/\n", docs)
	return b.String(), nil
}

// sectionOf returns version's heading in changelog, without its "## ", and
// the lines under it, up to the next version's heading.
func sectionOf(changelog, version string) (heading, section string) {
	var b strings.Builder
	found := false
	for line := range strings.Lines(changelog) {
		if strings.HasPrefix(line, "## [") {
			if found {
				break
			}
			if strings.HasPrefix(line, "## ["+version+"]") {
				found = true
				heading = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			}
			continue
		}
		if found {
			b.WriteString(line)
		}
	}
	return heading, b.String()
}

// summaryOf returns the text of section above its first ### heading, and
// false if there's none, or no such heading.
func summaryOf(section string) (string, bool) {
	var b strings.Builder
	for line := range strings.Lines(section) {
		if strings.HasPrefix(line, "### ") {
			summary := strings.TrimSpace(b.String())
			return summary, summary != ""
		}
		b.WriteString(line)
	}
	return "", false
}

// anchor returns the anchor GitHub gives a heading: lower case, with
// spaces as hyphens, and everything but letters, digits, hyphens and
// underscores left out. "[0.2.0] - 2026-10-30" is 020---2026-10-30.
func anchor(heading string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == ' ':
			return '-'
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_':
			return unicode.ToLower(r)
		}
		return -1
	}, heading)
}
