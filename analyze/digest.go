package analyze

import (
	"crypto/sha256"
	"encoding/hex"
)

// Digest returns the lower-case hex SHA-256 of b. It is the single content
// address used by analysis, caching and checkpoints.
func Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// DigestString is Digest rendered as a stable result fingerprint.
func DigestString(s string) string { return Digest([]byte(s)) }
