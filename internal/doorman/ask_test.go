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

package doorman

import (
	"bufio"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

const chrome = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) " +
	"Chrome/141.0.0.0 Safari/537.36"

// personGET is a person's browser opening a page of the preview.
const personGET = "GET /orders?id=7 HTTP/1.1\r\nHost: pr-42.preview.example.dev\r\nUser-Agent: " + chrome +
	"\r\nAccept: text/html,application/xhtml+xml\r\nSec-Fetch-Mode: navigate\r\nSec-Fetch-Site: none\r\n\r\n"

// askingWorkload is a paused workload whose doorman asks before waking it.
func askingWorkload(port int32) *v1alpha1.ManagedWorkload {
	w := pausedWorkload("api", port, time.Minute)
	w.UID = "uid-api"
	w.Spec.Wake.Mode = v1alpha1.WakeOnAsk
	return w
}

// askedPage loads the asleep page as a person would, and returns its token.
func askedPage(t *testing.T, port int32) string {
	t.Helper()
	conn := dial(t, port)
	send(t, conn, personGET)
	resp, body := readResponse(t, conn)
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	return tokenIn(t, body)
}

var tokenInput = regexp.MustCompile(`name="` + wakeField + `" value="([^"]+)"`)

// shownTitle is a notice's title as the asleep page shows it, in bold:
// one notice's title can begin another's text, as "Nothing woke" does.
func shownTitle(n notice) string {
	return "<b>" + template.HTMLEscapeString(n.title) + "</b>"
}

func tokenIn(t *testing.T, page string) string {
	t.Helper()
	m := tokenInput.FindStringSubmatch(page)
	require.Len(t, m, 2, "the page has a Wake it button carrying a token: %s", page)
	return m[1]
}

// clickRequest is the POST the Wake it button sends, with token, back to
// the page it's on.
func clickRequest(token string) string {
	return clickFrom(token, chrome, chromeClick)
}

// What browsers send with a form a person submits by clicking it, besides
// their User-Agent. Chrome, Edge and Firefox mark a navigation a person
// started with Sec-Fetch-User; Safari, and every browser on iOS, which run
// WebKit, send the rest of the fetch metadata without it.
const (
	chromeClick = "Origin: https://pr-42.preview.example.dev\r\nSec-Fetch-Site: same-origin\r\n" +
		"Sec-Fetch-Mode: navigate\r\nSec-Fetch-User: ?1\r\nSec-Fetch-Dest: document\r\n"
	safariClick = "Origin: https://pr-42.preview.example.dev\r\nSec-Fetch-Site: same-origin\r\n" +
		"Sec-Fetch-Mode: navigate\r\nSec-Fetch-Dest: document\r\n"
)

const (
	firefox = "Mozilla/5.0 (Macintosh; Intel Mac OS X 15.6; rv:144.0) Gecko/20100101 Firefox/144.0"
	safari  = "Mozilla/5.0 (Macintosh; Intel Mac OS X 15_6) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.0 " +
		"Safari/605.1.15"
	chromeOnIPhone = "Mozilla/5.0 (iPhone; CPU iPhone OS 26_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) " +
		"CriOS/141.0.7390.41 Mobile/15E148 Safari/604.1"
	firefoxOnIPhone = "Mozilla/5.0 (iPhone; CPU iPhone OS 26_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) " +
		"FxiOS/144.0 Mobile/15E148 Safari/605.1.15"
)

// clickFrom is the POST the Wake it button sends, with token, back to the
// page it's on, from a browser of agent sending headers.
func clickFrom(token, agent, headers string) string {
	return clickAt("pr-42.preview.example.dev", token, agent, headers)
}

// clickAt is the POST the Wake it button sends, with token, back to a page
// served at host.
func clickAt(host, token, agent, headers string) string {
	body := url.Values{wakeField: {token}}.Encode()
	return "POST /orders?id=7 HTTP/1.1\r\nHost: " + host + "\r\nUser-Agent: " + agent + "\r\n" +
		"Content-Type: application/x-www-form-urlencoded\r\n" + headers +
		fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body)) + body
}

func click(t *testing.T, port int32, request string) (*http.Response, string) {
	t.Helper()
	conn := dial(t, port)
	send(t, conn, request)
	return readResponse(t, conn)
}

// A person's browser opening a paused workload that asks gets the asleep
// page, with a Wake it button, and nothing wakes.
func TestServer_AsksAPersonBeforeWaking(t *testing.T) {
	port := freePort(t)
	s, c := newTestServer(t, nil, askingWorkload(port))
	stop := run(t, s)
	before := wakes(resultAsked)

	conn := dial(t, port)
	send(t, conn, personGET)
	resp, body := readResponse(t, conn)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	assert.Equal(t, "noindex", resp.Header.Get("X-Robots-Tag"))
	assert.Contains(t, resp.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'")
	assert.Equal(t, "same-origin", resp.Header.Get("Referrer-Policy"),
		"a click carries the page's Origin, which no-referrer would make null, and other sites get nothing")
	assert.Contains(t, body, `<meta name="referrer" content="same-origin">`)
	assert.Contains(t, body, `<meta name="robots" content="noindex">`)
	assert.Contains(t, body, `<meta http-equiv="refresh" content="60">`,
		"a tab left open reloads itself, into the app once someone has woken it, and with a fresh token until then")
	assert.Contains(t, body, "<title>Asleep · pr-42.preview.example.dev</title>")
	assert.Contains(t, body, `<form class="ask" id="wake" method="post">`, "the button posts back to the page it's on")
	assert.NotContains(t, body, `name="next"`, "with nowhere else to go")
	assert.Contains(t, body, "Wake it")
	tokenIn(t, body)
	assert.NotContains(t, body, "dev/api", "the page names nothing inside the cluster")
	assert.NotContains(t, body, "uid-api")

	stop()
	assert.Empty(t, lastRequest(t, c), "nothing wakes it")
	assert.Equal(t, before+1, wakes(resultAsked))
}

// A click carrying the page's token wakes the workload, as a held request
// does, and the waking-up page follows, reloading the page the person
// asked for.
func TestServer_AClickWakesIt(t *testing.T) {
	port := freePort(t)
	recorder := events.NewFakeRecorder(10)
	s, c := newTestServer(t, nil, askingWorkload(port))
	s.recorder = recorder
	run(t, s)

	resp, body := click(t, port, clickRequest(askedPage(t, port)))

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "the waking-up page")
	assert.Contains(t, body, "<title>Waking up pr-42.preview.example.dev</title>")
	assert.Contains(t, body, `<meta http-equiv="refresh" content="3">`, "it reloads the page it's on, with a GET")
	assert.Contains(t, body, `<meta name="referrer" content="no-referrer">`, "the waking-up page posts nothing")
	assert.NotEmpty(t, lastRequest(t, c), "the click stamps the request annotation, as a held request does")
	assert.Equal(t, 1, wokenEvents(recorder))
}

// Once it has been asked to wake, a person's browser gets the waking-up
// page and an API call is held, as for a workload that wakes on any
// request, and neither starts another wake.
func TestServer_AsksNothingWhileItWakes(t *testing.T) {
	tests := []struct {
		name  string
		waken func(*v1alpha1.ManagedWorkload)
	}{
		{name: "resuming", waken: func(w *v1alpha1.ManagedWorkload) { w.Status.Phase = v1alpha1.PhaseResuming }},
		{name: "a click's stamp waiting for the operator", waken: func(w *v1alpha1.ManagedWorkload) {
			w.Annotations = map[string]string{v1alpha1.AnnotationLastRequest: time.Now().UTC().Format(time.RFC3339)}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port := freePort(t)
			w := askingWorkload(port)
			w.Spec.Wake.MaxWait = &metav1.Duration{Duration: time.Second}
			tt.waken(w)
			s, c := newTestServer(t, nil, w)
			stop := run(t, s)
			stamp := lastRequest(t, c)
			timedOut := wakes(resultTimeout)

			resp, body := click(t, port, personGET)
			assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
			assert.Contains(t, body, "<title>Waking up pr-42.preview.example.dev</title>")

			api := dial(t, port)
			send(t, api, "GET /api HTTP/1.1\r\nHost: api\r\nUser-Agent: curl/8.7.1\r\n\r\n")
			closedWithin(t, api)

			stop()
			assert.Equal(t, timedOut+1, wakes(resultTimeout), "held for maxWait, not turned away")
			assert.Equal(t, stamp, lastRequest(t, c), "nothing stamped a wake of its own")
		})
	}
}

// A click whose token isn't this pause's of this workload, or has expired,
// or comes from another site, wakes nothing, and a browser is asked again.
func TestServer_AClickWithoutAGoodTokenWakesNothing(t *testing.T) {
	good := func(s *Server, scope tokenScope) string { return s.wakeKeys.sign(scope, time.Now().Add(tokenTTL)) }
	tests := []struct {
		name   string
		token  func(*Server, tokenScope) string
		site   string
		wakes  bool
		notice notice
	}{
		{name: "a good token, which the others are measured against", token: good, wakes: true},
		{name: "no token", token: func(*Server, tokenScope) string { return "" }},
		{name: "not a token", token: func(*Server, tokenScope) string { return "wake-me-up" }},
		{name: "expired", token: func(s *Server, scope tokenScope) string {
			return s.wakeKeys.sign(scope, time.Now().Add(-time.Second))
		}},
		{name: "another workload's", token: func(s *Server, scope tokenScope) string {
			scope.workload = types.NamespacedName{Namespace: "dev", Name: "web"}
			return good(s, scope)
		}},
		{name: "a workload of the same name made since", token: func(s *Server, scope tokenScope) string {
			scope.uid = "uid-earlier"
			return good(s, scope)
		}},
		{name: "an earlier pause's", token: func(s *Server, scope tokenScope) string {
			scope.began = scope.began.Add(-24 * time.Hour)
			return good(s, scope)
		}},
		{name: "signed with another key", token: func(_ *Server, scope tokenScope) string {
			return newWakeKeys().sign(scope, time.Now().Add(tokenTTL))
		}},
		{name: "a good token, from another site", token: good, site: "cross-site", notice: noticeElsewhere},
		{name: "a good token, from another subdomain", token: good, site: "same-site", notice: noticeElsewhere},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port := freePort(t)
			w := askingWorkload(port)
			s, c := newTestServer(t, nil, w)
			stop := run(t, s)
			var stored v1alpha1.ManagedWorkload
			require.NoError(t, c.Get(context.Background(), apiKey, &stored))
			scope := tokenScope{workload: apiKey, uid: stored.UID, began: pauseBegan(&stored)}

			request := clickRequest(tt.token(s, scope))
			if tt.site != "" {
				request = strings.Replace(request, "Sec-Fetch-Site: same-origin", "Sec-Fetch-Site: "+tt.site, 1)
			}
			resp, body := click(t, port, request)

			stop()
			if tt.wakes {
				assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "the waking-up page")
				assert.NotEmpty(t, lastRequest(t, c))
				return
			}
			want, other := noticeExpired, noticeElsewhere
			if tt.notice == noticeElsewhere {
				want, other = noticeElsewhere, noticeExpired
			}
			assert.Equal(t, http.StatusForbidden, resp.StatusCode, "the asleep page again, refused")
			assert.Contains(t, body, shownTitle(want))
			assert.NotContains(t, body, shownTitle(other))
			tokenIn(t, body)
			assert.Empty(t, lastRequest(t, c), "nothing wakes it")
		})
	}
}

// Only a person's click wakes the workload: a browser's, that the browser
// says came from the page, and, from a browser that marks the navigations a
// person starts, marked so. A spider that fetches the page and posts its
// token, even as a browser, or a script that submits the form, wakes
// nothing.
func TestServer_AClickMustComeFromAPerson(t *testing.T) {
	fromSite := func(headers, site string) string {
		return strings.Replace(headers, "Sec-Fetch-Site: same-origin", "Sec-Fetch-Site: "+site, 1)
	}
	scripted := strings.Replace(chromeClick, "Sec-Fetch-User: ?1\r\n", "", 1)
	tests := []struct {
		name    string
		agent   string
		headers string
		wakes   bool
		page    bool
		notice  notice
	}{
		{name: "Chrome, clicked", agent: chrome, headers: chromeClick, wakes: true},
		{name: "Firefox, clicked", agent: firefox, headers: chromeClick, wakes: true},
		{name: "Safari, clicked", agent: safari, headers: safariClick, wakes: true},
		{name: "Chrome on an iPhone, which is WebKit, clicked", agent: chromeOnIPhone, headers: safariClick, wakes: true},
		{name: "Firefox on an iPhone, which is WebKit, clicked", agent: firefoxOnIPhone, headers: safariClick, wakes: true},
		{name: "Chrome, saying nothing of where the click came from", agent: chrome, page: true,
			notice: noticeNoFetchMetadata},
		{name: "Safari, saying nothing of where the click came from", agent: safari, page: true,
			notice: noticeNoFetchMetadata},
		{name: "Chrome, marked as a person's but saying nothing of where it came from", agent: chrome, page: true,
			headers: "Sec-Fetch-User: ?1\r\n", notice: noticeNoFetchMetadata},
		{name: "Safari, from another site", agent: safari, page: true, headers: fromSite(safariClick, "cross-site"),
			notice: noticeElsewhere},
		{name: "Safari, from another subdomain", agent: safari, page: true, headers: fromSite(safariClick, "same-site"),
			notice: noticeElsewhere},
		{name: "Safari, from no page at all", agent: safari, page: true, headers: fromSite(safariClick, "none"),
			notice: noticeExpired},
		{name: "Chrome, a script submitting the form", agent: chrome, page: true, headers: scripted},
		{name: "Firefox, a script submitting the form", agent: firefox, page: true, headers: scripted},
		{name: "not started by a person, said outright", agent: safari, page: true,
			headers: safariClick + "Sec-Fetch-User: ?0\r\n"},
		{name: "a spider posting the token it fetched", agent: "python-requests/2.32.3", headers: chromeClick},
		{name: "a headless browser", agent: "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) " +
			"HeadlessChrome/141.0.0.0 Safari/537.36", headers: chromeClick},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port := freePort(t)
			s, c := newTestServer(t, nil, askingWorkload(port))
			stop := run(t, s)

			resp, body := click(t, port, clickFrom(askedPage(t, port), tt.agent, tt.headers))

			stop()
			switch {
			case tt.wakes:
				assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "the waking-up page")
				assert.Contains(t, body, "Waking up")
				assert.NotEmpty(t, lastRequest(t, c))
				return
			case tt.page:
				status := http.StatusOK
				if tt.notice == noticeExpired || tt.notice == noticeElsewhere {
					status = http.StatusForbidden
				}
				assert.Equal(t, status, resp.StatusCode, "the asleep page again")
				tokenIn(t, body)
				for _, other := range []notice{noticeExpired, noticeElsewhere, noticeNoFetchMetadata} {
					if other != tt.notice {
						assert.NotContains(t, body, shownTitle(other))
					}
				}
				if tt.notice != (notice{}) {
					assert.Contains(t, body, shownTitle(tt.notice))
				}
			default:
				assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
				assert.Contains(t, body, asleepMessage)
			}
			assert.Empty(t, lastRequest(t, c), "nothing wakes it")
		})
	}
}

// A browser posting the app's own form while it's asleep, as from a page it
// cached, is asked as when it opens a page, with no notice, and isn't
// counted as turned away, whatever fetch metadata it sends.
func TestServer_AnAppsOwnFormFromABrowserIsAsked(t *testing.T) {
	tests := []struct {
		name    string
		headers string
	}{
		{name: "with fetch metadata", headers: "Sec-Fetch-Site: same-origin\r\nSec-Fetch-Mode: navigate\r\n"},
		{name: "without", headers: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port := freePort(t)
			s, c := newTestServer(t, nil, askingWorkload(port))
			stop := run(t, s)
			asked, away := wakes(resultAsked), wakes(resultTurnedAway)
			form := "user=ada"
			request := "POST /login HTTP/1.1\r\nHost: pr-42.preview.example.dev\r\nUser-Agent: " + chrome + "\r\n" +
				"Content-Type: application/x-www-form-urlencoded\r\n" + tt.headers +
				fmt.Sprintf("Content-Length: %d\r\n\r\n", len(form)) + form

			resp, body := click(t, port, request)

			stop()
			assert.Equal(t, http.StatusOK, resp.StatusCode, "the asleep page")
			assert.NotContains(t, body, `class="note`)
			tokenIn(t, body)
			assert.Empty(t, lastRequest(t, c))
			assert.Equal(t, asked+1, wakes(resultAsked))
			assert.Equal(t, away, wakes(resultTurnedAway), "not turned away")
		})
	}
}

// A browser that says nothing of where a click came from, neither with
// fetch metadata nor with its Origin, as browsers from before about 2020
// don't, can't be told from a spider that fetched the page and posted its
// token as a browser. It wakes nothing, and the page says how else to wake
// it, since clicking again won't.
func TestServer_AClickThatDoesntSayWhereItCameFromSaysHowElseToWake(t *testing.T) {
	tests := []struct {
		name    string
		headers string
	}{
		{name: "no Origin"},
		{name: "a null Origin, as a page with Referrer-Policy: no-referrer sends", headers: "Origin: null\r\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port := freePort(t)
			s, c := newTestServer(t, nil, askingWorkload(port))
			stop := run(t, s)
			asked, away := wakes(resultAsked), wakes(resultTurnedAway)

			resp, body := click(t, port, clickFrom(askedPage(t, port), chrome, tt.headers))

			stop()
			assert.Equal(t, http.StatusOK, resp.StatusCode, "the asleep page again")
			tokenIn(t, body)
			for _, how := range []string{
				"didn&#39;t say the click came from this page", "<code>kubectl napwake wake</code>",
				"Use a current browser", `href="` + asleepDocs + `"`,
			} {
				assert.Contains(t, body, how)
			}
			assert.NotContains(t, body, "HTTPS", "a current browser over plain HTTP says it with its Origin")
			assert.Empty(t, lastRequest(t, c), "nothing wakes it")
			assert.Equal(t, asked+2, wakes(resultAsked), "a browser shown the page, then asked again")
			assert.Equal(t, away, wakes(resultTurnedAway))
		})
	}
}

// Over plain HTTP, browsers send no fetch metadata, so a click there is
// judged by its Origin, which the page's Referrer-Policy: same-origin lets
// them send: the scheme, host and port of the page the click came from. It
// must be the address the page was served at, the host the browser asked
// for, as the page shows it, and its port.
func TestServer_AClickOverPlainHTTPIsJudgedByItsOrigin(t *testing.T) {
	const page = "pr-42.preview.example.dev"
	tests := []struct {
		name       string
		agent      string
		host       string
		origin     string
		wakes      bool
		notice     notice
		turnedAway bool
	}{
		{name: "Chrome", agent: chrome, origin: "http://" + page, wakes: true},
		{name: "Safari", agent: safari, origin: "http://" + page, wakes: true},
		{name: "Firefox", agent: firefox, origin: "http://" + page, wakes: true},
		{name: "a browser from before fetch metadata, over HTTPS", agent: safari, origin: "https://" + page, wakes: true},
		{name: "on a port of its own", agent: chrome, host: "localhost:8080", origin: "http://localhost:8080", wakes: true},
		{name: "with the default port a proxy named", agent: chrome, host: page + ":80", origin: "http://" + page,
			wakes: true},
		{name: "an IPv6 address", agent: chrome, host: "[2001:db8::7]:8080", origin: "http://[2001:db8::7]:8080",
			wakes: true},
		{name: "the host in capitals", agent: chrome, host: "PR-42.preview.example.dev", origin: "http://" + page,
			wakes: true},
		{name: "no Origin", agent: chrome, notice: noticeNoFetchMetadata},
		{name: "a null Origin", agent: safari, origin: "null", notice: noticeNoFetchMetadata},
		{name: "another site", agent: chrome, origin: "http://attacker.example", notice: noticeElsewhere},
		{name: "another subdomain", agent: chrome, origin: "http://pr-43.preview.example.dev", notice: noticeElsewhere},
		{name: "another port", agent: chrome, host: "localhost:8080", origin: "http://localhost:8081",
			notice: noticeElsewhere},
		{name: "the default port, for a page on another", agent: chrome, host: "localhost:8080",
			origin: "http://localhost", notice: noticeElsewhere},
		{name: "HTTPS on its default port, for a page on port 80", agent: chrome, host: page + ":80",
			origin: "https://" + page, notice: noticeElsewhere},
		{name: "behind a proxy that rewrites Host", agent: chrome, host: "web.preview-42.svc.cluster.local",
			origin: "http://" + page, notice: noticeElsewhere},
		{name: "not a web origin", agent: chrome, origin: "file://", notice: noticeElsewhere},
		{name: "a spider sending the page's Origin", agent: "python-requests/2.32.3", origin: "http://" + page,
			turnedAway: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port := freePort(t)
			s, c := newTestServer(t, nil, askingWorkload(port))
			stop := run(t, s)
			host := cmp.Or(tt.host, page)
			headers := ""
			if tt.origin != "" {
				headers = "Origin: " + tt.origin + "\r\n"
			}

			resp, body := click(t, port, clickAt(host, askedPage(t, port), tt.agent, headers))

			stop()
			switch {
			case tt.wakes:
				assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "the waking-up page")
				assert.Contains(t, body, "Waking up")
				assert.NotEmpty(t, lastRequest(t, c))
				return
			case tt.turnedAway:
				assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
				assert.Contains(t, body, asleepMessage)
			default:
				status := http.StatusOK
				if tt.notice == noticeExpired || tt.notice == noticeElsewhere {
					status = http.StatusForbidden
				}
				assert.Equal(t, status, resp.StatusCode, "the asleep page again")
				tokenIn(t, body)
				assert.Contains(t, body, shownTitle(tt.notice))
			}
			assert.Empty(t, lastRequest(t, c), "nothing wakes it")
		})
	}
}

// Anything but a person's browser, and the probes and hidden files the
// doorman knows, gets a 503 saying it's asleep and how to wake it, and
// nothing wakes: crawlers, scanners and API clients on a public URL can't
// keep it awake.
func TestServer_AskingTurnsAwayAllButAPerson(t *testing.T) {
	tests := []struct {
		name     string
		request  string
		wantJSON bool
		closed   bool
	}{
		{name: "curl", request: "GET / HTTP/1.1\r\nHost: pr-42.preview.example.dev\r\nUser-Agent: curl/8.7.1\r\nAccept: */*\r\n\r\n"},
		{name: "Googlebot asking for a page",
			request: "GET / HTTP/1.1\r\nHost: pr-42.preview.example.dev\r\nAccept: text/html\r\nUser-Agent: Mozilla/5.0 " +
				"(compatible; Googlebot/2.1; +http://www.google.com/bot.html)\r\n\r\n"},
		{name: "a link previewer",
			request: "GET / HTTP/1.1\r\nHost: pr-42.preview.example.dev\r\nAccept: text/html\r\nUser-Agent: " +
				"facebookexternalhit/1.1\r\n\r\n"},
		{name: "a headless browser", request: "GET / HTTP/1.1\r\nHost: pr-42.preview.example.dev\r\nAccept: text/html\r\n" +
			"Sec-Fetch-Mode: navigate\r\nUser-Agent: Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) " +
			"HeadlessChrome/141.0.0.0 Safari/537.36\r\n\r\n"},
		{name: "a scanner", request: "GET /wp-login.php HTTP/1.1\r\nHost: 203.0.113.9\r\nUser-Agent: " +
			"Mozilla/5.0 zgrab/0.x\r\n\r\n"},
		{name: "a browser's HEAD", request: "HEAD /orders HTTP/1.1\r\nHost: pr-42.preview.example.dev\r\nUser-Agent: " +
			chrome + "\r\nAccept: text/html\r\n\r\n"},
		{name: "a browser's script fetching JSON", request: "GET /api/orders HTTP/1.1\r\nHost: pr-42.preview.example.dev\r\n" +
			"User-Agent: " + chrome + "\r\nAccept: application/json\r\nSec-Fetch-Mode: cors\r\n\r\n", wantJSON: true},
		{name: "an API client's POST", request: "POST /api/orders HTTP/1.1\r\nHost: api\r\nUser-Agent: Go-http-client/1.1\r\n" +
			"Content-Type: application/json\r\nAccept: application/json\r\nContent-Length: 2\r\n\r\n{}", wantJSON: true},
		{name: "a form that isn't the button's", request: "POST /login HTTP/1.1\r\nHost: api\r\nUser-Agent: python-requests/2.32\r\n" +
			"Content-Type: application/x-www-form-urlencoded\r\nContent-Length: 9\r\n\r\nuser=root"},
		{name: "a websocket", request: "GET /ws HTTP/1.1\r\nHost: api\r\nUser-Agent: " + chrome + "\r\nUpgrade: websocket\r\n" +
			"Connection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n"},
		{name: "Postgres", request: "\x00\x00\x00\x08\x04\xd2\x16\x2f", closed: true},
		{name: "TLS", request: "\x16\x03\x01\x02\x00\x01\x00\x01\xfc\x03\x03", closed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port := freePort(t)
			s, c := newTestServer(t, nil, askingWorkload(port))
			stop := run(t, s)
			before := wakes(resultTurnedAway)

			conn := dial(t, port)
			send(t, conn, tt.request)
			if tt.closed {
				closedWithin(t, conn)
			} else {
				resp, body := readResponseTo(t, conn, strings.Fields(tt.request)[0])
				assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
				assert.Equal(t, "300", resp.Header.Get("Retry-After"))
				assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
				if strings.HasPrefix(tt.request, "HEAD") {
					assert.Empty(t, body)
				} else {
					assertSaysHowToWake(t, resp, body, tt.wantJSON)
				}
			}

			stop()
			assert.Empty(t, lastRequest(t, c), "nothing wakes it")
			assert.Equal(t, before+1, wakes(resultTurnedAway))
		})
	}
}

// readResponseTo reads the response to a request of method, which for HEAD
// has headers alone.
func readResponseTo(t *testing.T, conn net.Conn, method string) (*http.Response, string) {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: method})
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, string(body)
}

func assertSaysHowToWake(t *testing.T, resp *http.Response, body string, wantJSON bool) {
	t.Helper()
	if wantJSON {
		assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
		var answer struct {
			Status, Message, Docs string
			Wake                  []string
		}
		require.NoError(t, json.Unmarshal([]byte(body), &answer), body)
		assert.Equal(t, "asleep", answer.Status)
		assert.Len(t, answer.Wake, 3, "each way to wake it")
		body = answer.Message + " " + strings.Join(answer.Wake, " ") + " " + answer.Docs
	} else {
		assert.Equal(t, "text/plain; charset=utf-8", resp.Header.Get("Content-Type"))
		assert.Equal(t, "This environment is asleep. It was paused because it looked unused, to save money.\n"+
			"Requests don't wake it. To wake it, do one of these, then try again:\n"+
			"- Open it in a browser and press Wake it.\n"+
			"- Run kubectl napwake wake NAME -n NAMESPACE.\n"+
			"- From tooling, set the annotation napwake.dev/wake-requested to a new value on its ManagedWorkload.\n"+
			"More at https://docs.napwake.dev/guides/preview-environments/\n", body)
	}
	for _, how := range []string{"Requests don't wake it", "Wake it", "napwake.dev/wake-requested",
		"kubectl napwake wake", "https://docs.napwake.dev/guides/preview-environments/"} {
		assert.Contains(t, body, how)
	}
	assert.NotContains(t, body, "dev/api", "it names nothing inside the cluster")
}

// Probes, health checks and hidden files are answered as for a workload
// that wakes on any request: the asking is for what's left.
func TestServer_AskingAnswersProbesAsBefore(t *testing.T) {
	tests := []struct {
		name     string
		request  string
		want     int
		closeNow bool
	}{
		{name: "a TCP health check", closeNow: true},
		{name: "a kubelet probe", request: "GET /healthz HTTP/1.1\r\nHost: 10.0.0.1\r\nUser-Agent: kube-probe/1.31\r\n\r\n",
			want: http.StatusServiceUnavailable},
		{name: "Google's load balancer", request: "GET /healthz HTTP/1.1\r\nHost: 10.0.0.1\r\nUser-Agent: GoogleHC/1.0\r\n\r\n",
			want: http.StatusOK},
		{name: "a hidden file", request: "GET /.env HTTP/1.1\r\nHost: pr-42.preview.example.dev\r\nUser-Agent: " + chrome +
			"\r\nAccept: text/html\r\n\r\n", want: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port := freePort(t)
			s, c := newTestServer(t, nil, askingWorkload(port))
			stop := run(t, s)
			before := wakes(resultIgnored)

			conn := dial(t, port)
			if tt.closeNow {
				require.NoError(t, conn.Close())
				// The dial returns before the doorman accepts, so stopping
				// now could close the listener on it uncounted.
				require.Eventually(t, func() bool { return wakes(resultIgnored) == before+1 },
					5*time.Second, 10*time.Millisecond)
			} else {
				send(t, conn, tt.request)
				resp, body := readResponse(t, conn)
				assert.Equal(t, tt.want, resp.StatusCode)
				assert.Empty(t, body)
			}

			stop()
			assert.Equal(t, before+1, wakes(resultIgnored), "counted as before")
			assert.Empty(t, lastRequest(t, c))
		})
	}
}

// A workload is woken by a click at most once a minute, by any replica, so
// a bot that clicks costs at most a wake. One woken by a request moments
// before its pause asks the person to try again shortly.
func TestServer_ClickWakesAreRateLimited(t *testing.T) {
	tests := []struct {
		name      string
		stampedAt time.Duration
		wakes     bool
	}{
		{name: "stamped half a minute ago", stampedAt: -30 * time.Second},
		{name: "stamped two minutes ago", stampedAt: -2 * time.Minute, wakes: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port := freePort(t)
			w := askingWorkload(port)
			stamp := time.Now().Add(tt.stampedAt).UTC().Format(time.RFC3339)
			w.Annotations = map[string]string{v1alpha1.AnnotationLastRequest: stamp}
			w.Status.Pause.WakeAnnotations.Workload = map[string]string{v1alpha1.AnnotationLastRequest: stamp}
			s, c := newTestServer(t, nil, w)
			stop := run(t, s)

			resp, body := click(t, port, clickRequest(askedPage(t, port)))

			stop()
			if tt.wakes {
				assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "the waking-up page")
				assert.NotEqual(t, stamp, lastRequest(t, c))
				return
			}
			assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode, "asked again")
			assert.Equal(t, "60", resp.Header.Get("Retry-After"), "when a click may wake it")
			assert.Contains(t, body, template.HTMLEscapeString(noticeLimited.text))
			assert.Equal(t, stamp, lastRequest(t, c), "no new wake")
		})
	}
}

// A click reaches whichever doorman replica its connection lands on, so
// every replica signs with the key in the shared Secret: the first fills
// it, and the others use it.
func TestServer_ReplicasShareTheWakeKey(t *testing.T) {
	port := freePort(t)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "napwake-doorman-wake-key", Namespace: "napwake-system"}}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(askingWorkload(port), secret,
		apiPod("127.0.0.1")).Build()
	key := client.ObjectKeyFromObject(secret)
	first := NewServer(c, nil, Options{WakeKeySecret: key})
	second := NewServer(c, nil, Options{WakeKeySecret: key})

	require.NoError(t, first.loadWakeKey(context.Background()))
	require.NoError(t, second.loadWakeKey(context.Background()))

	var filled corev1.Secret
	require.NoError(t, c.Get(context.Background(), key, &filled))
	assert.Len(t, filled.Data[WakeKeyField], wakeKeySize, "filled with a key")
	scope := tokenScope{workload: apiKey, uid: "uid-api", began: time.Now().Truncate(time.Second)}
	token := first.wakeKeys.sign(scope, time.Now().Add(tokenTTL))
	assert.True(t, second.wakeKeys.valid(token, scope, time.Now()), "a page shown by one is clicked through another")
}

// A key that changes, as one deleted and filled again does, doesn't fail a
// click on a page shown just before.
func TestWakeKeys_APageShownBeforeTheKeyChangedStillWorks(t *testing.T) {
	keys := newWakeKeys()
	scope := tokenScope{workload: apiKey, uid: "uid-api", began: time.Now().Truncate(time.Second)}
	token := keys.sign(scope, time.Now().Add(tokenTTL))

	keys.use(newWakeKey(), time.Now())

	assert.True(t, keys.valid(token, scope, time.Now()))
	keys.use(newWakeKey(), time.Now())
	assert.False(t, keys.valid(token, scope, time.Now()), "but not two keys on")
}

// Once the key changes, the one before it passes only as long as a token
// signed with it could last, so a key that leaked stops working once the
// Secret is emptied, rather than for as long as the doorman runs.
func TestWakeKeys_RetireTheKeyBefore(t *testing.T) {
	keys := newWakeKeys()
	leaked := &wakeKeys{current: keys.current}
	scope := tokenScope{workload: apiKey, uid: "uid-api", began: time.Now().Truncate(time.Second)}
	changed := time.Now()
	keys.use(newWakeKey(), changed)

	soon := changed.Add(tokenTTL)
	assert.True(t, keys.valid(leaked.sign(scope, soon.Add(time.Minute)), scope, soon),
		"a page shown before the change works for as long as its token lasts")
	late := changed.Add(tokenTTL + tokenSkew + time.Second)
	assert.False(t, keys.valid(leaked.sign(scope, late.Add(time.Minute)), scope, late),
		"the key before signs nothing that passes once its tokens could all have expired")
}

// A token is good for its whole lifetime and not a moment longer, and one
// claiming a lifetime longer than any doorman gives is forged.
func TestWakeKeys_Lifetime(t *testing.T) {
	keys := newWakeKeys()
	scope := tokenScope{workload: apiKey, uid: "uid-api", began: time.Now().Truncate(time.Second)}
	now := time.Now()
	issued := keys.sign(scope, now.Add(tokenTTL))

	assert.True(t, keys.valid(issued, scope, now.Add(tokenTTL-time.Second)))
	assert.False(t, keys.valid(issued, scope, now.Add(tokenTTL+time.Second)))
	assert.False(t, keys.valid(keys.sign(scope, now.Add(24*time.Hour)), scope, now), "longer than any doorman signs")
	raw, err := base64.RawURLEncoding.DecodeString(issued)
	require.NoError(t, err)
	for _, i := range []int{0, 1, len(raw) - 1} {
		tampered := slices.Clone(raw)
		tampered[i] ^= 1
		assert.False(t, keys.valid(base64.RawURLEncoding.EncodeToString(tampered), scope, now), "byte %d changed", i)
	}
	padded := []byte(issued)
	padded[len(padded)-1] = base64URLWithLowBit(padded[len(padded)-1])
	assert.False(t, keys.valid(string(padded), scope, now), "only one spelling of a token passes")
	half := len(issued) / 2
	for _, extra := range []string{"\r\n", "\n", "\r", "=", " ", "+", "/", "%2D"} {
		spelling := issued[:half] + extra + issued[half:]
		assert.False(t, keys.valid(spelling, scope, now), "only one spelling of a token passes, not with %q", extra)
	}
	assert.False(t, keys.valid(issued+"\n", scope, now), "nor with a line end after it")
	assert.False(t, keys.valid(issued+"==", scope, now), "nor padded")
}

// base64URLWithLowBit is the base64url digit c's value with its lowest bit
// flipped, which in a token's last digit is padding a lenient decoder
// ignores.
func base64URLWithLowBit(c byte) byte {
	const digits = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	return digits[strings.IndexByte(digits, c)^1]
}

// Every browser in use starts its User-Agent with Mozilla/, and so do many
// crawlers, which name themselves after it.
func TestLooksLikeBrowser(t *testing.T) {
	browsers := []string{
		chrome,
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:144.0) Gecko/20100101 Firefox/144.0",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) " +
			"Version/18.6 Mobile/15E148 Safari/604.1",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 " +
			"Safari/537.36 Edg/141.0.0.0",
		"Mozilla/5.0 (Linux; Android 9; CUBOT X19) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 " +
			"Mobile Safari/537.36",
	}
	for _, agent := range browsers {
		assert.True(t, looksLikeBrowser(agent), agent)
	}
	others := []string{
		"", "curl/8.7.1", "Wget/1.24.5", "python-requests/2.32.3", "Go-http-client/1.1", "PostmanRuntime/7.43.0",
		"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
		"Mozilla/5.0 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; GPTBot/1.2; +https://openai.com/gptbot)",
		"Mozilla/5.0 (Linux; Android 6.0.1; Nexus 5X Build/MMB29P) AppleWebKit/537.36 (KHTML, like Gecko) " +
			"Chrome/141.0.0.0 Mobile Safari/537.36 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) " +
			"Version/13.1.1 Safari/605.1.15 (Applebot/0.1; +http://www.apple.com/go/applebot)",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/141.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Linux; Android 11; moto g power (2022)) AppleWebKit/537.36 (KHTML, like Gecko) " +
			"Chrome/141.0.0.0 Mobile Safari/537.36 Chrome-Lighthouse",
		"Mozilla/5.0 zgrab/0.x",
		"Mozilla/5.0 (compatible; Bytespider; spider-feedback@bytedance.com)",
		"facebookexternalhit/1.1 (+http://www.facebook.com/externalhit_uatext.php)",
		"Slackbot-LinkExpanding 1.0 (+https://api.slack.com/robots)",
		"Mozilla/5.0 (compatible; Discordbot/2.0; +https://discordapp.com)",
	}
	for _, agent := range others {
		assert.False(t, looksLikeBrowser(agent), agent)
	}
}

// Requests turned away are counted for each workload's pause, in an
// annotation every replica adds to about once a minute rather than once a
// request, and a new pause starts the count again.
func TestServer_CountsTheRequestsItTurnsAway(t *testing.T) {
	port := freePort(t)
	w := askingWorkload(port)
	s, c := newTestServer(t, nil, w)
	s.awayFlush = time.Hour
	stop := run(t, s)
	curl := "GET / HTTP/1.1\r\nHost: pr-42.preview.example.dev\r\nUser-Agent: curl/8.7.1\r\n\r\n"

	for range 3 {
		resp, _ := click(t, port, curl)
		require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	}
	_, _ = click(t, port, personGET)
	assert.Empty(t, turnedAwayOf(t, c), "nothing written per request")
	stop()

	got := turnedAwayOf(t, c)
	assert.Equal(t, int64(3), got.Requests, "a person asked isn't turned away")
	assert.True(t, got.PausedAt.Equal(&metav1.Time{Time: pausedAt(w)}), "counted for the pause")

	another := NewServer(c, nil, Options{})
	another.turnedAway(route{workload: apiKey, pausedAt: pausedAt(w)})
	another.flushTurnedAway(context.Background())
	assert.Equal(t, int64(4), turnedAwayOf(t, c).Requests, "another replica's add to it")

	another.turnedAway(route{workload: apiKey, pausedAt: pausedAt(w).Add(-24 * time.Hour)})
	another.flushTurnedAway(context.Background())
	assert.Equal(t, int64(4), turnedAwayOf(t, c).Requests, "an earlier pause's is dropped")

	next := pausedAt(w).Add(2 * time.Hour)
	another.turnedAway(route{workload: apiKey, pausedAt: next})
	another.flushTurnedAway(context.Background())
	got = turnedAwayOf(t, c)
	assert.Equal(t, int64(1), got.Requests, "a new pause starts again")
	assert.True(t, got.PausedAt.Equal(&metav1.Time{Time: next}))
}

// A request turned away while the workload is still pausing, before its
// pause has a time to be counted under, counts for that pause once it has
// one; and nothing counts for a pause called off before it had a time
// (P57).
func TestServer_CountsWhatItTurnsAwayBeforeThePauseHasATime(t *testing.T) {
	ctx := context.Background()
	w := askingWorkload(freePort(t))
	began := time.Now().Add(-time.Minute).Truncate(time.Second).UTC()
	w.Status.Phase = v1alpha1.PhasePausing
	w.Status.Pause = &v1alpha1.PauseStatus{PreviousReplicas: 2, StartedAt: &metav1.Time{Time: began},
		WakeAnnotations: &v1alpha1.WakeAnnotations{}}
	s, c := newTestServer(t, nil, w)

	s.turnedAway(route{workload: apiKey, began: began})
	s.turnedAway(route{workload: apiKey, began: began})
	s.flushTurnedAway(ctx)
	assert.Zero(t, turnedAwayOf(t, c).Requests, "nothing to count them under yet")

	paused := metav1.NewTime(time.Now().Truncate(time.Second).UTC())
	require.NoError(t, c.Get(ctx, apiKey, w))
	w.Status.Phase, w.Status.Pause.PausedAt = v1alpha1.PhasePaused, &paused
	require.NoError(t, c.Update(ctx, w))
	s.turnedAway(route{workload: apiKey, began: began, pausedAt: paused.Time})
	s.flushTurnedAway(ctx)
	got := turnedAwayOf(t, c)
	assert.Equal(t, int64(3), got.Requests, "those turned away as it paused count too")
	assert.True(t, got.PausedAt.Equal(&paused), "counted for the pause")

	calledOff := began.Add(time.Hour)
	s.turnedAway(route{workload: apiKey, began: calledOff})
	require.NoError(t, c.Get(ctx, apiKey, w))
	w.Status.Phase, w.Status.Pause = v1alpha1.PhaseRunning, nil
	require.NoError(t, c.Update(ctx, w))
	s.flushTurnedAway(ctx)
	assert.Equal(t, int64(3), turnedAwayOf(t, c).Requests, "a pause called off counts nothing")
	assert.Empty(t, s.away, "and isn't kept for later")
}

// A count is always written with the pause it counts, and one in the
// annotation without a pause, or with one later than the workload's
// current pause, as one edited by hand might be, is started again rather
// than added to, or kept in place of this pause's.
func TestWithTurnedAway(t *testing.T) {
	paused := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	count := func(at time.Time, n int64) string {
		return fmt.Sprintf(`{"pausedAt":%q,"requests":%d}`, at.Format(time.RFC3339), n)
	}
	tests := []struct {
		name    string
		current string
		latest  time.Time
		add     awayCount
		want    int64
		writes  bool
	}{
		{name: "none yet", latest: paused, add: awayCount{pausedAt: paused, requests: 3}, want: 3, writes: true},
		{name: "this pause's", current: count(paused, 4), latest: paused, add: awayCount{pausedAt: paused, requests: 3},
			want: 7, writes: true},
		{name: "a later pause's, since begun", current: count(paused.Add(time.Hour), 4), latest: paused.Add(time.Hour),
			add: awayCount{pausedAt: paused, requests: 3}},
		{name: "one without a pause", current: `{"requests":40}`, latest: paused,
			add: awayCount{pausedAt: paused, requests: 3}, want: 3, writes: true},
		{name: "one of a pause still to come", current: count(paused.Add(48*time.Hour), 40), latest: paused,
			add: awayCount{pausedAt: paused, requests: 3}, want: 3, writes: true},
		{name: "counted without a pause", latest: paused, add: awayCount{requests: 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, writes := withTurnedAway(tt.current, tt.add, tt.latest)
			assert.Equal(t, tt.writes, writes)
			if writes {
				assert.Equal(t, tt.want, got.Requests)
				assert.True(t, got.PausedAt.Equal(&metav1.Time{Time: tt.add.pausedAt}))
			}
		})
	}
}

func turnedAwayOf(t *testing.T, c client.Client) v1alpha1.TurnedAway {
	t.Helper()
	var w v1alpha1.ManagedWorkload
	require.NoError(t, c.Get(context.Background(), apiKey, &w))
	var count v1alpha1.TurnedAway
	if value, ok := w.Annotations[v1alpha1.AnnotationTurnedAway]; ok {
		require.NoError(t, json.Unmarshal([]byte(value), &count))
	}
	return count
}

// A proxy in front of the doorman may send the click's form chunked, as
// ingress-nginx does a body it doesn't buffer, from a browser speaking
// HTTP/2 to it without a Content-Length. It still wakes the workload; a
// chunked body too large to be the button's doesn't.
func TestServer_AChunkedClickWakesIt(t *testing.T) {
	tests := []struct {
		name  string
		pad   int
		wakes bool
	}{
		{name: "the button's form", wakes: true},
		{name: "a body too large to be the button's", pad: 2 * maxClickBody},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port := freePort(t)
			s, c := newTestServer(t, nil, askingWorkload(port))
			stop := run(t, s)
			form := url.Values{wakeField: {askedPage(t, port)}, "pad": {strings.Repeat("a", tt.pad)}}.Encode()
			half := len(form) / 2
			request := "POST /orders?id=7 HTTP/1.1\r\nHost: pr-42.preview.example.dev\r\nUser-Agent: " + chrome + "\r\n" +
				"Content-Type: application/x-www-form-urlencoded\r\n" + chromeClick + "Transfer-Encoding: chunked\r\n\r\n" +
				fmt.Sprintf("%x\r\n%s\r\n%x\r\n%s\r\n0\r\n\r\n", half, form[:half], len(form)-half, form[half:])

			resp, _ := click(t, port, request)

			stop()
			if tt.wakes {
				assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "the waking-up page")
				assert.NotEmpty(t, lastRequest(t, c))
				return
			}
			assert.Equal(t, http.StatusOK, resp.StatusCode, "the asleep page again")
			assert.Empty(t, lastRequest(t, c))
		})
	}
}

// HTTP/2 spoken to the doorman itself, as a proxy told to use it for its
// backends does, can't be asked: it wakes nothing, and is turned away. A
// browser's HTTP/2 to the proxy, which speaks HTTP/1.1 on, is asked as
// usual.
func TestServer_AskingTurnsAwayHTTP2(t *testing.T) {
	port := freePort(t)
	s, c := newTestServer(t, nil, askingWorkload(port))
	stop := run(t, s)
	before := wakes(resultTurnedAway)

	conn := dial(t, port)
	send(t, conn, "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n\x00\x00\x00\x04\x00\x00\x00\x00\x00")
	_, _ = io.Copy(io.Discard, conn)

	stop()
	assert.Empty(t, lastRequest(t, c), "nothing wakes it")
	assert.Equal(t, before+1, wakes(resultTurnedAway))
}

// The click's form can arrive after its headers, as a slow link or a proxy
// may send it.
func TestServer_ReadsAClickSentInPieces(t *testing.T) {
	port := freePort(t)
	_, c := startServer(t, askingWorkload(port))
	request := clickRequest(askedPage(t, port))
	headers, body, _ := strings.Cut(request, "\r\n\r\n")

	conn := dial(t, port)
	send(t, conn, headers+"\r\n\r\n")
	time.Sleep(50 * time.Millisecond) // the body arrives in a later read
	send(t, conn, body)
	resp, _ := readResponse(t, conn)

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "the waking-up page")
	assert.NotEmpty(t, lastRequest(t, c))
}

// The doorman fronts a workload from the moment its pause starts. Until its
// pods have gone, a browser's page load passes to them as any request does,
// and once none is Ready it gets the page it would get were the workload
// paused: the asleep page, whose click calls the pause off, for one that
// asks first, and the waking-up page, which calls it off at once, for one
// that doesn't.
func TestServer_APageLoadAsItPausesGetsWhatAPausedWorkloadGives(t *testing.T) {
	pausing := func(port int32, asks bool) *v1alpha1.ManagedWorkload {
		w := pausingWorkload(port)
		w.UID = "uid-api"
		if asks {
			w.Spec.Wake.Mode = v1alpha1.WakeOnAsk
		}
		return w
	}

	t.Run("a pod still Ready", func(t *testing.T) {
		for _, asks := range []bool{true, false} {
			port := freePort(t)
			_, c := startServer(t, pausing(port, asks), readySlice(echoPort(t)))

			conn := dial(t, port)
			send(t, conn, personGET)
			echoed(t, conn, personGET)
			if asks {
				require.Never(t, func() bool { return lastRequest(t, c) != "" }, 300*time.Millisecond,
					20*time.Millisecond, "only a click calls off the pause of a workload that asks")
			} else {
				require.Eventually(t, func() bool { return lastRequest(t, c) != "" }, 5*time.Second,
					20*time.Millisecond, "a page load passed on calls the pause off")
			}
		}
	})

	t.Run("asks, its pods gone", func(t *testing.T) {
		port := freePort(t)
		_, c := startServer(t, pausing(port, true))

		token := askedPage(t, port)
		assert.Empty(t, lastRequest(t, c), "the asleep page wakes nothing")

		resp, body := click(t, port, clickRequest(token))
		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, body)
		assert.Contains(t, body, "<title>Waking up pr-42.preview.example.dev</title>")
		assert.NotEmpty(t, lastRequest(t, c), "the click calls the pause off")
	})

	t.Run("wakes on any request, its pods gone", func(t *testing.T) {
		port := freePort(t)
		_, c := startServer(t, pausing(port, false))

		resp, body := click(t, port, personGET)
		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, body)
		assert.Contains(t, body, "<title>Waking up pr-42.preview.example.dev</title>")
		require.Eventually(t, func() bool { return lastRequest(t, c) != "" }, 5*time.Second, 20*time.Millisecond,
			"the page load calls the pause off, as the page is shown")
	})
}

// A click can come while the doorman's cache shows the workload awake with
// its route still listed: its pause was called off, or it's Running and the
// operator hasn't yet taken the route down. It's awake, or waking, so the
// waking-up page follows, which reloads into the app, rather than a notice
// that it didn't wake, and it isn't counted as a wake that failed.
func TestServer_AClickOnAWorkloadTheCacheShowsAwakeGetsTheWakingUpPage(t *testing.T) {
	port := freePort(t)
	_, c := startServer(t, askingWorkload(port))
	token := askedPage(t, port)
	failed, paged := wakes(resultError), wakes(resultPage)

	var live v1alpha1.ManagedWorkload
	require.NoError(t, c.Get(context.Background(), apiKey, &live))
	live.Status.Phase = v1alpha1.PhaseRunning
	require.NoError(t, c.Update(context.Background(), &live))

	resp, body := click(t, port, clickRequest(token))

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, body)
	assert.Contains(t, body, "<title>Waking up pr-42.preview.example.dev</title>")
	assert.NotContains(t, body, "It didn&#39;t wake")
	require.Eventually(t, func() bool { return wakes(resultPage) == paged+1 }, 5*time.Second, 20*time.Millisecond,
		"counted as a browser shown the waking-up page, once the connection is done")
	assert.Equal(t, failed, wakes(resultError), "not a wake that failed")
	assert.Empty(t, lastRequest(t, c), "an awake workload isn't stamped")
}

// synced waits for the server to sync its routes once more, so they show
// what the test has just written.
func synced(t *testing.T, s *Server) {
	t.Helper()
	since := time.Now().UnixNano()
	require.Eventually(t, func() bool { return s.lastSync.Load() > since }, 5*time.Second, 20*time.Millisecond)
}

// pausingAsks is a workload that asks first, its pause begun at began and
// under way, its pods gone.
func pausingAsks(port int32, began time.Time) *v1alpha1.ManagedWorkload {
	w := askingWorkload(port)
	w.Status.Phase = v1alpha1.PhasePausing
	w.Status.Pause.StartedAt = ptr.To(metav1.NewTime(began))
	w.Status.Pause.PausedAt = nil
	return w
}

// An asleep page shown as the workload pauses is for that pause from its
// first moment, so its button wakes the workload once it's Paused, rather
// than being refused as expired.
func TestServer_APageShownAsItPausesWakesItOncePaused(t *testing.T) {
	port := freePort(t)
	s, c := startServer(t, pausingAsks(port, time.Now().Add(-time.Minute)))
	token := askedPage(t, port)

	var live v1alpha1.ManagedWorkload
	require.NoError(t, c.Get(context.Background(), apiKey, &live))
	live.Status.Phase = v1alpha1.PhasePaused
	live.Status.Pause.PausedAt = ptr.To(metav1.Now())
	require.NoError(t, c.Update(context.Background(), &live))
	synced(t, s)

	resp, body := click(t, port, clickRequest(token))

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "the waking-up page")
	assert.Contains(t, body, "<title>Waking up pr-42.preview.example.dev</title>")
	assert.NotEmpty(t, lastRequest(t, c), "the click wakes it")
}

// A page's button is good for its own pause alone: one shown as an earlier
// pause began wakes nothing in a later one, though neither had scaled the
// workload to zero when it was shown.
func TestServer_APageFromAnEarlierPauseIsRefusedInALaterOne(t *testing.T) {
	port := freePort(t)
	first := time.Now().Add(-5 * time.Minute)
	s, c := startServer(t, pausingAsks(port, first))
	token := askedPage(t, port)

	var live v1alpha1.ManagedWorkload
	require.NoError(t, c.Get(context.Background(), apiKey, &live))
	live.Status.Pause.StartedAt = ptr.To(metav1.NewTime(first.Add(3 * time.Minute)))
	require.NoError(t, c.Update(context.Background(), &live))
	synced(t, s)

	resp, body := click(t, port, clickRequest(token))

	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "refused as a page left open")
	assert.Contains(t, body, shownTitle(noticeExpired))
	tokenIn(t, body)
	assert.Empty(t, lastRequest(t, c), "nothing wakes it")
}

// A token for no pause, as one signed for a route whose pause the cache
// hasn't recorded, is good for none.
func TestWakeKeys_ATokenForNoPauseIsRefused(t *testing.T) {
	keys := newWakeKeys()
	none := tokenScope{workload: apiKey, uid: "uid-api"}
	now := time.Now()

	assert.False(t, keys.valid(keys.sign(none, now.Add(tokenTTL)), none, now))
}

// A pause is named by when it began, and one an operator from before
// status.pause.startedAt began, by when it scaled the workload to zero.
func TestPauseBegan(t *testing.T) {
	began := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	scaled := began.Add(12 * time.Second)
	tests := []struct {
		name  string
		pause *v1alpha1.PauseStatus
		want  time.Time
	}{
		{name: "pausing", pause: &v1alpha1.PauseStatus{StartedAt: ptr.To(metav1.NewTime(began.Add(300 * time.Millisecond)))},
			want: began},
		{name: "paused", pause: &v1alpha1.PauseStatus{StartedAt: ptr.To(metav1.NewTime(began)),
			PausedAt: ptr.To(metav1.NewTime(scaled))}, want: began},
		{name: "paused by an earlier operator", pause: &v1alpha1.PauseStatus{PausedAt: ptr.To(metav1.NewTime(scaled))},
			want: scaled},
		{name: "no pause"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &v1alpha1.ManagedWorkload{Status: v1alpha1.ManagedWorkloadStatus{Pause: tt.pause}}
			assert.True(t, tt.want.Equal(pauseBegan(w)), "got %s", pauseBegan(w))
		})
	}
}

// Once the doorman's routes show the workload Running, its pause record
// gone, before the operator takes the route down, a click from a page shown
// while it was paused gets the waking-up page too: it's awake, and there's
// no pause left for a button to be good for.
func TestServer_AClickOnARouteSyncedAwakeGetsTheWakingUpPage(t *testing.T) {
	port := freePort(t)
	s, c := startServer(t, askingWorkload(port))
	token := askedPage(t, port)
	failed, paged := wakes(resultError), wakes(resultPage)

	var live v1alpha1.ManagedWorkload
	require.NoError(t, c.Get(context.Background(), apiKey, &live))
	live.Status.Phase, live.Status.Pause = v1alpha1.PhaseRunning, nil
	require.NoError(t, c.Update(context.Background(), &live))
	synced(t, s)

	resp, body := click(t, port, clickRequest(token))

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, body)
	assert.Contains(t, body, "<title>Waking up pr-42.preview.example.dev</title>")
	require.Eventually(t, func() bool { return wakes(resultPage) == paged+1 }, 5*time.Second, 20*time.Millisecond,
		"counted as a browser shown the waking-up page, once the connection is done")
	assert.Equal(t, failed, wakes(resultError))
	assert.Empty(t, lastRequest(t, c), "an awake workload isn't stamped")
}

// A click this replica stamped in one pause doesn't make the next look
// waking, though it begins within wakeStampInterval: a person opening the
// workload gets the asleep page, not a waking-up page with nothing waking,
// and a click within the minute is told to try again in a minute.
func TestServer_AClickInOnePauseDoesntMakeTheNextLookWaking(t *testing.T) {
	port := freePort(t)
	s, c := startServer(t, pausingAsks(port, time.Now().Add(-time.Minute)))
	resp, _ := click(t, port, clickRequest(askedPage(t, port)))
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "the first pause called off")

	nextPauseBegins(t, c)
	synced(t, s)

	token := askedPage(t, port)
	resp, body := click(t, port, clickRequest(token))
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode, body)
	assert.Contains(t, body, shownTitle(noticeLimited))
}
