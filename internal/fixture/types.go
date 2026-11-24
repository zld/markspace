// Package fixture builds self-contained sample media in memory: a valid
// MP4/fMP4 with multiple timescales and cross-track timed metadata, plus the
// deliberately damaged variants the demo and tests exercise.
package fixture

import (
	"crypto/sha256"
	"fmt"
)

// Payload is one generated fixture.
type Payload struct {
	Name string
	Data []byte
	// Damage describes the intentional corruption so tests and the CLI can
	// assert that the reported offset points at the first unparsable byte.
	Damage *Damage
}

// Damage describes an intentionally corrupted fixture.
type Damage struct {
	Kind string
	// Offset is the byte position of the first damaged or missing input byte.
	Offset int64
	// Detail is a short human-readable description.
	Detail string
}

// Range is a half-open verified byte range.
type Range struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// Digest returns the SHA-256 of a fixture.
func Digest(data []byte) string {
	s := sha256.Sum256(data)
	return fmt.Sprintf("%x", s[:])
}
