// Package timeline defines the unified media timebase (integer
// nanoseconds), timestamp conversion, wrap handling and monotonicity
// checks shared by demux, compositing and the manifest.
package timeline

import (
	"fmt"

	"mediapack/merr"
)

// NanosPerSecond is the unified media timebase: integer nanoseconds.
const NanosPerSecond int64 = 1_000_000_000

// TicksToNanos converts ticks at the given rate (ticks per second) into
// the shared nanosecond timebase, rounding down the sub-tick remainder.
// It is overflow-safe for any realistic tick value.
func TicksToNanos(ticks, ticksPerSecond int64) int64 {
	if ticksPerSecond <= 0 {
		return 0
	}
	q := ticks / ticksPerSecond
	r := ticks % ticksPerSecond
	return q*NanosPerSecond + r*NanosPerSecond/ticksPerSecond
}

// NanosToTicks converts nanoseconds back to ticks at the given rate.
func NanosToTicks(nanos, ticksPerSecond int64) int64 {
	if ticksPerSecond <= 0 {
		return 0
	}
	q := nanos / NanosPerSecond
	r := nanos % NanosPerSecond
	return q*ticksPerSecond + r*ticksPerSecond/NanosPerSecond
}

// SeqExtender extends 16-bit RTP sequence numbers across wraparound
// (RFC 3711 style), tolerating moderate reordering.
type SeqExtender struct {
	init   bool
	last   uint16
	cycles int32
}

// Extend maps a 16-bit sequence number to a monotonic extended value.
func (e *SeqExtender) Extend(seq uint16) int32 {
	if !e.init {
		e.init, e.last = true, seq
		return int32(seq)
	}
	switch d := int32(seq) - int32(e.last); {
	case d < -0x8000: // forward wrap
		e.cycles += 1 << 16
	case d > 0x8000: // backward jump (late packet from previous cycle)
		e.cycles -= 1 << 16
	}
	e.last = seq
	return e.cycles + int32(seq)
}

// TsExtender extends 32-bit RTP timestamps across wraparound.
type TsExtender struct {
	init   bool
	last   uint32
	cycles int64
}

// Extend maps a 32-bit timestamp to a monotonic extended value.
func (e *TsExtender) Extend(ts uint32) int64 {
	if !e.init {
		e.init, e.last = true, ts
		return int64(ts)
	}
	switch d := int64(ts) - int64(e.last); {
	case d < -(1 << 31):
		e.cycles += 1 << 32
	case d > (1 << 31):
		e.cycles -= 1 << 32
	}
	e.last = ts
	return e.cycles + int64(ts)
}

// FrameInterval returns the nominal nanosecond interval for a frame rate.
func FrameInterval(fps int64) int64 {
	if fps <= 0 {
		return 0
	}
	return NanosPerSecond / fps
}

// FrameTime returns the timestamp of frame i on the output grid with
// exact rational rounding, so long timelines do not accumulate drift.
func FrameTime(startNanos, fps int64, i int64) int64 {
	if fps <= 0 {
		return startNanos
	}
	return startNanos + (i*NanosPerSecond+fps/2)/fps
}

// FrameCount returns how many frames of the given rate fit the half-open
// interval [start, end).
func FrameCount(start, end, fps int64) (int, error) {
	if fps <= 0 {
		return 0, merr.Paramf("clock", "non-positive frame rate %d", fps)
	}
	if end < start {
		return 0, merr.Corruptf("clock", "", "negative timeline span [%d,%d)", start, end)
	}
	span := end - start
	n := span * fps
	count := n / NanosPerSecond
	if n%NanosPerSecond != 0 {
		count++
	}
	return int(count), nil
}

// CheckMonotonic verifies frame timestamps are strictly increasing; it is
// run across stages (demux, render, manifest) on the shared timebase.
func CheckMonotonic(stage string, times []int64) error {
	for i := 1; i < len(times); i++ {
		if times[i] <= times[i-1] {
			return merr.Corruptf(stage, "", "frame time not monotonic at index %d: %d <= %d", i, times[i], times[i-1])
		}
	}
	return nil
}

// GridError reports a frame that does not align with the output grid.
func GridError(stage string, index int, want, got int64) error {
	d := want - got
	if d < 0 {
		d = -d
	}
	if d > 1 { // allow 1ns rounding tolerance
		return fmt.Errorf("stage=%s: frame %d off grid: want %d got %d", stage, index, want, got)
	}
	return nil
}
