package bridge

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"sync"
	"time"
)

// base62Chars matches OpenCode's alphabet exactly (digits, uppercase, lowercase).
const base62Chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// randomLength is the number of random base62 characters appended to an ID.
const randomLength = 14

// idPrefixes maps logical kinds to OpenCode's short prefix tokens.
var idPrefixes = map[string]string{
	"session": "ses",
	"message": "msg",
	"part":    "prt",
}

// identifier generates OpenCode-compatible ascending IDs.
//
// Port of OpenCode's TypeScript implementation:
// https://github.com/anomalyco/opencode/blob/8f0d08fae07c97a090fcd31d0d4c4a6fa7eeaa1d/packages/opencode/src/id/id.ts
//
// Format: {prefix}_{timestampHex}{randomBase62}
//   - timestampHex: 12 hex chars encoding (timestampMs*0x1000 + counter) as a
//     big-endian 48-bit integer
//   - randomBase62: 14 random base62 characters
//
// IDs are monotonically increasing so a new user message always sorts after the
// previous assistant message, which OpenCode's prompt loop relies on. Unlike the
// Python original this generator is safe for concurrent use.
type identifier struct {
	mu            sync.Mutex
	lastTimestamp int64
	counter       int64
}

// ascending returns a new ascending ID for the given kind ("message", etc.).
func (id *identifier) ascending(kind string) (string, error) {
	prefix, ok := idPrefixes[kind]
	if !ok {
		return "", fmt.Errorf("unknown id prefix: %s", kind)
	}

	id.mu.Lock()
	now := time.Now().UnixMilli()
	if now != id.lastTimestamp {
		id.lastTimestamp = now
		id.counter = 0
	}
	id.counter++
	counter := id.counter
	id.mu.Unlock()

	encoded := (now*0x1000 + counter) & 0xFFFFFFFFFFFF
	buf := []byte{
		byte(encoded >> 40),
		byte(encoded >> 32),
		byte(encoded >> 24),
		byte(encoded >> 16),
		byte(encoded >> 8),
		byte(encoded),
	}

	suffix, err := randomBase62(randomLength)
	if err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(buf) + suffix, nil
}

// randomBase62 returns a cryptographically random base62 string of length n.
func randomBase62(n int) (string, error) {
	out := make([]byte, n)
	max := big.NewInt(int64(len(base62Chars)))
	for i := range out {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = base62Chars[idx.Int64()]
	}
	return string(out), nil
}
