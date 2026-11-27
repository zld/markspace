package fixture

import (
	"github.com/pion/rtp"
)

// RTPScenario configures a synthesized RTP event stream.
type RTPScenario struct {
	SSRC           uint32
	ClockHz        uint32
	PacketCount    int
	StartSeq       uint16
	// DropSeqs are 16-bit sequence numbers removed from the arrival stream
	// (simulated burst/random loss).
	DropSeqs map[uint16]bool
	// DupSeqs are sequence numbers delivered twice.
	DupSeqs map[uint16]bool
	// ReorderPairs swap adjacent delivery order while keeping timestamps.
	Reorder bool
	// Wrap forces the sequence range to cross the 16-bit boundary.
	Wrap bool
	// LateSeqs are delivered LateBy positions later than their in-order slot.
	// When LateBy is smaller than the reorder window they are refilled and
	// count as recovered losses; otherwise they are given up first.
	LateSeqs map[uint16]bool
	LateBy   int
}

// RTPStream is the generated event stream plus bookkeeping for assertions.
type RTPStream struct {
	Datagrams      [][]byte
	Arrivals       []int64 // nanosecond arrival timestamps
	Packets        []*rtp.Packet
	InjectedLosses int
	SequenceSeqs   []uint16
}

// BuildRTP synthesizes an ordered (then perturbed) RTP event stream. Arrival
// timestamps stay within the P95 latency budget: 10 ms per packet spacing.
func BuildRTP(s RTPScenario) *RTPStream {
	if s.ClockHz == 0 {
		s.ClockHz = 90000
	}
	out := &RTPStream{}
	stepTS := uint32(s.ClockHz / 100) // 10 ms of media per packet
	type arrival struct {
		pkt *rtp.Packet
		at  int64
		seq uint16
	}
	var ordered []arrival
	for i := 0; i < s.PacketCount; i++ {
		seq := s.StartSeq + uint16(i)
		if s.Wrap {
			seq = s.StartSeq + uint16(i) // arithmetic naturally wraps at uint16
		}
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: seq,
				Timestamp:      uint32(i) * stepTS,
				SSRC:           s.SSRC,
			},
			Payload: []byte{byte(i), 0xAA},
		}
		ordered = append(ordered, arrival{pkt: pkt, at: int64(i) * 10_000_000, seq: seq})
	}
	// Apply reordering by swapping two adjacent middle packets.
	if s.Reorder && len(ordered) >= 4 {
		ordered[1], ordered[2] = ordered[2], ordered[1]
	}
	// Delay flagged packets by re-inserting them LateBy slots later.
	if len(s.LateSeqs) > 0 && s.LateBy > 0 {
		work := make([]arrival, len(ordered))
		copy(work, ordered)
		// Move late packets one at a time, from last to first, so earlier
		// indices remain valid while repositioning.
		var lateIdx []int
		for i, a := range work {
			if s.LateSeqs[a.seq] {
				lateIdx = append(lateIdx, i)
			}
		}
		for k := len(lateIdx) - 1; k >= 0; k-- {
			i := lateIdx[k]
			target := i + s.LateBy
			if target >= len(work) {
				target = len(work) - 1
			}
			a := work[i]
			work = append(work[:i], work[i+1:]...)
			// Recompute target after removal (slice shrank by one).
			ins := target
			if ins > len(work) {
				ins = len(work)
			}
			work = append(work[:ins], append([]arrival{a}, work[ins:]...)...)
		}
		ordered = work
	}
	emit := func(a arrival, dup bool) {
		raw, err := a.pkt.Marshal()
		if err != nil {
			panic(err)
		}
		out.Datagrams = append(out.Datagrams, raw)
		out.Arrivals = append(out.Arrivals, a.at)
		out.Packets = append(out.Packets, a.pkt)
		out.SequenceSeqs = append(out.SequenceSeqs, a.seq)
		_ = dup
	}
	for _, a := range ordered {
		if s.DropSeqs[a.seq] {
			out.InjectedLosses++
			continue
		}
		emit(a, false)
		if s.DupSeqs[a.seq] {
			emit(a, true) // retransmitted copy arrives immediately
		}
	}
	return out
}
