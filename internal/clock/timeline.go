// Package clock provides the shared integer-nanosecond timebase used by
// the Matroska and RTP subsystems. All conversions are exact integer
// arithmetic; overflow is reported, never silently wrapped.
package clock

import (
	"math"

	"continuity/internal/cerr"
)

const path = "clock"

// ToNanos converts integer ticks to nanoseconds: ns = ticks * scale.
// A zero scale or an overflowing product is an error.
func ToNanos(ticks int64, scale uint64) (int64, error) {
	if scale == 0 {
		return 0, cerr.New(cerr.CodeTimestampOverflow, -1, path, "zero timecode scale")
	}
	neg := ticks < 0
	var mag uint64
	if neg {
		mag = uint64(-(ticks + 1)) + 1 // |ticks|, safe for math.MinInt64
	} else {
		mag = uint64(ticks)
	}
	lim := uint64(math.MaxInt64) / scale
	if neg {
		lim++ // negative side may reach 2^63
	}
	if mag > lim {
		return 0, cerr.New(cerr.CodeTimestampOverflow, -1, path, "tick*scale overflows int64 nanoseconds")
	}
	return ticks * int64(scale), nil
}

// ToNanosAtRate converts clock units at the given rate (Hz) to
// nanoseconds: ns = units * 1e9 / rate. The division is exact for the
// fractional part and overflow of the whole part is an error.
func ToNanosAtRate(units int64, rate uint32) (int64, error) {
	if rate == 0 {
		return 0, cerr.New(cerr.CodeTimestampOverflow, -1, path, "zero clock rate")
	}
	const billion = int64(1_000_000_000)
	r := int64(rate)
	q, rem := units/r, units%r
	if q > math.MaxInt64/billion || q < math.MinInt64/billion {
		return 0, cerr.New(cerr.CodeTimestampOverflow, -1, path, "timestamp overflows int64 nanoseconds")
	}
	// |rem| < rate <= 2^32, so rem*billion cannot overflow int64.
	return q*billion + rem*billion/r, nil
}
