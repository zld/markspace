package fixtures

import (
	"github.com/pion/rtp"

	seq "github.com/zld/mediapipe/rtp"
)

const ssrcFixture = 0x12345678

// BuildRTPSequence emits an arrival-ordered packet list for a track. Every
// media sample becomes exactly one RTP packet, so RTP timestamps advance by the
// sample delta with no rounding.
//
// dropEvery > 0 drops the last packet of every Nth group; burstLen > 0 adds a
// contiguous burst loss starting at packet burstStart. Out-of-order arrivals
// are introduced deterministically so recovery must reorder without trusting
// arrival order. Sequence.Sent always lists everything the sender emitted, so
// the recovery rate is measured against ground truth.
func BuildRTPSequence(ts TrackSpec, dropEvery, burstStart, burstLen int) *seq.Sequence {
	return buildSequence(ts, dropEvery, burstStart, burstLen)
}

// BuildRTPDrops emits a sequence that loses every Nth packet.
func BuildRTPDrops(ts TrackSpec, every int) *seq.Sequence {
	return buildSequence(ts, every, 0, 0)
}

// BuildRTPBurst emits a sequence with a contiguous burst loss.
func BuildRTPBurst(ts TrackSpec, start, burstLen int) *seq.Sequence {
	return buildSequence(ts, 0, start, burstLen)
}

func buildSequence(ts TrackSpec, dropEvery, burstStart, burstLen int) *seq.Sequence {
	clockRate := ts.Timescale
	if clockRate == 0 {
		clockRate = 90000
	}
	pt := uint8(96)
	if ts.Kind == "audio" {
		pt = 111
	}
	n := ts.SampleCount
	if n <= 0 {
		n = 60
	}

	emitted := make([]seq.Packet, n)
	sent := make([]uint16, n)
	baseSeq := uint16(1000)
	var tsAccum uint32
	for i := 0; i < n; i++ {
		p := &rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: pt,
				SequenceNumber: baseSeq + uint16(i), Timestamp: tsAccum,
				SSRC: ssrcFixture, Marker: i == n-1,
			},
			Payload: makeSample(ts.Kind, i, 16),
		}
		wire, err := p.Marshal()
		if err != nil {
			// Fixture packets are generated locally and always marshal.
			panic("fixture RTP marshal: " + err.Error())
		}
		emitted[i] = seq.Packet{
			Seq: p.SequenceNumber, TS: p.Timestamp, Raw: wire,
			Marker: p.Marker, PT: p.PayloadType, SSRC: p.SSRC,
		}
		sent[i] = p.SequenceNumber
		tsAccum += ts.SampleDelta
	}

	loss := make(map[int]bool, n)
	for i := 0; i < n; i++ {
		if dropEvery > 0 && (i+1)%dropEvery == 0 {
			loss[i] = true
		}
		if burstLen > 0 && i >= burstStart && i < burstStart+burstLen {
			loss[i] = true
		}
	}

	// Surviving indices in nominal order.
	alive := make([]int, 0, n)
	for i := 0; i < n; i++ {
		if !loss[i] {
			alive = append(alive, i)
		}
	}
	// Deterministic reordering: swap two adjacent surviving arrivals every
	// 17 packets, so reordering is exercised without randomness.
	for k := 5; k+1 < len(alive); k += 17 {
		alive[k], alive[k+1] = alive[k+1], alive[k]
	}

	packets := make([]seq.Packet, 0, len(alive))
	for arrival, idx := range alive {
		p := emitted[idx]
		p.Arrival = arrival
		packets = append(packets, p)
	}
	return &seq.Sequence{SSRC: ssrcFixture, ClockRate: clockRate, Sent: sent, Packets: packets}
}
