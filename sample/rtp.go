package sample

import (
	"github.com/pion/rtp"
)

// RTPStream is the generated sent and post-loss/reorder wire sequence.
type RTPStream struct {
	Packets []*rtp.Packet
	Wire    []*rtp.Packet
	Lost    map[uint16]bool
}

// BuildRTPStream generates count RTP packets, removes loss sequences, and
// rotates deterministic blocks of size reorder+1 to create in-window reorder.
func BuildRTPStream(count, reorder int, losses []int) *RTPStream {
	if count <= 0 {
		count = 48
	}
	lost := make(map[uint16]bool, len(losses))
	for _, l := range losses {
		if l >= 0 && l < count {
			lost[uint16(l)] = true
		}
	}
	sent := make([]*rtp.Packet, 0, count)
	for i := 0; i < count; i++ {
		seq := uint16(i)
		sent = append(sent, &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: seq,
				Timestamp:      uint32(i) * 3600,
				SSRC:           0x17171717,
			},
			Payload: []byte{byte(i >> 8), byte(i), 0xAA, 0xBB},
		})
	}

	wire := make([]*rtp.Packet, 0, count-len(lost))
	if reorder <= 0 {
		for _, p := range sent {
			if !lost[p.SequenceNumber] {
				wire = append(wire, p)
			}
		}
		return &RTPStream{Packets: sent, Wire: wire, Lost: lost}
	}

	block := reorder + 1
	for start := 0; start < count; start += block {
		end := start + block
		if end > count {
			end = count
		}
		// Rotate [start,end) left by one: the first packet arrives last.
		for j := start + 1; j < end; j++ {
			p := sent[j]
			if !lost[p.SequenceNumber] {
				wire = append(wire, p)
			}
		}
		p := sent[start]
		if !lost[p.SequenceNumber] {
			wire = append(wire, p)
		}
	}
	return &RTPStream{Packets: sent, Wire: wire, Lost: lost}
}
