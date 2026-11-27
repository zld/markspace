// Package clock defines the single integer media timebase shared by every
// pipeline stage. All timestamps, durations and alignment comparisons are
// performed in TicksPerSecond integer ticks; floating point is never used for
// media time so repeated runs produce bit-identical manifests.
package clock

import "fmt"

type clockError string

func (e clockError) Error() string { return string(e) }

func errBadRate(format string, args ...any) error {
	return clockError(fmt.Sprintf(format, args...))
}

// TicksPerSecond is the common RTP-style media clock rate (90 kHz). Every
// stage — scan, source normalization, ladder alignment, muxing and manifest
// generation — expresses time in this tick grid.
const TicksPerSecond int64 = 90_000

// Tick is one integer tick on the shared media clock.
type Tick int64

// FromFrames converts a frame count at framesPerSecond into ticks using
// exact integer division semantics and reports the remainder so callers can
// detect non-integer frame durations instead of rounding silently.
func FromFrames(frame int64, framesPerSecond int64) (Tick, int64) {
	if framesPerSecond <= 0 {
		return 0, 0
	}
	num := frame * TicksPerSecond
	return Tick(num / framesPerSecond), num % framesPerSecond
}

// FrameDuration returns the integer tick duration of one frame at the given
// frame rate. Rates that do not divide the tick grid are rejected.
func FrameDuration(framesPerSecond int64) (Tick, error) {
	if framesPerSecond <= 0 {
		return 0, errBadRate("frame rate %d must be positive", framesPerSecond)
	}
	if TicksPerSecond%framesPerSecond != 0 {
		return 0, errBadRate("frame rate %d does not divide the %d tick grid",
			framesPerSecond, TicksPerSecond)
	}
	return Tick(TicksPerSecond / framesPerSecond), nil
}

// Add applies a signed tick delta, which may be negative while building the
// normalized mapping after a source switch or clock wrap.
func (t Tick) Add(delta Tick) Tick { return t + delta }

// IsAfter reports whether t lands strictly later than other.
func (t Tick) IsAfter(other Tick) bool { return int64(t) > int64(other) }

// IsBefore reports whether t lands strictly earlier than other.
func (t Tick) IsBefore(other Tick) bool { return int64(t) < int64(other) }

// Distance returns the signed tick distance from other to t.
func (t Tick) Distance(other Tick) Tick { return t - other }

// HalfPeriod returns half of the modular clock span for the configured
// wrap bit width, which is the threshold used by wrap detection.
func HalfPeriod(bitWidth uint) Tick {
	if bitWidth <= 0 || bitWidth > 63 {
		return 0
	}
	return Tick(int64(1) << (bitWidth - 1))
}

// Period returns the full modular clock span 2^bitWidth.
func Period(bitWidth uint) int64 {
	if bitWidth <= 0 || bitWidth > 63 {
		return 0
	}
	return int64(1) << bitWidth
}
