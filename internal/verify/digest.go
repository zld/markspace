package verify

import (
	"crypto/sha256"
	"encoding/hex"
)

// SHA256 returns "sha256:<hex>" for arbitrary bytes.
func SHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
