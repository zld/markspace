// Package rtp parses RTP packets, reorders them by extended sequence
// number and recovers burst loss with explicit silence placeholders.
package rtp

import (
	"github.com/pion/rtp"

	"mediasync/internal/clock"
)

// Packet is a parsed RTP packet plus the extended sequence number assigned
// during recovery. The embedded pion packet keeps the original wire values
// (16-bit sequence, 32-bit timestamp) for auditability.
type Packet struct {
	*rtp.Packet
	ExtSeq int64
}

// Seq returns the on-wire 16-bit sequence number.
func (p *Packet) Seq() clock.Seq16 { return clock.Seq16(p.Packet.SequenceNumber) }

// TS returns the on-wire 32-bit timestamp.
func (p *Packet) TS() clock.Tick32 { return clock.Tick32(p.Packet.Timestamp) }

// ParsePacket unmarshals one RTP datagram. Pion errors are translated into
// the library's structured clock.MediaError type so every subsystem shares
// one error vocabulary.
func ParsePacket(buf []byte) (*Packet, error) {
	if len(buf) < 12 {
		return nil, clock.TruncationError(int64(len(buf)), int64(12-len(buf)), "RTP header")
	}
	pr := &rtp.Packet{}
	if err := pr.Unmarshal(buf); err != nil {
		return nil, clock.AsMediaError(err)
	}
	if pr.Version != 2 {
		return nil, clock.ErrorAt(clock.ErrInvalidArgument, 0,
			"unsupported RTP version %d", pr.Version)
	}
	return &Packet{Packet: pr, ExtSeq: clock.ExtendSeq16(0, clock.Seq16(pr.SequenceNumber))}, nil
}

// MarshalPacket is the deterministic inverse of ParsePacket used by the
// fixture generator and tests.
func MarshalPacket(seq uint16, ts uint32, ssrc uint32, payload []byte) ([]byte, error) {
	p := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           ssrc,
		},
		Payload: payload,
	}
	buf, err := p.Marshal()
	if err != nil {
		return nil, clock.AsMediaError(err)
	}
	return buf, nil
}
