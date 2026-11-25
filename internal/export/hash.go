package export

import (
	"crypto/sha256"
	"encoding/hex"
)

func aggregate(items []string) string {
	h := sha256.New()
	for _, item := range items {
		_, _ = h.Write([]byte(item))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
