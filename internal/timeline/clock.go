package timeline

import (
	"fmt"
	"math"
)

// Nanosecond is the single media time base. Every subsystem converts its own
// container clock into nanoseconds and reports deviations in that unit, so
// cross-track comparisons never depend on which timescale a file used.
type Nanosecond int64

// NanosPerSecond is the media time base denominator.
const NanosPerSecond Nanosecond = 1_000_000_000

// MaxTolerance is the contract limit for cross-track alignment: 1 millisecond.
const MaxTolerance = Nanosecond(1_000_000)

// Clock converts a container timestamp in [timescale] units per second into
// the media time base. A zero timescale is rejected rather than treated as 1,
// because Matroska/MP4 files with a zero timescale cannot be aligned at all.
type Clock struct {
	timescale uint64
}

// NewClock builds a clock. Timescale is the number of time units per second.
func NewClock(timescale uint64) (Clock, error) {
	if timescale == 0 {
		return Clock{}, New(StageValidate, CatIllegalLength, 0, -1,
			"timescale must be positive")
	}
	if timescale > math.MaxUint32 {
		return Clock{}, New(StageValidate, CatLimitExceeded, 0, -1,
			"timescale %d exceeds 32-bit container field", timescale)
	}
	return Clock{timescale: timescale}, nil
}

// MustClock is for constants and tests that already proved the timescale.
func MustClock(timescale uint64) Clock {
	c, err := NewClock(timescale)
	if err != nil {
		panic(err)
	}
	return c
}

// Timescale reports the raw container units-per-second value.
func (c Clock) Timescale() uint64 { return c.timescale }

// ToNanos converts a container timestamp. The conversion works in 128-bit
// intermediate precision so a 64-bit timestamp with a large timescale cannot
// overflow before the division by one second.
func (c Clock) ToNanos(ts uint64) Nanosecond {
	if c.timescale == 0 {
		return 0
	}
	// ts * 1e9 may overflow uint64 for extreme inputs; split it.
	seconds := ts / c.timescale
	rem := ts % c.timescale
	ns := seconds*1_000_000_000 + (rem*1_000_000_000)/c.timescale
	if ns > math.MaxInt64 {
		return Nanosecond(math.MaxInt64)
	}
	return Nanosecond(ns)
}

// FromNanos converts a media-base instant back to this clock's units, which is
// needed when writing normalized output.
func (c Clock) FromNanos(ns Nanosecond) uint64 {
	if c.timescale == 0 || ns <= 0 {
		return 0
	}
	seconds := uint64(ns) / 1_000_000_000
	rem := uint64(ns) % 1_000_000_000
	return seconds*c.timescale + (rem*c.timescale)/1_000_000_000
}

// DeltaNS returns the absolute difference between two media-base instants.
func DeltaNS(a, b Nanosecond) Nanosecond {
	d := a - b
	if d < 0 {
		return -d
	}
	return d
}

// WithinTolerance reports whether two instants differ by at most 1 ms.
func WithinTolerance(a, b Nanosecond) bool {
	return DeltaNS(a, b) <= MaxTolerance
}

// FormatNS renders a media-base instant as seconds with nine decimals. The
// fixed width keeps CLI output and golden hashes stable.
func FormatNS(ns Nanosecond) string {
	sign := ""
	v := int64(ns)
	if v < 0 {
		sign = "-"
		v = -v
	}
	return fmt.Sprintf("%s%d.%09d", sign, v/1_000_000_000, v%1_000_000_000)
}

// Range is an inclusive time span on the media base.
type Range struct {
	Start Nanosecond
	End   Nanosecond
}

// Duration returns the span length, clamping reversed ranges to zero.
func (r Range) Duration() Nanosecond {
	if r.End <= r.Start {
		return 0
	}
	return r.End - r.Start
}

// Valid reports whether the range is non-negative and forward-ordered.
func (r Range) Valid() bool { return r.Start >= 0 && r.End >= r.Start }
