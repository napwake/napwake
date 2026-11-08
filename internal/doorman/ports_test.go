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
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// boundSocket is a TCP socket bound to a port on 127.0.0.1, 0 for any, that
// isn't listening: nothing else can take the port, and a connection to it
// is refused.
func boundSocket(t *testing.T, port int) (int, int32) {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	require.NoError(t, err)
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Port: port, Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		_ = syscall.Close(fd) // the bind's error is the one that matters
		require.NoError(t, err)
	}
	sa, err := syscall.Getsockname(fd)
	require.NoError(t, err)
	return fd, int32(sa.(*syscall.SockaddrInet4).Port)
}

// refusingPort is a port that refuses every connection until the test
// ends: it's held, so no listener another test opens can take it and
// answer, as one could a port merely closed.
func refusingPort(t *testing.T) int32 {
	t.Helper()
	fd, port := boundSocket(t, 0)
	t.Cleanup(func() { _ = syscall.Close(fd) }) // nothing to do if it fails
	return port
}
