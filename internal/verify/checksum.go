// Package verify computes SHA-256 checksums of artifacts so that runs can
// be compared byte-for-byte (full run vs. interrupted-then-resumed run).
package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// Sum256 streams r and returns the lowercase hex SHA-256 of its contents.
func Sum256(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Sum256Bytes returns the lowercase hex SHA-256 of b.
func Sum256Bytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// SumFile returns the lowercase hex SHA-256 of the file at path.
func SumFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return Sum256(f)
}
