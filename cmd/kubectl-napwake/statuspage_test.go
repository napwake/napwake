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
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

func renderStatusPage(t *testing.T, objs ...client.Object) string {
	t.Helper()
	result, err := clusterStatus(context.Background(), newStatusClient(t, interceptor.Funcs{}, objs...), "preview-42",
		statusOpts())
	require.NoError(t, err)
	result.Cluster = testCluster
	var out bytes.Buffer
	require.NoError(t, writeStatusHTML(&out, result, 24*time.Hour, statusNow))
	return out.String()
}

// The page says what the terminal does, from the same status: the
// overview, freed and saved, dry-run's estimate, what needs attention, each
// workload, and recent pauses and wakes.
func TestWriteStatusHTML(t *testing.T) {
	got := renderStatusPage(t, statusCluster()...)

	assert.Contains(t, got, "<title>Napwake status: staging (EKS us-east-1)</title>")
	assert.Contains(t, got, "<span>staging (EKS us-east-1) &middot; 5 October 2026, 12:00 UTC</span>")
	assert.Contains(t, got, `<p class="lede">Napwake manages 4 workloads: 1 paused, 2 running, 1 resuming; 1 in `+
		`dry-run, never paused: pausing it would have freed $7 since Oct 1.</p>`)
	assert.Contains(t, got, `<p class="prices">won&#39;t drop until something removes nodes`)
	assert.Contains(t, got, `<p class="prices">Forecast time zone: Europe/London</p>`)
	assert.Contains(t, got, `<div class="figure"><b>$42</b><span>freed by pausing this month</span>`)
	assert.Contains(t, got, `<div class="figure"><b>$35</b><span>saved of it</span>`)
	assert.Contains(t, got, `<div class="figure"><b>$7</b><span>pausing would have freed</span>`+
		`<small>measured in dry-run since Oct 1, for 1 workload</small></div>`)
	assert.Contains(t, got, "<h2>Needs attention</h2>")
	assert.Contains(t, got, `<td class="ref">worker<span class="sub">preview-7</span></td>
        <td class="ref">GitOpsConflict</td>
        <td class="message">Argo CD set the replicas from Git</td>`)
	assert.Contains(t, got, `<td class="message">resuming for 25m: waiting for preview-42/postgres</td>`)
	assert.Contains(t, got, `<td class="ref">deployment/api<span class="sub">preview-42</span></td>`)
	assert.Contains(t, got,
		`<span class="state paused"><span class="dot"></span>paused</span><span class="sub">for 3h</span>`)
	assert.Contains(t, got, `<td>held awake while preview-7/worker uses it</td>`)
	assert.Contains(t, got, `$12<span class="sub">$10 saved</span>`)
	assert.Contains(t, got, `<span class="state active"><span class="dot"></span>running (dry-run)</span>`)
	assert.Contains(t, got, `<button type="button">Would free</button>`)
	assert.Contains(t, got, `<td class="n" data-v="7.4">$7</td>`)
	assert.Contains(t, got, `<td class="ref" data-v="1791190800">Oct 5, 09:00 UTC<span class="sub">3h ago</span></td>
        <td class="ref">api<span class="sub">preview-42</span></td>
        <td class="message">paused</td>`)
	assert.Contains(t, got, "over the last 24h")
	assert.NotContains(t, got, "<script src", "nothing loads from elsewhere")
	assert.NotContains(t, got, "<link", "nothing loads from elsewhere")
}

// Messages come from conditions and events that anyone who can write a
// ManagedWorkload or an event can set, so they're escaped, never markup.
func TestWriteStatusHTML_Escapes(t *testing.T) {
	w := statusWorkloadObj("shop", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning, time.Hour)
	w.Status.Conditions = []metav1.Condition{condition("GitOpsConflict", metav1.ConditionTrue, "PauseUndone",
		`<script>alert("x")</script>`)}

	got := renderStatusPage(t, w, statusEvent("shop", "api", "Paused", `api: <img src=x onerror=alert(1)>`, time.Minute))

	assert.NotContains(t, got, `<script>alert`)
	assert.NotContains(t, got, `<img src=x`)
	assert.Contains(t, got, `&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;`)
	assert.Contains(t, got, `&lt;img src=x onerror=alert(1)&gt;`)
}

func TestWriteStatusHTML_NothingManaged(t *testing.T) {
	got := renderStatusPage(t)

	assert.Contains(t, got, "Napwake manages no workloads here.")
	assert.NotContains(t, got, "<h2>Workloads</h2>")
	assert.NotContains(t, got, "Would free")
	assert.Contains(t, got, `<p class="empty">No pauses or wakes in this period.</p>`)
}

// --html writes the page as well as the table: given a file, there, with
// or without "=", and given none, to a new temporary file. In a terminal it
// opens it, unless --open=false; elsewhere it says where it is. Without
// --html, status takes no arguments.
func TestStatusCLI_HTML(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		terminal   bool
		wantFile   string
		wantOpened bool
		wantErr    string
	}{
		{name: "in a terminal, opened from a temporary file", args: []string{"--html"}, terminal: true,
			wantOpened: true},
		{name: "a temporary file, piped", args: []string{"--html"}},
		{name: "a file after a space", args: []string{"--html", "status.html"}, terminal: true,
			wantFile: "status.html", wantOpened: true},
		{name: "a file after =, not opened", args: []string{"--html=status.html", "--open=false"}, terminal: true,
			wantFile: "status.html"},
		{name: "an argument without --html", args: []string{"status.html"}, wantErr: `unknown command "status.html"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("TMPDIR", dir)
			t.Chdir(dir)
			var opened string
			restoreBrowse, restoreInteractive := browse, interactive
			t.Cleanup(func() { browse, interactive = restoreBrowse, restoreInteractive })
			interactive = func(io.Writer) bool { return tt.terminal }
			browse = func(path string) error { opened = path; return nil }

			out, err := runCLI(t, newStatusClient(t, interceptor.Funcs{}, statusCluster()...),
				append([]string{"status"}, tt.args...)...)

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Contains(t, out, "Napwake manages 4 workloads.", "the table is shown as well")
			path := tt.wantFile
			if tt.wantFile == "" {
				files, err := filepath.Glob(filepath.Join(dir, "napwake-status-*.html"))
				require.NoError(t, err)
				require.Len(t, files, 1)
				path = files[0]
				info, err := os.Stat(path)
				require.NoError(t, err)
				assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the page names the cluster's workloads")
			}
			page, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Contains(t, string(page), "<h1>Napwake status</h1>")
			if tt.wantOpened {
				assert.Equal(t, path, opened)
				assert.Contains(t, out, "Status page: "+path+" (opened in your browser; --open=false to skip)\n")
				return
			}
			assert.Empty(t, opened)
			assert.Contains(t, out, "Status page: ")
			assert.NotContains(t, out, "opened in your browser")
		})
	}

	t.Run("without --html, no page", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("TMPDIR", dir)
		restoreInteractive := interactive
		t.Cleanup(func() { interactive = restoreInteractive })
		interactive = func(io.Writer) bool { return true }

		out, err := runCLI(t, newStatusClient(t, interceptor.Funcs{}, statusCluster()...), "status")

		require.NoError(t, err)
		assert.NotContains(t, out, "Status page")
		files, err := filepath.Glob(filepath.Join(dir, "*.html"))
		require.NoError(t, err)
		assert.Empty(t, files)
	})
}
