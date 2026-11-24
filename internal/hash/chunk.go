// Package hash computes deterministic content hashes for media chunks and
// the segment-level canonical records that reference them.
package hash

import (
	"crypto/sha256"
	"encoding/hex"
	stdhash "hash"
)

// Hasher is the small streaming interface used by the cutter.
type Hasher interface {
	Write(p []byte) (int, error)
	Sum(b []byte) []byte
}

// New returns the hash used throughout MediaRC.
func New() stdhash.Hash { return sha256.New() }

// Sum returns the lowercase hexadecimal SHA-256 of data.
func Sum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// SumParts hashes ordered parts. Parts remain order-sensitive.
func SumParts(parts ...[]byte) string {
	h := New()
	for _, part := range parts {
		_, _ = h.Write(part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Verify reports whether got equals the SHA-256 of data.
func Verify(data []byte, expected string) bool {
	return Sum(data) == expected
}
