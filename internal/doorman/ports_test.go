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
	"errors"
	"net"
	"os"
	"strconv"
	"sync"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// A port the tests give a doorman route is held from the moment freePort
// picks it until the doorman listens on it, and again from when the doorman
// lets go of it, while the test may give it back: a port nobody holds can be
// taken, in that gap, by any listener on :0, in this package or another that
// go test runs alongside, which would then answer for the doorman, or keep it
// from listening.
//
// A port is held by a TCP socket bound to it that isn't listening, which
// refuses connections, on Linux at once and on macOS by dropping them. The
// doorman takes it over by listening on that very socket, so there's no gap
// between the two.

// boundSocket is a TCP socket bound to a port on 127.0.0.1, 0 for any, that
// isn't listening.
func boundSocket(port int) (int, int32, error) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		return 0, 0, err
	}
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Port: port, Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		_ = syscall.Close(fd) // the bind's error is the one that matters
		return 0, 0, err
	}
	sa, err := syscall.Getsockname(fd)
	if err != nil {
		_ = syscall.Close(fd) // the name's error is the one that matters
		return 0, 0, err
	}
	return fd, int32(sa.(*syscall.SockaddrInet4).Port), nil
}

// refusingPort is a port that refuses every connection until the test ends:
// it's held, so no listener another test opens can take it and answer, as one
// could a port merely closed.
func refusingPort(t *testing.T) int32 {
	t.Helper()
	fd, port, err := boundSocket(0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = syscall.Close(fd) }) // nothing to do if it fails
	return port
}

// freePort is a port for a doorman route, held until the doorman listens.
func freePort(t *testing.T) int32 {
	t.Helper()
	fd, port, err := boundSocket(0)
	require.NoError(t, err)
	reserved.hold(port, fd)
	t.Cleanup(func() { reserved.drop(port) })
	return port
}

// reserved is the ports freePort gave out, held while no doorman listens on
// them.
var reserved = &reservations{held: map[int32]int{}, owned: map[int32]bool{}}

type reservations struct {
	mu sync.Mutex
	// held is each port's socket, while it's held; owned is the ports a
	// test still runs with, which are held again once the doorman lets go.
	held  map[int32]int
	owned map[int32]bool
}

func (r *reservations) hold(port int32, fd int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.held[port], r.owned[port] = fd, true
}

// drop lets go of a port as its test ends.
func (r *reservations) drop(port int32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if fd, ok := r.held[port]; ok {
		_ = syscall.Close(fd) // nothing to do if it fails
		delete(r.held, port)
	}
	delete(r.owned, port)
}

func (r *reservations) take(port int32) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fd, ok := r.held[port]
	delete(r.held, port)
	return fd, ok
}

// rehold holds a port the doorman let go of, for as long as its test runs,
// if it can: one with connections in TIME_WAIT can't be bound without
// SO_REUSEADDR, and holding it with that could let another listener share
// it.
func (r *reservations) rehold(port int32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.owned[port] {
		return
	}
	if _, ok := r.held[port]; ok {
		return
	}
	if fd, _, err := boundSocket(int(port)); err == nil {
		r.held[port] = fd
	}
}

// listen is the test doorman's net.Listen: on a port that's held, it
// listens on the socket that holds it.
func (r *reservations) listen(network, address string) (net.Listener, error) {
	_, p, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		return nil, err
	}
	port := int32(n)
	l, err := r.listenOn(network, address, port)
	if err != nil {
		return nil, err
	}
	return &heldAgain{Listener: l, held: r, port: port}, nil
}

func (r *reservations) listenOn(network, address string, port int32) (net.Listener, error) {
	fd, ok := r.take(port)
	if !ok {
		return net.Listen(network, address)
	}
	if err := syscall.Listen(fd, syscall.SOMAXCONN); err != nil {
		_ = syscall.Close(fd) // the listen's error is the one that matters
		return nil, err
	}
	f := os.NewFile(uintptr(fd), address)
	defer func() { _ = f.Close() }() // the listener has its own copy
	return net.FileListener(f)
}

// heldAgain is a doorman's listener on a test's port, which is held again
// once the doorman closes it.
type heldAgain struct {
	net.Listener
	held   *reservations
	port   int32
	closed sync.Once
}

func (l *heldAgain) Close() error {
	err := l.Listener.Close()
	if !errors.Is(err, net.ErrClosed) {
		l.closed.Do(func() { l.held.rehold(l.port) })
	}
	return err
}
