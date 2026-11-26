// Package media implements the ingestion side of the media processing
// kernel: exact rational time conversion, structured errors and parsers for
// MP4 containers, RTP captures and edit-decision lists.
//
// All time values are represented as integer nanoseconds or as exact
// rationals. Floating point arithmetic is never used to accumulate time, so
// repeated conversions do not drift.
package media

import (
	"errors"
	"math/bits"
)

// NanoPerSecond is the number of nanoseconds in one second.
const NanoPerSecond int64 = 1_000_000_000

// Rational is an exact signed rational Num/Den with Den always positive.
type Rational struct {
	Num int64
	Den int64
}

// NewRational validates and returns a rational number. A zero or negative
// denominator is rejected because media time bases must be positive.
func NewRational(num, den int64) (Rational, error) {
	if den <= 0 {
		return Rational{}, errors.New("rational denominator must be positive")
	}
	return Rational{Num: num, Den: den}, nil
}

// IsZero reports whether the rational equals zero.
func (r Rational) IsZero() bool { return r.Num == 0 }

// Cmp returns -1, 0 or 1 comparing r and o exactly, without reducing.
func (r Rational) Cmp(o Rational) int {
	// r.Num/r.Den vs o.Num/o.Den; both denominators are positive.
	lh, ll := bits.Mul64(uint64(r.Num), uint64(o.Den))
	rh, rl := bits.Mul64(uint64(o.Num), uint64(r.Den))
	if lh != rh {
		if lh < rh {
			return -1
		}
		return 1
	}
	if ll < rl {
		return -1
	}
	if ll > rl {
		return 1
	}
	return 0
}

// mulDiv computes a*b/c exactly using 128-bit intermediates and reports an
// overflow when the result does not fit int64. All inputs must be positive.
func mulDiv(a, b, c int64) (int64, error) {
	if c <= 0 {
		return 0, errors.New("mulDiv: divisor must be positive")
	}
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	q, _ := bits.Div64(hi, lo, uint64(c))
	if hi != 0 && q > uint64(^uint64(0)>>1) {
		return 0, errors.New("mulDiv: result overflows int64")
	}
	return int64(q), nil
}

// TimeBase maps a media clock (MP4 timescale or RTP clock) to nanoseconds.
// The clock rate is stored as an exact rational ticks/second.
type TimeBase struct {
	ticksPerSecond Rational
}

// NewTimeBase builds a time base for an integer ticks-per-second clock such
// as an MP4 timescale. A zero clock is rejected.
func NewTimeBase(ticksPerSecond int64) (TimeBase, error) {
	if ticksPerSecond <= 0 {
		return TimeBase{}, errors.New("time base ticks-per-second must be positive")
	}
	return TimeBase{ticksPerSecond: Rational{Num: ticksPerSecond, Den: 1}}, nil
}

// NewTimeBaseRational builds a time base from a fractional ticks/second rate.
func NewTimeBaseRational(rate Rational) (TimeBase, error) {
	if rate.Num <= 0 || rate.Den <= 0 {
		return TimeBase{}, errors.New("rational time base must be positive")
	}
	return TimeBase{ticksPerSecond: rate}, nil
}

// TicksToNs converts a clock timestamp to integer nanoseconds, truncating
// any sub-nanosecond remainder. With rate r = Num/Den ticks per second,
// one tick spans Den/Num seconds, hence ns = ticks*Den*1e9/Num.
func (tb TimeBase) TicksToNs(ticks int64) (int64, error) {
	if ticks < 0 {
		return 0, errors.New("negative clock ticks are not representable")
	}
	step1, err := mulDiv(ticks, tb.ticksPerSecond.Den*NanoPerSecond, tb.ticksPerSecond.Num)
	if err != nil {
		return 0, err
	}
	return step1, nil
}

// DeltaNs converts a duration expressed in clock ticks to nanoseconds.
func (tb TimeBase) DeltaNs(ticks int64) (int64, error) {
	return tb.TicksToNs(ticks)
}

// NsToTicks converts nanoseconds back to clock ticks, truncating.
func (tb TimeBase) NsToTicks(ns int64) (int64, error) {
	if ns < 0 {
		return 0, errors.New("negative nanosecond duration is not representable")
	}
	return mulDiv(ns, tb.ticksPerSecond.Num, tb.ticksPerSecond.Den*NanoPerSecond)
}

// WrapDelta32 returns the forward distance from prev to cur on a 32-bit
// modulo-2^32 clock, always in [0, 2^32). It handles RTP timestamp wrap
// without any assumption about absolute ordering.
func WrapDelta32(prev, cur uint32) uint32 { return cur - prev }

// Interval is a half-open time range [StartNs, EndNs) in nanoseconds.
type Interval struct {
	StartNs int64
	EndNs   int64
}

// Validate rejects empty or inverted intervals.
func (iv Interval) Validate() error {
	if iv.StartNs < 0 || iv.EndNs < 0 {
		return errors.New("interval bounds must be non-negative")
	}
	if iv.EndNs <= iv.StartNs {
		return errors.New("interval must be half-open with EndNs > StartNs")
	}
	return nil
}

// Duration returns the interval length in nanoseconds.
func (iv Interval) Duration() int64 { return iv.EndNs - iv.StartNs }

// Contains reports whether ts belongs to the half-open interval.
func (iv Interval) Contains(ts int64) bool {
	return ts >= iv.StartNs && ts < iv.EndNs
}

// Overlaps reports whether two half-open intervals share any instant.
func (iv Interval) Overlaps(o Interval) bool {
	return iv.StartNs < o.EndNs && o.StartNs < iv.EndNs
}

// SumDurations adds nanosecond durations with explicit overflow detection.
func SumDurations(parts ...int64) (int64, error) {
	var total int64
	for _, p := range parts {
		if p < 0 {
			return 0, errors.New("duration parts must be non-negative")
		}
		if p > (1<<63-1)-total {
			return 0, errors.New("duration sum overflows int64")
		}
		total += p
	}
	return total, nil
}
