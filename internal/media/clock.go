// Package media defines the shared time base and the deterministic media
// primitives used by ingest, alignment and the publishing pipeline.
package media

import "time"

// NanosecondBase is the single time base of the whole kernel. Every video
// frame and every audio packet is eventually expressed as an integer number
// of nanoseconds counted from the same epoch, so alignment never mixes units.
const NanosecondBase int64 = int64(time.Second)

// Clock converts codec/RTP tick domains into the shared nanosecond time base.
// All arithmetic is integer based and therefore deterministic across runs and
// machines; floating point is never used for timestamp conversion.
type Clock struct {
	// VideoTimescale is ticks per second for video frame timestamps.
	VideoTimescale int64
	// AudioClockRate is the RTP clock rate in Hz for audio packets.
	AudioClockRate int64
}

// VideoToNanos converts a video timestamp expressed in VideoTimescale ticks.
func (c Clock) VideoToNanos(ticks int64) int64 {
	return ticks * NanosecondBase / c.VideoTimescale
}

// AudioToNanos converts an RTP timestamp expressed on AudioClockRate.
func (c Clock) AudioToNanos(rtpTicks uint32) int64 {
	return int64(rtpTicks) * NanosecondBase / c.AudioClockRate
}

// NanosToMs converts the shared nanosecond base into milliseconds, keeping
// one fractional digit so small sync errors stay observable in reports.
func NanosToMs(nanos int64) float64 {
	return float64(nanos) / float64(int64(time.Millisecond))
}
