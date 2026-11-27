// Package clock defines the single integer media time base used by the
// transport-stream reader, the audio pipeline and the recovery bookkeeping.
//
// Two coordinate systems share one origin:
//
//   - nanoseconds, for wall-clock/PCR derived timing; and
//   - sample ticks, at a fixed sample rate, for audio positioning.
//
// Everything is int64.  No floating point value ever crosses a package
// boundary; conversions round toward zero deterministically so that a
// persisted resume position replays byte-for-byte.
package clock

import (
	"fmt"

	"example.com/assetpack/internal/fault"
)

// TicksPerSecond is the fixed audio sample clock used for sample positions.
const TicksPerSecond int64 = 90000

// Timeline couples a nanosecond epoch to a sample clock.
type Timeline struct {
	// SampleRate is the rate at which sample positions are expressed.
	SampleRate int64
}

// New validates and constructs a timeline.
func New(sampleRate int64) (*Timeline, error) {
	if sampleRate <= 0 || sampleRate > 768000 {
		return nil, fault.New(fault.StageAudio, fault.KindInvalidArg, "invalid sample rate %d", sampleRate)
	}
	return &Timeline{SampleRate: sampleRate}, nil
}

// SamplesToNanos converts a sample count to nanoseconds at the timeline rate.
func (t *Timeline) SamplesToNanos(samples int64) int64 {
	return samples * 1e9 / t.SampleRate
}

// NanosToSamples converts nanoseconds to a sample count, rounding to the
// nearest sample so that SamplesToNanos/NanosToSamples round-trips exactly
// for any sample count the timestamps can represent.
func (t *Timeline) NanosToSamples(nanos int64) int64 {
	if t == nil || t.SampleRate <= 0 {
		return 0
	}
	if nanos < 0 {
		return -((-nanos*t.SampleRate + 5e8) / 1e9)
	}
	return (nanos*t.SampleRate + 5e8) / 1e9
}

// Rescale converts a sample count from one rate to another using integer
// arithmetic only, so repeated conversions of a stored position are stable.
func Rescale(samples, fromRate, toRate int64) (int64, error) {
	if fromRate <= 0 || toRate <= 0 {
		return 0, fault.New(fault.StageAudio, fault.KindInvalidArg, "invalid rescale rates %d -> %d", fromRate, toRate)
	}
	if samples > 0 && toRate > (1<<62)/samples {
		return 0, fault.New(fault.StageAudio, fault.KindInvalidArg, "rescale overflow: %d samples at %d -> %d", samples, fromRate, toRate)
	}
	return samples * toRate / fromRate, nil
}

// Position is a dual-coordinate resume point.  Offset is a transport-stream
// byte offset; Sample is a position on the audio clock.  Both are stored so
// that a resumed run can seek either representation.
type Position struct {
	Offset int64 `json:"offset"`
	Sample int64 `json:"sample"`
	Nanos  int64 `json:"nanos"`
}

// NewPosition derives a position from a byte offset and a sample index.
func (t *Timeline) NewPosition(offset, sample int64) Position {
	return Position{Offset: offset, Sample: sample, Nanos: t.SamplesToNanos(sample)}
}

// String renders a compact, grep-friendly resume point.
func (p Position) String() string {
	return fmt.Sprintf("offset=%d sample=%d nanos=%d", p.Offset, p.Sample, p.Nanos)
}

// Valid reports that the position has non-negative coordinates.
func (p Position) Valid() bool {
	return p.Offset >= 0 && p.Sample >= 0 && p.Nanos >= 0
}

// Zero is the canonical start position.
var Zero = Position{Offset: 0, Sample: 0, Nanos: 0}
