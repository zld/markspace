// Package media defines the kernel-wide media clock, rational time scales and
// the unified error model shared by every processing stage.
package media

import (
	"errors"
	"math"
)

// NanosecondsPerSecond is the resolution of the kernel's unified media
// timeline. All track time bases are converted into this scale before events
// cross a subsystem boundary.
const NanosecondsPerSecond int64 = 1_000_000_000

// Kernel limits. Declared in one place so adapters and the pipeline reject
// oversized input identically.
const (
	MaxInputDuration = 24 * 60 * 60 * NanosecondsPerSecond // 24 hours
	MaxTracks        = 16
	MaxReorderWindow = 512 // packets
	MaxParseBytes    = 256 * 1024 * 1024
)

// Rational expresses a media time scale as ticks per second, exactly as
// carried by an MP4 mdhd box or an RTP clock-rate declaration.
type Rational struct {
	Num int64 // ticks per ...
	Den int64 // ... Den seconds; Den is normally 1
}

// TimeScale returns the common "ticks per second" rational (Den == 1).
func TimeScale(ticksPerSecond uint32) Rational {
	return Rational{Num: int64(ticksPerSecond), Den: 1}
}

// Valid reports whether the rational is usable for time conversion.
func (r Rational) Valid() bool {
	return r.Num > 0 && r.Den > 0
}

func mulOverflows(a, b int64) bool {
	if a == 0 || b == 0 {
		return false
	}
	if a == math.MinInt64 || b == math.MinInt64 {
		return true
	}
	aa, bb := a, b
	if aa < 0 {
		aa = -aa
	}
	if bb < 0 {
		bb = -bb
	}
	return aa > math.MaxInt64/bb
}

// Convert maps value measured in `from` onto the `to` scale, rounding to the
// nearest tick (half away from zero). It returns an error on overflow so that
// oversized container declarations are rejected instead of wrapping.
func Convert(value int64, from, to Rational) (int64, error) {
	if !from.Valid() || !to.Valid() {
		return 0, errors.New("media: invalid rational time scale")
	}
	if mulOverflows(to.Num, from.Den) || mulOverflows(from.Num, to.Den) {
		return 0, errors.New("media: time conversion overflows int64")
	}
	scaleNum, scaleDen := to.Num*from.Den, from.Num*to.Den
	if mulOverflows(value, scaleNum) || scaleDen == 0 {
		return 0, errors.New("media: time conversion overflows int64")
	}
	num := value * scaleNum
	den := scaleDen
	if den < 0 {
		num, den = -num, -den
	}
	q := num / den
	rem := num % den
	if rem < 0 {
		if -rem*2 >= den {
			q--
		}
	} else if rem*2 >= den {
		q++
	}
	return q, nil
}

// ToNanos converts ticks in scale r onto the unified nanosecond timeline.
func ToNanos(ticks int64, r Rational) (int64, error) {
	return Convert(ticks, r, Rational{Num: NanosecondsPerSecond, Den: 1})
}

// FromNanos converts unified-timeline nanoseconds into ticks of scale r.
func FromNanos(nanos int64, r Rational) (int64, error) {
	return Convert(nanos, Rational{Num: NanosecondsPerSecond, Den: 1}, r)
}

// Interval is a half-open [Start, End) span expressed on the unified timeline.
type Interval struct {
	Start int64 // inclusive, nanoseconds since the timeline origin
	End   int64 // exclusive, nanoseconds
}

// Duration returns the span length; a negative or zero-length interval is
// considered invalid by the pipeline invariants.
func (i Interval) Duration() int64 { return i.End - i.Start }

// Valid reports a well-formed non-empty interval.
func (i Interval) Valid() bool { return i.Start >= 0 && i.End > i.Start }

// Contains reports whether t falls inside the half-open interval.
func (i Interval) Contains(t int64) bool { return t >= i.Start && t < i.End }

// Overlaps reports whether two intervals share any instant.
func (i Interval) Overlaps(o Interval) bool {
	return i.Start < o.End && o.Start < i.End
}

// Merge returns the smallest interval covering both inputs.
func (i Interval) Merge(o Interval) Interval {
	m := i
	if o.Start < m.Start {
		m.Start = o.Start
	}
	if o.End > m.End {
		m.End = o.End
	}
	return m
}

// AssertMonotonic verifies that `next` does not move backwards relative to
// `committed`, returning the signed advance (always >= 0 on success).
func AssertMonotonic(committed, next int64) (advance int64, ok bool) {
	if next < committed {
		return 0, false
	}
	return next - committed, true
}
