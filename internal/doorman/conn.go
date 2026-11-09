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
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	opmetrics "github.com/napwake/napwake/internal/metrics"
)

const (
	dialTimeout = 5 * time.Second

	// silentWake is how long a caller may stay connected without sending
	// anything before it's taken for a client of a protocol where the
	// server speaks first, such as MySQL, and the workload is woken. A TCP
	// health check closes well before it.
	silentWake = 3 * time.Second

	// headTimeout bounds how long the doorman waits for the rest of an HTTP
	// request's headers once the first bytes have arrived.
	headTimeout = 500 * time.Millisecond

	// maxPeek is enough for nearly any request line and headers. Longer
	// ones are judged by what fits (see parseRequest).
	maxPeek = 16 << 10

	// lingerTimeout and maxLinger bound how long, and how much, the doorman
	// reads from a caller it has answered, so that closing doesn't reset
	// the connection before the caller reads the answer.
	lingerTimeout = time.Second
	maxLinger     = 256 << 10

	wakeRetryInterval = 2 * time.Second
	dialRetryInterval = time.Second

	// clientBufferSize is what a held connection reads its caller with. It's
	// small because thousands of connections can be held at once.
	clientBufferSize = 8 << 10

	// proxyBufferSize is what each direction of a proxied connection copies
	// through, for the same reason.
	proxyBufferSize = 8 << 10
)

// The results a connection the doorman accepted ends with, as counted by
// napwake_doorman_wakes_total.
const (
	resultSuccess  = "success"
	resultPage     = "page"
	resultTimeout  = "timeout"
	resultError    = "error"
	resultCanceled = "canceled"
	resultIgnored  = "ignored"
	resultLimited  = "limited"
	resultRefused  = "refused"
	// resultDrained is a connection held for a wake, closed once its
	// route drained with none of the workload's pods Ready: something else
	// took it back to zero as it woke, such as an HPA with minReplicas 0,
	// or it stopped waking on request.
	resultDrained = "drained"
)

// handle holds one connection: it wakes the workload, waits for a Ready pod,
// and passes the connection to it, or closes it once maxWait runs out.
func (s *Server) handle(ctx context.Context, port int32, rt route, conn net.Conn) {
	start := s.now()
	from := remoteAddr(conn)
	logger := log.FromContext(ctx).WithName("doorman").WithValues(
		"workload", rt.workload.Name, "namespace", rt.workload.Namespace, "service", rt.service)

	if !s.admission.hold(from, rt.workload, start) {
		s.record(rt, resultLimited, s.now().Sub(start))
		logger.V(1).Info("refusing connection: too many held", "source", from.String())
		return
	}
	opmetrics.DoormanHeldConnections.Inc()
	held := true
	unhold := func() {
		if held {
			held = false
			s.admission.release(from, rt.workload, s.now())
			opmetrics.DoormanHeldConnections.Dec()
		}
	}
	defer unhold()

	h := &heldConn{Server: s, port: port, route: rt, from: from, conn: conn}
	defer h.close()
	upstream, result := h.hold(ctx)
	waited := s.now().Sub(start)
	if upstream == nil {
		s.record(rt, result, waited)
		if cause, ok := h.unserved(result); ok {
			logger.Info("closing held connection", "result", result, "cause", cause,
				"waited", waited.Round(time.Millisecond).String())
			s.warnUnserved(ctx, rt, cause, waited)
		}
		return
	}

	// One passed on before anything was read from it is counted once its
	// caller sends something. A TCP check, as the operator's of each
	// replica before a pause scales the workload down, asked for nothing.
	fromCaller := h.watched(ctx)
	if h.readNothing() {
		fromCaller = &firstByte{r: fromCaller, sent: func() { s.record(rt, result, waited) }}
	} else {
		s.record(rt, result, waited)
	}
	defer s.admission.unproxy(from, s.now())
	unhold()
	opmetrics.DoormanProxiedConnections.Inc()
	defer opmetrics.DoormanProxiedConnections.Dec()
	s.track(upstream)
	defer s.forget(upstream)
	defer s.proxying(rt)()
	s.proxy(conn, fromCaller, upstream)
}

// routeKey is a route's place: its workload, Service and port.
type routeKey struct {
	workload types.NamespacedName
	service  string
	portName string
}

// proxying counts a connection proxied for rt until the function it returns
// is called, in napwake_doorman_proxied_connections_by_route.
func (s *Server) proxying(rt route) func() {
	key := routeKey{workload: rt.workload, service: rt.service, portName: rt.portName}
	labels := []string{rt.workload.Namespace, rt.workload.Name, rt.service, rt.portName}
	s.mu.Lock()
	s.proxied[key]++
	opmetrics.DoormanRouteProxiedConnections.WithLabelValues(labels...).Set(float64(s.proxied[key]))
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.proxied[key]--; s.proxied[key] > 0 {
			opmetrics.DoormanRouteProxiedConnections.WithLabelValues(labels...).Set(float64(s.proxied[key]))
			return
		}
		delete(s.proxied, key)
		opmetrics.DoormanRouteProxiedConnections.DeleteLabelValues(labels...)
	}
}

func (s *Server) record(rt route, result string, waited time.Duration) {
	s.mu.Lock()
	s.metered[rt.workload] = true
	s.mu.Unlock()
	opmetrics.DoormanWakes.WithLabelValues(rt.workload.Namespace, rt.workload.Name, result).Inc()
	opmetrics.DoormanWaitSeconds.WithLabelValues(result).Observe(waited.Seconds())
}

// heldConn is a connection from the moment it's accepted until it's passed
// to the workload or closed.
type heldConn struct {
	*Server
	port  int32
	route route
	from  netip.Addr
	conn  net.Conn

	// head is what the caller sent before the doorman decided what to do
	// with the connection; it's passed on first.
	head []byte

	// stream carries the rest of what the caller sends, read in the
	// background while the connection is held so that a caller who leaves
	// frees its place at once.
	stream *clientStream

	// passedOn says the connection went straight to a Ready pod, before
	// anything was read from it.
	passedOn bool

	// unstamped says it ended in an error because its wake couldn't be
	// recorded, rather than because the pods couldn't be reached.
	unstamped bool
}

// unserved is why a connection that ended with result wasn't served, for
// its warning, if that's a result that warns.
func (h *heldConn) unserved(result string) (string, bool) {
	switch result {
	case resultTimeout:
		return unservedSlow, true
	case resultError:
		if h.unstamped {
			return unservedUnstamped, true
		}
		return unservedUnreachable, true
	case resultDrained:
		return unservedDrained, true
	default:
		return "", false
	}
}

// hold decides what becomes of the connection, and returns the connection
// to the workload when it's to be passed through, with a proxied place
// taken for it.
func (h *heldConn) hold(ctx context.Context) (net.Conn, string) {
	waitCtx, cancel := context.WithTimeout(ctx, h.route.maxWait)
	defer cancel()
	key := h.route.backendKey()

	if h.route.draining() {
		return h.passThrough(waitCtx)
	}

	// A workload with a Ready pod is awake, or its Service is served by
	// another workload's pods: the connection goes straight through, a
	// health check's included, since what it checks is up. That's decided
	// before reading anything, since a client of a protocol where the
	// server speaks first, such as MySQL, sends nothing until it has.
	if addrs, _, err := h.backends.get(waitCtx, key); err == nil && len(addrs) > 0 {
		if upstream, _ := h.dial(waitCtx, addrs); upstream != nil {
			h.passedOn = true
			return upstream, resultSuccess
		}
	}

	head, state := readHead(h.conn, h.silent)
	if state == headClosed {
		return nil, resultIgnored
	}
	h.head = head
	req, isHTTP := parseRequest(head)

	if isHTTP && isHealthCheck(req, failClosedCheckAgents) {
		_ = h.reply(writeHealthy) // the caller may already have gone
		return nil, resultIgnored
	}
	if isHTTP && isHealthCheck(req, h.healthCheckAgents) {
		_ = h.reply(writeUnavailable) // the caller may already have gone
		return nil, resultIgnored
	}
	if isHTTP && isHiddenFileRequest(req) {
		_ = h.reply(writeNotFound) // the caller may already have gone
		return nil, resultIgnored
	}
	if h.route.asks {
		return h.ask(ctx, waitCtx, req)
	}

	// A browser gets the page at once, while the wake goes on without it;
	// one that fails, or is over the limits, is asked for again by the
	// page's next reload. A held connection retries its own.
	if isHTTP && h.route.page && isPageLoad(req) {
		h.handling.Go(func() {
			wakeCtx, cancel := context.WithTimeout(ctx, apiTimeout)
			defer cancel()
			_ = h.requestWake(ctx, wakeCtx, h.route, h.from, forPage) // logged by requestWake
		})
		h.servePage(waitCtx, req)
		return nil, resultPage
	}
	wakeErr := h.requestWake(ctx, waitCtx, h.route, h.from, forHeld)
	h.streamClient()
	return h.await(ctx, waitCtx, wakeErr)
}

// await waits for a Ready pod to pass the connection to, retrying a wake or
// a dial that failed, until waitCtx ends, the caller leaves, or the route
// starts draining with nothing Ready. That last is a wake that ended with
// nothing to serve the caller, which is told so as a timeout is, unlike a
// connection that arrives once the route is draining (see passThrough).
func (h *heldConn) await(ctx, waitCtx context.Context, wakeErr error) (net.Conn, string) {
	key := h.route.backendKey()
	lastWake := h.now()
	var dialFailed, proxyLimited bool
	for {
		current := h.currentRoute(h.port, h.route)
		addrs, changed, err := h.backends.get(waitCtx, key)
		if len(addrs) > 0 {
			upstream, limited := h.dial(waitCtx, addrs)
			if upstream != nil {
				return upstream, resultSuccess
			}
			proxyLimited = limited
			dialFailed = !limited
		} else if current.draining() && err == nil {
			return nil, resultDrained
		}
		if wakeErr != nil && !current.draining() && h.now().Sub(lastWake) >= wakeRetryInterval {
			lastWake = h.now()
			wakeErr = h.requestWake(ctx, waitCtx, h.route, h.from, forHeld)
		}

		retry := time.NewTimer(dialRetryInterval)
		if !dialFailed && !proxyLimited && wakeErr == nil && err == nil {
			retry.Stop()
		}
		select {
		case <-changed:
			retry.Stop()
		case <-retry.C:
		case <-h.stream.gone:
			retry.Stop()
			return nil, resultCanceled
		case <-waitCtx.Done():
			retry.Stop()
			switch {
			case ctx.Err() != nil:
				return nil, resultCanceled
			case errors.Is(wakeErr, errWakeLimited) || proxyLimited:
				return nil, resultLimited
			case dialFailed:
				return nil, resultError
			case wakeErr != nil && !errors.Is(wakeErr, errNotAsleep):
				h.unstamped = true
				return nil, resultError
			default:
				return nil, resultTimeout
			}
		}
	}
}

// passThrough handles a connection to a draining route: it goes to a Ready
// pod if there is one, or is refused at once, as it would be without the
// doorman. It never wakes the workload, which has stopped being routed for
// a reason: it's awake, or no longer wakes on request.
func (h *heldConn) passThrough(ctx context.Context) (net.Conn, string) {
	addrs, _, err := h.backends.get(ctx, h.route.backendKey())
	if err != nil || len(addrs) == 0 {
		return nil, resultRefused
	}
	upstream, limited := h.dial(ctx, addrs)
	switch {
	case upstream != nil:
		return upstream, resultSuccess
	case limited:
		return nil, resultLimited
	default:
		return nil, resultError
	}
}

// dial connects to one of addrs, once a proxied place is free for the
// connection. It reports whether none was.
func (h *heldConn) dial(ctx context.Context, addrs []string) (net.Conn, bool) {
	if !h.admission.proxy(h.from, h.now()) {
		return nil, true
	}
	upstream := dialAny(ctx, addrs)
	if upstream == nil {
		h.admission.unproxy(h.from, h.now())
	}
	return upstream, false
}

// streamClient starts reading the caller in the background.
func (h *heldConn) streamClient() {
	h.stream = newClientStream(h.conn, h.admission)
	h.handling.Go(h.stream.run)
}

// readNothing says the connection was passed on before anything was read
// from it: as its workload pauses, or its route drains, or another
// workload's pods serve its Service.
func (h *heldConn) readNothing() bool {
	return len(h.head) == 0 && h.stream == nil
}

// reader is everything the caller sends, in order.
func (h *heldConn) reader() io.Reader {
	var rest io.Reader = h.conn
	if h.stream != nil {
		rest = h.stream
	}
	return io.MultiReader(bytes.NewReader(h.head), rest)
}

// watched is everything the caller sends, as reader gives it. What's
// passed straight to a Ready pod of a workload that's pausing is watched on
// its way: a request that would wake the workload were it paused shows it's
// in use, and is stamped, which calls the pause off. The caller doesn't
// wait for it. A workload that asks first is called off only by a person's
// click, which only its asleep page asks for.
func (h *heldConn) watched(ctx context.Context) io.Reader {
	r := h.reader()
	if !h.passedOn || !h.route.pausing || h.route.asks {
		return r
	}
	return &firstRequest{r: r, seen: func(head []byte) { h.seenAsItPauses(ctx, head) }}
}

// seenAsItPauses stamps the workload for a request passed on as it pauses,
// unless it's one that wouldn't wake it were it paused: a recognised probe
// or scrape, or a hunt for hidden files.
func (h *heldConn) seenAsItPauses(ctx context.Context, head []byte) {
	if req, isHTTP := parseRequest(head); isHTTP && (isHealthCheck(req, failClosedCheckAgents) ||
		isHealthCheck(req, h.healthCheckAgents) || isHiddenFileRequest(req)) {
		return
	}
	h.handling.Go(func() {
		wakeCtx, cancel := context.WithTimeout(ctx, apiTimeout)
		defer cancel()
		_ = h.requestWake(ctx, wakeCtx, h.route, h.from, forPassedOn) // logged by requestWake
	})
}

// firstRequest passes on what a caller sends, and once it holds the start
// of it, its first bytes or, for HTTP, the request's headers, hands those to
// seen, once. A caller that sends nothing, as a TCP check doesn't, is never
// seen.
type firstRequest struct {
	r    io.Reader
	head []byte
	seen func(head []byte)
	done bool
}

func (f *firstRequest) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if f.done {
		return n, err
	}
	f.head = append(f.head, p[:n]...)
	whole := !startsMethod(f.head) && (!looksLikeHTTP(f.head) || headersEnded(f.head) || len(f.head) >= maxPeek)
	if err != nil || len(f.head) > 0 && whole {
		f.done = true
		if len(f.head) > 0 {
			f.seen(f.head)
		}
		f.head = nil
	}
	return n, err
}

// firstByte passes on what a caller sends, and calls sent as the first of it
// arrives. For a caller that sends nothing, as a TCP check, it never does.
type firstByte struct {
	r    io.Reader
	sent func()
	done bool
}

func (f *firstByte) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if n > 0 && !f.done {
		f.done = true
		f.sent()
	}
	return n, err
}

func (h *heldConn) close() {
	if h.stream != nil {
		h.stream.close()
	}
}

// servePage answers a browser with the waking-up page.
func (h *heldConn) servePage(ctx context.Context, req *http.Request) {
	data := pageData{Address: pageAddress(req)}
	getCtx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	var w v1alpha1.ManagedWorkload
	if err := h.client.Get(getCtx, h.route.workload, &w); err == nil &&
		w.Status.Phase == v1alpha1.PhaseResuming && w.Status.LastTransitionTime != nil {
		data.Elapsed = sinceLabel(h.now().Sub(w.Status.LastTransitionTime.Time))
	}
	if err := h.reply(func(w io.Writer) error { return writePage(w, req, data) }); err != nil {
		log.FromContext(ctx).V(1).Info("writing waking-up page", "error", err.Error(),
			"workload", h.route.workload.Name, "namespace", h.route.workload.Namespace)
	}
}

// reply answers the caller and closes the connection. What it sent and the
// doorman didn't read, such as the rest of headers past maxPeek, is read and
// dropped first: closing with it unread resets the connection, and a reset
// can discard the answer before the caller reads it.
func (h *heldConn) reply(write func(io.Writer) error) error {
	_ = h.conn.SetWriteDeadline(time.Now().Add(dialTimeout)) // a failure just means no deadline
	err := write(h.conn)
	closeWrite(h.conn)
	_ = h.conn.SetReadDeadline(time.Now().Add(lingerTimeout)) // a failure just means no deadline
	_, _ = io.CopyN(io.Discard, h.conn, maxLinger)            // ends at the caller's close or the deadline
	return err
}

// headState is how a caller began.
type headState int

const (
	headSent headState = iota
	headSilent
	headClosed
)

// readHead reads what the caller sends first: the first bytes, and for
// HTTP, the rest of the request's headers. A caller that sends nothing has
// either closed the connection or waited out silent.
func readHead(conn net.Conn, silent time.Duration) ([]byte, headState) {
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }() // a failure just leaves the deadline
	buf := make([]byte, 4<<10)
	_ = conn.SetReadDeadline(time.Now().Add(silent)) // a failure just means no deadline
	n, err := conn.Read(buf)
	if n == 0 {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return nil, headSilent
		}
		return nil, headClosed
	}
	head := buf[:n]
	_ = conn.SetReadDeadline(time.Now().Add(headTimeout)) // a failure just means no deadline
	for startsMethod(head) {
		n, err := conn.Read(buf[len(head):])
		head = buf[:len(head)+n]
		if err != nil {
			break
		}
	}
	if !looksLikeHTTP(head) {
		return head, headSent
	}
	for len(head) < maxPeek && !headersEnded(head) {
		if len(head) == cap(head) {
			head = append(head, make([]byte, 4<<10)...)[:len(head)]
		}
		n, err := conn.Read(head[len(head):min(cap(head), maxPeek)])
		head = head[:len(head)+n]
		if err != nil {
			break
		}
	}
	return head, headSent
}

// dialAny connects to one of addrs, trying them in random order so held
// connections spread over the workload's pods.
func dialAny(ctx context.Context, addrs []string) net.Conn {
	dialer := net.Dialer{Timeout: dialTimeout}
	for _, i := range rand.Perm(len(addrs)) {
		conn, err := dialer.DialContext(ctx, "tcp", addrs[i])
		if err == nil {
			return conn
		}
	}
	return nil
}

func remoteAddr(conn net.Conn) netip.Addr {
	if tcp, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		return tcp.AddrPort().Addr().Unmap()
	}
	return netip.Addr{}
}

// proxy copies in both directions until both sides are done. Closing the
// write half on EOF lets the other direction finish its response.
//
// A connection is kept until one end closes it, however long its route has
// drained or it has been quiet, and the doorman sets no limit of its own: a
// connection made while the workload woke, such as a database pool's, is
// one its client means to keep, and closing it fails the client's next use
// of it, which a pool, such as PostgREST's, answers with a 503 (P86). A
// pool's own maximum lifetime moves it to the pods in time. An end that
// vanishes without closing is found by TCP keep-alive, which Go turns on
// for the connections the doorman accepts and dials, and a stopping doorman
// closes what it still carries at the end of its grace period.
func (s *Server) proxy(downstream net.Conn, fromDownstream io.Reader, upstream net.Conn) {
	var copies sync.WaitGroup
	copies.Go(func() {
		copyThrough(upstream, fromDownstream)
		closeWrite(upstream)
	})
	copies.Go(func() {
		copyThrough(downstream, upstream)
		closeWrite(downstream)
	})
	copies.Wait()
}

// copyThrough copies src to dst through a buffer of proxyBufferSize.
// Hiding the connections' ReadFrom and WriteTo keeps io from allocating its
// own, larger buffer.
func copyThrough(dst io.Writer, src io.Reader) {
	_, _ = io.CopyBuffer(struct{ io.Writer }{dst}, struct{ io.Reader }{src}, make([]byte, proxyBufferSize)) // an error just ends this direction
}

func closeWrite(conn net.Conn) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.CloseWrite() // signals EOF; nothing to do if already closed
	}
}
