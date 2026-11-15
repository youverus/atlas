package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"time"
)

// sessionIDPattern is pi's validation rule for an explicit session id.
var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)

// ValidSessionID reports whether id may be used as an explicit session id.
func ValidSessionID(id string) bool { return sessionIDPattern.MatchString(id) }

// newSessionID returns a UUIDv7, the same shape pi generates. The timestamp
// prefix keeps ids sortable by creation time.
func newSessionID(now time.Time) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is unrecoverable; a fixed value keeps the format
		// valid instead of panicking in the middle of a session.
		b = [16]byte{}
	}
	ms := uint64(now.UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	b[6] = (b[6] & 0x0f) | 0x70 // version 7
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	)
}

// newEntryID returns pi's short entry id: 8 hex characters, checked for
// collisions against the entries already in the session.
func newEntryID(taken map[string]int) string {
	for i := 0; i < 100; i++ {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			break
		}
		id := hex.EncodeToString(b[:])
		if _, exists := taken[id]; !exists {
			return id
		}
	}
	// Fall back to a full UUID, which cannot realistically collide.
	return newSessionID(time.Now())
}
