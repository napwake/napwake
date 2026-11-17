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
	"net"

	"github.com/napwake/napwake/internal/dialrace"
)

// errProxyLimited means no proxied place was free for the connection.
var errProxyLimited = errors.New("too many connections proxied")

// dialAny connects to one of addrs, a Service port's Ready addresses, within
// dialTimeout. Each pod has dialrace.Stagger to answer before the next is
// tried beside it. When changed closes, the addresses are read again with
// list: a pod that has left, as one a pause has just scaled away, is given
// up, and with none left, dialAny ends with dialrace.ErrNoneLeft (P89).
func dialAny(ctx context.Context, dial dialrace.DialFunc, addrs []string, changed <-chan struct{},
	list dialrace.ListFunc) (net.Conn, error) {
	return dialrace.Dialer{Dial: dial, Timeout: dialTimeout}.DialAny(ctx, addrs, changed, list)
}
