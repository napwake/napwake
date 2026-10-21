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

// Package forwarder passes connections, bytes untouched, from a paused
// workload's namespace to the doorman.
//
// GKE's NEG controller only accepts endpoints whose pods are in the
// Service's own namespace, so a Service behind container-native load
// balancing can't be pointed at the doorman's pods, which are in
// Hybernate's. It's pointed at a forwarder's instead, which runs beside the
// paused workload. Every decision about a connection, whether to hold it,
// wake the workload, or answer it, stays with the doorman.
package forwarder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	dialTimeout = 5 * time.Second

	// checkInterval paces the checks that the doorman answers on every
	// port, which readiness reports.
	checkInterval = 5 * time.Second

	// idleTimeout closes a connection that has carried nothing either way
	// for this long. It outlasts the doorman's own idle timeout, so the
	// doorman, which knows what the connection is for, closes it first.
	idleTimeout = 15 * time.Minute
	idleCheck   = 30 * time.Second

	// acceptFor is how long a stopping forwarder goes on accepting
	// connections: the operator takes its pod out of the Service's
	// endpoints at once, but load balancers take a few seconds to stop
	// sending to it.
	acceptFor = 15 * time.Second

	// shutdownGrace is how long connections then have to finish. With
	// acceptFor, it fits in the pod's termination grace period.
	shutdownGrace = 20 * time.Second

	// bufferSize is what each direction of a connection copies through. It's
	// small because every connection to a paused workload's Service passes
	// through, and most carry a request or two.
	bufferSize = 8 << 10

	acceptRetry = 100 * time.Millisecond
)

// Options configure a Forwarder.
type Options struct {
	// Routes maps each port the forwarder listens on to the doorman port its
	// connections are passed to.
	Routes map[int32]int32

	// Doorman is a DNS name that resolves to the doorman's Ready pods, such
	// as a headless Service's.
	Doorman string
}

type resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Forwarder listens on its routes' ports and passes each connection to a
// doorman pod, on the doorman port for the route.
type Forwarder struct {
	routes   map[int32]int32
	doorman  string
	resolver resolver
	listen   func(port int32) (net.Listener, error)

	dialTimeout time.Duration
	checkEvery  time.Duration
	idle        time.Duration
	idleCheck   time.Duration
	acceptFor   time.Duration
	grace       time.Duration

	mu        sync.Mutex
	addrs     []netip.Addr
	notReady  error
	listeners map[int32]net.Listener
	conns     map[net.Conn]struct{}
	closed    bool

	stopping  atomic.Bool
	accepting sync.WaitGroup
	handling  sync.WaitGroup
}

// New returns a forwarder for opts. It does nothing until Start.
func New(opts Options) *Forwarder {
	return &Forwarder{
		routes:   maps.Clone(opts.Routes),
		doorman:  opts.Doorman,
		resolver: net.DefaultResolver,
		listen: func(port int32) (net.Listener, error) {
			return net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(int(port))))
		},
		dialTimeout: dialTimeout,
		checkEvery:  checkInterval,
		idle:        idleTimeout,
		idleCheck:   idleCheck,
		acceptFor:   acceptFor,
		grace:       shutdownGrace,
		notReady:    errNotChecked,
		listeners:   map[int32]net.Listener{},
		conns:       map[net.Conn]struct{}{},
	}
}

// Start listens on every route's port and passes connections to the doorman
// until ctx is done. Then it reports itself not Ready, goes on accepting
// connections while load balancers stop sending them, stops accepting,
// gives those it has the grace period to finish, and closes the rest.
func (f *Forwarder) Start(ctx context.Context) error {
	if err := f.listenAll(); err != nil {
		f.closeListeners()
		return err
	}
	// Connections outlive ctx by the drain, so they get their own.
	connCtx, cancelConns := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelConns()
	f.mu.Lock()
	for port, l := range f.listeners {
		f.accepting.Go(func() { f.accept(connCtx, port, l) })
	}
	f.mu.Unlock()

	ticker := time.NewTicker(f.checkEvery)
	defer ticker.Stop()
	for f.check(ctx); ctx.Err() == nil; {
		select {
		case <-ctx.Done():
		case <-ticker.C:
			f.check(ctx)
		}
	}

	f.stopping.Store(true)
	accepting := time.NewTimer(f.acceptFor)
	defer accepting.Stop()
	<-accepting.C
	f.shutdown(cancelConns)
	return nil
}

// Ready reports whether the forwarder can pass connections on: it isn't
// shutting down, and at its last check the doorman answered on every port
// it forwards to.
func (f *Forwarder) Ready(_ *http.Request) error {
	if f.stopping.Load() {
		return errors.New("forwarder is shutting down")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.notReady
}

func (f *Forwarder) listenAll() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, port := range slices.Sorted(maps.Keys(f.routes)) {
		l, err := f.listen(port)
		if err != nil {
			return fmt.Errorf("listening on port %d: %w", port, err)
		}
		f.listeners[port] = l
	}
	return nil
}

func (f *Forwarder) closeListeners() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for port, l := range f.listeners {
		_ = l.Close() // ends its accept loop; nothing to do if it fails
		delete(f.listeners, port)
	}
}

// check resolves the doorman and dials it on every port, recording the
// addresses for connections to use and whether all ports answered. It logs
// the first result and every change between reaching the doorman and not.
func (f *Forwarder) check(ctx context.Context) {
	checkCtx, cancel := context.WithTimeout(ctx, f.checkEvery)
	defer cancel()
	addrs, err := f.resolve(checkCtx)
	if err == nil {
		err = f.reachEveryPort(checkCtx, addrs)
	}
	if ctx.Err() != nil {
		return
	}

	f.mu.Lock()
	previous := f.notReady
	f.notReady = err
	if len(addrs) > 0 {
		f.addrs = addrs
	}
	f.mu.Unlock()

	logger := log.FromContext(ctx).WithName("forwarder")
	switch {
	case err == nil && previous != nil:
		logger.Info("Reached the doorman", "doorman", f.doorman, "addresses", len(addrs))
	case err != nil && (previous == nil || errors.Is(previous, errNotChecked)):
		logger.Info("Can't reach the doorman", "doorman", f.doorman, "error", err.Error())
	}
}

var errNotChecked = errors.New("the doorman hasn't been reached yet")

func (f *Forwarder) resolve(ctx context.Context) ([]netip.Addr, error) {
	found, err := f.resolver.LookupNetIP(ctx, "ip", f.doorman)
	if err != nil {
		return nil, fmt.Errorf("resolving the doorman's pods, %s: %w", f.doorman, err)
	}
	addrs := make([]netip.Addr, 0, len(found))
	for _, a := range found {
		addrs = append(addrs, a.Unmap())
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("%s has no addresses, so the doorman has no Ready pods", f.doorman)
	}
	return addrs, nil
}

// reachEveryPort dials each doorman port the forwarder sends to, on any of
// addrs, and closes the connection at once. The doorman takes one that
// sends nothing for a TCP health check, and wakes nothing.
func (f *Forwarder) reachEveryPort(ctx context.Context, addrs []netip.Addr) error {
	ports := slices.Sorted(maps.Values(f.routes))
	for _, port := range slices.Compact(ports) {
		conn, err := f.dialAny(ctx, addrs, port)
		if err != nil {
			return fmt.Errorf("can't reach the doorman on port %d: %w", port, err)
		}
		_ = conn.Close() // only reaching it mattered
	}
	return nil
}

// dialAny connects to one of addrs on port, trying them in random order so
// connections spread over the doorman's pods.
func (f *Forwarder) dialAny(ctx context.Context, addrs []netip.Addr, port int32) (net.Conn, error) {
	dialer := net.Dialer{Timeout: f.dialTimeout}
	var errs []error
	for _, i := range rand.Perm(len(addrs)) {
		conn, err := dialer.DialContext(ctx, "tcp", netip.AddrPortFrom(addrs[i], uint16(port)).String())
		if err == nil {
			return conn, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

// dialDoorman connects to a doorman pod on port: one of those the last check
// found, or, before any check has found one, those the doorman resolves to
// now.
func (f *Forwarder) dialDoorman(ctx context.Context, port int32) (net.Conn, error) {
	f.mu.Lock()
	addrs := f.addrs
	f.mu.Unlock()
	if len(addrs) == 0 {
		resolveCtx, cancel := context.WithTimeout(ctx, f.dialTimeout)
		defer cancel()
		var err error
		if addrs, err = f.resolve(resolveCtx); err != nil {
			return nil, err
		}
	}
	return f.dialAny(ctx, addrs, port)
}

func (f *Forwarder) accept(ctx context.Context, port int32, l net.Listener) {
	for {
		conn, err := l.Accept()
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err != nil {
			// Out of file descriptors, most likely; connections closing will
			// free some.
			time.Sleep(acceptRetry)
			continue
		}
		if !f.track(conn) {
			continue
		}
		f.handling.Go(func() {
			defer f.forget(conn)
			f.handle(ctx, port, conn)
		})
	}
}

// handle passes one connection to the doorman. One the doorman can't be
// reached for is closed, as the doorman would close one it can't serve.
func (f *Forwarder) handle(ctx context.Context, port int32, client net.Conn) {
	upstream, err := f.dialDoorman(ctx, f.routes[port])
	if err != nil {
		log.FromContext(ctx).WithName("forwarder").V(1).Info("closing connection: can't reach the doorman",
			"port", port, "doorman_port", f.routes[port], "error", err.Error())
		return
	}
	if !f.track(upstream) {
		return
	}
	defer f.forget(upstream)
	f.pipe(client, upstream)
}

// pipe copies both ways until both sides are done, or the connection idles
// out. A side that finishes sending has its end passed on as a half-close,
// so a caller that shuts down its sending side looks the same to the
// doorman as it would directly. A side that fails, such as one reset,
// closes both.
func (f *Forwarder) pipe(client, upstream net.Conn) {
	var active atomic.Int64
	active.Store(time.Now().UnixNano())
	var copies sync.WaitGroup
	copies.Go(func() { f.copyOneWay(upstream, client, &active) })
	copies.Go(func() { f.copyOneWay(client, upstream, &active) })

	done := make(chan struct{})
	var watch sync.WaitGroup
	watch.Go(func() { f.closeIdle(done, &active, client, upstream) })
	copies.Wait()
	close(done)
	watch.Wait()
}

// copyOneWay copies src to dst through a buffer of bufferSize, noting when
// bytes last moved. Hiding the connections' ReadFrom and WriteTo keeps io
// from allocating its own, larger buffer.
func (f *Forwarder) copyOneWay(dst, src net.Conn, active *atomic.Int64) {
	w := activityWriter{w: dst, active: active}
	if _, err := io.CopyBuffer(w, struct{ io.Reader }{src}, make([]byte, bufferSize)); err != nil {
		_ = dst.Close() // the other copy then ends too
		_ = src.Close()
		return
	}
	closeWrite(dst)
}

type activityWriter struct {
	w      io.Writer
	active *atomic.Int64
}

func (a activityWriter) Write(p []byte) (int, error) {
	n, err := a.w.Write(p)
	if n > 0 {
		a.active.Store(time.Now().UnixNano())
	}
	return n, err
}

// closeIdle closes conns once nothing has moved either way for f.idle,
// until done.
func (f *Forwarder) closeIdle(done <-chan struct{}, active *atomic.Int64, conns ...net.Conn) {
	ticker := time.NewTicker(f.idleCheck)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
		}
		if time.Since(time.Unix(0, active.Load())) >= f.idle {
			for _, c := range conns {
				_ = c.Close() // ends both copies; the caller sees the connection drop
			}
			return
		}
	}
}

func closeWrite(conn net.Conn) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.CloseWrite() // signals EOF; nothing to do if already closed
	}
}

// track registers a connection so shutdown can close it, and reports
// whether it may go on: one opened as shutdown closes everything is closed
// at once.
func (f *Forwarder) track(conn net.Conn) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		_ = conn.Close() // shutdown has already closed the rest
		return false
	}
	f.conns[conn] = struct{}{}
	return true
}

func (f *Forwarder) forget(conn net.Conn) {
	_ = conn.Close() // may already be closed by the copies or shutdown
	f.mu.Lock()
	delete(f.conns, conn)
	f.mu.Unlock()
}

// shutdown stops accepting connections, waits up to the grace period for
// those it has, then closes the rest.
func (f *Forwarder) shutdown(cancelConns context.CancelFunc) {
	f.closeListeners()
	f.accepting.Wait()

	done := make(chan struct{})
	go func() {
		f.handling.Wait()
		close(done)
	}()
	timer := time.NewTimer(f.grace)
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
	}

	cancelConns()
	f.mu.Lock()
	f.closed = true
	for c := range f.conns {
		_ = c.Close() // ends its copies; the caller sees the connection drop
	}
	f.mu.Unlock()
	<-done
}

// FormatRoutes is how Routes are given on the command line: comma-separated
// listen=doorman port pairs, such as 8080=23456,9090=24567.
func FormatRoutes(routes map[int32]int32) string {
	pairs := make([]string, 0, len(routes))
	for _, listen := range slices.Sorted(maps.Keys(routes)) {
		pairs = append(pairs, fmt.Sprintf("%d=%d", listen, routes[listen]))
	}
	return strings.Join(pairs, ",")
}

// ParseRoutes reads routes FormatRoutes wrote.
func ParseRoutes(value string) (map[int32]int32, error) {
	routes := map[int32]int32{}
	for pair := range strings.SplitSeq(value, ",") {
		if pair = strings.TrimSpace(pair); pair == "" {
			continue
		}
		listen, doorman, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("%q isn't listen=doorman", pair)
		}
		l, err := parsePort(listen)
		if err != nil {
			return nil, fmt.Errorf("%q: listen port: %w", pair, err)
		}
		d, err := parsePort(doorman)
		if err != nil {
			return nil, fmt.Errorf("%q: doorman port: %w", pair, err)
		}
		if _, dup := routes[l]; dup {
			return nil, fmt.Errorf("port %d is listed twice", l)
		}
		routes[l] = d
	}
	if len(routes) == 0 {
		return nil, errors.New("no routes")
	}
	return routes, nil
}

func parsePort(s string) (int32, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 16)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("%q isn't a port from 1 to 65535", s)
	}
	return int32(n), nil
}
