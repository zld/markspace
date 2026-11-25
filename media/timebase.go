// Package media defines the shared time base, time-scale conversion helpers
// and clock-drift estimation used by every stage of the replay pipeline.
package media

import (
	"math"
	"time"
)

// Clock rates supported by the sample fixtures. The implementation boundary
// states that only RTP clock rates covered by samples are accepted.
const (
	AudioClockRate uint32 = 8000
	VideoClockRate uint32 = 90000
)

// MaxInputBytes bounds the total number of bytes accepted for one run.
// Length prefixes that would exceed the remaining file or this limit are
// rejected instead of being used for allocation.
const MaxInputBytes int64 = 256 << 20 // 256 MiB

// MaxPacketBytes bounds a single RTP record payload.
const MaxPacketBytes = 65535

// TimeBase unifies an RTP clock: its rate, the monotonic extended sequence
// numbering and a linear drift estimate against the receiver arrival clock.
type TimeBase struct {
	SSRC      uint32
	ClockRate uint32
	// FirstSeq/MaxSeq track the raw 16-bit RTP sequence window.
	FirstSeq uint16
	// Cycles counts 16-bit sequence rollovers (extended sequence number).
	Cycles uint32
	// BaseRTP is the RTP timestamp of the first accepted packet.
	BaseRTP uint32
	// BaseArrival is the receiver time of the first accepted packet.
	BaseArrival time.Time
	// DriftPPM is the estimated clock drift in parts per million: positive
	// means the RTP clock runs faster than the arrival clock.
	DriftPPM float64
	// Samples counts packets accepted into the estimate.
	Samples int
}

// SupportedClockRate reports whether rate is one of the sample-covered rates.
func SupportedClockRate(rate uint32) bool {
	return rate == AudioClockRate || rate == VideoClockRate
}

// TicksToMS converts clock ticks to milliseconds on the given rate.
func TicksToMS(ticks int64, rate uint32) float64 {
	if rate == 0 {
		return 0
	}
	return float64(ticks) * 1000.0 / float64(rate)
}

// MSToTicks converts milliseconds to whole clock ticks.
func MSToTicks(ms float64, rate uint32) int64 {
	return int64(math.Round(ms * float64(rate) / 1000.0))
}

// ExtendedSeq expands a 16-bit sequence number against the tracked cycles.
func (tb *TimeBase) ExtendedSeq(seq uint16) uint64 {
	return uint64(tb.Cycles)*uint64(65536) + uint64(seq)
}

// DriftPoint is one observation used for drift estimation: Arrival is the
// receiver timestamp, RTPTicks the (unwrapped) sender clock at that instant.
type DriftPoint struct {
	Arrival  time.Duration // relative to the stream base arrival
	RTPTicks int64         // relative to the stream base RTP timestamp
}

// EstimateDrift fits rtpTicks = slope*arrival + intercept by ordinary least
// squares and returns the drift in PPM. A minimum of two points is required;
// below that the drift is reported as zero.
func EstimateDrift(points []DriftPoint, rate uint32) float64 {
	if len(points) < 2 || rate == 0 {
		return 0
	}
	var sx, sy, sxx, sxy float64
	for _, p := range points {
		x := float64(p.Arrival.Nanoseconds()) / 1e9
		y := float64(p.RTPTicks) / float64(rate)
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	n := float64(len(points))
	denom := n*sxx - sx*sx
	if denom == 0 {
		return 0
	}
	slope := (n*sxy - sx*sy) / denom
	return (slope - 1.0) * 1e6
}

// CorrectTicks applies the drift estimate to a relative tick count, returning
// the drift-corrected ticks on the shared (arrival) time line.
func CorrectTicks(relTicks int64, driftPPM float64) float64 {
	return float64(relTicks) / (1.0 + driftPPM/1e6)
}
