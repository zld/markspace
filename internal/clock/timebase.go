// Package clock defines the unified integer time base used by every track
// in the pipeline. All manifest timestamps are converted to 90 kHz ticks and
// all rounding is performed by the single helper below so the conversion is
// deterministic across stages and reruns.
package clock

import "fmt"

// TickRate is the shared timeline frequency: 90,000 integer ticks per second.
// It is divisible by the common video rates (24/25/30/60 fps) and by the
// supported audio rates (44.1/48/96 kHz), which keeps most conversions exact.
const TickRate int64 = 90000

// Ticks is a point or duration on the shared 90 kHz integer timeline.
type Ticks int64

// FromSeconds converts a duration expressed in a source rate's integer units
// to 90 kHz ticks using round-half-away-from-zero. Fractional values never
// silently truncate: a half tick always resolves away from zero.
func FromSeconds(units, sourceRate int64) Ticks {
	if sourceRate <= 0 {
		panic(fmt.Sprintf("clock: invalid source rate %d", sourceRate))
	}
	num := units * TickRate
	if num >= 0 {
		return Ticks((num + sourceRate/2) / sourceRate)
	}
	return Ticks(-((-num + sourceRate/2) / sourceRate))
}

// ToRate converts ticks back to integer units of the given source rate,
// applying the same round-half-away-from-zero rule.
func ToRate(t Ticks, sourceRate int64) int64 {
	if sourceRate <= 0 {
		panic(fmt.Sprintf("clock: invalid target rate %d", sourceRate))
	}
	num := int64(t) * sourceRate
	if num >= 0 {
		return (num + TickRate/2) / TickRate
	}
	return -((-num + TickRate/2) / TickRate)
}

// Millis converts ticks to whole milliseconds (half-away-from-zero).
func Millis(t Ticks) int64 {
	return ToRate(t, 1000)
}

// FrameInterval returns the exact or rounded tick duration of one frame at
// the given frames-per-second rate.
func FrameInterval(fps int64) Ticks {
	if fps <= 0 {
		panic(fmt.Sprintf("clock: invalid fps %d", fps))
	}
	return FromSeconds(1, fps)
}

// OrderKey gives a stable total order for entries that share a tick position:
// the track ID breaks ties first, then the frame index inside the track.
type OrderKey struct {
	At         Ticks
	TrackID    string
	FrameIndex int
}

// Less defines the canonical tie-broken ordering on the shared timeline.
func (k OrderKey) Less(o OrderKey) bool {
	if k.At != o.At {
		return k.At < o.At
	}
	if k.TrackID != o.TrackID {
		return k.TrackID < o.TrackID
	}
	return k.FrameIndex < o.FrameIndex
}
