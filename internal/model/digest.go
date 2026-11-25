package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
)

// SHA256Hex returns the lowercase hex SHA-256 digest of b.
func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// DigestJSON returns the SHA-256 of the canonical JSON encoding of v.
// Structs and slices are used throughout the kernel so encoding is stable.
func DigestJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("digest json: %w", err)
	}
	return SHA256Hex(b), nil
}

// Fingerprint joins labelled digests into a single content key.
func Fingerprint(parts ...string) string {
	h := sha256.New()
	for i, p := range parts {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ValidDigest reports whether s is a lowercase 64-character hex digest.
func ValidDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// HashReader returns the SHA-256 hex digest and byte count of r.
func HashReader(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.CopyBuffer(h, r, make([]byte, 64<<10))
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
