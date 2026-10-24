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
	"cmp"
	"context"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

const (
	// wakeField is the form field the asleep page's Wake it button posts
	// its token in. The button posts back to the page's own address, not
	// to a path of the doorman's: behind an ingress that routes a path
	// prefix to the workload, only that prefix reaches it, and the waking
	// page that answers the click then reloads the page the person asked
	// for. A POST carrying this field is the only request that wakes a
	// workload that asks.
	wakeField = "napwake-wake"

	// maxClickBody is the most a click's form may hold: the field and its
	// token, with room to spare. A larger body isn't a click.
	maxClickBody = 1 << 10

	// maxClickRead bounds what's read of a click past the first bytes,
	// headers a proxy added included.
	maxClickRead = 64 << 10

	// clickReadTimeout bounds how long the doorman waits for the rest of a
	// click.
	clickReadTimeout = 2 * time.Second

	// clickWakeInterval is how soon after one wake request a click may
	// make another for the same workload, on any replica: a bot that
	// clicks costs at most a wake.
	clickWakeInterval = time.Minute
)

// The results of a connection to a workload that asks, besides the usual
// ones, as counted by napwake_doorman_wakes_total.
const (
	// resultAsked is a person's browser shown the asleep page.
	resultAsked = "asked"
	// resultTurnedAway is anything else answered that the workload is
	// asleep, and not woken.
	resultTurnedAway = "turned_away"
)

// A notice is what the asleep page says when a click woke nothing, and,
// where clicking again won't help, the docs that say how else to wake it.
type notice struct {
	text string
	docs string
}

// The notices the asleep page gives when a click woke nothing.
var (
	noticeExpired = notice{text: "That page had expired, so nothing woke. Click Wake it again."}
	noticeLimited = notice{text: "It was asked to wake less than a minute ago. Try again in a minute."}
	noticeFailed  = notice{text: "It couldn't be woken just now. Try again in a moment."}
	// noticeNoFetchMetadata answers a browser that said nothing of where its
	// click came from; see sameOrigin.
	noticeNoFetchMetadata = notice{
		text: "This browser didn't say the click came from this page, so nothing woke. Wake it with kubectl " +
			"napwake wake, the napwake.dev/wake-requested annotation, or a current browser.",
		docs: asleepDocs,
	}
)

// errClickLimited means a click came too soon after the last wake request.
var errClickLimited = errors.New("woken by request too recently")

// ask handles a connection to a paused workload that asks before waking:
// only a person's click on the asleep page wakes it. Probes, health checks
// and hidden files have been answered already, as for any workload.
func (h *heldConn) ask(ctx, waitCtx context.Context, req *http.Request) (net.Conn, string) {
	if req != nil && req.Method == http.MethodPost && isClick(req) {
		return nil, h.click(ctx, waitCtx, req)
	}
	if h.waking() {
		// Someone has asked already: callers are answered as a workload
		// that wakes on request answers them, short of starting a wake.
		if req != nil && h.route.page && isPageLoad(req) {
			h.servePage(waitCtx, req)
			return nil, resultPage
		}
		h.streamClient()
		return h.await(ctx, waitCtx, nil)
	}
	if req != nil && isPersonLoadingAPage(req) {
		h.serveAsleepPage(ctx, req, notice{})
		return nil, resultAsked
	}
	h.turnedAway(h.route)
	if req != nil {
		_ = h.reply(func(w io.Writer) error { return writeAsleep(w, req) }) // the caller may already have gone
	}
	return nil, resultTurnedAway
}

// click answers a POST from the asleep page's Wake it button. A token this
// doorman signed for this pause of this workload, not yet expired, posted
// by a person's browser from the page, wakes it as a held request does,
// and the waking-up page follows. Anything else wakes nothing: a browser
// is asked again, and anything else turned away. Once the workload is
// waking, every click gets the waking-up page.
func (h *heldConn) click(ctx, waitCtx context.Context, req *http.Request) string {
	if h.waking() {
		h.servePage(waitCtx, req)
		return resultPage
	}
	token, posted := h.readClick()
	if posted && clickedByAPerson(req) && sameOrigin(req) && h.wakeKeys.valid(token, h.route.scope(), h.now()) {
		return h.wakeForClick(ctx, waitCtx, req)
	}
	if !looksLikeBrowser(req.UserAgent()) {
		h.turnedAway(h.route)
		_ = h.reply(func(w io.Writer) error { return writeAsleep(w, req) }) // the caller may already have gone
		return resultTurnedAway
	}
	h.serveAsleepPage(ctx, req, clickNotice(req, posted))
	return resultAsked
}

// clickNotice is what the asleep page says to a browser whose post woke
// nothing. A form of the app's own, posted with no token, gets the page as
// any page load does. A click that doesn't say where it came from is told
// how else to wake the workload, since clicking again won't do it. A
// person's click whose token had expired, or that came from another site,
// is asked to click again; a script's is just shown the page.
func clickNotice(req *http.Request, posted bool) notice {
	switch {
	case !posted:
		return notice{}
	case req.Header.Get("Sec-Fetch-Site") == "" && !namesAnOrigin(req):
		return noticeNoFetchMetadata
	case clickedByAPerson(req):
		return noticeExpired
	default:
		return notice{}
	}
}

// wakeForClick wakes the workload for a person's click, and answers with
// the waking-up page, or the asleep page again saying why it didn't.
func (h *heldConn) wakeForClick(ctx, waitCtx context.Context, req *http.Request) string {
	switch err := h.requestWake(ctx, waitCtx, h.route, h.from); {
	case err == nil:
		h.servePage(waitCtx, req)
		return resultPage
	case errors.Is(err, errClickLimited):
		h.serveAsleepPage(ctx, req, noticeLimited)
		return resultLimited
	case errors.Is(err, errWakeLimited):
		h.serveAsleepPage(ctx, req, noticeFailed)
		return resultLimited
	default:
		h.serveAsleepPage(ctx, req, noticeFailed)
		return resultError
	}
}

// clickedByAPerson reports whether a click comes from a person: from a
// browser, as the asleep page is shown to, and, from a browser that sends
// fetch metadata and marks the navigations a person starts with
// Sec-Fetch-User, marked so. A spider that fetches the page and posts its
// token isn't a browser, and a script submitting the form isn't marked.
// Safari, and every browser on iOS, which runs WebKit, never send that
// mark, and no browser sends fetch metadata over plain HTTP, so their
// clicks are judged without it: by the token, the User-Agent and
// sameOrigin, which every browser's click must pass too.
func clickedByAPerson(req *http.Request) bool {
	agent := req.UserAgent()
	if !looksLikeBrowser(agent) {
		return false
	}
	if req.Header.Get("Sec-Fetch-Site") == "" {
		return true
	}
	if user := req.Header.Get("Sec-Fetch-User"); user != "" {
		return user == "?1"
	}
	return !marksPeoplesNavigations(agent)
}

// marksPeoplesNavigations reports whether a browser sends Sec-Fetch-User
// with the navigations a person starts: Chrome and the browsers built on
// Chromium, such as Edge, and Firefox, but not on iOS, where they're
// WebKit and name themselves otherwise (CriOS, EdgiOS, FxiOS).
func marksPeoplesNavigations(agent string) bool {
	return strings.Contains(agent, "Chrome/") || strings.Contains(agent, "Firefox/")
}

// isClick reports whether a POST could be the Wake it button's: a form,
// small as the page's is, or chunked, as a proxy may send one a browser
// sent it over HTTP/2 without a length; readClick reads no more of that
// than the button's could be. Its token is read and checked next.
func isClick(req *http.Request) bool {
	media, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil || media != "application/x-www-form-urlencoded" {
		return false
	}
	chunked := req.ContentLength == -1 && slices.Equal(req.TransferEncoding, []string{"chunked"})
	return chunked || (req.ContentLength > 0 && req.ContentLength <= maxClickBody)
}

// sameOrigin reports whether the browser says the click came from a page of
// the same origin, the same scheme, host and port, as the asleep page's
// does. Where it sends fetch metadata, to an HTTPS or localhost address, as
// every browser has since Safari 16.4 in March 2023, Sec-Fetch-Site says
// so. Over plain HTTP, where none does, the Origin of the form it posts
// must be the address the page was served at, which the page's
// Referrer-Policy: same-origin lets it send. A click that says neither, as
// browsers from before about 2020 don't, can't be told from a spider's that
// fetched the page and posted its token as a browser, so it wakes nothing,
// and the page says how else to wake it.
func sameOrigin(req *http.Request) bool {
	if site := req.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin"
	}
	return fromPageAddress(req)
}

// namesAnOrigin reports whether a click says what page it came from with
// its Origin, which is "null" from a page whose referrer policy forbids
// saying, or from one with no origin of its own.
func namesAnOrigin(req *http.Request) bool {
	origin := req.Header.Get("Origin")
	return origin != "" && origin != "null"
}

// fromPageAddress reports whether a click's Origin is the address the
// asleep page was served at: the host the browser asked for, which
// pageAddress shows, and its port. Browsers leave a scheme's default port
// out of both, though a proxy may name it in the host.
func fromPageAddress(req *http.Request) bool {
	origin, err := url.Parse(req.Header.Get("Origin"))
	if err != nil || origin.Hostname() == "" {
		return false
	}
	var defaultPort string
	switch origin.Scheme {
	case "http":
		defaultPort = "80"
	case "https":
		defaultPort = "443"
	default:
		return false
	}
	host, port, err := net.SplitHostPort(req.Host)
	if err != nil {
		host, port = strings.TrimSuffix(strings.TrimPrefix(req.Host, "["), "]"), ""
	}
	return strings.EqualFold(origin.Hostname(), host) && cmp.Or(origin.Port(), defaultPort) == cmp.Or(port, defaultPort)
}

// readClick reads the rest of a click, its body, and returns the token it
// posted, if it posted one: a form without one is the app's own.
func (h *heldConn) readClick() (string, bool) {
	_ = h.conn.SetReadDeadline(time.Now().Add(clickReadTimeout)) // a failure just means no deadline
	defer func() { _ = h.conn.SetReadDeadline(time.Time{}) }()   // a failure just leaves the deadline
	rest := io.LimitReader(h.conn, maxClickRead)
	req, err := http.ReadRequest(bufio.NewReader(io.MultiReader(bytes.NewReader(h.head), rest)))
	if err != nil {
		return "", false
	}
	req.Body = http.MaxBytesReader(nil, req.Body, maxClickBody)
	if err := req.ParseForm(); err != nil {
		return "", false
	}
	values := req.PostForm[wakeField]
	if len(values) != 1 {
		return "", false
	}
	return values[0], true
}

// serveAsleepPage answers a person's browser with the asleep page, its Wake
// it button signed for this pause of the workload.
func (h *heldConn) serveAsleepPage(ctx context.Context, req *http.Request, n notice) {
	data := pageData{
		Address:    pageAddress(req),
		Token:      h.wakeKeys.sign(h.route.scope(), h.now().Add(tokenTTL)),
		Notice:     n.text,
		NoticeDocs: n.docs,
	}
	if err := h.reply(func(w io.Writer) error { return writeAsleepPage(w, data) }); err != nil {
		log.FromContext(ctx).V(1).Info("writing asleep page", "error", err.Error(),
			"workload", h.route.workload.Name, "namespace", h.route.workload.Namespace)
	}
}

// waking reports whether the workload has been asked to wake already: the
// route sync found it waking, or this replica stamped it during this pause
// moments ago, too recently for the sync to have seen it.
func (h *heldConn) waking() bool {
	rt := h.currentRoute(h.port, h.route)
	if rt.waking {
		return true
	}
	h.mu.Lock()
	last, ok := h.stamped[rt.workload]
	h.mu.Unlock()
	return ok && last.pausedAt.Equal(rt.pausedAt) && h.now().Sub(last.at) < wakeStampInterval
}

// isWaking reports whether a paused workload has been asked to wake: it's
// Resuming, or a request's stamp is waiting for the operator.
func isWaking(w *v1alpha1.ManagedWorkload) bool {
	switch w.Status.Phase {
	case v1alpha1.PhaseResuming:
		return true
	case v1alpha1.PhasePaused:
		return !needsWake(w)
	default:
		return false
	}
}

// stampedRecently reports whether the workload's last request stamp was
// made within clickWakeInterval, by any replica.
func stampedRecently(w *v1alpha1.ManagedWorkload, now time.Time) bool {
	stamped, err := time.Parse(time.RFC3339Nano, w.Annotations[v1alpha1.AnnotationLastRequest])
	return err == nil && now.Sub(stamped) < clickWakeInterval
}
