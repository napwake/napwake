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

package doorman

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

const (
	// tokenTTL is how long the Wake it button on an asleep page works: long
	// enough for someone to read the page, short enough that a copy of it
	// kept by a crawler or a cache is soon no use.
	tokenTTL = 10 * time.Minute

	// tokenSkew is how far ahead of this replica's clock another replica's
	// may be when it signs a token.
	tokenSkew = time.Minute

	// wakeKeySize is the length of the key tokens are signed with, as long
	// as the MAC.
	wakeKeySize = sha256.Size

	tokenVersion = 1
	tokenSize    = 1 + 8 + sha256.Size
)

// tokenContext keeps the MAC for this use alone, should the key ever sign
// anything else.
const tokenContext = "napwake.dev/wake-it\x00"

// tokenScope is what a token is good for: one workload's one pause. A
// token from another workload's page, or from an earlier pause of this one,
// wakes nothing.
type tokenScope struct {
	workload types.NamespacedName
	uid      types.UID
	pausedAt time.Time
}

// wakeKeys sign and check the tokens behind the asleep page's Wake it
// button. Tokens signed with the key before the current one still pass
// until previousUntil, as long as one signed just before the change could
// last, so a key changed by another replica doesn't fail a click on a page
// shown a moment before, and a key that leaked stops working soon after
// it's replaced.
type wakeKeys struct {
	mu            sync.RWMutex
	current       []byte
	previous      []byte
	previousUntil time.Time
}

// newWakeKeys starts with a key of this replica's own, used until one
// shared with the other replicas is loaded.
func newWakeKeys() *wakeKeys {
	return &wakeKeys{current: newWakeKey()}
}

func newWakeKey() []byte {
	key := make([]byte, wakeKeySize)
	_, _ = rand.Read(key) // never fails, as of Go 1.24
	return key
}

// use makes key the one tokens are signed with, from now.
func (k *wakeKeys) use(key []byte, now time.Time) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if hmac.Equal(key, k.current) {
		return
	}
	k.previous, k.current = k.current, append([]byte(nil), key...)
	k.previousUntil = now.Add(tokenTTL + tokenSkew)
}

// sign returns a token for scope, good until expires.
func (k *wakeKeys) sign(scope tokenScope, expires time.Time) string {
	k.mu.RLock()
	key := k.current
	k.mu.RUnlock()
	token := make([]byte, 0, tokenSize)
	token = append(token, tokenVersion)
	token = binary.BigEndian.AppendUint64(token, uint64(expires.Unix()))
	token = append(token, tokenMAC(key, scope, token[1:])...)
	return base64.RawURLEncoding.EncodeToString(token)
}

// valid reports whether token was signed by a doorman holding the key, for
// scope, and hasn't expired. The MAC is compared in constant time, so the
// time taken says nothing about how much of a forged one was right.
func (k *wakeKeys) valid(token string, scope tokenScope, now time.Time) bool {
	if !wellFormed(token) {
		return false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != tokenSize || raw[0] != tokenVersion {
		return false
	}
	expires := time.Unix(int64(binary.BigEndian.Uint64(raw[1:9])), 0)
	if !now.Before(expires) || expires.After(now.Add(tokenTTL+tokenSkew)) {
		return false
	}
	k.mu.RLock()
	keys := [][]byte{k.current}
	if now.Before(k.previousUntil) {
		keys = append(keys, k.previous)
	}
	k.mu.RUnlock()
	ok := false
	for _, key := range keys {
		if key != nil && hmac.Equal(raw[9:], tokenMAC(key, scope, raw[1:9])) {
			ok = true
		}
	}
	return ok
}

// tokenLength is how long a token is, spelt out.
var tokenLength = base64.RawURLEncoding.EncodedLen(tokenSize)

// wellFormed reports whether token is spelt as sign spells one: base64url
// digits alone, and as many as a token takes. The decoder skips line ends
// wherever they are, so without this a token would pass in more than one
// spelling.
func wellFormed(token string) bool {
	if len(token) != tokenLength {
		return false
	}
	for i := range len(token) {
		switch c := token[i]; {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// tokenMAC is the HMAC-SHA256 of the scope and expiry, each field ended by
// a NUL, which no namespace, name or UID contains, so no two scopes run
// together into the same input.
func tokenMAC(key []byte, scope tokenScope, expiry []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(tokenContext))
	for _, field := range []string{scope.workload.Namespace, scope.workload.Name, string(scope.uid)} {
		mac.Write([]byte(field))
		mac.Write([]byte{0})
	}
	mac.Write(binary.BigEndian.AppendUint64(nil, uint64(scope.pausedAt.UnixNano())))
	mac.Write(expiry)
	return mac.Sum(nil)
}
