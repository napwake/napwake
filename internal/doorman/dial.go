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
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"slices"
	"sync"
	"time"
)

// dialStagger is how long a pod has to answer before the next is tried
// beside it, as Happy Eyeballs (RFC 8305) waits between addresses. A live
// pod on the cluster network answers in a millisecond or two. One listed
// Ready that has gone, as a workload scales to zero, never does, and the
// caller would wait out dialTimeout for it (P89).
const dialStagger = 250 * time.Millisecond

var (
	// errNoneReady means the Service's endpoints changed during the dial
	// and listed no Ready pod: the workload has none to serve the caller.
	errNoneReady = errors.New("no Ready pod is left to connect to")

	// errProxyLimited means no proxied place was free for the connection.
	errProxyLimited = errors.New("too many connections proxied")
)

// dialFunc opens a connection, as a net.Dialer's DialContext does.
type dialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// listFunc reads a Service port's Ready addresses again, with a channel
// closed when they may have changed since, as backends.get does.
type listFunc func(ctx context.Context) ([]string, <-chan struct{}, error)

// dialAny connects to one of addrs, a Service port's Ready addresses,
// trying them in random order so connections spread over the workload's
// pods. Each has dialStagger to answer before the next is tried beside it,
// and one that fails has the next tried at once. The first to answer is
// kept. The others are given up, or closed if they answer too.
//
// When changed closes, the addresses are read again with list. A dial to an
// address no longer listed is given up, and any newly listed joins the
// queue. One still listed goes on: the endpoints are read again on every
// resync, and a pod still listed is as likely to answer as before. Once
// none is listed, dialAny ends with errNoneReady. It gives up after
// dialTimeout in all.
func dialAny(ctx context.Context, dial dialFunc, addrs []string, changed <-chan struct{}, list listFunc) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	r := &dialRace{dial: dial, results: make(chan dialed), done: make(chan struct{}),
		inFlight: map[int]attempt{}, tried: map[string]bool{}}
	defer r.stop(cancel)
	r.enqueue(addrs)

	stagger := time.NewTimer(dialStagger)
	defer stagger.Stop()
	// due says the last dial started has had its dialStagger.
	due := false
	next := func() {
		if r.start(ctx) {
			stagger.Reset(dialStagger)
			due = false
		}
	}
	next()
	lastErr := errNoneReady
	for {
		if len(r.inFlight) == 0 {
			next()
			if len(r.inFlight) == 0 {
				return nil, lastErr
			}
		}
		select {
		case d := <-r.results:
			addr := r.inFlight[d.id].addr
			delete(r.inFlight, d.id)
			if d.err == nil {
				return d.conn, nil
			}
			lastErr = fmt.Errorf("dialing %s: %w", addr, d.err)
			next()
		case <-stagger.C:
			due = true
			next()
		case <-changed:
			var listed []string
			var err error
			if listed, changed, err = list(ctx); err != nil {
				// Not knowing what changed, keep to what was listed.
				continue
			}
			if len(listed) == 0 {
				return nil, errNoneReady
			}
			r.keep(listed)
			if due {
				next()
			}
		case <-ctx.Done():
			return nil, fmt.Errorf("dialing the workload's pods: %w", ctx.Err())
		}
	}
}

// dialRace is the dials one dialAny has under way, and those it has still
// to start.
type dialRace struct {
	dial    dialFunc
	results chan dialed
	// done is closed once dialAny returns, so a dial still under way
	// closes what it opens.
	done  chan struct{}
	dials sync.WaitGroup

	queue    []string
	inFlight map[int]attempt
	tried    map[string]bool
	started  int
}

// attempt is one dial under way.
type attempt struct {
	addr   string
	cancel context.CancelFunc
}

// dialed is how one dial ended.
type dialed struct {
	id   int
	conn net.Conn
	err  error
}

// enqueue adds the addresses not yet tried or queued, in random order.
func (r *dialRace) enqueue(addrs []string) {
	for _, i := range rand.Perm(len(addrs)) {
		if addr := addrs[i]; !r.tried[addr] && !slices.Contains(r.queue, addr) {
			r.queue = append(r.queue, addr)
		}
	}
}

// keep gives up the dials, and drops the queued addresses, not in listed,
// and queues those newly listed.
func (r *dialRace) keep(listed []string) {
	for _, a := range r.inFlight {
		if !slices.Contains(listed, a.addr) {
			a.cancel()
		}
	}
	r.queue = slices.DeleteFunc(r.queue, func(addr string) bool { return !slices.Contains(listed, addr) })
	r.enqueue(listed)
}

// start dials the next queued address, if there is one.
func (r *dialRace) start(ctx context.Context) bool {
	if len(r.queue) == 0 {
		return false
	}
	addr := r.queue[0]
	r.queue = r.queue[1:]
	r.tried[addr] = true
	dialCtx, cancel := context.WithCancel(ctx)
	id := r.started
	r.started++
	r.inFlight[id] = attempt{addr: addr, cancel: cancel}
	r.dials.Go(func() {
		defer cancel()
		conn, err := r.dial(dialCtx, "tcp", addr)
		select {
		case r.results <- dialed{id: id, conn: conn, err: err}:
		case <-r.done:
			if conn != nil {
				_ = conn.Close() // another pod answered first; nothing to do if it fails
			}
		}
	})
	return true
}

// stop gives up every dial still under way, with cancel, and waits for
// them.
func (r *dialRace) stop(cancel context.CancelFunc) {
	close(r.done)
	cancel()
	r.dials.Wait()
}
