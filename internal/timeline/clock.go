package timeline

import "fmt"

// unwrapSpan is the modulus of 32-bit decode-time fields.
const unwrapSpan = int64(1) << 32

// Unwrap32 extends a sequence of raw 32-bit decode times to monotonically
// non-decreasing 64-bit values. A drop from near 2^32 to near 0 is a real
// wraparound; any other backward move is reported as corruption.
// It returns the unwrapped values and the number of genuine wraps.
func Unwrap32(raw []uint32) ([]int64, int, error) {
	out := make([]int64, len(raw))
	if len(raw) == 0 {
		return out, 0, nil
	}
	out[0] = int64(raw[0])
	wraps := 0
	for i := 1; i < len(raw); i++ {
		prev, cur := raw[i-1], raw[i]
		switch {
		case cur >= prev:
			out[i] = out[i-1] + int64(cur-prev)
		case int64(prev)-int64(cur) > unwrapSpan/2:
			// Forward jump across the 2^32 boundary: genuine wrap.
			wraps++
			out[i] = out[i-1] + (int64(cur) + unwrapSpan - int64(prev))
		default:
			return nil, 0, fmt.Errorf("decode time moved backward at sample %d: %#x -> %#x (corrupt, not wraparound)", i, prev, cur)
		}
	}
	return out, wraps, nil
}
