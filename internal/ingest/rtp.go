// Package ingest turns wire-level RTP audio packets into located, time-checked
// media packets. Parsing never invents samples: truncated or unknown input
// returns a stable model.Error with track, sequence, stage and byte offset.
package ingest

import (
	"github.com/pion/rtp"

	"mediad/internal/model"
)

// Codec identifies the payload coding used by a track.
type Codec string

const (
	CodecOpus Codec = "opus"
	CodecAAC  Codec = "aac"
)

// TrackSpec describes the fixed parameters of one RTP receive track.
type TrackSpec struct {
	ID        string
	Codec     Codec
	ClockRate uint32 // RTP timestamp rate; Opus is always 48000
	// AACSampleRate is the codec sample rate used for AU duration when the AU
	// header carries no frequency info; zero selects the rate table default.
	AACSampleRate uint32
}

// AudioPacket is one parsed RTP audio packet. Duration is expressed in the
// track's RTP clock ticks and, for convenience, in integer nanoseconds.
type AudioPacket struct {
	Track       string
	ExtSequence uint64 // 16-bit RTP sequence expanded against the rollover mod
	Sequence    uint16 // raw RTP sequence number
	RTPTime     uint32
	Payload     []byte
	Offset      int64 // byte offset of the payload inside the raw datagram
	DurationTC  int64 // frame duration in RTP clock ticks
	DurationNS  model.Nano
}

// ParseRTP validates one raw RTP datagram and expands its 16-bit sequence
// number against lastExt (the highest extended sequence seen so far). The
// returned packet carries the raw payload; codec duration is filled by the
// Opus/AAC parsers.
func ParseRTP(spec TrackSpec, raw []byte, lastExt uint64) (*AudioPacket, error) {
	const stage = "ingest"
	if len(raw) < 12 {
		return nil, model.ErrRTPTruncated.At(spec.ID, 0, stage, int64(len(raw)),
			"RTP datagram shorter than 12-byte fixed header")
	}
	if v := raw[0] >> 6; v != 2 {
		return nil, model.ErrRTPVersion.At(spec.ID, 0, stage, 0,
			"unsupported RTP version")
	}
	var p rtp.Packet
	if err := p.Unmarshal(raw); err != nil {
		return nil, model.ErrRTPTruncated.At(spec.ID, uint32(p.SequenceNumber),
			stage, int64(len(raw)), err.Error())
	}
	if len(p.Payload) == 0 {
		return nil, model.ErrRTPTruncated.At(spec.ID, uint32(p.SequenceNumber),
			stage, int64(p.PayloadOffset), "empty RTP payload")
	}
	return &AudioPacket{
		Track:       spec.ID,
		ExtSequence: ExtendSequence(p.SequenceNumber, lastExt),
		Sequence:    p.SequenceNumber,
		RTPTime:     p.Timestamp,
		Payload:     p.Payload,
		Offset:      int64(p.PayloadOffset),
	}, nil
}

// ExtendSequence expands a 16-bit RTP sequence number into a 64-bit extended
// sequence given the greatest extended sequence already observed. The
// expansion follows RFC 3550 rollover semantics modulo 2^16: the candidate is
// placed in the 2^15-wide neighbourhood of lastExt.
func ExtendSequence(seq uint16, lastExt uint64) uint64 {
	const mod = 1 << 16
	base := lastExt &^ (mod - 1)
	cand := base | uint64(seq)
	diff := int64(cand) - int64(lastExt)
	switch {
	case diff > mod/2:
		cand -= mod // sequence number just rolled over
	case diff < -mod/2:
		cand += mod // old sequence number from before a rollover
	}
	return cand
}

// Parse applies the codec-specific duration parser to an already-parsed RTP
// packet, filling DurationTC and DurationNS.
func Parse(spec TrackSpec, pkt *AudioPacket) error {
	switch spec.Codec {
	case CodecOpus:
		return parseOpus(spec, pkt)
	case CodecAAC:
		return parseAAC(spec, pkt)
	default:
		return model.ErrUsage.At(spec.ID, uint32(pkt.Sequence), "ingest",
			pkt.Offset, "unknown codec "+string(spec.Codec))
	}
}
