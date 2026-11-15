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

// Package dialrace connects to one of several addresses that serve the
// same thing, such as a Service's Ready pods, so that one that has gone but
// is still listed costs a caller a moment, not a timeout. The doorman dials
// a workload's pods with it, and the NEG forwarder the doorman's.
package dialrace

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"slices"
	"strings"
	"sync"
	"time"
)

// Stagger is how long an address has to answer before the next is tried
// beside it, as Happy Eyeballs (RFC 8305) waits between addresses. A live
// pod on the cluster network answers in a millisecond or two. One listed
// Ready that has gone, as a workload scales to zero or a pod is drained,
// never does, and the caller would wait out the whole timeout for it (P89).
const Stagger = 250 * time.Millisecond

// ErrNoneLeft means the addresses changed during the dial and listed none.
var ErrNoneLeft = errors.New("no address is left to connect to")

// DialFunc opens a connection, as a net.Dialer's DialContext does.
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// ListFunc reads the addresses again, with a channel closed when they may
// have changed since.
type ListFunc func(ctx context.Context) ([]string, <-chan struct{}, error)

// Dialer connects over TCP to one of several addresses.
type Dialer struct {
	// Dial opens each connection.
	Dial DialFunc
	// Timeout bounds the whole dial, however many addresses are tried.
	Timeout time.Duration
}

// DialAny connects to one of addrs, trying them in random order so
// connections spread over what they address. Each has Stagger to answer
// before the next is tried beside it, and one that fails has the next tried
// at once. The first to answer is kept. The others are given up, or closed
// if they answer too.
//
// When changed closes, the addresses are read again with list. A dial to an
// address no longer listed is given up, and any newly listed joins the
// queue. One still listed goes on: addresses are read again on every
// resync, and one still listed is as likely to answer as before. Once none
// is listed, DialAny ends with ErrNoneLeft. A nil changed is never read
// again. DialAny gives up after d.Timeout, with every error it met.
func (d Dialer) DialAny(ctx context.Context, addrs []string, changed <-chan struct{}, list ListFunc) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, d.Timeout)
	defer cancel()
	r := &race{dial: d.Dial, results: make(chan dialed), done: make(chan struct{}),
		inFlight: map[int]attempt{}, tried: map[string]bool{}}
	defer r.stop(cancel)
	r.enqueue(addrs)

	stagger := time.NewTimer(Stagger)
	defer stagger.Stop()
	// due says the last dial started has had its Stagger.
	due := false
	next := func() {
		if r.start(ctx) {
			stagger.Reset(Stagger)
			due = false
		}
	}
	next()
	var errs []error
	for {
		if len(r.inFlight) == 0 {
			next()
			if len(r.inFlight) == 0 {
				if len(errs) == 0 {
					return nil, ErrNoneLeft
				}
				return nil, errors.Join(errs...)
			}
		}
		select {
		case res := <-r.results:
			delete(r.inFlight, res.id)
			if res.err == nil {
				return res.conn, nil
			}
			errs = append(errs, res.err)
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
				return nil, ErrNoneLeft
			}
			r.keep(listed)
			if due {
				next()
			}
		case <-ctx.Done():
			return nil, errors.Join(append(errs, fmt.Errorf("dialing %s: %w", r.dialing(), ctx.Err()))...)
		}
	}
}

// race is the dials one DialAny has under way, and those it has still to
// start.
type race struct {
	dial    DialFunc
	results chan dialed
	// done is closed once DialAny returns, so a dial still under way
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
func (r *race) enqueue(addrs []string) {
	for _, i := range rand.Perm(len(addrs)) {
		if addr := addrs[i]; !r.tried[addr] && !slices.Contains(r.queue, addr) {
			r.queue = append(r.queue, addr)
		}
	}
}

// keep gives up the dials, and drops the queued addresses, not in listed,
// and queues those newly listed.
func (r *race) keep(listed []string) {
	for _, a := range r.inFlight {
		if !slices.Contains(listed, a.addr) {
			a.cancel()
		}
	}
	r.queue = slices.DeleteFunc(r.queue, func(addr string) bool { return !slices.Contains(listed, addr) })
	r.enqueue(listed)
}

// dialing is the addresses of the dials under way, for an error.
func (r *race) dialing() string {
	addrs := make([]string, 0, len(r.inFlight))
	for _, a := range r.inFlight {
		addrs = append(addrs, a.addr)
	}
	slices.Sort(addrs)
	return strings.Join(addrs, ", ")
}

// start dials the next queued address, if there is one.
func (r *race) start(ctx context.Context) bool {
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
				_ = conn.Close() // another address answered first; nothing to do if it fails
			}
		}
	})
	return true
}

// stop gives up every dial still under way, with cancel, and waits for
// them.
func (r *race) stop(cancel context.CancelFunc) {
	close(r.done)
	cancel()
	r.dials.Wait()
}
