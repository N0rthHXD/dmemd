// Package idgen provides the identifier representation shared by all
// persisted entities.
package idgen

import (
	"crypto/rand"
	"fmt"
)

// NewUUID returns a random RFC 4122 version 4 UUID in canonical string form.
//
// All service-generated entity identifiers (memories, revisions' parent IDs,
// sources, evidence and ingest jobs) use this representation. A failure of the
// operating system entropy source is unrecoverable, so it panics instead of
// returning a partially initialized identifier.
func NewUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("idgen: crypto/rand unavailable: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
