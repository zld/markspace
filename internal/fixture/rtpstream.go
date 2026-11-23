package fixture

import (
	"sort"

	"github.com/pion/rtp"
)

// writeRTP builds a packet stream with one cross-segment burst loss.
// Most dropped packets are re-sent as retransmissions while a small tail
// ages out of the reorder window and stays unrecoverable.
func (b *Bundle) writeRTP(r *rng, opts Options) {
	total := opts.Segments * opts.FramesPerSeg * 2
	burstStart := total / 2
	burstLen := 34 // crosses a segment boundary
	if b.Healthy {
		burstLen = 0
	}
	recoverable := burstLen - 1
	for i := 0; i < total; i++ {
		seq := uint16(i)
		pkt := rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: 96,
				SequenceNumber: seq,
				Timestamp:      uint32(i * 3000),
				SSRC:           0xC0FFEE,
			},
			Payload: []byte{byte(i), byte(i >> 8), byte(r.intn(256))},
		}
		missing := burstLen > 0 && i >= burstStart && i < burstStart+burstLen
		b.Packets = append(b.Packets, PacketSpec{
			SequenceNumber: seq,
			Timestamp:      pkt.Timestamp,
			SSRC:           pkt.SSRC,
			Payload:        pkt.Payload,
			Missing:        missing,
			Retransmitted:  missing && i < burstStart+recoverable,
			SegmentNumber:  i / (opts.FramesPerSeg * 2) + 1,
		})
	}
}

// OrderedPackets returns the transmission sequence including late
// retransmissions: retransmitted packets are re-injected shortly after the
// burst (within the receiver's reorder window) in sequence order.
func (b *Bundle) OrderedPackets() []PacketSpec {
	var out []PacketSpec
	rtxBySeq := map[uint16]PacketSpec{}
	var burstEnd int
	for _, p := range b.Packets {
		if p.Missing && p.Retransmitted {
			rtxBySeq[p.SequenceNumber] = p
			burstEnd = int(p.SequenceNumber) + 1
			continue
		}
		if p.Missing {
			continue // permanently lost
		}
		out = append(out, p)
		// Once the burst is 12 packets behind us, flush its RTX fills in
		// ascending sequence order so they land inside the reorder window.
		if int(p.SequenceNumber) == burstEnd+12 {
			var seqs []int
			for s := range rtxBySeq {
				seqs = append(seqs, int(s))
			}
			sort.Ints(seqs)
			for _, s := range seqs {
				out = append(out, rtxBySeq[uint16(s)])
			}
			rtxBySeq = map[uint16]PacketSpec{}
		}
	}
	return out
}
