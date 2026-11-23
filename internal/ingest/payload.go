package ingest

import (
	"crypto/sha256"
	"encoding/hex"
)

// payloadHash returns the hex SHA-256 of data[off:off+length], or "" when the
// range is not fully present. Hashing the actual bytes makes duplicate content
// detectable regardless of container offset or metadata differences.
func payloadHash(data []byte, off, length int64) string {
	if off < 0 || length <= 0 || int(off)+int(length) > len(data) {
		return ""
	}
	sum := sha256.Sum256(data[off : off+length])
	return hex.EncodeToString(sum[:])
}

// sha256Hex returns the hex SHA-256 of an in-memory payload.
func sha256Hex(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
