// Package clock provides the unified media time base. All track timestamps
// are converted through exact rational arithmetic so that conversions are
// reproducible across runs and platforms. Negative starts, wraparound and
// non-monotonic timestamps are recorded as faults, never silently rewritten.
package clock

import (
	"math/big"
	"math/bits"

	"mediacheck/internal/fault"
)

// NanosPerSecond is the number of nanoseconds in one second.
const NanosPerSecond = int64(1_000_000_000)

// Timescale is a rational media time base: one tick is 1/Timescale seconds.
type Timescale uint32

// Rational is an exact rational number Num/Den (Den > 0).
type Rational struct {
	Num int64
	Den int64
}

// NewRational builds a normalized rational (reduced, positive denominator).
func NewRational(num, den int64) Rational {
	if den < 0 {
		num, den = -num, -den
	}
	g := gcd(abs64(num), abs64(den))
	if g > 1 {
		gi := int64(g)
		num, den = num/gi, den/gi
	}
	return Rational{Num: num, Den: den}
}

// MulDiv computes a*b/c exactly, rounding toward zero. It uses 128-bit
// arithmetic and falls back to big integers on overflow, so results are
// deterministic for any input.
func MulDiv(a, b, c int64) int64 {
	if c == 0 {
		panic("clock: division by zero")
	}
	neg := (a < 0) != (b < 0) != (c < 0)
	ua, ub, uc := abs64(a), abs64(b), abs64(c)
	hi, lo := bits.Mul64(ua, ub)
	var q uint64
	if hi >= uc {
		// 128/64 division would overflow; use exact big-int fallback.
		bi := new(big.Int).Mul(new(big.Int).SetUint64(ua), new(big.Int).SetUint64(ub))
		bi.Quo(bi, new(big.Int).SetUint64(uc))
		if bi.BitLen() > 63 {
			if neg {
				return -1 << 63
			}
			return 1<<63 - 1
		}
		q = bi.Uint64()
	} else {
		q, _ = bits.Div64(hi, lo, uc)
	}
	if neg {
		return -int64(q)
	}
	return int64(q)
}

// ToNanos converts ticks in this timescale to nanoseconds exactly.
func (ts Timescale) ToNanos(ticks int64) int64 {
	return MulDiv(ticks, NanosPerSecond, int64(ts))
}

// FromNanos converts nanoseconds to ticks in this timescale exactly.
func (ts Timescale) FromNanos(nanos int64) int64 {
	return MulDiv(nanos, int64(ts), NanosPerSecond)
}

// Convert converts ticks from this timescale to another exactly.
func (ts Timescale) Convert(ticks int64, dst Timescale) int64 {
	return MulDiv(ticks, int64(dst), int64(ts))
}

// TickNanos returns the exact duration of one tick as a rational number
// of nanoseconds.
func (ts Timescale) TickNanos() Rational {
	return NewRational(NanosPerSecond, int64(ts))
}

// Tracker validates a sequence of timestamps (in ticks) for one track.
// It records NEGATIVE_START and NON_MONOTONIC_TS faults but never alters
// the observed values.
type Tracker struct {
	Track   string
	faults  *fault.List
	last    int64
	started bool
}

func NewTracker(track string, fl *fault.List) *Tracker {
	return &Tracker{Track: track, faults: fl}
}

// Check validates the next timestamp observed at byte offset (or -1).
func (t *Tracker) Check(ts int64, offset int64) {
	if !t.started {
		t.started = true
		if ts < 0 {
			t.faults.Add(fault.ErrNegativeStart, offset,
				"track %s starts at negative timestamp %d", t.Track, ts)
		}
	} else if ts < t.last {
		t.faults.Add(fault.ErrNonMonotonic, offset,
			"track %s: timestamp %d goes backwards (previous %d)", t.Track, ts, t.last)
	}
	t.last = ts
}

// WrapTracker validates a sequence of 32-bit timestamps (e.g. RTP) and
// records TS_WRAPAROUND when the counter wraps or jumps backwards by more
// than half the range, which cannot be a legitimate reorder.
type WrapTracker struct {
	faults  *fault.List
	last    uint32
	started bool
}

func NewWrapTracker(fl *fault.List) *WrapTracker {
	return &WrapTracker{faults: fl}
}

// Check validates the next 32-bit timestamp observed at byte offset (or -1).
func (w *WrapTracker) Check(ts uint32, offset int64) {
	if w.started {
		diff := int32(ts - w.last)
		if diff < -(1<<30) {
			w.faults.Add(fault.ErrWraparound, offset,
				"32-bit timestamp jumped backwards: 0x%08x -> 0x%08x", w.last, ts)
		}
	}
	w.last = ts
	w.started = true
}

func abs64(v int64) uint64 {
	if v < 0 {
		return uint64(-(v + 1)) + 1
	}
	return uint64(v)
}

func gcd(a, b uint64) uint64 {
	if a == 0 {
		return b
	}
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
