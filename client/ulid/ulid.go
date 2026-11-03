// Package ulid mints monotonic, time-ordered ids (a ULID shape: 48-bit
// millisecond timestamp + 80 random bits, Crockford base32, 26 chars).
// Ids sort by creation time as strings, which is what an SSE resume
// cursor (Last-Event-ID) compares against. The server and the client
// both mint through it.
package ulid

import (
	"crypto/rand"
	"encoding/binary"
	"sync"
	"time"
)

// Len is the length of an id New returns.
const Len = 26

var (
	idMu    sync.Mutex
	lastRaw [16]byte
	haveRaw bool
)

// New returns a time-ordered id, monotonic within the process: the
// millisecond field never moves backwards (a wall-clock step back is
// clamped to the last value), and inside one millisecond the random
// suffix increments — so in-process ordering never regresses, which is
// what the SSE resume cursor relies on.
func New() string {
	idMu.Lock()
	defer idMu.Unlock()
	ms := time.Now().UnixMilli()
	if haveRaw {
		if last := int64(binary.BigEndian.Uint64(lastRaw[:8]) >> 16); ms <= last {
			ms = last
		}
	}
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(ms)<<16) // top 48 bits
	if _, err := rand.Read(b[6:]); err != nil {
		panic("ulid: crypto/rand unavailable: " + err.Error())
	}
	if haveRaw && bytesLE(b, lastRaw) {
		// The fresh draw sorted below the last id (same millisecond):
		// take the last value and bump it, so the new id is exactly
		// last+1, never last-epsilon.
		copy(b[:], lastRaw[:])
		incBytes(b[6:])
	}
	copy(lastRaw[:], b[:])
	haveRaw = true
	id := encodeULID(b)
	return string(id[:])
}

// encodeULID renders 128 bits as 26 Crockford base32 chars (130 bits
// with two zero bits of front padding).
func encodeULID(b [16]byte) [Len]byte {
	const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	var out [Len]byte
	for i := Len - 1; i >= 0; i-- {
		out[i] = crockford[b[15]&31]
		shrBytes(b[:], 5)
	}
	return out
}

// shrBytes shifts a big-endian byte slice right by n (< 8) bits,
// in place.
func shrBytes(b []byte, n uint) {
	var carry byte
	for i := 0; i < len(b); i++ {
		v := b[i]
		b[i] = v>>n | carry
		carry = v << (8 - n)
	}
}

// incBytes adds one to a big-endian byte slice, in place.
func incBytes(b []byte) {
	for i := len(b) - 1; i >= 0; i-- {
		b[i]++
		if b[i] != 0 {
			return
		}
	}
}

// bytesLE reports a < b.
func bytesLE(a, b [16]byte) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
