package ebml

import (
	"math/bits"

	"medtrace/internal/mederr"
)

// DecodeVINT decodes a VINT from the front of b, returning the value, the
// consumed width and whether the reserved unknown-length marker is present.
func DecodeVINT(b []byte) (val uint64, width int, unknown bool, err error) {
	if len(b) == 0 {
		return 0, 0, false, mederr.At(mederr.CatTruncatedHeader, 0, "empty VINT")
	}
	if b[0] == 0 {
		return 0, 0, false, mederr.At(mederr.CatInvalidVINT, 0, "VINT starts with 0x00 and has no length marker")
	}
	width = bits.LeadingZeros8(b[0]) + 1
	if len(b) < width {
		return 0, width, false, mederr.At(mederr.CatTruncatedHeader, 0, "VINT needs %d bytes, got %d", width, len(b))
	}
	val = uint64(b[0] & (0xFF >> uint(width)))
	for i := 1; i < width; i++ {
		val = (val << 8) | uint64(b[i])
	}
	if val == (uint64(1)<<uint(7*width))-1 {
		unknown = true
	}
	return val, width, unknown, nil
}
