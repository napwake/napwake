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
	"context"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
)

const (
	// wakeField is the form field the asleep page's Wake it button posts
	// its token in. The button posts back to the page's own address, not
	// to a path of the doorman's: behind an ingress that routes a path
	// prefix to the workload, only that prefix reaches it, and the waking
	// page that answers the click then reloads the page the person asked
	// for. A POST carrying this field is the only request that wakes a
	// workload that asks.
	wakeField = "hybernate-wake"

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
// ones, as counted by hybernate_doorman_wakes_total.
const (
	// resultAsked is a person's browser shown the asleep page.
	resultAsked = "asked"
	// resultTurnedAway is anything else answered that the workload is
	// asleep, and not woken.
	resultTurnedAway = "turned_away"
)

// The notices the asleep page gives when a click woke nothing.
const (
	noticeExpired = "That page had expired, so nothing woke. Click Wake it again."
	noticeLimited = "It was asked to wake less than a minute ago. Try again in a minute."
	noticeFailed  = "It couldn't be woken just now. Try again in a moment."
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
		h.serveAsleepPage(ctx, req, "")
		return nil, resultAsked
	}
	h.turnedAway(h.route)
	if req != nil {
		_ = h.reply(func(w io.Writer) error { return writeAsleep(w, req) }) // the caller may already have gone
	}
	return nil, resultTurnedAway
}

// click answers a POST from the asleep page's Wake it button. A token this
// doorman signed for this pause of this workload, not yet expired, wakes it
// as a held request does, and the waking-up page follows. Anything else
// wakes nothing: a browser is asked again, and anything else turned away.
// Once the workload is waking, every click gets the waking-up page.
func (h *heldConn) click(ctx, waitCtx context.Context, req *http.Request) string {
	if h.waking() {
		h.servePage(waitCtx, req)
		return resultPage
	}
	token, posted := h.readClick()
	if !posted || !sameOrigin(req) || !h.wakeKeys.valid(token, h.route.scope(), h.now()) {
		if looksLikeBrowser(req.UserAgent()) {
			notice := noticeExpired
			if !posted {
				notice = ""
			}
			h.serveAsleepPage(ctx, req, notice)
			return resultAsked
		}
		h.turnedAway(h.route)
		_ = h.reply(func(w io.Writer) error { return writeAsleep(w, req) }) // the caller may already have gone
		return resultTurnedAway
	}
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

// isClick reports whether a POST could be the Wake it button's: a small
// form, as the page's is. Its token is read and checked next.
func isClick(req *http.Request) bool {
	media, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	return err == nil && media == "application/x-www-form-urlencoded" &&
		req.ContentLength > 0 && req.ContentLength <= maxClickBody
}

// sameOrigin reports whether a browser says the click came from a page of
// the same site and port, as the asleep page's does. A browser that doesn't
// say, one from before 2020, is judged by its token alone; so is anything
// else, which a token from a GET of the page lets click too.
func sameOrigin(req *http.Request) bool {
	site := req.Header.Get("Sec-Fetch-Site")
	return site == "" || site == "same-origin"
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
func (h *heldConn) serveAsleepPage(ctx context.Context, req *http.Request, notice string) {
	data := pageData{
		Address: pageAddress(req),
		Token:   h.wakeKeys.sign(h.route.scope(), h.now().Add(tokenTTL)),
		Notice:  notice,
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
