// Package clock provides the single nanosecond time base shared by every
// subsystem in assetcheck.
//
// Media containers and RTP streams express time in different units. Every
// conversion in this project goes through this package so that samples from
// MP4 tracks (any timescale), RTP clock rates and subtitle timings can be
// compared without accumulating a second, ad-hoc rounding convention.
package clock

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
)

// Nanos is a time position or duration on the shared nanosecond base.
// The zero value is a valid instant; Invalid marks "unknown / not
// applicable" and is used by fault records when a failure cannot be tied
// to a media time.
type Nanos int64

const (
	// Invalid marks an unknown time position.
	Invalid Nanos = -1
	// Second is the number of nanoseconds in one second.
	Second Nanos = 1_000_000_000
)

// ErrZeroTimescale is returned when a conversion would divide by zero.
var ErrZeroTimescale = errors.New("clock: timescale must be greater than zero")

// ErrTimeOverflow is returned when a converted value does not fit in int64
// nanoseconds.
var ErrTimeOverflow = errors.New("clock: time overflows nanosecond range")

// FromUnits converts a count of units expressed at timescale ticks per second
// into nanoseconds. The result is rounded half away from zero using exact
// 128-bit intermediate arithmetic, so conversions are deterministic and do not
// lose precision for large unit counts.
func FromUnits(units uint64, timescale uint32) (Nanos, error) {
	if timescale == 0 {
		return 0, ErrZeroTimescale
	}
	value, err := mulDiv(units, uint64(Second), uint64(timescale))
	if err != nil {
		return 0, err
	}
	if value > math.MaxInt64 {
		return 0, ErrTimeOverflow
	}
	return Nanos(value), nil
}

// FromSamples converts an audio sample count at sampleRate samples per second
// into nanoseconds.
func FromSamples(samples uint64, sampleRate uint32) (Nanos, error) {
	return FromUnits(samples, sampleRate)
}

// Scale converts a duration from one timescale to another, returning the
// duration rounded to the nearest tick.
func Scale(units uint64, from, to uint32) (uint64, error) {
	if from == 0 || to == 0 {
		return 0, ErrZeroTimescale
	}
	return mulDiv(units, uint64(to), uint64(from))
}

// mulDiv computes roundHalfUp(a*b/d) using 128-bit intermediates. A result
// that does not fit in 64 bits is reported as overflow rather than wrapping.
func mulDiv(a, b, d uint64) (uint64, error) {
	if d == 0 {
		return 0, ErrZeroTimescale
	}
	hi, lo := bits.Mul64(a, b)
	if hi >= d {
		return 0, ErrTimeOverflow
	}
	q, r := bits.Div64(hi, lo, d)
	// Round half away from zero (all inputs are unsigned).
	if r >= d-r {
		if q == math.MaxUint64 {
			return 0, ErrTimeOverflow
		}
		q++
	}
	return q, nil
}

// Timebase maps ticks in a single media timescale onto the nanosecond base.
// Origin is added after conversion; it allows a track that starts at a
// non-zero baseMediaDecodeTime without a second rounding step at the call
// site.
type Timebase struct {
	Timescale uint32
	Origin    Nanos
}

// NewTimebase validates the timescale and returns a timebase rooted at zero.
func NewTimebase(timescale uint32) (Timebase, error) {
	if timescale == 0 {
		return Timebase{}, ErrZeroTimescale
	}
	return Timebase{Timescale: timescale}, nil
}

// At converts a media time in ticks to the shared base.
func (tb Timebase) At(units uint64) (Nanos, error) {
	d, err := FromUnits(units, tb.Timescale)
	if err != nil {
		return 0, err
	}
	return tb.Origin + d, nil
}

// Duration converts a tick duration to the shared base.
func (tb Timebase) Duration(units uint64) (Nanos, error) {
	return FromUnits(units, tb.Timescale)
}

// Add returns t+d, or Invalid if either side is Invalid.
func Add(t, d Nanos) Nanos {
	if t == Invalid || d == Invalid {
		return Invalid
	}
	return t + d
}

// Sub returns a-b, or Invalid if either side is Invalid.
func Sub(a, b Nanos) Nanos {
	if a == Invalid || b == Invalid {
		return Invalid
	}
	return a - b
}

// String renders a nanosecond position in a stable, human-readable form.
func (n Nanos) String() string {
	if n == Invalid {
		return "unknown"
	}
	return fmt.Sprintf("%dns", int64(n))
}
