package fixture

import (
	"github.com/pion/rtp"
)

// RTPConfig fixes the simulated session. 90kHz is the standard video
// clock; 30fps yields a 3000-tick timestamp step.
const (
	RTPClockHz     uint32 = 90000
	RTPFrameStep   uint32 = 3000
	RTPPayloadMax         = 140
	RTPSSRC        uint32 = 0x4D4C4231 // "MLB1"
	// RepairPT identifies XOR parity packets; media uses PT 96.
	RepairPT = 97
	MediaPT  = 96
)

// RTPManifest lists the session ground truth for recovery assertions.
type RTPManifest struct {
	MediaCount   int
	RepairCount  int
	GroupSize    int
	BaseSequence uint16
	BaseTS       uint32
}

// GenerateRTPSession creates media packets with contiguous sequence
// numbers plus one XOR repair packet per group. Repair packets carry a
// separate high sequence space so they never perturb media sequencing.
func GenerateRTPSession(seed int64, mediaCount, groupSize int, baseSeq uint16, baseTS uint32) ([]*rtp.Packet, RTPManifest) {
	rng := NewRand(seed)
	var pkts []*rtp.Packet
	groups := (mediaCount + groupSize - 1) / groupSize
	seq := baseSeq
	for g := 0; g < groups; g++ {
		start := g * groupSize
		end := start + groupSize
		if end > mediaCount {
			end = mediaCount
		}
		var parity []byte
		for i := start; i < end; i++ {
			// Fixed-length payloads keep XOR parity lossless: variable
			// lengths cannot be reconstructed from a single parity vector.
			payload := make([]byte, RTPPayloadMax)
			rng.Fill(payload)
			payload[0] = byte(i)
			p := &rtp.Packet{
				Header: rtp.Header{
					Version: 2, PayloadType: MediaPT,
					SequenceNumber: seq,
					Timestamp:      baseTS + uint32(i)*RTPFrameStep,
					SSRC:           RTPSSRC,
					Marker:         i == end-1,
				},
				Payload: payload,
			}
			parity = xorBytes(parity, payload)
			seq++
			pkts = append(pkts, p)
		}
		// Repair packet lives in an independent sequence space starting at
		// 0x8000 + group index; its timestamp equals the group base.
		repair := &rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: RepairPT,
				SequenceNumber: 0x8000 + uint16(g),
				Timestamp:      baseTS + uint32(start)*RTPFrameStep,
				SSRC:           RTPSSRC,
			},
			Payload: parity,
		}
		pkts = append(pkts, repair)
	}
	return pkts, RTPManifest{
		MediaCount: mediaCount, RepairCount: groups,
		GroupSize: groupSize, BaseSequence: baseSeq, BaseTS: baseTS,
	}
}

// RTPPerturbation configures network damage applied to an ordered session.
type RTPPerturbation struct {
	LossBurst int // delete this many consecutive media packets once
	LossAt    int // media index where the burst starts
	Reorder   int // maximum displacement distance for shuffling
	Duplicate int // number of media packets duplicated
	Seed      int64
}

// PerturbRTP returns the packets as they would arrive over the network:
// media packets reordered/duplicated/lost, each followed by the repair
// packet of the group whose highest media sequence just passed. Sequence
// numbers inside packets are untouched; only slice order and membership
// change.
func PerturbRTP(pkts []*rtp.Packet, p RTPPerturbation) []*rtp.Packet {
	rng := NewRand(p.Seed + 3)

	var media, repair []*rtp.Packet
	repairByTS := map[uint32]*rtp.Packet{}
	for _, pkt := range pkts {
		if pkt.PayloadType == RepairPT {
			repair = append(repair, pkt)
			repairByTS[pkt.Timestamp] = pkt
		} else {
			media = append(media, pkt)
		}
	}

	drop := map[int]bool{}
	if p.LossBurst > 0 {
		start := p.LossAt
		if start <= 0 {
			start = 8
		}
		for i := start; i < start+p.LossBurst && i < len(media); i++ {
			drop[i] = true
		}
	}

	var surviving []*rtp.Packet
	for i, pkt := range media {
		if !drop[i] {
			surviving = append(surviving, pkt)
		}
	}

	// Duplicate chosen surviving media packets by value.
	for d := 0; d < p.Duplicate; d++ {
		idx := rng.Intn(len(surviving))
		clone := *surviving[idx]
		clone.Payload = append([]byte(nil), surviving[idx].Payload...)
		surviving = append(surviving, &clone)
	}

	// Bounded reorder: swap positions within a Reorder-sized window.
	if p.Reorder > 0 {
		for i := 0; i < len(surviving); i++ {
			j := i + rng.Intn(p.Reorder+1)
			if j < len(surviving) {
				surviving[i], surviving[j] = surviving[j], surviving[i]
			}
		}
	}

	// Highest media timestamp per group tells us when a group is complete
	// as the perturbed stream flows by.
	groupEndTS := map[uint32]uint32{}
	for _, m := range media {
		g := m.Timestamp
		if m.Timestamp >= groupEndTS[g] {
			groupEndTS[g] = m.Timestamp
		}
	}
	emittedRepair := map[uint32]bool{}
	var out []*rtp.Packet
	flushRepair := func(ts uint32) {
		base := ts
		if rep, ok := repairByTS[base]; ok && !emittedRepair[base] {
			out = append(out, rep)
			emittedRepair[base] = true
		}
	}
	for _, pkt := range surviving {
		out = append(out, pkt)
		if pkt.Timestamp == groupEndTS[pkt.Timestamp] {
			flushRepair(pkt.Timestamp)
		}
	}
	// Any repair whose whole group was lost still arrives eventually.
	for _, rep := range repair {
		if !emittedRepair[rep.Timestamp] {
			out = append(out, rep)
		}
	}
	return out
}

func xorBytes(a, b []byte) []byte {
	if len(a) < len(b) {
		grown := make([]byte, len(b))
		copy(grown, a)
		a = grown
	}
	for i := range b {
		a[i] ^= b[i]
	}
	return a
}
