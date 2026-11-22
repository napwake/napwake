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
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

// answerBound is how soon the doorman must answer once a pod that never
// answers has left the Service's endpoints, or once it has a live pod
// beside it: far more than giving it up and answering takes, a few
// milliseconds, or than dialrace.Stagger, and far less than the dialTimeout
// such a pod cost a caller before (P89).
const answerBound = time.Second

// unanswered dials as the doorman does, except the dials silent picks, by
// their place among the dials and their address. Those are never answered,
// as a pod that has gone but is still listed Ready isn't: each ends only
// when its context does.
type unanswered struct {
	silent func(n int, addr string) bool

	mu sync.Mutex
	n  int

	// dialing has a value as each silent dial starts, and ended why each
	// ended: given up, or out of time.
	dialing chan struct{}
	ended   chan error
}

func newUnanswered(silent func(n int, addr string) bool) *unanswered {
	return &unanswered{silent: silent, dialing: make(chan struct{}, 64), ended: make(chan error, 64)}
}

func (u *unanswered) dial(ctx context.Context, network, address string) (net.Conn, error) {
	u.mu.Lock()
	n := u.n
	u.n++
	u.mu.Unlock()
	if !u.silent(n, address) {
		var d net.Dialer
		return d.DialContext(ctx, network, address)
	}
	u.dialing <- struct{}{}
	<-ctx.Done()
	u.ended <- ctx.Err()
	return nil, ctx.Err()
}

// dialed waits for the doorman to dial a pod that never answers.
func (u *unanswered) dialed(t *testing.T) {
	t.Helper()
	select {
	case <-u.dialing:
	case <-time.After(5 * time.Second):
		t.Fatal("the doorman never dialed the pod that doesn't answer")
	}
}

// gaveUp asserts the doorman gave up its dial to a pod that never answers,
// rather than waiting out dialTimeout.
func (u *unanswered) gaveUp(t *testing.T) {
	t.Helper()
	select {
	case err := <-u.ended:
		assert.ErrorIs(t, err, context.Canceled, "the dial to the pod that never answers is given up, not timed out")
	case <-time.After(dialTimeout + closeBound):
		t.Fatal("the dial to the pod that never answers never ended")
	}
}

// podSlice is a slice of the "api" Service, as the EndpointSlice controller
// writes it, with pod Ready on port, or with no pod for no port.
func podSlice(name, pod string, port int32) *discoveryv1.EndpointSlice {
	slice := readySlice(port)
	slice.Name = name
	slice.Endpoints[0].TargetRef.Name = pod
	if port == 0 {
		slice.Endpoints = nil
	}
	return slice
}

// endpointsChange writes the "api" Service's slices, and tells the doorman,
// as its informer does.
func endpointsChange(t *testing.T, s *Server, c client.Client, slices ...*discoveryv1.EndpointSlice) {
	t.Helper()
	ctx := context.Background()
	for _, slice := range slices {
		var current discoveryv1.EndpointSlice
		err := c.Get(ctx, client.ObjectKeyFromObject(slice), &current)
		if apierrors.IsNotFound(err) {
			require.NoError(t, c.Create(ctx, slice))
			continue
		}
		require.NoError(t, err)
		slice.ResourceVersion = current.ResourceVersion
		require.NoError(t, c.Update(ctx, slice))
	}
	s.backends.changed(serviceRef{namespace: "dev", name: "api"})
}

// awayFrom is how many requests the doorman has turned away from "api"
// since its last flush.
func awayFrom(s *Server) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.away[apiKey].requests
}

// answeredWithin reads the doorman's answer to a GET, which must come
// within answerBound.
func answeredWithin(t *testing.T, conn net.Conn) *http.Response {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(answerBound)))
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodGet})
	require.NoError(t, err, "answered within %s", answerBound)
	_, _ = io.Copy(io.Discard, resp.Body) // only the status matters
	return resp
}

// echoedWithin reads back what was sent, as the woken workload echoes it,
// within answerBound.
func echoedWithin(t *testing.T, conn net.Conn, want string) {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(answerBound)))
	got := make([]byte, len(want))
	_, err := io.ReadFull(conn, got)
	require.NoError(t, err, "passed to a live pod within %s", answerBound)
	assert.Equal(t, want, string(got))
}

// closedWithinTheBound asserts the doorman closes conn, without a reply,
// within answerBound.
func closedWithinTheBound(t *testing.T, conn net.Conn) {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(answerBound)))
	n, err := conn.Read(make([]byte, 1))
	assert.Zero(t, n)
	require.Error(t, err)
	var netErr net.Error
	require.False(t, errors.As(err, &netErr) && netErr.Timeout(), "the connection was still open after %s", answerBound)
}

// As a workload pauses, a pod it scaled away can be listed Ready for a
// moment after it's gone, and a dial to it is never answered. Once it
// leaves the Service's endpoints, the doorman stops waiting on it: it
// answers as it does once the workload has no Ready pod, or passes the
// connection to a pod that's left. It doesn't wait out dialTimeout, which a
// caller with a shorter timeout of its own gave up before (P89).
func TestServer_StopsWaitingOnAPodThatLeaves(t *testing.T) {
	const request = "GET /orders HTTP/1.1\r\nHost: api\r\nUser-Agent: curl/8.7.1\r\n\r\n"
	asking := func(port int32) *v1alpha1.ManagedWorkload { return pausingAsks(port, time.Now().Add(-time.Minute)) }
	paused := func(port int32) *v1alpha1.ManagedWorkload { return pausedWorkload("api", port, time.Minute) }
	tests := []struct {
		name     string
		workload func(port int32) *v1alpha1.ManagedWorkload
		// drains says the route has left status when the connection comes,
		// and held that the gone pod is listed only once the connection is
		// held for a wake. Otherwise it's listed when the connection comes.
		drains, held bool
		// leaves is what the caller sees once the gone pod has left.
		leaves func(t *testing.T, s *Server, c client.Client, conn net.Conn)
	}{
		{
			name:     "asks first, as it pauses",
			workload: asking,
			leaves: func(t *testing.T, s *Server, c client.Client, conn net.Conn) {
				turnedAway := wakes(resultTurnedAway)
				endpointsChange(t, s, c, podSlice("api-real", "api-0", 0))
				resp := answeredWithin(t, conn)
				assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
				assert.Eventually(t, func() bool { return wakes(resultTurnedAway) == turnedAway+1 },
					5*time.Second, 10*time.Millisecond, "counted as turned away")
				assert.Equal(t, int64(1), awayFrom(s), "and counted for the pause")
				assert.Empty(t, lastRequest(t, c), "nothing woken")
			},
		},
		{
			name:     "wakes on request, as it pauses",
			workload: pausingWorkload,
			leaves: func(t *testing.T, s *Server, c client.Client, conn net.Conn) {
				endpointsChange(t, s, c, podSlice("api-real", "api-0", 0))
				require.Eventually(t, func() bool { return lastRequest(t, c) != "" }, answerBound, 10*time.Millisecond,
					"held and woken within %s", answerBound)
				endpointsChange(t, s, c, podSlice("api-real", "api-0", echoPort(t)))
				echoed(t, conn, request)
			},
		},
		{
			name:     "as its route drains",
			workload: paused,
			drains:   true,
			leaves: func(t *testing.T, s *Server, c client.Client, conn net.Conn) {
				refused := wakes(resultRefused)
				endpointsChange(t, s, c, podSlice("api-real", "api-0", 0))
				closedWithinTheBound(t, conn)
				assert.Eventually(t, func() bool { return wakes(resultRefused) == refused+1 },
					5*time.Second, 10*time.Millisecond, "refused, as it would be without the doorman")
			},
		},
		{
			name:     "held for a wake, another pod Ready in its place",
			workload: paused,
			held:     true,
			leaves: func(t *testing.T, s *Server, c client.Client, conn net.Conn) {
				endpointsChange(t, s, c, podSlice("api-real", "api-0", echoPort(t)))
				echoedWithin(t, conn, request)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port, gone := freePort(t), refusingPort(t)
			goneAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(gone)))
			pods := newUnanswered(func(_ int, addr string) bool { return addr == goneAddr })
			objs := []client.Object{tt.workload(port)}
			if !tt.held {
				objs = append(objs, podSlice("api-real", "api-0", gone))
			}
			s, c := startServerWith(t, func(s *Server) {
				s.dialPod = pods.dial
				// No poll comes within the test, so only a change of the
				// endpoints can end a dial.
				s.pollEvery = time.Hour
			}, objs...)
			if tt.drains {
				removeRoute(t, s, c)
			}

			conn := dial(t, port)
			send(t, conn, request)
			if tt.held {
				require.Eventually(t, func() bool { return lastRequest(t, c) != "" }, 5*time.Second, 20*time.Millisecond,
					"the request is held for the wake")
				endpointsChange(t, s, c, podSlice("api-real", "api-0", gone))
			}
			pods.dialed(t)

			tt.leaves(t, s, c, conn)
			pods.gaveUp(t)
		})
	}
}

// A workload's pods can include one that has gone but is still listed
// Ready, which never answers. Tried first, it costs a caller
// dialrace.Stagger, not dialTimeout: the next pod is tried beside it, and
// the connection goes to the first to answer (P89).
func TestServer_PassesAConnectionOnPastAPodThatNeverAnswers(t *testing.T) {
	const request = "GET /orders HTTP/1.1\r\nHost: api\r\nUser-Agent: curl/8.7.1\r\n\r\n"
	paused := func(port int32) *v1alpha1.ManagedWorkload { return pausedWorkload("api", port, time.Minute) }
	tests := []struct {
		name     string
		workload func(port int32) *v1alpha1.ManagedWorkload
		// drains says the route has left status when the connection comes,
		// and held that the pods are Ready only once it's held for a wake.
		drains, held bool
	}{
		{name: "as it pauses", workload: pausingWorkload},
		{name: "as its route drains", workload: paused, drains: true},
		{name: "held for a wake", workload: paused, held: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port := freePort(t)
			first, firstAccepted := echoServer(t)
			second, secondAccepted := echoServer(t)
			slices := []*discoveryv1.EndpointSlice{podSlice("api-real", "api-0", first), podSlice("api-real-1", "api-1", second)}
			// Whichever pod is tried first never answers.
			pods := newUnanswered(func(n int, _ string) bool { return n == 0 })
			objs := []client.Object{tt.workload(port), &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "dev"},
				Status:     corev1.PodStatus{PodIPs: []corev1.PodIP{{IP: "127.0.0.1"}}},
			}}
			if !tt.held {
				objs = append(objs, slices[0], slices[1])
			}
			s, c := startServerWith(t, func(s *Server) {
				s.dialPod = pods.dial
				s.pollEvery = time.Hour
			}, objs...)
			if tt.drains {
				removeRoute(t, s, c)
			}

			conn := dial(t, port)
			send(t, conn, request)
			if tt.held {
				require.Eventually(t, func() bool { return lastRequest(t, c) != "" }, 5*time.Second, 20*time.Millisecond,
					"the request is held for the wake")
				endpointsChange(t, s, c, slices...)
			}
			pods.dialed(t)

			echoedWithin(t, conn, request)
			assert.Equal(t, int32(1), firstAccepted.Load()+secondAccepted.Load(), "one live pod has the connection")
			pods.gaveUp(t)
		})
	}
}

// within receives from ch, which must send within 5 seconds.
func within[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("%s never came", what)
		var zero T
		return zero
	}
}

// The endpoints are read again on every resync, which lists the same pods,
// and a read can fail. Neither gives up a dial: only a pod that has left
// the endpoints is given up.
func TestDialAny_GoesOnDialingAPodStillListed(t *testing.T) {
	const pod = "10.244.1.5:8080"
	answer, answerPeer := net.Pipe()
	t.Cleanup(func() {
		_ = answer.Close()     // nothing to do if it fails
		_ = answerPeer.Close() // nothing to do if it fails
	})
	attempts := make(chan context.Context, 4)
	answers := make(chan struct{})
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		attempts <- ctx
		select {
		case <-answers:
			return answer, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	// Each read but the last comes with a change of its own, so dialAny
	// reads again at once, and each read shows it has acted on the one
	// before.
	reads := make(chan struct{}, 4)
	read := 0
	list := func(context.Context) ([]string, <-chan struct{}, error) {
		reads <- struct{}{}
		read++
		changed := make(chan struct{})
		switch read {
		case 1:
			close(changed)
			return nil, changed, errors.New("listing endpoints for service api: the cache isn't synced")
		case 2:
			close(changed)
		}
		return []string{pod}, changed, nil
	}
	type result struct {
		conn net.Conn
		err  error
	}
	changed := make(chan struct{})
	results := make(chan result, 1)
	go func() {
		conn, err := dialAny(context.Background(), dial, []string{pod}, changed, list)
		results <- result{conn: conn, err: err}
	}()

	attempt := within(t, attempts, "the dial")
	close(changed)
	for range 3 {
		within(t, reads, "a read of the endpoints")
	}
	assert.NoError(t, attempt.Err(), "the dial to a pod still listed goes on")

	close(answers)
	got := within(t, results, "the end of the dial")
	require.NoError(t, got.err)
	assert.Same(t, answer, got.conn, "and connects")
	assert.Empty(t, attempts, "dialed once")
}

// A pod that answers once another has is closed: the caller has its
// connection, and the workload isn't left holding one nobody uses.
func TestDialAny_ClosesAPodThatAnswersLate(t *testing.T) {
	late, latePeer := net.Pipe()
	won, wonPeer := net.Pipe()
	t.Cleanup(func() {
		for _, c := range []net.Conn{late, latePeer, won, wonPeer} {
			_ = c.Close() // some are closed already
		}
	})
	var dials atomic.Int32
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		if dials.Add(1) == 1 {
			// It answers just as it's given up.
			<-ctx.Done()
			return late, nil
		}
		return won, nil
	}

	conn, err := dialAny(context.Background(), dial, []string{"10.244.1.5:8080", "10.244.1.6:8080"}, nil, nil)

	require.NoError(t, err)
	assert.Same(t, won, conn, "the first to answer is kept")
	_ = latePeer.SetReadDeadline(time.Now().Add(5 * time.Second)) // refused once the other end is closed, as it should be
	_, err = latePeer.Read(make([]byte, 1))
	assert.ErrorIs(t, err, io.EOF, "the one that answered late is closed")
}

// A held connection whose dial to a Ready pod failed, or found no proxied
// place free, and whose pod then left, has nothing Ready when maxWait runs
// out. It's reported as a wake that was slow, in napwake_doorman_wakes_total
// and RequestNotServed, as it would be had the dial never been made: what
// the last dial found no longer holds once no pod is Ready (P93).
func TestServer_AHoldWhosePodLeftTimesOut(t *testing.T) {
	const maxWait = 2 * time.Second
	refused := errors.New("connection refused")
	tests := []struct {
		name string
		// limited says every proxied place is taken.
		limited bool
	}{
		{name: "a dial failed"},
		{name: "no proxied place was free", limited: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port, pod := freePort(t), refusingPort(t)
			podAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(pod)))
			failed := make(chan struct{}, 16)
			// listed has a value each time the doorman reads the pod
			// listed Ready.
			listed := make(chan struct{}, 16)
			recorder, written := recordedEvents(t)
			s, c := newTestServer(t, func(c client.WithWatch) client.WithWatch {
				return interceptor.NewClient(c, interceptor.Funcs{
					List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						if err := cl.List(ctx, list, opts...); err != nil {
							return err
						}
						if slices, ok := list.(*discoveryv1.EndpointSliceList); ok {
							for _, slice := range slices.Items {
								if len(slice.Endpoints) > 0 {
									listed <- struct{}{}
								}
							}
						}
						return nil
					},
				})
			}, pausedWorkload("api", port, maxWait))
			s.recorder = recorder
			s.pollEvery = time.Hour
			s.dialPod = func(ctx context.Context, network, address string) (net.Conn, error) {
				if address == podAddr {
					failed <- struct{}{}
					return nil, refused
				}
				var d net.Dialer
				return d.DialContext(ctx, network, address)
			}
			if tt.limited {
				l := defaultLimits
				l.maxProxied = 1
				s.admission = newAdmission(l)
				require.True(t, s.admission.proxy(netip.MustParseAddr("10.244.0.99"), time.Now()),
					"another connection takes the one proxied place")
			}
			run(t, s)
			timedOut, errored, limited := wakes(resultTimeout), wakes(resultError), wakes(resultLimited)

			conn := dial(t, port)
			began := time.Now()
			send(t, conn, "hello\n")
			require.Eventually(t, func() bool { return lastRequest(t, c) != "" }, 5*time.Second, 20*time.Millisecond,
				"the request is held for the wake")
			endpointsChange(t, s, c, podSlice("api-real", "api-0", pod))
			if tt.limited {
				// Once the doorman has read the pod, its dial is refused a
				// place whatever follows.
				within(t, listed, "the doorman reading the pod")
			} else {
				within(t, failed, "the dial that fails")
				// The dial has ended once its proxied place is given back.
				require.Eventually(t, func() bool { return proxiedCount(s) == 0 }, 5*time.Second, 10*time.Millisecond)
			}
			endpointsChange(t, s, c, podSlice("api-real", "api-0", 0))
			require.Less(t, time.Since(began), maxWait, "the pod left before maxWait ran out")

			closedWithin(t, conn)
			assert.Eventually(t, func() bool { return wakes(resultTimeout) == timedOut+1 }, 5*time.Second,
				10*time.Millisecond, "counted as timeout")
			assert.Equal(t, errored, wakes(resultError), "not as error")
			assert.Equal(t, limited, wakes(resultLimited), "nor as limited")
			if !assert.Eventually(t, func() bool { return len(notServed(written())) == 1 }, 5*time.Second,
				10*time.Millisecond) {
				t.Fatalf("want one RequestNotServed warning, have %q", notServed(written()))
			}
			assert.Contains(t, notServed(written())[0], "The workload wasn't Ready within maxWait")
		})
	}
}
