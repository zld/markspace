package index

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// DigestIndex canonicalizes the index. Struct field order and input event
// order make the JSON deterministic without sorting maps.
func DigestIndex(idx *Index) string {
	b, err := json.Marshal(idx)
	if err != nil {
		// All fields are JSON-safe by construction.
		panic(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
