// Package clock provides the shared rational timebase used by every
// subsystem, plus RTP sequence/timestamp unwrapping and drift estimation.
// All cross-track math is done in the common nanosecond base.
package clock

import (
	"errors"
	"math"
	"math/bits"
)

// NanosPerSecond is the shared common timebase: 1 tick = 1 nanosecond.
const NanosPerSecond = int64(1_000_000_000)

// ErrOverflow reports an out-of-range rational time conversion.
var ErrOverflow = errors.New("clock: time conversion overflow")

// Timebase is a rational number of seconds per tick: seconds = ticks*Num/Den.
type Timebase struct {
	Num int64
	Den int64
}

// Nano returns the shared nanosecond timebase.
func Nano() Timebase { return Timebase{Num: 1, Den: NanosPerSecond} }

// Valid reports whether the timebase is usable.
func (tb Timebase) Valid() bool { return tb.Num > 0 && tb.Den > 0 }

func absU64(v int64) (uint64, bool) {
	if v < 0 {
		return uint64(-(v + 1)) + 1, true
	}
	return uint64(v), false
}

// mulDiv computes v*m/d with a 128-bit intermediate, truncating toward zero.
func mulDiv(v, m, d int64) (int64, error) {
	uv, neg := absU64(v)
	hi, lo := bits.Mul64(uv, uint64(m))
	ud := uint64(d)
	if hi >= ud {
		return 0, ErrOverflow
	}
	q, _ := bits.Div64(hi, lo, ud)
	if q > math.MaxInt64 {
		return 0, ErrOverflow
	}
	n := int64(q)
	if neg {
		n = -n
	}
	return n, nil
}

// ToNanos converts v ticks of tb into nanoseconds, checking overflow.
func (tb Timebase) ToNanos(v int64) (int64, error) {
	if !tb.Valid() {
		return 0, errors.New("clock: invalid timebase")
	}
	prod, err := mulDiv(v, tb.Num, 1) // v*Num, overflow-checked
	if err != nil {
		return 0, err
	}
	q := prod / tb.Den
	r := prod % tb.Den
	if q != 0 && (q > math.MaxInt64/NanosPerSecond || q < math.MinInt64/NanosPerSecond) {
		return 0, ErrOverflow
	}
	rem, err := mulDiv(r, NanosPerSecond, tb.Den)
	if err != nil {
		return 0, err
	}
	return q*NanosPerSecond + rem, nil
}

// FromNanos converts nanoseconds into ticks of tb (truncating).
func (tb Timebase) FromNanos(ns int64) (int64, error) {
	if !tb.Valid() {
		return 0, errors.New("clock: invalid timebase")
	}
	t, err := mulDiv(ns, tb.Den, tb.Num) // ticks * NanosPerSecond
	if err != nil {
		return 0, err
	}
	return t / NanosPerSecond, nil
}

// To converts v ticks of tb into dst ticks via the nanosecond common base.
func (tb Timebase) To(v int64, dst Timebase) (int64, error) {
	ns, err := tb.ToNanos(v)
	if err != nil {
		return 0, err
	}
	return dst.FromNanos(ns)
}
