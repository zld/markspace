// Package timebase defines the single nanosecond time scale shared by the
// subtitle, timecode, stream and manifest subsystems.
package timebase

import (
	"fmt"
	"math/bits"
)

// Nanos is a duration or timestamp expressed in nanoseconds. It is the only
// time unit that crosses subsystem boundaries.
type Nanos int64

// MaxNanos derivable from one 24-hour timecode at nanosecond resolution.
const MaxNanos = Nanos(24 * 3600 * 1e9)

// Rate is a rational frame rate in frames per second.
type Rate struct {
	Num int64 // frames
	Den int64 // seconds
}

// Supported frame rates.
var (
	FPS24   = Rate{24, 1}
	FPS25   = Rate{25, 1}
	FPS2997 = Rate{30000, 1001} // 29.97, drop-frame capable
	FPS50   = Rate{50, 1}
)

// Supported reports whether r is one of the archive timecode rates.
func (r Rate) Supported() bool {
	return r == FPS24 || r == FPS25 || r == FPS2997 || r == FPS50
}

// DropFrame reports whether r uses NTSC drop-frame numbering. Only 30000/1001
// does.
func (r Rate) DropFrame() bool { return r == FPS2997 }

// Nominal returns the integer frame count used by timecode numbering. 29.97
// labels use a 30-frame numbering scheme.
func (r Rate) Nominal() int64 { return roundDiv(r.Num, r.Den) }

// FrameDuration is the exact nanosecond duration of one frame. Only 29.97 is
// non-integral; it is rounded to the nearest nanosecond.
func (r Rate) FrameDuration() Nanos {
	// seconds * 1e9 = Den/Num * 1e9, computed as rounded integer division.
	return Nanos(roundDiv(1e9*r.Den, r.Num))
}

// TimecodeToNanos converts a frame number at rate r into nanoseconds since
// midnight. Drop-frame numbering in the caller (FrameIndex) has already mapped
// labels to real frame counts.
func (r Rate) TimecodeToNanos(frames int64) (Nanos, error) {
	if !r.Supported() {
		return 0, fmt.Errorf("unsupported rate %d/%d", r.Num, r.Den)
	}
	if frames < 0 {
		return 0, fmt.Errorf("negative frame count %d", frames)
	}
	// ns = frames * Den * 1e9 / Num. Both multiplications are checked so a
	// huge frame count cannot wrap into a plausible small value.
	hi, lo := bits.Mul64(uint64(frames), uint64(r.Den))
	if hi != 0 {
		return 0, fmt.Errorf("frame count %d overflows nanoseconds", frames)
	}
	hi, lo = bits.Mul64(lo, 1e9)
	if hi != 0 {
		return 0, fmt.Errorf("frame count %d overflows nanoseconds", frames)
	}
	q, rem := bits.Div64(0, lo, uint64(r.Num))
	if 2*rem >= uint64(r.Num) {
		q++
	}
	if q > uint64(MaxNanos) || q > uint64(1<<63-1) {
		return 0, fmt.Errorf("frame count %d exceeds the nanosecond range", frames)
	}
	return Nanos(q), nil
}

// NanosToFrames is the inverse of TimecodeToNanos, rounded down.
func (r Rate) NanosToFrames(n Nanos) (int64, error) {
	if n < 0 {
		return 0, fmt.Errorf("negative timestamp %d", int64(n))
	}
	if !r.Supported() {
		return 0, fmt.Errorf("unsupported rate %d/%d", r.Num, r.Den)
	}
	// frames = n * Num / (Den * 1e9)
	hi, lo := bits.Mul64(uint64(n), uint64(r.Num))
	if hi != 0 {
		return 0, fmt.Errorf("timestamp %d overflows frame conversion", int64(n))
	}
	q, _ := bits.Div64(0, lo, uint64(r.Den)*1e9)
	if q > uint64(1<<63-1) {
		return 0, fmt.Errorf("timestamp %d exceeds the frame range", int64(n))
	}
	return int64(q), nil
}

func roundDiv(a, b int64) int64 {
	q, r := a/b, a%b
	if 2*r >= b {
		q++
	}
	return q
}

// Range is a closed-open interval on the nanosecond scale.
type Range struct {
	Start Nanos `json:"start"`
	End   Nanos `json:"end"`
}

// Valid reports whether the range is non-inverted and non-negative.
func (r Range) Valid() bool { return r.Start >= 0 && r.End >= r.Start }

// Duration is the range length.
func (r Range) Duration() Nanos { return r.End - r.Start }

// Merge folds two ranges into their hull.
func (r Range) Merge(o Range) Range {
	if o.Start < r.Start {
		r.Start = o.Start
	}
	if o.End > r.End {
		r.End = o.End
	}
	return r
}

// Overlaps reports whether the ranges share at least one nanosecond.
func (r Range) Overlaps(o Range) bool { return r.Start < o.End && o.Start < r.End }

// Clock is shared by every stage that maps source positions to time. A zero
// Clock is a clock starting at zero.
type Clock struct {
	origin Nanos
}

// NewClock returns a clock whose zero position maps to origin.
func NewClock(origin Nanos) Clock { return Clock{origin: origin} }

// At translates a position relative to the clock's origin.
func (c Clock) At(n Nanos) Nanos { return c.origin + n }

// Relative translates an absolute timestamp back to clock-relative time.
func (c Clock) Relative(n Nanos) Nanos { return n - c.origin }
