package rtpreplay

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// sha256Hex returns the lowercase hex SHA-256 of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// digestJSON hashes the canonical JSON encoding of v.
func digestJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return sha256Hex([]byte("marshal-error:" + err.Error()))
	}
	return sha256Hex(b)
}
