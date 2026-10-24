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

package doorman

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func request(lines ...string) []byte {
	return []byte(strings.Join(lines, "\r\n") + "\r\n\r\n")
}

func TestIsPageLoad(t *testing.T) {
	const htmlAccept = "Accept: text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	tests := []struct {
		name string
		head []byte
		want bool
	}{
		{name: "a browser opening a page", head: request("GET /orders HTTP/1.1", "Host: shop", htmlAccept, "Sec-Fetch-Mode: navigate"), want: true},
		{name: "a browser checking a page", head: request("HEAD / HTTP/1.1", "Host: shop", "Sec-Fetch-Mode: navigate"), want: true},
		{name: "an older browser", head: request("GET / HTTP/1.1", "Host: shop", htmlAccept), want: true},
		{name: "a script's fetch", head: request("GET /api/orders HTTP/1.1", "Host: shop", "Accept: */*", "Sec-Fetch-Mode: cors")},
		{name: "a script asking for HTML", head: request("GET /partial HTTP/1.1", "Host: shop", htmlAccept, "Sec-Fetch-Mode: cors")},
		{name: "curl", head: request("GET / HTTP/1.1", "Host: shop", "User-Agent: curl/8.7.1", "Accept: */*")},
		{name: "a form submission", head: request("POST /checkout HTTP/1.1", "Host: shop", htmlAccept, "Sec-Fetch-Mode: navigate")},
		{name: "a WebSocket", head: request("GET /ws HTTP/1.1", "Host: shop", "Upgrade: websocket", "Connection: Upgrade", "Sec-Fetch-Mode: websocket")},
		{name: "an older client's WebSocket", head: request("GET /ws HTTP/1.1", "Host: shop", htmlAccept, "Upgrade: websocket", "Connection: Upgrade")},
		{name: "a browser's lines ending in a bare LF", head: []byte("GET / HTTP/1.1\nHost: shop\nSec-Fetch-Mode: navigate\n\n"), want: true},
		{name: "headers still arriving", head: []byte("GET / HTTP/1.1\r\nHost: shop\r\nSec-Fetch-Mode: navi")},
		{name: "bare-LF headers still arriving", head: []byte("GET / HTTP/1.1\nHost: shop\nSec-Fetch-Mode: navi")},
		{name: "a browser whose cookies run past the peek", want: true,
			head: []byte("GET / HTTP/1.1\r\nHost: shop\r\nSec-Fetch-Mode: navigate\r\nCookie: sso=" + strings.Repeat("a", maxPeek))[:maxPeek]},
		{name: "a request line alone", head: []byte("GET / HTTP/1.1\r\nCookie: sso=" + strings.Repeat("a", maxPeek))[:maxPeek]},
		{name: "a request line cut short", head: []byte("GET /" + strings.Repeat("a", maxPeek))[:maxPeek]},
		{name: "TLS", head: []byte{0x16, 0x03, 0x01, 0x02, 0x00, 0x01}},
		{name: "HTTP/2", head: []byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")},
		{name: "nothing sent", head: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, ok := parseRequest(tt.head)
			assert.Equal(t, tt.want, ok && isPageLoad(req))
		})
	}
}

func TestIsHealthCheck(t *testing.T) {
	tests := []struct {
		method string
		agent  string
		want   bool
	}{
		{method: "GET", agent: "kube-probe/1.31", want: true},
		{method: "GET", agent: "Prometheus/2.53.0", want: true},
		{method: "HEAD", agent: "ELB-HealthChecker/2.0", want: true},
		{method: "GET", agent: "GoogleHC/1.0", want: true},
		{method: "GET", agent: "Envoy/HC", want: true},
		{method: "GET", agent: "OpenTelemetry Collector Contrib/0.104.0 (linux/amd64)", want: true},
		{method: "GET", agent: "otelcol-contrib/0.110.0", want: true},
		{method: "GET", agent: "Datadog Agent/7.55.0", want: true},
		{method: "GET", agent: "Blackbox Exporter/0.25.0", want: true},
		{method: "GET", agent: "Datadog/Synthetics", want: true},
		{method: "GET", agent: "Amazon-Route53-Health-Check-Service (ref 7f0e2a3c; report http://amzn.to/1vsZADi)", want: true},
		{method: "GET", agent: "GoogleStackdriverMonitoring-UptimeChecks(https://cloud.google.com/monitoring)", want: true},
		{method: "HEAD", agent: "Mozilla/5.0+(compatible; UptimeRobot/2.0; http://www.uptimerobot.com/)", want: true},
		{method: "GET", agent: "Pingdom.com_bot_version_1.4_(http://www.pingdom.com/)", want: true},
		{method: "GET", agent: "NewRelicPinger/1.0 (269919)", want: true},
		{method: "GET", agent: "Uptime-Kuma/1.23.13", want: true},
		{method: "GET", agent: "Gatus/1.0", want: true},
		{method: "GET", agent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5)"},
		{method: "GET", agent: "curl/8.7.1"},
		{method: "GET", agent: ""},
		// Remote write and Alertmanager notifications send real data with
		// the same agents; dropping them would lose samples and alerts.
		{method: "POST", agent: "Prometheus/2.53.0"},
		{method: "POST", agent: "Alertmanager/0.27.0"},
		{method: "PUT", agent: "GrafanaAgent/0.40.0"},
	}
	for _, tt := range tests {
		req, ok := parseRequest(request(tt.method+" /healthz HTTP/1.1", "Host: shop", "User-Agent: "+tt.agent))
		require.True(t, ok)
		assert.Equal(t, tt.want, isHealthCheck(req, defaultHealthCheckAgents), "%s %s", tt.method, tt.agent)
	}
}

// Agents added with Options.HealthCheckAgents are recognised alongside the
// built-in ones, and a blank one is dropped rather than matching everything.
func TestHealthCheckAgents_AddToTheBuiltIn(t *testing.T) {
	agents := healthCheckAgents([]string{"MyCorpMonitor/", "  uptime-kuma ", "", "   "})
	probe := func(agent string) bool {
		req, ok := parseRequest(request("GET /healthz HTTP/1.1", "Host: shop", "User-Agent: "+agent))
		require.True(t, ok)
		return isHealthCheck(req, agents)
	}

	assert.True(t, probe("MyCorpMonitor/3.1"))
	assert.True(t, probe("uptime-kuma/1.23"))
	assert.True(t, probe("kube-probe/1.31"), "the built-in agents still count")
	assert.True(t, probe("Mozilla/5.0 (compatible; MyCorpMonitor/3.1; +https://monitor.example.com)"),
		"a monitor presenting itself as a browser")
	assert.False(t, probe("Mozilla/5.0 (X11; Linux x86_64)"))
	assert.False(t, probe("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36"))
	assert.False(t, probe("Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"),
		"only the agents listed")
	assert.Len(t, agents, len(defaultHealthCheckAgents)+2)
}

// Scanners probe every address for hidden files, hoping for leaked secrets;
// none of them is anything an app serves its users.
func TestIsHiddenFileRequest(t *testing.T) {
	tests := map[string]bool{
		"/.env":                             true,
		"/.env.production":                  true,
		"/.git/config":                      true,
		"/.aws/credentials":                 true,
		"/.ssh/id_rsa":                      true,
		"/api/.env":                         true,
		"/static/.DS_Store":                 true,
		"/%2eenv":                           true,
		"/.well-known/acme-challenge/token": false,
		"/.well-known/openid-configuration": false,
		"/api/.well-known/x":                true,
		"/":                                 false,
		"/index.html":                       false,
		"/api/v1/users":                     false,
		"/files/report.v2.pdf":              false,
		"/a/../b":                           false,
		"/./index.html":                     false,
	}
	for path, want := range tests {
		req, ok := parseRequest(request("GET "+path+" HTTP/1.1", "Host: shop"))
		require.True(t, ok, path)
		assert.Equal(t, want, isHiddenFileRequest(req), path)
	}
}

func readPage(t *testing.T, raw []byte, method string) (*http.Response, string) {
	t.Helper()
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), &http.Request{Method: method})
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, string(body)
}

func pageLoad(t *testing.T, method string) *http.Request {
	t.Helper()
	req, ok := parseRequest(request(method+" / HTTP/1.1", "Host: shop", "Sec-Fetch-Mode: navigate"))
	require.True(t, ok)
	require.True(t, isPageLoad(req))
	return req
}

func TestWritePage(t *testing.T) {
	var out bytes.Buffer

	require.NoError(t, writePage(&out, pageLoad(t, http.MethodGet), pageData{
		Address: "admin.preview-42.example.dev", Elapsed: "14s",
	}))

	resp, body := readPage(t, out.Bytes(), http.MethodGet)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "a 503, so nothing mistakes it for the app")
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	assert.Equal(t, "3", resp.Header.Get("Retry-After"))
	assert.True(t, resp.Close, "the connection closes after the page")
	assert.Contains(t, body, "<title>Waking up admin.preview-42.example.dev</title>")
	assert.Contains(t, body, `<meta http-equiv="refresh" content="3">`)
	assert.Contains(t, body, "<header>admin.preview-42.example.dev</header>")
	assert.Contains(t, body, `<li class="now">`)
	assert.Contains(t, body, ">14s<")
	assert.Contains(t, body, `<svg class="scene" viewBox="0 0 300 236" aria-hidden="true">`,
		"the doorman holds the door of the waking room")
	assert.Contains(t, body, `<meta name="referrer" content="no-referrer">`, "the address stays with the page")
	assert.Contains(t, body, `<a href="https://napwake.dev/" target="_blank" rel="noopener noreferrer">`+
		`<svg viewBox="0 0 24 24" aria-hidden="true"><path fill="currentColor" fill-rule="evenodd" d="`+Mark+
		`"/></svg>Paused by Napwake</a>`)
}

// The waking-up page draws its tab icon inline, so a browser never asks the
// doorman for /favicon.ico, which it would hold until the workload is
// Ready. Its policy lets it show images from data: alone, and run only its
// own style, which the policy names by its hash.
func TestWritePage_HasItsOwnTabIcon(t *testing.T) {
	var out bytes.Buffer

	require.NoError(t, writePage(&out, pageLoad(t, http.MethodGet), pageData{Address: "shop", Elapsed: "3s"}))

	resp, body := readPage(t, out.Bytes(), http.MethodGet)
	assert.Contains(t, body, `<link rel="icon" href="data:image/svg+xml,`)
	policy := resp.Header.Get("Content-Security-Policy")
	assert.Contains(t, policy, "default-src 'none'")
	assert.Contains(t, policy, "img-src data:;", "images from data: alone")
	assert.NotContains(t, policy, "unsafe")
	assert.NotContains(t, policy, "frame-ancestors", "an app framed by another site still shows its waking-up page")
	styles := regexp.MustCompile(`(?s)<style>(.*?)</style>`).FindAllStringSubmatch(body, -1)
	require.Len(t, styles, 1)
	sum := sha256.Sum256([]byte(styles[0][1]))
	assert.Contains(t, policy, "style-src 'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	assert.NotContains(t, body, ` style="`, "the policy would block a style attribute")
	assert.NotContains(t, body, "<script", "the page runs no script")
}

// The asleep page runs only its own style and script, which the policy
// names by their hashes, so nothing injected into it could run, and loads
// nothing: its one image is its tab icon, inline, so a browser never asks
// for /favicon.ico, which a workload that asks would turn away.
func TestWriteAsleepPage_RunsOnlyItsOwnStyleAndScript(t *testing.T) {
	var out bytes.Buffer

	require.NoError(t, writeAsleepPage(&out, pageData{Address: "pr-42.preview.example.dev", Token: "t"}, notice{}))

	resp, body := readPage(t, out.Bytes(), http.MethodGet)
	policy := resp.Header.Get("Content-Security-Policy")
	for _, directive := range []string{"default-src 'none'", "img-src data:", "form-action 'self'", "base-uri 'none'",
		"frame-ancestors 'none'"} {
		assert.Contains(t, policy, directive)
	}
	assert.NotContains(t, policy, "unsafe")
	inline := regexp.MustCompile(`(?s)<(style|script)>(.*?)</(?:style|script)>`).FindAllStringSubmatch(body, -1)
	require.Len(t, inline, 2, "a style and a script")
	for _, m := range inline {
		sum := sha256.Sum256([]byte(m[2]))
		assert.Contains(t, policy, m[1]+"-src 'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'",
			"the policy names the page's %s", m[1])
	}
	assert.Contains(t, body, `<link rel="icon" href="data:image/svg+xml,`)
	for _, external := range regexp.MustCompile(`(?:src|href|action)="([^"#]*)"`).FindAllStringSubmatch(body, -1) {
		if !strings.HasPrefix(external[1], "data:") {
			assert.Equal(t, "https://napwake.dev/", external[1], "only the footer links elsewhere, and loads nothing")
		}
	}
}

// The asleep page's button posts its token back to the page's own address,
// with nothing else: no address to go to after, which would need guarding
// against redirects elsewhere. It reloads every minute, so its token stays
// fresh, and says nothing of how long the app takes to start.
func TestWriteAsleepPage_PostsBackToItself(t *testing.T) {
	var out bytes.Buffer

	require.NoError(t, writeAsleepPage(&out, pageData{Address: "pr-42.preview.example.dev", Token: "tok"}, notice{}))

	resp, body := readPage(t, out.Bytes(), http.MethodGet)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Retry-After"))
	assert.Contains(t, body, `<form class="ask" id="wake" method="post">`+"\n"+
		`        <input type="hidden" name="napwake-wake" value="tok">`)
	assert.Equal(t, 1, strings.Count(body, "<input"), "the token alone")
	assert.Contains(t, body, `<meta http-equiv="refresh" content="60">`)
	assert.Contains(t, body, `<meta name="referrer" content="same-origin">`)
	assert.Contains(t, body, "<title>Asleep · pr-42.preview.example.dev</title>")
	assert.Contains(t, body, `<span class="a">Wake it up?</span>`)
	assert.Contains(t, body, "Starting takes as long as the app needs.")
	assert.Contains(t, body, `<p class="lede a">This environment was paused while nobody was using it`)
	assert.NotContains(t, body, `class="note`)
}

// When a click woke nothing, the page says why, with the status that
// fits: an expired button is refused, and a click the rate limit turned
// down says when to try again.
func TestWriteAsleepPage_Notices(t *testing.T) {
	tests := []struct {
		name       string
		notice     notice
		status     int
		retryAfter string
		says       string
	}{
		{name: "expired", notice: noticeExpired, status: http.StatusForbidden,
			says: "<b>The button had run out</b>This page was open a while, so nothing woke. Press Wake it again."},
		{name: "from another address", notice: noticeElsewhere, status: http.StatusForbidden,
			says: "This click didn&#39;t come from this page&#39;s address, so nothing woke. Wake it with kubectl " +
				"napwake wake or the napwake.dev/wake-requested annotation. <a href=\"" + wakeItDocs +
				`" target="_blank" rel="noopener noreferrer">How to wake it</a>`},
		{name: "rate-limited", notice: noticeLimited, status: http.StatusTooManyRequests, retryAfter: "60",
			says: "<b>Try again in a minute</b>Wake it was pressed a moment ago. Wakes are limited, so robots can&#39;t " +
				"keep it awake."},
		{name: "couldn't wake", notice: noticeFailed, status: http.StatusOK,
			says: "It couldn&#39;t be woken just now. Try again in a moment."},
		{name: "no fetch metadata", notice: noticeNoFetchMetadata, status: http.StatusOK,
			says: "This browser didn&#39;t say the click came from this page, so nothing woke. Wake it with kubectl " +
				"napwake wake, the napwake.dev/wake-requested annotation, or a current browser. " +
				`<a href="` + asleepDocs + `" target="_blank" rel="noopener noreferrer">How to wake it</a>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer

			require.NoError(t, writeAsleepPage(&out, pageData{Token: "tok"}, tt.notice))

			resp, body := readPage(t, out.Bytes(), http.MethodGet)
			assert.Equal(t, tt.status, resp.StatusCode)
			assert.Equal(t, tt.retryAfter, resp.Header.Get("Retry-After"))
			assert.Contains(t, body, `<p class="note a">`+tt.says+`</p>`)
			assert.Contains(t, body, `<span class="a">Still asleep</span>`)
			assert.Contains(t, body, `name="napwake-wake" value="tok"`, "a fresh button with every notice")
			assert.NotContains(t, body, `<p class="lede a">`)
		})
	}
}

// The page is shown to whoever reaches the Service, so it says nothing about
// what runs inside the cluster.
func TestWritePage_NamesNothingInTheCluster(t *testing.T) {
	var out bytes.Buffer

	require.NoError(t, writePage(&out, pageLoad(t, http.MethodGet), pageData{}))

	_, body := readPage(t, out.Bytes(), http.MethodGet)
	assert.Contains(t, body, "Waking up <span>this app</span>", "a request with no host gets a generic name")
	for _, internal := range []string{"Namespace", "Service", "Paused</dt>"} {
		assert.NotContains(t, body, internal)
	}
}

func TestPageAddress(t *testing.T) {
	tests := []struct {
		host string
		want string
	}{
		{host: "admin.preview-42.example.dev", want: "admin.preview-42.example.dev"},
		{host: "admin.preview-42.example.dev:8443", want: "admin.preview-42.example.dev"},
		{host: "[fd00::1]:80", want: "fd00::1"},
		{host: "", want: ""},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, pageAddress(&http.Request{Host: tt.host}), tt.host)
	}
}

func TestWritePage_HEADHasNoBody(t *testing.T) {
	var out bytes.Buffer

	require.NoError(t, writePage(&out, pageLoad(t, http.MethodHead), pageData{Address: "shop"}))

	assert.True(t, bytes.HasSuffix(out.Bytes(), []byte("\r\n\r\n")), "headers only")
	resp, _ := readPage(t, out.Bytes(), http.MethodHead)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

func TestWritePage_EscapesTheHost(t *testing.T) {
	var out bytes.Buffer

	require.NoError(t, writePage(&out, pageLoad(t, http.MethodGet), pageData{Address: "<img src=x>"}))

	assert.NotContains(t, out.String(), "<img src=x>", "the Host header is the caller's, so it's escaped")
}

func TestSinceLabel(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "just now"},
		{1500 * time.Millisecond, "just now"},
		{14 * time.Second, "14s"},
		{75 * time.Second, "1m 15s"},
		{10*time.Minute + 3*time.Second, "10m 03s"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, sinceLabel(tt.d), tt.d.String())
	}
}
