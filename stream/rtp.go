package stream

import (
	"context"
	"fmt"

	"github.com/pion/rtp"

	"github.com/zld/media-regress/internal/error"
)

// RTP framing
//
// Every published block travels as one RTP packet. The packet carries the
// timeline unambiguously: the RTP timestamp (target sample rate) locates the
// block on the media clock, the sequence number orders packets on the wire,
// and the SSRC identifies the track. Routing blocks through a real RTP
// marshal/unmarshal cycle lets the stream stage reject malformed packets and
// proves that the timestamps a consumer sees are the ones the producer meant.
//
// The payload type is fixed per track kind and the marker bit is set on the
// last block, matching common audio publication practice.

const (
	// PayloadTypeAudio is the static payload type for the profile's audio.
	PayloadTypeAudio uint8 = 96
	// audioClockRate is the RTP clock rate of the published audio timeline.
	audioClockRate = 48000
)

// rtpSSRC derives a stable SSRC from the track id so packets from different
// tracks never collide and the value is reproducible across runs.
func rtpSSRC(trackID uint32) uint32 {
	return 0x4d52_0000 | (trackID & 0xffff)
}

// encodePacket builds an RTP packet for one block.
func encodePacket(it Item, marker bool) (*rtp.Packet, error) {
	if len(it.Payload) == 0 {
		return nil, merr.New(merr.StageStream, merr.KindInconsistent, it.TrackID, it.InputOffset,
			"block %d has an empty payload", it.Index)
	}
	p := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			Marker:         marker,
			PayloadType:    PayloadTypeAudio,
			SequenceNumber: it.Sequence,
			Timestamp:      it.Timestamp,
			SSRC:           rtpSSRC(it.TrackID),
		},
		Payload: it.Payload,
	}
	return p, nil
}

// DecodePacket parses and validates an RTP packet, returning the block
// identity it carries. A packet that fails to unmarshal, names the wrong SSRC
// or clock rate, or carries no payload is a structured error: the stream stage
// never guesses what a broken packet meant.
func DecodePacket(raw []byte, trackID uint32, expectedClockRate uint32) (uint16, uint32, []byte, error) {
	p := &rtp.Packet{}
	if err := p.Unmarshal(raw); err != nil {
		return 0, 0, nil, merr.Wrap(merr.StageStream, merr.KindCorrupt, trackID, int64(len(raw)), err,
			"rtp packet does not parse")
	}
	if p.Version != 2 {
		return 0, 0, nil, merr.New(merr.StageStream, merr.KindCorrupt, trackID, int64(len(raw)),
			"rtp packet version %d is not 2", p.Version)
	}
	if p.SSRC != rtpSSRC(trackID) {
		return 0, 0, nil, merr.New(merr.StageStream, merr.KindInconsistent, trackID, int64(len(raw)),
			"rtp ssrc %08x does not belong to track %d", p.SSRC, trackID)
	}
	if p.PayloadType != PayloadTypeAudio {
		return 0, 0, nil, merr.New(merr.StageStream, merr.KindInconsistent, trackID, int64(len(raw)),
			"rtp payload type %d is not %d", p.PayloadType, PayloadTypeAudio)
	}
	if expectedClockRate != 0 && expectedClockRate != audioClockRate {
		return 0, 0, nil, merr.New(merr.StageStream, merr.KindInconsistent, trackID, int64(len(raw)),
			"rtp clock rate %d does not match the audio timeline", expectedClockRate)
	}
	if len(p.Payload) == 0 {
		return 0, 0, nil, merr.New(merr.StageStream, merr.KindCorrupt, trackID, int64(len(raw)),
			"rtp packet carries no payload")
	}
	return p.SequenceNumber, p.Timestamp, p.Payload, nil
}

// MarshalPacket serializes a block into an RTP packet and re-parses it. The
// round trip is the guard against producing a packet the consumer cannot read;
// any mismatch is reported before the block is committed.
func MarshalPacket(it Item, marker bool) ([]byte, error) {
	p, err := encodePacket(it, marker)
	if err != nil {
		return nil, err
	}
	raw, err := p.Marshal()
	if err != nil {
		return nil, merr.Wrap(merr.StageStream, merr.KindParse, it.TrackID, it.InputOffset, err,
			"marshal rtp packet for block %d", it.Index)
	}
	seq, ts, payload, err := DecodePacket(raw, it.TrackID, audioClockRate)
	if err != nil {
		return nil, err
	}
	if seq != it.Sequence || ts != it.Timestamp || len(payload) != len(it.Payload) {
		return nil, merr.New(merr.StageStream, merr.KindInconsistent, it.TrackID, it.InputOffset,
			"rtp round trip changed block %d (seq %d->%d ts %d->%d len %d->%d)",
			it.Index, it.Sequence, seq, it.Timestamp, ts, len(it.Payload), len(payload))
	}
	return raw, nil
}

// PacketStats reports the RTP framing counters for a pipeline.
type PacketStats struct {
	Packets int
	Bytes   int
	LastSeq uint16
	LastTS  uint32
}

// Packetizer wraps a pipeline so every block is RTP-framed before it reaches
// the queue. It is the only supported producer path for published audio.
type Packetizer struct {
	pipeline   *Pipeline
	stats      PacketStats
	markerSent bool
}

// NewPacketizer wraps an existing pipeline.
func NewPacketizer(p *Pipeline) *Packetizer {
	return &Packetizer{pipeline: p}
}

// Push frames one block as an RTP packet and enqueues the serialized bytes as
// the item payload. Timestamp ordering is still enforced by the pipeline.
func (pz *Packetizer) Push(ctx context.Context, it Item, last bool) error {
	raw, err := MarshalPacket(it, last)
	if err != nil {
		return err
	}
	framed := it
	framed.Essence = len(it.Payload)
	framed.Payload = raw
	if err := pz.pipeline.Push(ctx, framed); err != nil {
		return err
	}
	pz.stats.Packets++
	pz.stats.Bytes += len(raw)
	pz.stats.LastSeq = it.Sequence
	pz.stats.LastTS = it.Timestamp
	if last {
		pz.markerSent = true
	}
	return nil
}

// Stats returns the framing counters.
func (pz *Packetizer) Stats() PacketStats { return pz.stats }

// MarkerSent reports whether the final-block marker was emitted.
func (pz *Packetizer) MarkerSent() bool { return pz.markerSent }

// String formats the counters for terminal output.
func (s PacketStats) String() string {
	return fmt.Sprintf("rtp_packets=%d rtp_bytes=%d rtp_last_seq=%d rtp_last_ts=%d",
		s.Packets, s.Bytes, s.LastSeq, s.LastTS)
}
