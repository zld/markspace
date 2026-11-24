// Package timeline defines the single integer time base used by every
// subsystem in medialab. Container timestamps (MXF edit rate, MP4
// timescale, RTP 90kHz clock) are all converted into master ticks so the
// rest of the pipeline never mixes units.
package timeline

import "fmt"

// MasterHz is the common clock frequency in hertz. 48000 is divisible by
// the common video rates (24/25/30/60fps -> integer durations) and by the
// common RTP audio clocks, so conversions stay exact for the samples we
// generate.
const MasterHz int64 = 48000

// Clock maps a source timescale (ticks per second) onto the master clock.
type Clock struct {
	// Hz is the source clock frequency, e.g. 90000 for an RTP video
	// clock, 30000 for an MP4 video track timescale.
	Hz int64
}

// ToTicks converts value source ticks into master ticks using exact
// rational arithmetic: value * MasterHz / Hz.
func (c Clock) ToTicks(value int64) int64 {
	if c.Hz <= 0 {
		return 0
	}
	return value * MasterHz / c.Hz
}

// ToTicksRational converts a value expressed as num/den per second (the
// MXF edit-rate case, e.g. 30000/1001) into master ticks.
func ToTicksRational(value, num, den int64) int64 {
	if num <= 0 || den <= 0 {
		return 0
	}
	return value * MasterHz * den / num
}

// Millis converts master ticks into milliseconds, rounded to the nearest
// millisecond.
func Millis(ticks int64) int64 {
	if ticks == 0 {
		return 0
	}
	neg := ticks < 0
	if neg {
		ticks = -ticks
	}
	ms := (ticks*1000 + MasterHz/2) / MasterHz
	if neg {
		return -ms
	}
	return ms
}

// Micros converts master ticks into microseconds (exact for MasterHz).
func Micros(ticks int64) int64 {
	return ticks * 1_000_000 / MasterHz
}

// Window is a half-open time range [Start, End) on the master clock.
type Window struct {
	Start int64
	End   int64
}

// Duration returns the window length in master ticks.
func (w Window) Duration() int64 {
	if w.End < w.Start {
		return 0
	}
	return w.End - w.Start
}

// Contains reports whether t falls inside the half-open window.
func (w Window) Contains(t int64) bool {
	return t >= w.Start && t < w.End
}

// Overlaps reports whether two windows share any master tick.
func (w Window) Overlaps(o Window) bool {
	return w.Start < o.End && o.Start < w.End
}

// Merge returns the smallest window covering both receivers. Adjacent or
// overlapping windows collapse into one range.
func (w Window) Merge(o Window) Window {
	if o.Start < w.Start {
		w.Start = o.Start
	}
	if o.End > w.End {
		w.End = o.End
	}
	return w
}

// String renders the window in milliseconds for terminal output.
func (w Window) String() string {
	return fmt.Sprintf("[%dms,%dms)", Millis(w.Start), Millis(w.End))
}

// Advance32 performs modular subtraction on a 32-bit counter (RTP
// timestamp) and returns the signed forward distance from prev to next,
// correctly handling one wrap. A backwards jump larger than half the space
// is treated as forward wrap-around rather than clock reversal.
func Advance32(prev, next uint32) int64 {
	return int64(int32(next - prev))
}

// Advance16 is the 16-bit counterpart for RTP sequence numbers.
func Advance16(prev, next uint16) int64 {
	return int64(int16(next - prev))
}

// Monotonic32 maps a 32-bit wrapping counter onto a monotonically
// increasing int64 axis. The returned value never decreases for packets
// observed in arrival order whose true forward distance is at most 2^31.
func Monotonic32(prevAxis int64, prevVal, nextVal uint32) int64 {
	return prevAxis + Advance32(prevVal, nextVal)
}
