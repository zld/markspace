// Package rtp packetizes media access units and recovers streams from
// loss, reordering, duplication, and timestamp wrap. All timestamps are
// unified through media.Clock; this package never invents its own time.
package rtp

import (
	"github.com/pion/rtp"

	"mediacore/media"
)

const (
	// MaxPayload is the per-packet payload ceiling (fits a typical MTU).
	MaxPayload = 1200
	// MaxUnitBytes rejects absurd access-unit sizes so memory stays
	// bounded regardless of claimed input.
	MaxUnitBytes = 1 << 20
)

// Packetizer fragments access units into RTP packets on a unified clock.
type Packetizer struct {
	ssrc  uint32
	pt    uint8
	seq   uint16
	scale media.TimeScale
}

// NewPacketizer builds a packetizer for one SSRC/payload type.
func NewPacketizer(ssrc uint32, payloadType uint8, scale media.TimeScale) *Packetizer {
	return &Packetizer{ssrc: ssrc, pt: payloadType, scale: scale}
}

// SetNextSeq pins the next sequence number. Pipelines that derive
// sequence numbers statelessly (so a resumed run reproduces the same
// wire format) set it before each Packetize call.
func (p *Packetizer) SetNextSeq(seq uint16) { p.seq = seq }

// Packetize splits payload (one access unit) into RTP packets stamped
// with ts (ticks at the packetizer's scale, already unwrapped to int64;
// the wire carries the low 32 bits). The last packet gets the marker bit.
func (p *Packetizer) Packetize(payload []byte, ts media.Ticks) ([]*rtp.Packet, error) {
	return p.PacketizeStride(payload, ts, 0)
}

// PacketizeStride is Packetize with a per-fragment timestamp stride:
// fragment j is stamped ts + j*stride. Constant-stride streams (PCM
// chunks) let the recovery side extrapolate concealed timestamps
// accurately; frame-based streams use stride 0.
func (p *Packetizer) PacketizeStride(payload []byte, ts media.Ticks, stride uint32) ([]*rtp.Packet, error) {
	if len(payload) == 0 {
		return nil, media.Fault(media.CodeInternal, "rtp.Packetize", -1, "empty access unit")
	}
	if len(payload) > MaxUnitBytes {
		return nil, media.Fault(media.CodeLimitExceeded, "rtp.Packetize", -1,
			"access unit %d bytes exceeds %d", len(payload), MaxUnitBytes)
	}
	stamp := uint32(int64(ts)) // wire format wraps; recover side unwraps
	var out []*rtp.Packet
	for off := 0; off < len(payload); off += MaxPayload {
		end := off + MaxPayload
		if end > len(payload) {
			end = len(payload)
		}
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				SequenceNumber: p.seq,
				Timestamp:      stamp + uint32(off/MaxPayload)*stride,
				SSRC:           p.ssrc,
				PayloadType:    p.pt,
				Marker:         end == len(payload),
			},
			Payload: payload[off:end],
		}
		p.seq++
		out = append(out, pkt)
	}
	return out, nil
}
