// Package clock unifies RTP media clocks, sampling clocks and the MP4
// timescale onto a single reference timeline (milliseconds). All modules
// in the kernel convert their native tick domains through this package so
// that buffering, recovery and A/V sync share one event-time base.
package clock

import "time"

// Rate is a clock frequency in ticks per second.
type Rate uint32

// Declared clock domains used by the kernel.
const (
	// RTPClockVideo is the 90 kHz RTP clock mandated for video.
	RTPClockVideo Rate = 90000
	// RTPClockAudio is the 48 kHz RTP clock used for the audio fixtures.
	RTPClockAudio Rate = 48000
	// MP4Timescale is the timescale used when rebuilding sample tables.
	MP4Timescale Rate = 1000
	// RefClock is the kernel-wide reference clock: milliseconds.
	RefClock Rate = 1000
)

// ToMillis converts ticks in this clock domain to whole milliseconds.
func (r Rate) ToMillis(ticks uint64) int64 {
	return int64(ticks * uint64(RefClock) / uint64(r))
}

// MillisToTicks converts milliseconds to ticks in this clock domain.
func (r Rate) MillisToTicks(ms int64) uint64 {
	return uint64(ms) * uint64(r) / uint64(RefClock)
}

// ToDuration converts ticks to a time.Duration.
func (r Rate) ToDuration(ticks uint64) time.Duration {
	return time.Duration(ticks) * time.Second / time.Duration(r)
}

// Convert translates ticks between two clock domains sharing an epoch.
func Convert(ticks uint64, from, to Rate) uint64 {
	return ticks * uint64(to) / uint64(from)
}

// RTPDelta returns the signed difference newer-older of two RTP
// timestamps, correct across a single 32-bit wrap.
func RTPDelta(newer, older uint32) int32 {
	return int32(newer - older)
}

// RTPMillis converts a signed RTP timestamp delta to milliseconds.
func RTPMillis(delta int32, r Rate) int64 {
	return int64(delta) * 1000 / int64(r)
}
