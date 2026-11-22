// Package timeline normalizes heterogeneous track timebases into one exact
// integer-rational media clock and aligns multi-bitrate boundaries on common
// keyframes without floating point drift.
package timeline

import (
	"fmt"

	"archivist/internal/model"
)

// Clock is an exact media timebase. All conversions keep integer numerators
// and denominators so no rounding error accumulates across long assets.
type Clock struct {
	timescale uint32
}

// NewClock rejects a zero timescale up front (spec function 2).
func NewClock(timescale uint32) (Clock, error) {
	if timescale == 0 {
		return Clock{}, model.AtTrack(model.StageTimeline, model.ErrZeroTimescale, 0,
			"timescale must be non-zero")
	}
	return Clock{timescale: timescale}, nil
}

// Timescale returns the underlying ticks-per-second value.
func (c Clock) Timescale() uint32 { return c.timescale }

// ToSeconds expresses ticks as an exact rational number of seconds.
func (c Clock) ToSeconds(ticks uint64) (model.Rat, error) {
	if c.timescale == 0 {
		return model.Rat{}, model.AtTrack(model.StageTimeline, model.ErrZeroTimescale, 0, "")
	}
	return model.NewRat(int64(ticks), uint64(c.timescale))
}

// FromSeconds converts rational seconds into this clock's ticks, returning
// false when the result overflows uint64.
func (c Clock) FromSeconds(r model.Rat) (uint64, bool) {
	// ticks = r.Num/r.Den * timescale
	num, ok := mul64(uint64(abs(r.Num)), uint64(c.timescale))
	if !ok {
		return 0, false
	}
	if r.Den == 0 {
		return 0, false
	}
	return num / r.Den, true
}

// Convert maps ticks from this clock into another timescale exactly. It
// reports overflow rather than wrapping (spec function 2).
func (c Clock) Convert(ticks uint64, target Clock) (uint64, error) {
	num, ok := mul64(ticks, uint64(target.timescale))
	if !ok {
		return 0, model.AtTrack(model.StageTimeline, model.ErrTimestampOverflow, 0,
			fmt.Sprintf("convert %d ticks %d->%d", ticks, c.timescale, target.timescale))
	}
	return num / uint64(c.timescale), nil
}

// CommonTimescale returns the LCM of the supplied scales, the smallest clock
// into which every track maps with integer ticks. Overflow is rejected.
func CommonTimescale(scales []uint32) (uint64, error) {
	if len(scales) == 0 {
		return 0, model.AtTrack(model.StageTimeline, model.ErrZeroTimescale, 0, "no tracks")
	}
	lcm := uint64(1)
	for _, s := range scales {
		if s == 0 {
			return 0, model.AtTrack(model.StageTimeline, model.ErrZeroTimescale, 0,
				"zero scale in common-timescale computation")
		}
		next, ok := model.LCM(lcm, uint64(s))
		if !ok {
			return 0, model.AtTrack(model.StageTimeline, model.ErrTimestampOverflow, 0,
				fmt.Sprintf("LCM overflow at timescale %d", s))
		}
		lcm = next
		if lcm > 1<<40 {
			return 0, model.AtTrack(model.StageTimeline, model.ErrTimestampOverflow, 0,
				fmt.Sprintf("common timescale %d exceeds safe bound", lcm))
		}
	}
	return lcm, nil
}

// VerifyMonotonic enforces strictly increasing sample DTS on a track and
// returns an error carrying the track id and offending sample position.
func VerifyMonotonic(trackID uint32, timescale uint32, dts []uint64) error {
	for i := 1; i < len(dts); i++ {
		if dts[i] <= dts[i-1] {
			return model.AtSample(model.StageTimeline, model.ErrNonMonotonic,
				trackID, uint32(i), model.TimeRange{
					Start: dts[i-1], End: dts[i], Timescale: timescale,
				})
		}
	}
	return nil
}

// mul64 returns the 128-bit product's low half and an overflow flag.
func mul64(a, b uint64) (uint64, bool) {
	// Decompose to avoid importing math/bits plumbing at call sites.
	const base = 1 << 32
	aHi, aLo := a/base, a%base
	bHi, bLo := b/base, b%base
	if aHi != 0 && bHi != 0 {
		return 0, false
	}
	mid := aHi*bLo + aLo*bHi
	if mid >= base {
		return 0, false
	}
	lo := aLo * bLo
	carry := lo / base
	if mid+carry >= base {
		return 0, false
	}
	return mid*base + lo, true
}

func abs(v int64) uint64 {
	if v < 0 {
		return uint64(-v)
	}
	return uint64(v)
}
