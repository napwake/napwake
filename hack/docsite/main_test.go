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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func published(version, release string, aliases ...string) deployed {
	d := deployed{Version: version, Aliases: aliases}
	d.Properties.Release = release
	return d
}

func TestPlanFor(t *testing.T) {
	tests := []struct {
		name string
		tag  string
		site []deployed
		want [][]string
		skip string
	}{
		{
			name: "the first release candidate, before any release",
			tag:  "v0.2.0-rc.4",
			want: [][]string{
				{"mike", "deploy", "--title", "0.2.0-rc.4", "--prop-set-string", "release=0.2.0-rc.4", "next"},
				{"mike", "set-default", "next"},
			},
		},
		{
			name: "the first release",
			tag:  "v0.2.0",
			site: []deployed{published("next", "0.2.0-rc.4")},
			want: [][]string{
				{"mike", "deploy", "--title", "0.2", "--prop-set-string", "release=0.2.0", "--update-aliases", "0.2", "latest"},
				{"mike", "delete", "next"},
				{"mike", "set-default", "latest"},
			},
		},
		{
			name: "a patch release",
			tag:  "v0.2.1",
			site: []deployed{published("0.2", "0.2.0", "latest")},
			want: [][]string{
				{"mike", "deploy", "--title", "0.2", "--prop-set-string", "release=0.2.1", "--update-aliases", "0.2", "latest"},
				{"mike", "set-default", "latest"},
			},
		},
		{
			name: "a release candidate for the next minor",
			tag:  "v0.3.0-rc.1",
			site: []deployed{published("0.2", "0.2.1", "latest")},
			want: [][]string{
				{"mike", "deploy", "--title", "0.3.0-rc.1", "--prop-set-string", "release=0.3.0-rc.1", "next"},
				{"mike", "set-default", "latest"},
			},
		},
		{
			name: "a later release candidate replaces next",
			tag:  "v0.3.0-rc.2",
			site: []deployed{published("next", "0.3.0-rc.1"), published("0.2", "0.2.1", "latest")},
			want: [][]string{
				{"mike", "deploy", "--title", "0.3.0-rc.2", "--prop-set-string", "release=0.3.0-rc.2", "next"},
				{"mike", "set-default", "latest"},
			},
		},
		{
			name: "a patch to an older minor keeps latest and next",
			tag:  "v0.2.2",
			site: []deployed{
				published("next", "0.4.0-rc.1"),
				published("0.3", "0.3.0", "latest"),
				published("0.2", "0.2.1"),
			},
			want: [][]string{
				{"mike", "deploy", "--title", "0.2", "--prop-set-string", "release=0.2.2", "0.2"},
				{"mike", "set-default", "latest"},
			},
		},
		{
			name: "the release a candidate rehearsed removes it",
			tag:  "v0.3.0",
			site: []deployed{published("next", "0.3.0-rc.2"), published("0.2", "0.2.1", "latest")},
			want: [][]string{
				{"mike", "deploy", "--title", "0.3", "--prop-set-string", "release=0.3.0", "--update-aliases", "0.3", "latest"},
				{"mike", "delete", "next"},
				{"mike", "set-default", "latest"},
			},
		},
		{
			name: "republishing a release",
			tag:  "v0.2.1",
			site: []deployed{published("0.2", "0.2.1", "latest")},
			want: [][]string{
				{"mike", "deploy", "--title", "0.2", "--prop-set-string", "release=0.2.1", "--update-aliases", "0.2", "latest"},
				{"mike", "set-default", "latest"},
			},
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
			if tt.skip == "" {
				assert.Equal(t, tt.want, p.commands())
			}
		})
	}
}

func TestPlanForRefusesWhatIsntATag(t *testing.T) {
	for _, tag := range []string{"", "0.2.0", "v0.2", "v0.2.0+build", "v0.2.0; rm -rf /", "main"} {
		_, err := planFor(tag, nil)
		assert.ErrorContains(t, err, "isn't a release tag", tag)
	}
}

func TestNotFoundPage(t *testing.T) {
	page, err := notFoundPage("/hybernate/", "latest")
	require.NoError(t, err)
	assert.Contains(t, string(page), `var base = "/hybernate/";`)
	assert.Contains(t, string(page), `var root = "latest";`)
	assert.Contains(t, string(page), `<a href="/hybernate/">`)
}
