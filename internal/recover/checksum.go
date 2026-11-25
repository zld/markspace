package recover

import (
	"crypto/sha256"
	"encoding/hex"
)

// LocalizeSingleByte finds a one-byte mutation whose repair makes SHA-256 over
// r match expectedDigest. Generated checksum fixtures use exactly one mutation;
// arbitrary multi-byte damage is intentionally left as a range-level failure.
func LocalizeSingleByte(data []byte, r Range, expectedDigest string) (int64, bool) {
	if r.Start < 0 || r.End > int64(len(data)) || r.Start >= r.End {
		return -1, false
	}
	want, err := hex.DecodeString(expectedDigest)
	if err != nil || len(want) != sha256.Size {
		return -1, false
	}
	for off := r.Start; off < r.End; off++ {
		original := data[off]
		for v := 0; v < 256; v++ {
			if byte(v) == original {
				continue
			}
			data[off] = byte(v)
			sum := sha256.Sum256(data[r.Start:r.End])
			if hex.EncodeToString(sum[:]) == expectedDigest {
				data[off] = original
				return off, true
			}
		}
		data[off] = original
	}
	return -1, false
}
