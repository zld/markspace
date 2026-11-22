// Package timeline defines the unified time base shared by every
// subsystem (bitstream scan, index, segment planning, alignment,
// manifest and report). All timestamps and durations are expressed
// in Ticks on a 90 kHz clock so that H.264, H.265 and AV1
// representations can be aligned on one grid.
package timeline

import (
	"errors"
	"fmt"
	"math"
)

// Timescale is the unified clock rate: 1 tick = 1/90000 second.
const Timescale = 90000

// Ticks is a timestamp or duration on the unified time base.
type Ticks int64

// ErrOverflow is returned when a timestamp conversion would overflow int64.
var ErrOverflow = errors.New("timeline: timestamp conversion overflow")

// MsToTicks converts milliseconds to ticks with overflow protection.
func MsToTicks(ms int64) (Ticks, error) {
	if ms > math.MaxInt64/Timescale || ms < math.MinInt64/Timescale {
		return 0, ErrOverflow
	}
	return Ticks(ms * Timescale), nil
}

// MustMs is MsToTicks for compile-time-safe small constants; it panics
// on overflow which is unreachable for millisecond-scale inputs.
func MustMs(ms int64) Ticks {
	t, err := MsToTicks(ms)
	if err != nil {
		panic(err)
	}
	return t
}

// Millis converts ticks to whole milliseconds, truncated toward zero.
func (t Ticks) Millis() int64 { return int64(t) / Timescale }

// MillisFloat converts ticks to fractional milliseconds.
func (t Ticks) MillisFloat() float64 { return float64(t) * 1000 / Timescale }

// FrameDuration returns the duration of one frame in ticks for a
// rational frame rate fpsNum/fpsDen, with overflow protection.
func FrameDuration(fpsNum, fpsDen int64) (Ticks, error) {
	if fpsNum <= 0 || fpsDen <= 0 {
		return 0, fmt.Errorf("timeline: invalid frame rate %d/%d", fpsNum, fpsDen)
	}
	if fpsDen > math.MaxInt64/Timescale {
		return 0, ErrOverflow
	}
	return Ticks((int64(Timescale) * fpsDen) / fpsNum), nil
}

// AddSafe adds two tick values with overflow protection.
func AddSafe(a, b Ticks) (Ticks, error) {
	s := int64(a) + int64(b)
	if (b > 0 && s < int64(a)) || (b < 0 && s > int64(a)) {
		return 0, ErrOverflow
	}
	return Ticks(s), nil
}

// AbsDiff returns |a-b|, saturating at MaxInt64 on overflow.
func AbsDiff(a, b Ticks) Ticks {
	if a >= b {
		return a - b
	}
	d := int64(b) - int64(a) // may overflow when signs differ
	if d < 0 {
		return Ticks(math.MaxInt64)
	}
	return Ticks(d)
}
