package rtp

import (
	pionrtp "github.com/pion/rtp"

	"github.com/zld/medcheck/pkg/media"
)

// RepairKind identifies how missing media payload was recovered.
type RepairKind string

const (
	RepairRetransmission RepairKind = "retransmission"
	RepairFEC            RepairKind = "fec"
)

// RepairPacket is an explicit retransmission or FEC recovery unit. It must
// carry recovered payload; the buffer never invents payload for a gap.
type RepairPacket struct {
	Sequence   uint16
	Timestamp  uint32
	Payload    []byte
	Kind       RepairKind
	ArrivalUS  int64
	ByteOffset int64
}

// ApplyRepair inserts a repair payload if its extended sequence is still inside
// the bounded window and has not already been received.
func (b *Buffer) ApplyRepair(r RepairPacket) (*Packet, error) {
	if !b.started {
		return nil, media.NewError(media.CatInvalidArgument, b.TrackID, int64(r.Sequence), r.ByteOffset,
			"cannot repair before any RTP packet")
	}
	ext := unwrapSequence(r.Sequence, b.maxSeen)
	if b.flushed || ext < b.next || ext >= b.next+b.Window {
		b.Stats.Late++
		b.Issues.Add(media.CatLateWindow, b.TrackID, int64(ext), r.ByteOffset,
			"repair for sequence %d arrived outside repair window base=%d width=%d", r.Sequence, b.next, b.Window)
		return nil, media.NewError(media.CatLateWindow, b.TrackID, int64(ext), r.ByteOffset,
			"repair sequence %d outside window", r.Sequence)
	}
	if b.seen[ext] {
		b.Stats.Duplicates++
		b.Issues.Add(media.CatDuplicatePacket, b.TrackID, int64(ext), r.ByteOffset,
			"repair for already received sequence %d", r.Sequence)
		return b.pending[ext], nil
	}
	if len(r.Payload) == 0 {
		return nil, media.NewError(media.CatInvalidArgument, b.TrackID, int64(ext), r.ByteOffset,
			"refusing empty repair payload for sequence %d", r.Sequence)
	}

	p := &Packet{
		RTP: &pionrtp.Packet{
			Header:  pionrtp.Header{SequenceNumber: r.Sequence, Timestamp: r.Timestamp},
			Payload: append([]byte(nil), r.Payload...),
		},
		Sequence: r.Sequence, ExtSeq: ext, Timestamp: r.Timestamp,
		ArrivalUS: r.ArrivalUS, ByteOffset: r.ByteOffset, Recovered: true,
	}
	b.pending[ext] = p
	b.seen[ext] = true
	b.Stats.Repaired++
	if ext > b.maxSeen {
		b.maxSeen = ext
	}
	return p, nil
}

// MakeRawPacket serializes a simple RTP packet for locally generated fixtures.
func MakeRawPacket(payloadType uint8, ssrc uint32, seq uint16, timestamp uint32, marker bool, payload []byte) ([]byte, error) {
	p := &pionrtp.Packet{
		Header: pionrtp.Header{
			Version: 2, PayloadType: payloadType, SSRC: ssrc,
			SequenceNumber: seq, Timestamp: timestamp, Marker: marker,
		},
		Payload: payload,
	}
	b, err := p.Marshal()
	if err != nil {
		return nil, media.NewError(media.CatInvalidEncoding, "rtp", int64(seq), -1, "marshal RTP: %v", err)
	}
	return b, nil
}
