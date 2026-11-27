// Package provenance computes SHA-256 digests of inputs and step outputs so
// every reported result can be re-checked against its source.
package provenance

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
)

// Sum returns the hex SHA-256 of b.
func Sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// SumFile returns the hex SHA-256 of a file's contents.
func SumFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return Sum(b), nil
}

// Combine folds part digests into one digest using length-prefixed framing,
// so concatenation ambiguities cannot collide.
func Combine(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(p)))
		h.Write(n[:])
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}
