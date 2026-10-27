package envelope

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"strings"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewID returns a time-ordered ID: "pm-" and a 26-character ULID.
func NewID(now time.Time) string {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(now.UnixMilli())<<16)
	if _, err := rand.Read(b[6:]); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return "pm-" + encode(b)
}

// KeyedID derives the ID for a send that carries an idempotency key, so a
// retried send from the same pane gets the same ID.
func KeyedID(hostID, fromBlock, key string) string {
	sum := sha256.Sum256([]byte(hostID + "\x00" + fromBlock + "\x00" + key))
	var b [16]byte
	copy(b[:], sum[:16])
	return "pm-k" + encode(b)[1:]
}

// ValidID reports whether id has the shape NewID or KeyedID produce.
// The check keeps IDs safe to use as file names.
func ValidID(id string) bool {
	rest, ok := strings.CutPrefix(id, "pm-")
	if !ok || len(rest) != 26 {
		return false
	}
	for i, r := range rest {
		if i == 0 && r == 'k' {
			continue
		}
		if !strings.ContainsRune(crockford, r) {
			return false
		}
	}
	return true
}

func encode(b [16]byte) string {
	hi := binary.BigEndian.Uint64(b[:8])
	lo := binary.BigEndian.Uint64(b[8:])
	out := make([]byte, 26)
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out)
}
