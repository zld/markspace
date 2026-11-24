package fixture

import (
	"github.com/pion/rtp"
)

// RTPBurst models one contiguous sequence-number loss burst.
type RTPBurst struct {
	// Start is the first sequence number dropped on the primary path.
	Start uint16
	// Length is the number of consecutive packets dropped.
	Length int
	// Retransmit is how many of the dropped packets arrive later as
	// retransmissions; the remainder stay unrecovered.
	Retransmit int
	// DelaySlots is how many arrival slots after the burst tail each
	// retransmission is held.
	DelaySlots int
}

// RTPSpec describes a deterministic RTP arrival stream. Loss, reordering,
// duplication and retransmission are all explicit, never random, so two
// builds with the same spec are byte-identical.
type RTPSpec struct {
	SSRC          uint32
	PayloadType   uint8
	ClockRate     uint32
	Count         int
	BaseSeq       uint16
	BaseTimestamp uint32
	TimestampStep uint32
	// DriftPPM skews each generated RTP timestamp relative to the nominal
	// step, so the remote clock runs faster (positive) or slower (negative)
	// than the receiver's local clock. The skew accumulates linearly and is
	// fully deterministic.
	DriftPPM int
	Bursts   []RTPBurst
	// ReorderSwaps swaps arrival positions of two consecutive primary
	// packets (indexed into the primary arrival list).
	ReorderSwaps []int
	// Duplicates lists sequence numbers that arrive twice.
	Duplicates []uint16
}

type rtpEvent struct {
	seq       uint16
	packet    *rtp.Packet
	arrivalAt int
}

// BuildRTP materializes the arrival-order packet stream. Media timestamps
// are monotonic modulo 2^32; the wrap fixture chooses BaseTimestamp close
// to the modulus to exercise 32-bit rollover.
func BuildRTP(spec RTPSpec) []*rtp.Packet {
	lost := map[uint16]bool{}
	type retrans struct {
		seq     uint16
		arrival int
	}
	var retransList []retrans
	for _, b := range spec.Bursts {
		delay := b.DelaySlots
		if delay <= 0 {
			delay = 2
		}
		for i := 0; i < b.Length; i++ {
			seq := b.Start + uint16(i)
			lost[seq] = true
			if i < b.Retransmit {
				// Primary arrival index of the burst tail plus delay.
				tailArrival := int(b.Start-spec.BaseSeq) + b.Length - 1
				retransList = append(retransList, retrans{
					seq:     seq,
					arrival: tailArrival + delay + i,
				})
			}
		}
	}
	dup := map[uint16]bool{}
	for _, s := range spec.Duplicates {
		dup[s] = true
	}

	// Primary path: one arrival slot per packet, gaps skipped.
	var events []rtpEvent
	primaryPos := 0
	for i := 0; i < spec.Count; i++ {
		seq := spec.BaseSeq + uint16(i)
		if lost[seq] {
			continue
		}
		events = append(events, rtpEvent{
			seq:       seq,
			packet:    mkPacket(spec, i, seq, false),
			arrivalAt: primaryPos,
		})
		primaryPos++
	}
	// Retransmissions use slots beyond the primary stream tail so they
	// never collide with primary positions.
	tail := primaryPos
	for _, r := range retransList {
		events = append(events, rtpEvent{
			seq:       r.seq,
			packet:    mkPacket(spec, int(r.seq-spec.BaseSeq), r.seq, true),
			arrivalAt: tail + r.arrival,
		})
	}
	// Duplicates trail the stream as fresh arrivals.
	for _, seq := range spec.Duplicates {
		events = append(events, rtpEvent{
			seq:       seq,
			packet:    mkPacket(spec, int(seq-spec.BaseSeq), seq, false),
			arrivalAt: tail + 1000 + int(seq-spec.BaseSeq),
		})
	}
	sortEvents(events)
	applyReorderSwaps(events, spec.ReorderSwaps)

	out := make([]*rtp.Packet, len(events))
	for i, e := range events {
		out[i] = e.packet
	}
	return out
}

func mkPacket(spec RTPSpec, index int, seq uint16, marker bool) *rtp.Packet {
	// Nominal remote tick plus a linear PPM drift term accumulated over the
	// packet index. Rounding is integer-based so the stream is deterministic.
	nominal := uint64(index) * uint64(spec.TimestampStep)
	skew := uint64(0)
	if spec.DriftPPM != 0 {
		skew = nominal * uint64(absInt(spec.DriftPPM)) / 1_000_000
		if spec.DriftPPM < 0 {
			skew = 0
		}
	}
	ts := spec.BaseTimestamp
	if spec.DriftPPM >= 0 {
		ts += uint32(nominal + skew)
	} else {
		ts += uint32(nominal - skew)
	}
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    spec.PayloadType,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           spec.SSRC,
			Marker:         marker,
		},
		Payload: []byte{byte(index), byte(index >> 8)},
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func sortEvents(events []rtpEvent) {
	for i := 1; i < len(events); i++ {
		for j := i; j > 0 && events[j-1].arrivalAt > events[j].arrivalAt; j-- {
			events[j-1], events[j] = events[j], events[j-1]
		}
	}
}

func applyReorderSwaps(events []rtpEvent, swaps []int) {
	for _, pos := range swaps {
		if pos >= 0 && pos+1 < len(events) {
			events[pos], events[pos+1] = events[pos+1], events[pos]
		}
	}
}
