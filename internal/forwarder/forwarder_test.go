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

package forwarder

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const listenPort = 8080

type staticResolver struct {
	mu    sync.Mutex
	addrs []netip.Addr
	err   error
}

func (r *staticResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.addrs, r.err
}

func loopback() *staticResolver {
	return &staticResolver{addrs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}
}

// fakeDoorman listens on loopback, as a doorman pod does on a routed port,
// and hands each connection to serve. It returns the port. Those include the
// forwarder's readiness checks, which connect and close without sending.
func fakeDoorman(t *testing.T, serve func(net.Conn)) int32 {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var serving sync.WaitGroup
	t.Cleanup(func() {
		_ = l.Close() // ends the accept loop
		serving.Wait()
	})
	serving.Go(func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			serving.Go(func() {
				defer func() { _ = conn.Close() }() // the test may have closed it
				serve(conn)
			})
		}
	})
	return portOf(t, l.Addr())
}

func portOf(t *testing.T, addr net.Addr) int32 {
	t.Helper()
	_, port, err := net.SplitHostPort(addr.String())
	require.NoError(t, err)
	n, err := strconv.Atoi(port)
	require.NoError(t, err)
	return int32(n)
}

// closedPort is a loopback port nothing listens on.
func closedPort(t *testing.T) int32 {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := portOf(t, l.Addr())
	require.NoError(t, l.Close())
	return port
}

func newForwarder(routes map[int32]int32, r resolver) *Forwarder {
	f := New(Options{Routes: routes, Doorman: "napwake-doorman-pods.napwake-system.svc"})
	f.resolver = r
	f.listen = func(int32) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }
	f.checkEvery = 50 * time.Millisecond
	f.dialTimeout = time.Second
	f.acceptFor = 0
	f.grace = 2 * time.Second
	return f
}

// running is a forwarder started in the background. Once Start returns,
// err is what it returned and stopped is closed.
type running struct {
	*Forwarder
	cancel  context.CancelFunc
	err     error
	stopped chan struct{}
}

func (r *running) stopsWithin(t *testing.T, d time.Duration) bool {
	t.Helper()
	select {
	case <-r.stopped:
		return true
	case <-time.After(d):
		return false
	}
}

// start runs a forwarder with one route, to doormanPort, and returns it once
// it listens and has reached the doorman.
func start(t *testing.T, doormanPort int32, tweak func(*Forwarder)) *running {
	t.Helper()
	f := newForwarder(map[int32]int32{listenPort: doormanPort}, loopback())
	if tweak != nil {
		tweak(f)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{Forwarder: f, cancel: cancel, stopped: make(chan struct{})}
	go func() {
		r.err = f.Start(ctx)
		close(r.stopped)
	}()
	t.Cleanup(func() {
		cancel()
		assert.True(t, r.stopsWithin(t, 10*time.Second), "forwarder didn't stop")
	})
	require.Eventually(t, func() bool { return r.Ready(nil) == nil }, 5*time.Second, 10*time.Millisecond)
	return r
}

func (r *running) addr(t *testing.T) string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.listeners[listenPort]
	require.True(t, ok, "not listening")
	return l.Addr().String()
}

func (r *running) dial(t *testing.T) *net.TCPConn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", r.addr(t), time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() }) // may already be closed
	tcp, ok := conn.(*net.TCPConn)
	require.True(t, ok)
	return tcp
}

// readsEOF reports whether conn reaches the end of what it's sent, cleanly
// or by a reset, within a few seconds.
func readsEOF(t *testing.T, conn net.Conn) bool {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, err := io.Copy(io.Discard, conn)
	var netErr net.Error
	return !errors.As(err, &netErr) || !netErr.Timeout()
}

func echo(conn net.Conn) {
	_, _ = io.Copy(conn, conn) // ends at the caller's half-close
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.CloseWrite() // passes the end on
	}
}

func TestForwarder_PassesBytesThroughUntouched(t *testing.T) {
	f := start(t, fakeDoorman(t, echo), nil)
	payload := make([]byte, 1<<20)
	_, err := rand.Read(payload)
	require.NoError(t, err)

	conn := f.dial(t)
	go func() {
		_, _ = conn.Write(payload) // a failure shows as a short echo
		_ = conn.CloseWrite()
	}()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
	got, err := io.ReadAll(conn)

	require.NoError(t, err)
	assert.True(t, bytes.Equal(payload, got), "got %d bytes back of %d, or different ones", len(got), len(payload))
}

// The doorman treats a caller that shuts down its sending side as one that
// left, and the forwarder mustn't change that; nor may it cut off either
// side's sending when the other has finished.
func TestForwarder_PassesOnHalfClose(t *testing.T) {
	t.Run("the caller finishes sending first", func(t *testing.T) {
		received := make(chan string, 1)
		f := start(t, fakeDoorman(t, func(conn net.Conn) {
			request, _ := io.ReadAll(conn) // to the caller's half-close
			if len(request) == 0 {
				return // a readiness check
			}
			received <- string(request)
			_, _ = io.WriteString(conn, "response")
		}), nil)

		conn := f.dial(t)
		_, err := io.WriteString(conn, "request")
		require.NoError(t, err)
		require.NoError(t, conn.CloseWrite())

		select {
		case got := <-received:
			assert.Equal(t, "request", got, "the doorman sees the caller's end")
		case <-time.After(5 * time.Second):
			t.Fatal("the caller's half-close never reached the doorman")
		}
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
		response, err := io.ReadAll(conn)
		require.NoError(t, err)
		assert.Equal(t, "response", string(response), "the doorman can still answer")
	})

	t.Run("the doorman finishes sending first", func(t *testing.T) {
		received := make(chan string, 1)
		f := start(t, fakeDoorman(t, func(conn net.Conn) {
			_, _ = io.WriteString(conn, "greeting")
			if tcp, ok := conn.(*net.TCPConn); ok {
				_ = tcp.CloseWrite() // the server is done talking
			}
			rest, _ := io.ReadAll(conn)
			if len(rest) == 0 {
				return // a readiness check
			}
			received <- string(rest)
		}), nil)

		conn := f.dial(t)
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
		greeting, err := io.ReadAll(conn)
		require.NoError(t, err)
		assert.Equal(t, "greeting", string(greeting))
		_, err = io.WriteString(conn, "goodbye")
		require.NoError(t, err)
		require.NoError(t, conn.CloseWrite())

		select {
		case got := <-received:
			assert.Equal(t, "goodbye", got, "the caller can still send")
		case <-time.After(5 * time.Second):
			t.Fatal("the caller's bytes never reached the doorman")
		}
	})
}

func TestForwarder_ResetByTheDoormanClosesTheCaller(t *testing.T) {
	f := start(t, fakeDoorman(t, func(conn net.Conn) {
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetLinger(0) // so closing resets
		}
	}), nil)

	conn := f.dial(t)

	assert.True(t, readsEOF(t, conn), "the caller would wait on a connection that's gone")
}

func TestForwarder_ClosesIdleConnections(t *testing.T) {
	quick := func(f *Forwarder) {
		f.idle = 150 * time.Millisecond
		f.idleCheck = 10 * time.Millisecond
	}

	t.Run("one that carries nothing", func(t *testing.T) {
		doormanSaw := make(chan struct{})
		var once sync.Once
		f := start(t, fakeDoorman(t, func(conn net.Conn) {
			opened := time.Now()
			_, _ = io.Copy(io.Discard, conn)
			if time.Since(opened) >= 100*time.Millisecond {
				once.Do(func() { close(doormanSaw) }) // not a readiness check, which closes at once
			}
		}), quick)

		conn := f.dial(t)

		assert.True(t, readsEOF(t, conn), "the caller's side stays open")
		select {
		case <-doormanSaw:
		case <-time.After(5 * time.Second):
			t.Fatal("the doorman's side stays open")
		}
	})

	t.Run("not one that carries bytes", func(t *testing.T) {
		f := start(t, fakeDoorman(t, echo), quick)
		conn := f.dial(t)

		buf := make([]byte, 1)
		for range 10 {
			_, err := conn.Write([]byte{'x'})
			require.NoError(t, err)
			require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
			_, err = io.ReadFull(conn, buf)
			require.NoError(t, err, "closed while bytes were moving")
			time.Sleep(50 * time.Millisecond)
		}
	})
}

func TestForwarder_ClosesWhatItCantPassOn(t *testing.T) {
	f := newForwarder(map[int32]int32{listenPort: closedPort(t)}, loopback())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	r := &running{Forwarder: f}
	require.Eventually(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.listeners) == 1
	}, 5*time.Second, 10*time.Millisecond)

	conn := r.dial(t)

	assert.True(t, readsEOF(t, conn), "a caller the doorman can't be reached for would wait for nothing")
	// The first check of the doorman runs as the forwarder starts, beside
	// its listeners, so it may not have ended when the caller is closed.
	unreachable := func() bool {
		err := f.Ready(nil)
		return err != nil && strings.Contains(err.Error(), "can't reach the doorman")
	}
	if !assert.Eventually(t, unreachable, 5*time.Second, 10*time.Millisecond) {
		t.Errorf("readiness says %v, not that the doorman can't be reached", f.Ready(nil))
	}
}

func TestForwarder_Readiness(t *testing.T) {
	open, other := fakeDoorman(t, echo), fakeDoorman(t, echo)
	tests := []struct {
		name     string
		routes   map[int32]int32
		resolver *staticResolver
		want     string
	}{
		{
			name:     "every doorman port answers",
			routes:   map[int32]int32{8080: open, 9090: other},
			resolver: loopback(),
		},
		{
			name:     "the doorman's name doesn't resolve",
			routes:   map[int32]int32{8080: open},
			resolver: &staticResolver{err: errors.New("no such host")},
			want:     "resolving the doorman's pods",
		},
		{
			name:     "the doorman has no Ready pods",
			routes:   map[int32]int32{8080: open},
			resolver: &staticResolver{},
			want:     "no Ready pods",
		},
		{
			name:     "one doorman port doesn't answer",
			routes:   map[int32]int32{8080: open, 9090: closedPort(t)},
			resolver: loopback(),
			want:     "can't reach the doorman on port",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newForwarder(tt.routes, tt.resolver)
			assert.ErrorIs(t, f.Ready(nil), errNotChecked, "Ready before it has checked")

			f.check(context.Background())

			if tt.want == "" {
				assert.NoError(t, f.Ready(nil))
			} else {
				assert.ErrorContains(t, f.Ready(nil), tt.want)
			}
		})
	}
}

// A stopping forwarder reports itself not Ready at once, so it's taken out
// of the Service, but goes on serving while load balancers catch up, and
// lets the connections it has finish.
func TestForwarder_DrainsOnShutdown(t *testing.T) {
	f := start(t, fakeDoorman(t, echo), func(f *Forwarder) { f.acceptFor = 300 * time.Millisecond })
	held := f.dial(t)

	f.cancel()

	require.Eventually(t, func() bool { return f.Ready(nil) != nil }, time.Second, 5*time.Millisecond)
	late := f.dial(t)
	_, err := io.WriteString(late, "late")
	require.NoError(t, err)
	require.NoError(t, late.CloseWrite())
	require.NoError(t, late.SetReadDeadline(time.Now().Add(5*time.Second)))
	got, err := io.ReadAll(late)
	require.NoError(t, err)
	assert.Equal(t, "late", string(got), "a connection that arrives while load balancers catch up is passed on")

	require.Eventually(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.listeners) == 0
	}, 5*time.Second, 10*time.Millisecond, "it stops accepting once load balancers have caught up")
	_, err = io.WriteString(held, "still here")
	require.NoError(t, err)
	buf := make([]byte, len("still here"))
	require.NoError(t, held.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, err = io.ReadFull(held, buf)
	require.NoError(t, err, "a connection it had is cut off before the grace period")
	require.NoError(t, held.CloseWrite())

	require.True(t, f.stopsWithin(t, 5*time.Second), "Start didn't return once its connections finished")
	assert.NoError(t, f.err)
}

func TestForwarder_ClosesWhatsLeftAfterTheGracePeriod(t *testing.T) {
	f := start(t, fakeDoorman(t, echo), func(f *Forwarder) { f.grace = 100 * time.Millisecond })
	held := f.dial(t)

	f.cancel()

	require.True(t, f.stopsWithin(t, 5*time.Second), "Start waited past its grace period")
	assert.NoError(t, f.err)
	assert.True(t, readsEOF(t, held))
}

func TestForwarder_StartFailsWhenItCantListen(t *testing.T) {
	f := newForwarder(map[int32]int32{listenPort: 23456}, loopback())
	f.listen = func(int32) (net.Listener, error) { return nil, errors.New("address already in use") }

	err := f.Start(context.Background())

	assert.ErrorContains(t, err, "listening on port 8080")
}

func TestRoutes(t *testing.T) {
	routes := map[int32]int32{8080: 23456, 80: 20001}
	parsed, err := ParseRoutes(FormatRoutes(routes))
	require.NoError(t, err)
	assert.Equal(t, routes, parsed, "what the operator writes, the forwarder reads")
	assert.Equal(t, "80=20001,8080=23456", FormatRoutes(routes), "sorted, so the Deployment doesn't change from one reconcile to the next")

	for _, bad := range []string{"", "8080", "8080=x", "0=23456", "8080=70000", "8080=1,8080=2"} {
		_, err := ParseRoutes(bad)
		assert.Error(t, err, "%q", bad)
	}
}
