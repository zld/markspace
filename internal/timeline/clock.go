// Package timeline reconciles container block timestamps, client event
// logs and RTP packet timestamps onto a single nanosecond clock. It never
// invents a timestamp: when a correspondence cannot be established the gap
// is reported rather than guessed.
package timeline

import "errors"

// NanosecondsPerSecond is the common scale of every timeline in this system.
const NanosecondsPerSecond int64 = 1_000_000_000

// SampleRateFallback is used when an RTP stream's ssrc_map omits the clock
// rate. The delivery pipeline standardizes on 48 kHz audio.
const SampleRateFallback uint32 = 48000

// Clock converts media/RTP tick domains into absolute nanoseconds.
type Clock struct {
	// MediaTimecodeScale maps Matroska timecode units to nanoseconds.
	// Matroska default is 1,000,000 (one millisecond).
	MediaTimecodeScale int64
}

// NewClock builds a Clock from a Matroska TimecodeScale value.
func NewClock(timecodeScale uint64) (Clock, error) {
	if timecodeScale == 0 {
		return Clock{}, errors.New("timecode scale must be non-zero")
	}
	return Clock{MediaTimecodeScale: int64(timecodeScale)}, nil
}

// MediaToNS converts a Matroska scaled timecode into nanoseconds.
func (c Clock) MediaToNS(timecode int64) int64 {
	return timecode * c.MediaTimecodeScale
}

// RTPClock maps one RTP synchronization source (SSRC) onto nanoseconds.
type RTPClock struct {
	SSRC          uint32
	ClockRateHz   uint32
	// FirstRTP is the RTP timestamp of the reference packet.
	FirstRTP uint32
	// FirstNS is the nanosecond instant assigned to the reference packet.
	FirstNS int64
}

// RTPToNS converts an RTP timestamp to nanoseconds. RTP timestamps are
// unsigned 32-bit and wrap at 2^32; the subtraction is done in wrapping
// arithmetic and the signed difference is scaled by the clock rate.
func (r RTPClock) RTPToNS(ts uint32) int64 {
	deltaTicks := int32(ts - r.FirstRTP) // wrapping subtraction
	if r.ClockRateHz == 0 {
		return r.FirstNS
	}
	// Compute in nanoseconds without overflowing: ticks * 1e9 / rate.
	deltaNS := int64(deltaTicks) * (NanosecondsPerSecond / int64(r.ClockRateHz))
	return r.FirstNS + deltaNS
}

// RTPToNSExact keeps full precision for rates that do not divide 1e9 evenly.
func (r RTPClock) RTPToNSExact(ts uint32) int64 {
	deltaTicks := int64(int32(ts - r.FirstRTP))
	if r.ClockRateHz == 0 {
		return r.FirstNS
	}
	return r.FirstNS + deltaTicks*NanosecondsPerSecond/int64(r.ClockRateHz)
}

// Abs returns the absolute difference between two nanosecond instants.
func Abs(a, b int64) int64 {
	if a > b {
		return a - b
	}
	return b - a
}
