// Package timebase provides the single rational media time base shared by
// every parsing, indexing, planning and commit stage of replaypipe.
package timebase

import (
	"fmt"
	"math/big"
	"strings"
)

const nanoPerSecond = int64(1_000_000_000)

// Base is a rational number of ticks per second (Num/Den).
// It is used for MP4 timescales, SRT/WebVTT milliseconds and RTP clock rates.
type Base struct {
	Num int64
	Den int64
}

// New validates and returns a rational tick rate.
func New(num, den int64) (Base, error) {
	if num <= 0 || den <= 0 {
		return Base{}, fmt.Errorf("invalid timebase %d/%d: numerator and denominator must be positive", num, den)
	}
	return Base{Num: num, Den: den}, nil
}

// FromTimescale returns an integer MP4/RTP clock rate as a timebase.
func FromTimescale(ts uint32) (Base, error) {
	return New(int64(ts), 1)
}

// Millis is the timebase used by SRT and WebVTT.
func Millis() Base { return Base{Num: 1000, Den: 1} }

// String renders a stable canonical form, e.g. "90000/1".
func (b Base) String() string { return fmt.Sprintf("%d/%d", b.Num, b.Den) }

// Valid reports whether the base can be used for conversions.
func (b Base) Valid() bool { return b.Num > 0 && b.Den > 0 }

// Equal compares two bases by their rational value.
func (b Base) Equal(o Base) bool { return b.Num*o.Den == o.Num*b.Den }

var maxNS = big.NewInt(int64(^uint64(0) >> 1))
var minNS = big.NewInt(-int64(^uint64(0)>>1) - 1)

func roundRat(r *big.Rat) *big.Int {
	// Round half away from zero. Covers both positive and negative ticks.
	num := new(big.Int).Abs(r.Num())
	den := new(big.Int).Set(r.Denom())
	q, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	twice := new(big.Int).Lsh(rem, 1)
	if twice.Cmp(den) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if r.Sign() < 0 {
		q.Neg(q)
	}
	return q
}

// ToNanos converts ticks to nanoseconds, rounding to the nearest nanosecond.
// The conversion is exact rational arithmetic; an out-of-range result is
// reported as TIME_OVERFLOW instead of silently wrapping.
func (b Base) ToNanos(ticks int64, track uint32, offset int64) (int64, *Error) {
	if !b.Valid() {
		return 0, Newf(CodeTimeOverflow, track, offset, "invalid timebase %s", b.String())
	}
	r := new(big.Rat).SetFrac(
		new(big.Int).Mul(big.NewInt(ticks), big.NewInt(b.Den)),
		big.NewInt(b.Num),
	)
	r.Mul(r, new(big.Rat).SetInt64(nanoPerSecond))
	n := roundRat(r)
	if n.Cmp(maxNS) > 0 || n.Cmp(minNS) < 0 {
		return 0, Newf(CodeTimeOverflow, track, offset,
			"tick %d at %s maps outside int64 nanoseconds", ticks, b.String())
	}
	return n.Int64(), nil
}

// FromNanos converts nanoseconds to ticks, rounding to the nearest tick.
func (b Base) FromNanos(ns int64, track uint32, offset int64) (int64, *Error) {
	if !b.Valid() {
		return 0, Newf(CodeTimeOverflow, track, offset, "invalid timebase %s", b.String())
	}
	r := new(big.Rat).SetFrac(
		new(big.Int).Mul(big.NewInt(ns), big.NewInt(b.Num)),
		new(big.Int).Mul(big.NewInt(b.Den), big.NewInt(nanoPerSecond)),
	)
	n := roundRat(r)
	if !n.IsInt64() {
		return 0, Newf(CodeTimeOverflow, track, offset,
			"nanosecond %d at %s maps outside int64 ticks", ns, b.String())
	}
	return n.Int64(), nil
}

// NanoDuration converts a positive tick duration to nanoseconds.
func (b Base) NanoDuration(ticks int64, track uint32, offset int64) (int64, *Error) {
	if ticks < 0 {
		return 0, Newf(CodeTimeRegression, track, offset, "negative duration %d ticks", ticks)
	}
	return b.ToNanos(ticks, track, offset)
}

// ParseRate parses clock rates such as "90000", "30/1" or "30000/1001".
func ParseRate(s string) (Base, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Base{}, fmt.Errorf("empty timebase")
	}
	var num, den int64 = 0, 1
	if i := strings.IndexByte(s, '/'); i >= 0 {
		if _, err := fmt.Sscanf(s[:i], "%d", &num); err != nil {
			return Base{}, fmt.Errorf("invalid timebase %q: %w", s, err)
		}
		if _, err := fmt.Sscanf(s[i+1:], "%d", &den); err != nil {
			return Base{}, fmt.Errorf("invalid timebase %q: %w", s, err)
		}
	} else {
		if _, err := fmt.Sscanf(s, "%d", &num); err != nil {
			return Base{}, fmt.Errorf("invalid timebase %q: %w", s, err)
		}
	}
	return New(num, den)
}

// RangeNS is a validated half-open nanosecond interval.
type RangeNS struct {
	Start int64
	End   int64
}

// Len returns the interval length in nanoseconds.
func (r RangeNS) Len() int64 { return r.End - r.Start }

// Contains reports whether ns lies in [Start, End).
func (r RangeNS) Contains(ns int64) bool { return ns >= r.Start && ns < r.End }

// Valid reports whether Start <= End and the length cannot overflow.
func (r RangeNS) Valid() bool { return r.Start <= r.End && r.End >= 0 }

// Clamp trims the range into the given media range.
func (r RangeNS) Clamp(media RangeNS) RangeNS {
	if r.Start < media.Start {
		r.Start = media.Start
	}
	if r.End > media.End {
		r.End = media.End
	}
	if r.End < r.Start {
		r.End = r.Start
	}
	return r
}

// Overlaps reports whether two half-open ranges intersect.
func (r RangeNS) Overlaps(o RangeNS) bool { return r.Start < o.End && o.Start < r.End }
