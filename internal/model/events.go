package model

import "errors"

// ErrUnknownCodec is returned when a packet record names a codec the kernel
// does not know how to parse. The structured finding code is still
// ErrUnsupportedCodec.
var ErrUnknownCodec = errors.New("unsupported codec")

// CodecKind identifies the payload carried by an RTP packet record.
type CodecKind string

const (
	CodecOpus CodecKind = "opus"
	CodecAAC  CodecKind = "aac"
)

// Valid reports whether the codec is one the kernel knows how to parse.
func (c CodecKind) Valid() bool {
	return c == CodecOpus || c == CodecAAC
}

// SampleRate returns the reference sample rate used to convert packet
// sample counts into the unified tick clock. Opus always uses 48 kHz;
// AAC records carry their own rate and default to 44.1 kHz.
func (c CodecKind) SampleRate(rate uint32) (uint32, error) {
	switch c {
	case CodecOpus:
		return 48_000, nil
	case CodecAAC:
		if rate == 0 {
			return 44_100, nil
		}
		return rate, nil
	default:
		return 0, ErrUnknownCodec
	}
}

// PacketRecord is one captured RTP packet as stored in the offline trace.
// ArrivalTick is the capture time on the unified clock; RTPTimestamp is the
// 32-bit media timestamp carried in the RTP header. ExtendedSequence is
// filled in by the RTP reorder stage.
type PacketRecord struct {
	Stream           uint32    `json:"stream"`
	Codec            CodecKind `json:"codec"`
	Sequence         uint16    `json:"sequence"`
	ExtendedSequence uint32    `json:"extended_sequence,omitempty"`
	RTPTimestamp     uint32    `json:"rtp_timestamp"`
	Payload          []byte    `json:"-"`
	ArrivalTick      Tick      `json:"arrival_tick"`
	ClockRate        uint32    `json:"clock_rate,omitempty"`
}

// PacketVerdict is the per-packet processing conclusion. Every input packet
// produces exactly one verdict, including duplicates and truncated payloads.
type PacketVerdict struct {
	Stream       uint32    `json:"stream"`
	Codec        CodecKind `json:"codec"`
	Sequence     uint16    `json:"sequence"`
	ExtendedSeq  uint32    `json:"extended_sequence"`
	RTPTimestamp uint32    `json:"rtp_timestamp"`
	Start        Tick      `json:"start"`
	End          Tick      `json:"end"`
	Samples      int64     `json:"samples"`
	Duplicate    bool      `json:"duplicate"`
	Reordered    bool      `json:"reordered"`
	Wrapped      bool      `json:"wrapped"`
	Accepted     bool      `json:"accepted"`
	Conclusion   string    `json:"conclusion"`
	FindingCodes []ErrorCode `json:"finding_codes,omitempty"`
}

// EventKind separates packet events from track and flow events on the
// merged timeline.
type EventKind string

const (
	EventPacket    EventKind = "packet"
	EventKeyframe  EventKind = "keyframe"
	EventGap       EventKind = "gap"
	EventFlow      EventKind = "flow"
	EventAnomaly   EventKind = "anomaly"
)

// TimelineEvent is one ordered point on the unified media timeline. The
// merge stage sorts these deterministically across both analysis paths.
type TimelineEvent struct {
	Kind      EventKind `json:"kind"`
	Start     Tick      `json:"start"`
	End       Tick      `json:"end"`
	Stream    uint32    `json:"stream,omitempty"`
	Track     uint32    `json:"track,omitempty"`
	Sequence  uint32    `json:"sequence,omitempty"`
	Order     int       `json:"-"`
	Label     string    `json:"label,omitempty"`
}

// Less defines the stable ordering of the merged timeline: media start,
// then end, kind, track, stream and original input order.
func (e TimelineEvent) Less(o TimelineEvent) bool {
	if e.Start != o.Start {
		return e.Start < o.Start
	}
	if e.End != o.End {
		return e.End < o.End
	}
	if e.Kind != o.Kind {
		return e.Kind < o.Kind
	}
	if e.Track != o.Track {
		return e.Track < o.Track
	}
	if e.Stream != o.Stream {
		return e.Stream < o.Stream
	}
	if e.Sequence != o.Sequence {
		return e.Sequence < o.Sequence
	}
	return e.Order < o.Order
}
