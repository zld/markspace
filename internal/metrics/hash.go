package metrics

import "crypto/sha256"

// sha256Bytes returns the SHA-256 digest of b.
func sha256Bytes(b []byte) [32]byte { return sha256.Sum256(b) }

// hex renders bytes as lowercase hexadecimal without fmt allocation churn.
func hex(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = digits[v>>4]
		out[i*2+1] = digits[v&0x0f]
	}
	return string(out)
}
