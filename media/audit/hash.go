// Package audit is the reporting subsystem. It collects the flat anomaly
// manifest, per-segment boundary/hash/recovery records and the deterministic
// verification metrics, and writes them as sorted JSON. All hashing is
// SHA-256 over exact bytes; no wall-clock time or map iteration order is
// allowed to make two identical runs differ.
package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// HashBytes returns the hex SHA-256 of b.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// HashFile streams a file through SHA-256 so segment size does not cost an
// extra full read.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// NewHashingWriter returns a writer that tees every write into a SHA-256 and
// a closure returning the final hex digest. It lets callers hash an artifact
// in the same pass that writes it to disk.
func NewHashingWriter(w io.Writer) (io.Writer, func() string) {
	h := sha256.New()
	tw := io.MultiWriter(w, h)
	return tw, func() string { return hex.EncodeToString(h.Sum(nil)) }
}
