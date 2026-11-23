// Package timeline defines the unified integer time base used by every
// representation, and the exact scale conversions between track time bases
// and the common clock. All arithmetic is integer-only: no floating point is
// ever accumulated along the timeline, so drift cannot appear.
package timeline

import (
	"errors"
	"math"
)

// CommonTimescale is the ticks-per-second of the unified clock. 90 kHz is the
// classic MPEG/RTP video clock and divides evenly by every common audio rate
// factor (8000, 16000, 24000, 32000, 44100, 48000) through integer math.
const CommonTimescale uint32 = 90000

// Clock binds a source timescale to the common clock.
type Clock struct {
	// Timescale is the native ticks-per-second of the source track.
	Timescale uint32
}

// ErrBadTimescale is returned for zero or implausible time bases.
var ErrBadTimescale = errors.New("timescale must be in [1, 100000000]")

// NewClock validates and returns a clock for the given native timescale.
func NewClock(timescale uint32) (Clock, error) {
	if timescale == 0 || timescale > 100_000_000 {
		return Clock{}, ErrBadTimescale
	}
	return Clock{Timescale: timescale}, nil
}

// ToCommon converts n ticks on this clock to common-clock ticks using exact
// rational scaling (n * common / native). No floating point is involved.
func (c Clock) ToCommon(n uint64) uint64 {
	return n * uint64(CommonTimescale) / uint64(c.Timescale)
}

// FromCommon converts common-clock ticks back to this clock's native ticks.
func (c Clock) FromCommon(common uint64) uint64 {
	return common * uint64(c.Timescale) / uint64(CommonTimescale)
}

// ToCommonRound converts to common ticks keeping the remainder so callers can
// prove the rounding never exceeds one native source tick.
func (c Clock) ToCommonRound(n uint64) (ticks, remainder uint64) {
	num := n * uint64(CommonTimescale)
	return num / uint64(c.Timescale), num % uint64(c.Timescale)
}

// DurationMs renders common-clock ticks as whole milliseconds, rounding half up.
func DurationMs(commonTicks uint64) uint64 {
	return (commonTicks*1000 + uint64(CommonTimescale)/2) / uint64(CommonTimescale)
}

// MaxCommonTicks bounds any accepted timeline (a hard guard against
// unrestricted allocation from corrupt durations): 100 years at 90 kHz.
const MaxCommonTicks uint64 = 100 * 365 * 24 * 3600 * uint64(CommonTimescale)

// GuardOverflow panics-free checked multiplication for size planning.
func GuardOverflow(a, b uint64) (uint64, bool) {
	if a != 0 && b > math.MaxUint64/a {
		return 0, false
	}
	return a * b, true
}
