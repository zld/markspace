package rtp

import (
	"mediarc/model"
)

// Frame is one packet placed on the media timeline.
type Frame struct {
	// ExtendedSeq is the wraparound-unwrapped 64-bit sequence number.
	ExtendedSeq uint64
	// RTPTimestamp is the raw 32-bit RTP timestamp.
	RTPTimestamp uint32
	// StartNS and EndNS delimit the frame on the shared nanosecond timeline.
	StartNS int64
	EndNS   int64
	// DurationNS is EndNS-StartNS.
	DurationNS int64
	// Marker reports the RTP marker bit.
	Marker bool
	// Payload is a copy of the packet payload.
	Payload []byte
	// ReceivedNS is the arrival time.
	ReceivedNS int64
	// OutOfOrder is true when a higher sequence arrived before this one.
	OutOfOrder bool
}

// Gap is a run of missing packets between delivered frames.
type Gap struct {
	// StartSeq is the first missing extended sequence number.
	StartSeq uint64
	// Count is the number of missing packets.
	Count uint64
	// DurationNS is the media time the missing packets would have carried.
	DurationNS int64
}

// EndSeqExclusive is the first sequence after the gap.
func (g Gap) EndSeqExclusive() uint64 { return g.StartSeq + g.Count }

// Release is everything the window made deliverable in order.
type Release struct {
	// Frames and Gaps are each ordered by starting sequence number; merging
	// them by that key reproduces the original stream order.
	Frames []Frame
	Gaps   []Gap
	// Issues records non-fatal observations such as timestamp jumps in the
	// order they were detected.
	Issues []*model.Error
}
