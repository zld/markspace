// Package model defines the shared, integer-nanosecond time model and the
// structured error vocabulary used by every avpipe subsystem.
package model

import "fmt"

// TimeNS is the single time base of the whole pipeline: every timestamp,
// offset, duration and deadline is an integer number of nanoseconds. No float
// clock arithmetic is performed anywhere in avpipe.
type TimeNS int64

// ClockRTPWrap is the 32-bit RTP timestamp modulus (2^32).
const ClockRTPWrap uint32 = 1 << 31

// Kind enumerates the discontinuity kinds that boundary repair can observe.
type Kind string

const (
	// KindContinuous means no discontinuity was detected.
	KindContinuous Kind = "continuous"
	// KindWrap is a 32-bit media-clock wraparound, always repairable.
	KindWrap Kind = "wrap"
	// KindSwitch is a source switch identified by the explicit boundary
	// descriptor, always repairable.
	KindSwitch Kind = "switch"
	// KindIllegal is an undecidable boundary: a backward jump that is neither
	// a single 32-bit wrap nor a declared source switch. Not repairable.
	KindIllegal Kind = "illegal"
)

// SourceClock identifies the clock a raw sample originated from.
type SourceClock struct {
	// ClockRate is the RTP clock rate in ticks per second (e.g. 90000).
	ClockRate uint32 `json:"clock_rate"`
	// SourceID changes at every source switch and stays constant otherwise.
	SourceID uint32 `json:"source_id"`
}

// SampleMeta is one streamed sample. It keeps the raw clock value alongside
// the unified nanosecond timeline so every repair can be audited.
type SampleMeta struct {
	Track       int         `json:"track"`
	Seq         int         `json:"seq"` // zero-based sample ordinal on the track
	RawTS       uint32      `json:"raw_ts"`
	Clock       SourceClock `json:"clock"`
	RawTimeNS   TimeNS      `json:"raw_time_ns"` // raw RTP TS converted to ns
	FixedTimeNS TimeNS      `json:"fixed_time_ns"`
	InputOffset int64       `json:"input_offset"` // byte offset inside the .rtp file
	Size        int         `json:"size"`         // packet size in bytes
}

// StableKey returns the stable ordering / dedup key of a sample. Samples with
// an identical fixed time are ordered by track then sample sequence.
func (s SampleMeta) StableKey() string {
	return fmt.Sprintf("t%d/s%08d", s.Track, s.Seq)
}

// Boundary describes one timeline boundary (clip point / transition edge)
// between two consecutive samples on a track.
type Boundary struct {
	Track      int   `json:"track"`
	AfterSeq   int   `json:"after_seq"` // sequence number of the left sample
	Offset     int64 `json:"offset"`    // byte offset of the right sample
	Kind       Kind  `json:"kind"`
	Repairable bool  `json:"repairable"`
	// ExpectedNS is the descriptor-anchored fixed time for the right sample,
	// used to validate source-switch repairs.
	ExpectedNS TimeNS `json:"expected_ns,omitempty"`
}

// Correction is an auditable repair: the corrected time plus the exact raw
// offset and clock it was derived from.
type Correction struct {
	Track       int         `json:"track"`
	AfterSeq    int         `json:"after_seq"`
	Kind        Kind        `json:"kind"`
	Offset      int64       `json:"offset"`
	RawTimeNS   TimeNS      `json:"raw_time_ns"`
	FixedTimeNS TimeNS      `json:"fixed_time_ns"`
	ShiftNS     TimeNS      `json:"shift_ns"` // additive offset applied to raw time
	Clock       SourceClock `json:"clock"`
}

// TicksToNS converts an RTP clock tick count to integer nanoseconds.
func TicksToNS(ticks uint32, rate uint32) TimeNS {
	return TimeNS(int64(ticks) * 1_000_000_000 / int64(rate))
}

// NSToTicks converts integer nanoseconds back to RTP clock ticks (rounded).
func NSToTicks(ns TimeNS, rate uint32) uint32 {
	return uint32((int64(ns)*int64(rate) + 500_000_000) / 1_000_000_000)
}

// IsWrap reports whether a backward raw transition from prev to cur is
// explained by exactly one unsigned 32-bit wraparound. The unwrapped distance
// must be positive and no larger than half the modulus.
func IsWrap(prev, cur uint32) bool {
	delta := int64(cur) - int64(prev) // signed, negative on a backward jump
	if delta >= 0 {
		return false
	}
	wrapped := delta + int64(ClockRTPWrap)*2
	return wrapped > 0 && wrapped <= int64(ClockRTPWrap)
}

// SortSamples orders samples by fixed time, then track, then sequence so that
// equal timestamps have a fully deterministic order.
func SortSamples(in []SampleMeta) {
	for i := 1; i < len(in); i++ {
		for j := i; j > 0 && less(in[j], in[j-1]); j-- {
			in[j], in[j-1] = in[j-1], in[j]
		}
	}
}

func less(a, b SampleMeta) bool {
	if a.FixedTimeNS != b.FixedTimeNS {
		return a.FixedTimeNS < b.FixedTimeNS
	}
	if a.Track != b.Track {
		return a.Track < b.Track
	}
	return a.Seq < b.Seq
}
