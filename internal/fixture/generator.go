// Package fixture produces deterministic RTP packet sequences for reproducible
// end-to-end diagnostics. All data is generated in code; no external files.
package fixture

import (
	"math/rand"
)

const (
	// SSRC is the single simulated synchronization source.
	SSRC uint32 = 0x42424242
	// PayloadType identifies the generic diagnostic payload.
	PayloadType uint8 = 96
	// PacketsPerFrame is the number of RTP packets generated per frame; the
	// last packet of each frame carries the marker bit.
	PacketsPerFrame = 3
)

// Kind tells the transport layer how to treat an item.
type Kind int

const (
	// KindRTP is a normal RTP packet.
	KindRTP Kind = iota
	// KindMalformed is a byte stream that must fail RTP parsing.
	KindMalformed
)

// Item is one generated datagram plus how the harness should deliver it.
type Item struct {
	Kind Kind
	// Raw is the wire bytes. For KindRTP it is a valid RTP packet.
	Raw []byte
	// Seq is the intended 16-bit RTP sequence number (zero for malformed).
	Seq uint16
	// TS is the RTP timestamp of the packet.
	TS uint32
}

// Plan describes the faults injected into a generated stream.
type Plan struct {
	// Frames is the number of complete frames to generate.
	Frames int
	// WrapAt forces sequence numbers to start near the uint16 boundary so a
	// wrap happens during the stream.
	WrapAt uint16
	// MissingSeq is dropped from the output to create a gap.
	MissingSeq uint16
	// DuplicateSeq is emitted twice.
	DuplicateSeq uint16
	// SwapAdjacent reorders the two packets of the given sequence pair.
	SwapAdjacent [2]uint16
	// InjectMalformed appends a malformed datagram at the end of the stream.
	InjectMalformed bool
}

// DefaultPlan is the reproducible scenario used by the CLI demo: a sequence
// wrap, one gap, one duplicate, one reorder and one malformed packet.
func DefaultPlan(frames int) Plan {
	start := uint16(0xffff - 4) // wrap within the first frames
	return Plan{
		Frames:          frames,
		WrapAt:          start,
		MissingSeq:      start + 7,
		DuplicateSeq:    start + 3,
		SwapAdjacent:    [2]uint16{start + 9, start + 10},
		InjectMalformed: true,
	}
}

type spec struct {
	seq    uint16
	ts     uint32
	marker bool
}

// Generate builds the deterministic item list for a seed and plan. The same
// inputs always yield identical bytes, so runs are comparable.
func Generate(seed int64, plan Plan) []Item {
	rng := rand.New(rand.NewSource(seed))
	total := plan.Frames * PacketsPerFrame

	// Build the ideal ordered packet set first, marker on each frame's end.
	ideal := make([]spec, 0, total)
	seq := plan.WrapAt
	ts := uint32(1000)
	for f := 0; f < plan.Frames; f++ {
		for p := 0; p < PacketsPerFrame; p++ {
			ideal = append(ideal, spec{
				seq:    seq,
				ts:     ts,
				marker: p == PacketsPerFrame-1,
			})
			seq++
		}
		ts += 3600 // deterministic timestamp stride per frame
	}

	// Apply the adjacent swap before dropping the missing packet.
	ordered := make([]spec, len(ideal))
	copy(ordered, ideal)
	for i := range ordered {
		if ordered[i].seq == plan.SwapAdjacent[0] {
			for j := range ordered {
				if ordered[j].seq == plan.SwapAdjacent[1] {
					ordered[i], ordered[j] = ordered[j], ordered[i]
				}
			}
		}
	}

	items := make([]Item, 0, total+2)
	for _, s := range ordered {
		if s.seq == plan.MissingSeq {
			continue // simulate one lost packet
		}
		items = append(items, Item{
			Kind: KindRTP,
			Raw:  buildRTP(rng, s.seq, s.ts, s.marker),
			Seq:  s.seq,
			TS:   s.ts,
		})
		if s.seq == plan.DuplicateSeq {
			items = append(items, items[len(items)-1]) // exact retransmission
		}
	}

	if plan.InjectMalformed {
		// Truncated fixed header: the parser must count an error and skip it.
		items = append(items, Item{
			Kind: KindMalformed,
			Raw:  []byte{0x80, 0x60, 0x00},
		})
	}
	return items
}

func buildRTP(rng *rand.Rand, seq uint16, ts uint32, marker bool) []byte {
	buf := make([]byte, 12+8)
	buf[0] = 0x80 // V=2, no padding/extension/CSRC
	if marker {
		buf[1] = 0x80 | PayloadType
	} else {
		buf[1] = PayloadType
	}
	buf[2], buf[3] = byte(seq>>8), byte(seq)
	buf[4], buf[5], buf[6], buf[7] = byte(ts>>24), byte(ts>>16), byte(ts>>8), byte(ts)
	s := SSRC
	buf[8], buf[9], buf[10], buf[11] =
		byte(s>>24), byte(s>>16), byte(s>>8), byte(s)
	// Deterministic payload derived from the per-run seeded RNG.
	for i := 12; i < len(buf); i++ {
		buf[i] = byte(rng.Intn(256))
	}
	return buf
}
