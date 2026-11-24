package manifest

import (
	"crypto/sha256"
	"encoding/hex"
)

// HashSHA256 returns the lowercase hex SHA-256 of b.
func HashSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// FrameFileName is the deterministic output name for one frame.
func FrameFileName(trackID string, index int) string {
	return sanitize(trackID) + "-" + pad(index) + ".png"
}

func sanitize(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

func pad(n int) string {
	if n < 0 {
		n = 0
	}
	s := []byte("0000")
	for i := 3; i >= 0 && n > 0; i-- {
		s[i] = byte('0' + n%10)
		n /= 10
	}
	return string(s)
}
