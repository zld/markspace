package batch

import (
	"crypto/sha256"
	"encoding/hex"
)

// SHA256Hex returns the lower-case hex SHA-256 of data, used for checkpoint
// input digests and the demo summary.
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
