// Package clock defines the shared time base for every representation.
//
// All media timestamps are normalized from a track-specific timescale to a
// single nanosecond timeline so that multi-bitrate representations can be
// aligned on one reference clock without mutating any source value.
package clock

import (
	"errors"
	"math/big"
)

// NanoPerSecond is the number of nanoseconds in one second on the shared
// timeline.
const NanoPerSecond int64 = 1_000_000_000

// ErrInvalidTimescale is returned when a timescale of zero (or negative) is
// used in a conversion.
var ErrInvalidTimescale = errors.New("clock: timescale must be greater than zero")

// Timeline converts track timescale ticks into unified nanosecond values.
type Timeline struct {
	timescale uint32
}

// NewTimeline creates a timeline bound to the given track timescale.
func NewTimeline(timescale uint32) (*Timeline, error) {
	if timescale == 0 {
		return nil, ErrInvalidTimescale
	}
	return &Timeline{timescale: timescale}, nil
}

// Timescale returns the track timescale this timeline was built from.
func (t *Timeline) Timescale() uint32 { return t.timescale }

// ToNanos converts a value expressed in track timescale ticks to nanoseconds.
// The conversion is exact rational arithmetic followed by rounding to nearest
// nanosecond (halves round away from zero), avoiding the drift that repeated
// integer truncation would introduce.
func (t *Timeline) ToNanos(ticks uint64) int64 {
	num := new(big.Int).Mul(new(big.Int).SetUint64(ticks), big.NewInt(NanoPerSecond))
	q, rem := new(big.Int).QuoRem(num, big.NewInt(int64(t.timescale)), new(big.Int))
	twiceRem := new(big.Int).Lsh(rem, 1)
	if twiceRem.Cmp(big.NewInt(int64(t.timescale))) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	return q.Int64()
}

// TickNanos returns the duration of one tick of this timeline in nanoseconds,
// rounded up. It is the smallest non-zero duration distinguishable on this
// track and is used as the alignment tolerance unit.
func (t *Timeline) TickNanos() int64 {
	v := (NanoPerSecond + int64(t.timescale) - 1) / int64(t.timescale)
	if v < 1 {
		return 1
	}
	return v
}

// WithinTolerance reports whether two unified timestamps differ by no more
// than the given tolerance in nanoseconds. The tolerance must be non-negative.
func WithinTolerance(a, b, tolerance int64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= tolerance
}
