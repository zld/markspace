package clock

import (
	"math"
	"math/bits"
)

// gcd returns the greatest common divisor of a and b; gcd(0, b) == b.
func gcd(a, b uint64) uint64 {
	for b != 0 {
		a, b = b, a%b
	}
	if a == 0 {
		return 1
	}
	return a
}

func abs64(v int64) uint64 {
	if v < 0 {
		return uint64(-(v + 1)) + 1
	}
	return uint64(v)
}

func negate(v int64) (int64, error) {
	if v == math.MinInt64 {
		return 0, ErrOverflow
	}
	return -v, nil
}

func addInt64(a, b int64) (int64, error) {
	s := a + b
	if (b > 0 && s < a) || (b < 0 && s > a) {
		return 0, ErrOverflow
	}
	return s, nil
}

func mulInt64(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	neg := (a < 0) != (b < 0)
	hi, lo := bits.Mul64(abs64(a), abs64(b))
	if hi != 0 {
		return 0, ErrOverflow
	}
	if neg {
		if lo > 1<<63 {
			return 0, ErrOverflow
		}
		return -int64(lo), nil
	}
	if lo > math.MaxInt64 {
		return 0, ErrOverflow
	}
	return int64(lo), nil
}

// u128 is a tiny unsigned 128-bit value used only for exact comparison.
type u128 struct{ hi, lo uint64 }

func mul128(a, b uint64) u128 {
	hi, lo := bits.Mul64(a, b)
	return u128{hi: hi, lo: lo}
}

func cmp128(a, b u128) int {
	switch {
	case a.hi < b.hi:
		return -1
	case a.hi > b.hi:
		return 1
	case a.lo < b.lo:
		return -1
	case a.lo > b.lo:
		return 1
	default:
		return 0
	}
}
