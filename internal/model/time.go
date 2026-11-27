// Package model defines the shared time basis and media data structures
// exchanged between all mediarc subsystems.
package model

import "time"

// MediaTime is the unified time basis. Recovered timestamps are always kept
// next to the original raw value so that recovery never overwrites source
// timestamps.
type MediaTime struct {
	// Raw is the original timestamp exactly as present in the container
	// (tfdt baseMediaDecodeTime, PES PTS, RTP timestamp ...).
	Raw uint64
	// Unwrapped is the monotonically increasing tick count after wraparound
	// expansion. It is recovery output and never feeds back into Raw.
	Unwrapped int64
	// Timescale maps ticks to seconds: one second equals Timescale ticks.
	Timescale uint32
}

// Millis converts unwrapped ticks to the shared millisecond timeline.
func (t MediaTime) Millis() int64 {
	if t.Timescale == 0 {
		return 0
	}
	return t.Unwrapped * 1000 / int64(t.Timescale)
}

// DurationMillis converts a tick duration to milliseconds.
func DurationMillis(ticks int64, timescale uint32) int64 {
	if timescale == 0 {
		return 0
	}
	return ticks * 1000 / int64(timescale)
}

// Wallclock wraps the millisecond shared timeline. All fan-in comparisons and
// boundary cross-checks are expressed in this unit.
type CommonTime struct {
	Millis int64
}

// Sub returns the millisecond distance between two common-timeline instants.
func (c CommonTime) Sub(other CommonTime) int64 {
	return c.Millis - other.Millis
}

// AsDuration turns a millisecond value into a time.Duration.
func AsDuration(millis int64) time.Duration {
	return time.Duration(millis) * time.Millisecond
}
