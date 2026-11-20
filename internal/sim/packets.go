// Package sim generates the deterministic local RTSP publisher and its RTP
// fixtures. Every packet, sequence number and permutation is derived from a
// seed so repeated runs produce identical statistics.
package sim

import (
	"math/rand"

	"github.com/pion/rtp"
)

const (
	// PayloadType is the dynamic H.264 payload type advertised in the SDP.
	PayloadType uint8 = 96
	// SSRC is the single synchronisation source used by the fixture stream.
	SSRC uint32 = 0x494e4753
	// clockRate is the H.264 RTP clock rate (90 kHz).
	clockRate uint32 = 90000
	// firstSeq is where the fixture stream starts, deliberately just below
	// the 16-bit wrap point.
	firstSeq uint16 = 65534
)

// Fixture carries the generated RTP packets and the metadata the publisher
// needs to announce them.
type Fixture struct {
	// Packets is the transmission order, already including wraparound,
	// reordering, the dropped sequence number and the duplicate.
	Packets []*rtp.Packet
	// MissingSeq is the single sequence number that is never sent.
	MissingSeq uint16
	// DuplicateSeq is the sequence number that is sent twice.
	DuplicateSeq uint16
	// CompleteFrames is how many valid access units the stream contains.
	CompleteFrames int
	// DamagedFrames is how many access units are destroyed by a lost packet.
	DamagedFrames int
}

// BuildFixture constructs the deterministic packet stream.
//
// Layout, in logical sequence order:
//
//  1. a complete single-NAL frame (one packet),
//  2. a complete FU-A frame (four fragments),
//  3. a FU-A frame whose middle fragment is dropped, so the access unit is
//     damaged and must be discarded,
//  4. a duplicate of the frame-2 start packet.
//
// Sequence numbers start at 65534, so the stream crosses the 65535->0
// wraparound. Two adjacent packets are additionally swapped to create an
// out-of-order delivery that the reorder buffer must repair.
func BuildFixture(seed int64) *Fixture {
	rng := rand.New(rand.NewSource(seed))

	// Distinct, deterministic NAL payloads.
	singleNAL := append([]byte{0x65}, fill(rng, 40)...) // type 5, single packet
	fuBody := append([]byte{0x65}, fill(rng, 2400)...)
	damagedBody := append([]byte{0x65}, fill(rng, 1600)...)

	type spec struct {
		ts       uint32
		payloads [][]byte
		marker   []bool
	}

	// Fragmentation helper: split a NAL unit into FU-A packets of at most
	// fragmentSize payload bytes.
	fragment := func(nal []byte, ts uint32, fragmentSize int) spec {
		indicator := byte(0x7C) // NRI=3, type=28 (FU-A)
		nalType := nal[0] & 0x1F
		var payloads [][]byte
		var markers []bool
		off := 1
		first := true
		for off < len(nal) {
			end := off + fragmentSize
			if end > len(nal) {
				end = len(nal)
			}
			fuHeader := nalType
			if first {
				fuHeader |= 0x80
			}
			if end == len(nal) {
				fuHeader |= 0x40
			}
			payloads = append(payloads, append([]byte{indicator, fuHeader}, nal[off:end]...))
			markers = append(markers, end == len(nal))
			first = false
			off = end
		}
		return spec{ts: ts, payloads: payloads, marker: markers}
	}

	frameSingle := spec{
		ts:       clockRate,
		payloads: [][]byte{singleNAL},
		marker:   []bool{true},
	}
	frameFU := fragment(fuBody, 2*clockRate, 800)
	frameDamaged := fragment(damagedBody, 3*clockRate, 700)

	// Assign sequence numbers contiguously from firstSeq across every packet.
	var ordered []*rtp.Packet
	seq := firstSeq
	assign := func(s spec) {
		for i, payload := range s.payloads {
			ordered = append(ordered, &rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					PayloadType:    PayloadType,
					SSRC:           SSRC,
					SequenceNumber: seq,
					Timestamp:      s.ts,
					Marker:         s.marker[i],
				},
				Payload: payload,
			})
			seq++
		}
	}
	assign(frameSingle)  // 1 packet
	assign(frameFU)      // 4 packets
	assign(frameDamaged) // 3 packets

	// The middle fragment of the damaged frame (second of its three) is the
	// single sequence number the publisher never sends.
	damagedStart := firstSeq + uint16(1+len(frameFU.payloads))
	missing := damagedStart + 1

	// Apply transmission-order permutations on a copy.
	out := make([]*rtp.Packet, 0, len(ordered)+1)
	out = append(out, ordered...)

	// drop the unique missing sequence number
	filtered := out[:0]
	for _, pkt := range out {
		if pkt.SequenceNumber != missing {
			filtered = append(filtered, pkt)
		}
	}
	out = filtered

	// swap two adjacent in-stream packets to force out-of-order delivery.
	// Packets inside the stream are used (not the very first one) so both
	// stay within the 64-packet reorder window and are repaired rather than
	// being classified as late.
	swap(out, 1, 2)

	// append a duplicate of the single-NAL packet, with a fresh allocation
	// carrying identical fields, so the receiver cannot rely on pointer
	// identity.
	var origin *rtp.Packet
	for _, pkt := range out {
		if pkt.SequenceNumber == firstSeq {
			origin = pkt
			break
		}
	}
	dup := &rtp.Packet{
		Header:  origin.Header,
		Payload: append([]byte(nil), origin.Payload...),
	}
	out = append(out, dup)

	return &Fixture{
		Packets:        out,
		MissingSeq:     missing,
		DuplicateSeq:   firstSeq,
		CompleteFrames: 2,
		DamagedFrames:  1,
	}
}

func fill(rng *rand.Rand, n int) []byte {
	b := make([]byte, n)
	_, _ = rng.Read(b)
	return b
}

func swap(pkts []*rtp.Packet, i, j int) {
	pkts[i], pkts[j] = pkts[j], pkts[i]
}
