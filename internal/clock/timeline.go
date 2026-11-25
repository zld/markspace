// Package clock defines the single media timeline used by every pipeline
// stage. All durations and offsets are expressed in nanoseconds so that
// cutting, silence detection and fingerprinting share one time base.
package clock

import "math"

// Standard RTP video clock rate (RFC 3551 §4.1), reused through
// github.com/pion/rtp when stream offsets are placed on the wire timeline.
const RTPVideoClockRate uint32 = 90000

// TimeBase maps frame numbers to a deterministic nanosecond timeline.
// Frame f occupies [f*FrameDurNS, (f+1)*FrameDurNS).
type TimeBase struct {
	frameDurNS int64
	rate       uint32
}

// NewTimeBase builds a time base with the given per-frame duration.
// A non-positive duration falls back to the 25 fps default so callers can
// never construct an unusable zero-duration timeline.
func NewTimeBase(frameDurNS int64) *TimeBase {
	if frameDurNS <= 0 {
		frameDurNS = int64(40_000_000) // 25 fps
	}
	return &TimeBase{frameDurNS: frameDurNS, rate: RTPVideoClockRate}
}

// FrameDurNS returns the constant access-unit duration in nanoseconds.
func (t *TimeBase) FrameDurNS() int64 { return t.frameDurNS }

// FrameToNS returns the start time of frame f in nanoseconds.
func (t *TimeBase) FrameToNS(f uint64) int64 { return int64(f) * t.frameDurNS }

// FrameEndNS returns the exclusive end time of frame f in nanoseconds.
func (t *TimeBase) FrameEndNS(f uint64) int64 { return int64(f+1) * t.frameDurNS }

// NSToRTP converts a nanosecond offset to an RTP timestamp on the 90 kHz
// clock. RTP timestamps are uint32 and wrap every ~13.26 hours; the wrap is
// applied here so callers always compare wire-compatible values.
func (t *TimeBase) NSToRTP(ns int64) uint32 {
	if ns < 0 {
		ns = 0
	}
	scaled := uint64(math.Round(float64(ns) * float64(t.rate) / 1e9))
	return uint32(scaled)
}

// RTPToNS is the inverse of NSToRTP. It cannot recover wrap counts, so it
// returns the offset represented by the raw timestamp within one wrap.
func (t *TimeBase) RTPToNS(ts uint32) int64 {
	return int64(math.Round(float64(ts) * 1e9 / float64(t.rate)))
}

// Point is one instant on the shared timeline.
type Point struct {
	Frame  uint64 `json:"frame"`
	Offset int64  `json:"offset_ns"`
}

// Interval is a left-closed, right-open time range [StartNS, EndNS).
type Interval struct {
	StartNS int64 `json:"start_ns"`
	EndNS   int64 `json:"end_ns"`
}

// Contains reports whether instant ns falls inside the half-open interval.
func (i Interval) Contains(ns int64) bool { return ns >= i.StartNS && ns < i.EndNS }

// DurationNS returns the interval length in nanoseconds.
func (i Interval) DurationNS() int64 { return i.EndNS - i.StartNS }

// FrameInterval returns the half-open interval covered by frame f.
func (t *TimeBase) FrameInterval(f uint64) Interval {
	return Interval{StartNS: t.FrameToNS(f), EndNS: t.FrameEndNS(f)}
}
