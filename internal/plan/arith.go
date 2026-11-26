package plan

import "math/bits"

// mul64 returns the 128-bit product of two non-negative int64 values.
func mul64(a, b int64) (hi, lo uint64) {
	return bits.Mul64(uint64(a), uint64(b))
}
