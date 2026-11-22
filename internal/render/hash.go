// Package render exports composed timelines as PNG frame sequences with
// per-segment SHA-256 manifests for reproducible regression comparison.
package render

import (
	"crypto/sha256"
	"fmt"
	"os"
)

// SumSegment hashes the concatenated payloads of one segment in order.
func SumSegment(payloads [][]byte) [sha256.Size]byte {
	h := sha256.New()
	for _, p := range payloads {
		h.Write(p)
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

// SumFiles hashes the concatenated contents of files in order.
func SumFiles(paths []string) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	h := sha256.New()
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return sum, fmt.Errorf("hash %s: %w", p, err)
		}
		h.Write(b)
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

// Hex formats a hash as lowercase hex.
func Hex(sum [sha256.Size]byte) string { return fmt.Sprintf("%x", sum[:]) }
