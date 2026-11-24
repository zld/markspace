// Package clock provides the single nanosecond media timebase used by every
// archive subsystem, plus the shared, classifiable error model.
package clock

import (
	"errors"
	"fmt"
	"math"
)

// NanosPerSecond is the common denominator for all timeline math.
const NanosPerSecond int64 = 1_000_000_000

// Mod32 is the RTP timestamp modulus used when expanding 32 bit stamps.
const Mod32 uint64 = 1 << 32

// Timebase converts between integer container units and nanoseconds.
type Timebase struct {
	Timescale uint32
}

// NewTimebase validates a container timescale.
func NewTimebase(timescale uint32) (Timebase, error) {
	if timescale == 0 {
		return Timebase{}, New(KindInvalidMetadata, "timescale must be non-zero")
	}
	return Timebase{Timescale: timescale}, nil
}

// ToNanos converts media units to nanoseconds without overflowing. The
// conversion truncates sub-nanosecond remainders toward zero.
func (tb Timebase) ToNanos(units int64) (int64, error) {
	if tb.Timescale == 0 {
		return 0, New(KindInvalidMetadata, "timescale must be non-zero")
	}
	t := int64(tb.Timescale)
	q, r := units/t, units%t
	if q > math.MaxInt64/NanosPerSecond || q < math.MinInt64/NanosPerSecond {
		return 0, New(KindOverflow, fmt.Sprintf("units %d at timescale %d overflow nanoseconds", units, tb.Timescale))
	}
	// |r| < 2^32 so r*1e9 always fits in int64.
	return q*NanosPerSecond + r*NanosPerSecond/t, nil
}

// FromNanos converts nanoseconds back to media units, truncating.
func (tb Timebase) FromNanos(ns int64) (int64, error) {
	if tb.Timescale == 0 {
		return 0, New(KindInvalidMetadata, "timescale must be non-zero")
	}
	t := int64(tb.Timescale)
	q, r := ns/NanosPerSecond, ns%NanosPerSecond
	if q > math.MaxInt64/t || q < math.MinInt64/t {
		return 0, New(KindOverflow, fmt.Sprintf("nanoseconds %d overflow timescale %d", ns, tb.Timescale))
	}
	// r is in (-1e9, 1e9), so r*t can overflow int64 only for very large
	// timescales; widen the remainder term before comparing.
	frac := (r / t) * t
	if frac > 0 && r*t > math.MaxInt64 || frac < 0 && r*t < math.MinInt64 {
		return 0, New(KindOverflow, fmt.Sprintf("remainder of %d ns overflows timescale %d", ns, tb.Timescale))
	}
	return q*t + frac, nil
}

// Unwrap expands a modular counter value to the representation nearest to
// reference. It is used for RTP 32 bit timestamps that wrap during a session.
func Unwrap(value uint32, reference int64, modulus uint64) (int64, error) {
	if modulus == 0 {
		return 0, New(KindInvalidMetadata, "timestamp modulus must be non-zero")
	}
	m := int64(modulus)
	v := int64(value)
	delta := reference - v
	// Round division to the nearest multiple of the modulus. An exact half
	// modulus is inherently ambiguous; it resolves toward the epoch (k=0) so
	// repeated runs agree, rather than toward the reference.
	k := delta / m
	rem := delta % m
	if rem > m/2 {
		k++
	} else if rem <= -m/2 {
		k--
	}
	if k > 0 && v > math.MaxInt64-k*m || k < 0 && v < math.MinInt64-k*m {
		return 0, New(KindOverflow, fmt.Sprintf("expanded timestamp %d overflows", v))
	}
	return v + k*m, nil
}

// UnwrapSeries expands an ordered series of modular timestamps. The first
// value establishes the reference; every later value is unwrapped against the
// previously expanded value so wraparound is tracked once.
func UnwrapSeries(values []uint32, modulus uint64) ([]int64, error) {
	out := make([]int64, len(values))
	var ref int64
	for i, v := range values {
		var err error
		if i == 0 {
			out[i] = int64(v)
			ref = out[i]
			continue
		}
		out[i], err = Unwrap(v, ref, modulus)
		if err != nil {
			return nil, err
		}
		ref = out[i]
	}
	return out, nil
}

// Interval validates a half-open media interval.
func Interval(start, end int64) (int64, error) {
	if end < start {
		return 0, New(KindNonMonotonic, fmt.Sprintf("interval end %d precedes start %d", end, start))
	}
	return end - start, nil
}

// Duration returns the non-negative duration of an interval.
func Duration(start, end int64) (int64, error) {
	d := end - start
	if d < 0 {
		return 0, New(KindNegativeDuration, fmt.Sprintf("negative duration %d for interval [%d,%d)", d, start, end))
	}
	return d, nil
}

// IsZero reports whether a timebase is unset.
func (tb Timebase) IsZero() bool { return tb.Timescale == 0 }

// String formats the timebase as a rational rate.
func (tb Timebase) String() string { return fmt.Sprintf("1/%d", tb.Timescale) }

// EnsureErrorKind is a small helper used by callers that need to preserve a
// typed error while attaching location context.
func EnsureErrorKind(err error, kind Kind) Kind {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return kind
}
