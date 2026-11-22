package testmedia

import (
	"math/rand"

	"github.com/pion/rtp"
)

// RTPStreamSpec configures a synthetic, deterministic RTP packet stream
// including the network impairments the recovery layer must tolerate.
type RTPStreamSpec struct {
	Seed          int64
	SSRC          uint32
	Packets       int
	PayloadType   uint8
	ClockRate     uint32
	FrameStep     uint32 // timestamp increment per packet
	LossRate      float64
	ReorderRate   float64
	DuplicateRate float64
}

// RTPStream is the generated delivery order plus the truth manifest.
type RTPStream struct {
	Packets []*rtp.Packet // delivery order, including duplicates (no dropped)
	Lost    []uint16      // sequence numbers withheld entirely
	Order   []uint16      // intended sequence order
}

// BuildRTPStream produces packets with deterministic impairments. Reordering
// swaps adjacent deliveries; duplicates resend the previous packet; lost
// packets are omitted from the delivery slice but listed in the manifest.
func BuildRTPStream(spec RTPStreamSpec) RTPStream {
	rng := rand.New(rand.NewSource(spec.Seed))
	out := RTPStream{}
	ts := uint32(0)
	for seq := 0; seq < spec.Packets; seq++ {
		if rng.Float64() < spec.LossRate {
			out.Lost = append(out.Lost, uint16(seq))
			continue
		}
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    spec.PayloadType,
				SequenceNumber: uint16(seq),
				Timestamp:      ts,
				SSRC:           spec.SSRC,
				Marker:         seq%16 == 15,
			},
			Payload: []byte{byte(seq >> 8), byte(seq)},
		}
		ts += spec.FrameStep
		out.Order = append(out.Order, uint16(seq))
		out.Packets = append(out.Packets, pkt)
		if rng.Float64() < spec.DuplicateRate {
			out.Packets = append(out.Packets, pkt) // same pointer: exact duplicate
		}
	}
	// Adjacent swaps for reordering, applied after duplication so the
	// intended sequence manifest stays stable.
	for i := 0; i+1 < len(out.Packets); i++ {
		if rng.Float64() < spec.ReorderRate {
			out.Packets[i], out.Packets[i+1] = out.Packets[i+1], out.Packets[i]
			i++ // don't swap the same pair twice
		}
	}
	return out
}
