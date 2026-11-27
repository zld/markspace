// Package fingerprint computes content hashes over normalized PCM so
// repeated material is detected and committed results are reused.
package fingerprint

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"

	"medialib/internal/audio"
)

// Normalize renders PCM as canonical little-endian int16 with
// deterministic rounding, prefixed by a format header, so identical
// signals hash identically across runs and machines.
func Normalize(p audio.PCM) []byte {
	head := fmt.Sprintf("pcm:%d:%d:%d\n", p.Rate, p.Channels, p.Frames())
	b := make([]byte, 0, len(head)+len(p.Samples)*2)
	b = append(b, head...)
	var tmp [2]byte
	// Scale by 32768 so clipping is symmetric: both 1.0 and -1.0 map
	// onto the int16 rails.
	for _, s := range p.Samples {
		v := int32(math.Round(float64(s) * 32768))
		if v > 32767 {
			v = 32767
		}
		if v < -32768 {
			v = -32768
		}
		binary.LittleEndian.PutUint16(tmp[:], uint16(int16(v)))
		b = append(b, tmp[:]...)
	}
	return b
}

// Hash returns the SHA-256 hex digest of the normalized segment.
func Hash(p audio.PCM) string {
	return HashBytes(Normalize(p))
}

// HashBytes hashes raw bytes with SHA-256.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
