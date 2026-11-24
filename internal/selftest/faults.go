package selftest

import (
	"github.com/pion/rtp"
)

// InjectReorderDup deterministically reorders and duplicates packets to
// simulate a jittery network link. Every 4th packet pair is swapped and
// every 23rd packet is duplicated in place.
func InjectReorderDup(pkts []*rtp.Packet) []*rtp.Packet {
	out := make([]*rtp.Packet, 0, len(pkts)+len(pkts)/23+1)
	for i := 0; i < len(pkts); i++ {
		if i%4 == 0 && i+1 < len(pkts) {
			out = append(out, pkts[i+1], pkts[i])
			i++
		} else {
			out = append(out, pkts[i])
		}
		if i%23 == 0 {
			out = append(out, pkts[i]) // duplicate
		}
	}
	return out
}

// InjectLoss drops the packets with the given sequence numbers, simulating
// a burst loss that never gets retransmitted inside the jitter window.
func InjectLoss(pkts []*rtp.Packet, drop map[uint16]bool) []*rtp.Packet {
	out := make([]*rtp.Packet, 0, len(pkts))
	for _, p := range pkts {
		if !drop[p.SequenceNumber] {
			out = append(out, p)
		}
	}
	return out
}

// InjectNonMonotonic shifts timestamps backwards by backoff ticks for all
// packets from the given packet index on, simulating a timestamp jump.
func InjectNonMonotonic(pkts []*rtp.Packet, fromIndex int, backoff uint32) []*rtp.Packet {
	out := make([]*rtp.Packet, len(pkts))
	for i, p := range pkts {
		cp := *p
		if i >= fromIndex {
			cp.Timestamp -= backoff
		}
		out[i] = &cp
	}
	return out
}

// SeqOfFrame returns the sequence number of the first packet of a frame.
func SeqOfFrame(pkts []*rtp.Packet, frame uint32) (uint16, bool) {
	for _, p := range pkts {
		if len(p.Payload) >= 5 && frameOf(p) == frame {
			return p.SequenceNumber, true
		}
	}
	return 0, false
}

// FramePacketSeqs returns the sequence numbers of all packets of a frame.
func FramePacketSeqs(pkts []*rtp.Packet, frame uint32) []uint16 {
	var out []uint16
	for _, p := range pkts {
		if len(p.Payload) >= 5 && frameOf(p) == frame {
			out = append(out, p.SequenceNumber)
		}
	}
	return out
}

func frameOf(p *rtp.Packet) uint32 {
	return uint32(p.Payload[0])<<24 | uint32(p.Payload[1])<<16 |
		uint32(p.Payload[2])<<8 | uint32(p.Payload[3])
}
