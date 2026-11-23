// Package transport ingests RTP packets, reconstructs per-SSRC ordered
// streams despite duplication, reordering, loss bursts and sequence
// wraparound, and hands contiguous byte ranges to the codec/index stages.
package transport

import (
	"github.com/pion/rtp"

	"mediaroute/internal/clock"
)

// MaxPayloadBytes bounds memory for any single packet payload. Packets
// claiming more are rejected rather than buffered.
const MaxPayloadBytes = 64 * 1024

// Packet is the validated internal view of one RTP datagram.
type Packet struct {
	SSRC      uint32
	Seq       uint16
	Timestamp uint32
	Marker    bool
	Payload   []byte
}

// ParsePacket parses an on-wire RTP packet using pion/rtp and applies the
// platform's own bounds checks. The returned payload is a copy owned by the
// pipeline so later socket reuse cannot mutate buffered state.
func ParsePacket(raw []byte) (*Packet, error) {
	var rp rtp.Packet
	if err := rp.Unmarshal(raw); err != nil {
		return nil, clock.E(clock.PhaseTransport, 0, -1, true, "invalid RTP datagram", err)
	}
	if len(rp.Payload) == 0 {
		return nil, clock.E(clock.PhaseTransport, rp.SSRC, int(rp.SequenceNumber), true,
			"empty RTP payload", nil)
	}
	if len(rp.Payload) > MaxPayloadBytes {
		return nil, clock.E(clock.PhaseTransport, rp.SSRC, int(rp.SequenceNumber), false,
			"payload exceeds size cap", nil)
	}
	payload := make([]byte, len(rp.Payload))
	copy(payload, rp.Payload)
	return &Packet{
		SSRC:      rp.SSRC,
		Seq:       rp.SequenceNumber,
		Timestamp: rp.Timestamp,
		Marker:    rp.Marker,
		Payload:   payload,
	}, nil
}

// Marshal re-encodes a packet; used by the fixture generator and tests.
func (p *Packet) Marshal() ([]byte, error) {
	rp := rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: p.Seq,
			Timestamp:      p.Timestamp,
			SSRC:           p.SSRC,
			Marker:         p.Marker,
		},
		Payload: p.Payload,
	}
	raw, err := rp.Marshal()
	if err != nil {
		return nil, clock.E(clock.PhaseTransport, p.SSRC, int(p.Seq), false, "marshal failed", err)
	}
	return raw, nil
}
