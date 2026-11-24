package cli

import (
	"crypto/sha256"
	"encoding/hex"
)

// sha256Hex is a small convenience used for diagnostic digests.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
