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

package dialrace

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var addrs = []string{"10.244.1.5:8080", "10.244.1.6:8080"}

// answerBound is how soon a dial whose first address never answers must
// have the next: far more than Stagger, which is all it should take, and
// far less than the timeouts its callers set, which it cost before (P89).
const answerBound = time.Second

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

// pipe is one end of a connection, for a dial to answer with, closed when
// the test ends.
func pipe(t *testing.T) net.Conn {
	t.Helper()
	conn, peer := net.Pipe()
	t.Cleanup(func() {
		_ = conn.Close() // nothing to do if it fails
		_ = peer.Close() // nothing to do if it fails
	})
	return conn
}

// An address that never answers, as one that has gone doesn't, has the
// next tried beside it, after Stagger, which has the connection. The one
// that never answered is given up.
func TestDialAny_TriesTheNextBesideOneThatNeverAnswers(t *testing.T) {
	live := pipe(t)
	ended := make(chan error, 1)
	var dials atomic.Int32
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		if dials.Add(1) == 1 {
			<-ctx.Done()
			ended <- ctx.Err()
			return nil, ctx.Err()
		}
		return live, nil
	}

	began := time.Now()
	conn, err := Dialer{Dial: dial, Timeout: time.Minute}.DialAny(context.Background(), addrs, nil, nil)

	require.NoError(t, err)
	assert.Less(t, time.Since(began), answerBound, "the next tried within %s", answerBound)
	assert.Same(t, live, conn)
	assert.ErrorIs(t, within(t, ended, "the end of the dial that never answered"), context.Canceled,
		"the one that never answered is given up")
}

// With no address answering, DialAny gives up at its timeout, however many
// it tried, and says which it was waiting on.
func TestDialAny_GivesUpAtItsTimeout(t *testing.T) {
	ended := make(chan error, len(addrs))
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		ended <- ctx.Err()
		return nil, ctx.Err()
	}

	_, err := Dialer{Dial: dial, Timeout: 2 * Stagger}.DialAny(context.Background(), addrs, nil, nil)

	assert.ErrorIs(t, err, context.DeadlineExceeded)
	for _, addr := range addrs {
		assert.ErrorContains(t, err, addr)
	}
	for range addrs {
		assert.Error(t, within(t, ended, "the end of each dial"))
	}
}

// When every address fails, DialAny says how each did.
func TestDialAny_ReportsEveryFailure(t *testing.T) {
	failures := map[string]error{addrs[0]: errors.New("connection refused"), addrs[1]: errors.New("no route to host")}
	dial := func(_ context.Context, _, addr string) (net.Conn, error) { return nil, failures[addr] }

	_, err := Dialer{Dial: dial, Timeout: time.Minute}.DialAny(context.Background(), addrs, nil, nil)

	for _, failure := range failures {
		assert.ErrorIs(t, err, failure)
	}
	assert.NotErrorIs(t, err, ErrNoneLeft)
}

// Once the addresses are read again and list none, the dial ends with
// ErrNoneLeft, and the one under way is given up.
func TestDialAny_EndsWhenNoneIsLeft(t *testing.T) {
	dialing := make(chan struct{}, 1)
	ended := make(chan error, 1)
	d := Dialer{Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		dialing <- struct{}{}
		<-ctx.Done()
		ended <- ctx.Err()
		return nil, ctx.Err()
	}, Timeout: time.Minute}
	changed := make(chan struct{})
	list := func(context.Context) ([]string, <-chan struct{}, error) { return nil, make(chan struct{}), nil }
	results := make(chan error, 1)
	go func() {
		_, err := d.DialAny(context.Background(), addrs[:1], changed, list)
		results <- err
	}()

	within(t, dialing, "the dial")
	close(changed)

	assert.ErrorIs(t, within(t, results, "the end of DialAny"), ErrNoneLeft)
	assert.ErrorIs(t, within(t, ended, "the end of the dial"), context.Canceled, "the dial under way is given up")
}
