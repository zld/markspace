// Package testdata contains deterministic, code-generated RTSP/RTP
// scenarios shared by the UDP and TCP-interleaved transports. Nothing
// here is generated at runtime from randomness or scheduling, so both
// transports and every test run observe the exact same packet stream.
package testdata

import (
	"fmt"

	"github.com/pion/rtp"
)

// Scenario names accepted by the command line.
const (
	// ScenarioGenerated is the end-to-end acceptance scenario:
	// reordered arrival, one 65535->0 wraparound, one cross-frame gap
	// and one incomplete frame, plus two complete frames.
	ScenarioGenerated = "generated"
	// ScenarioWraparound is the independent small sample: one SSRC,
	// seq 65534,65535,0,1 on a single timestamp with the marker only
	// on seq 1, reassembling into one complete frame with no gap.
	ScenarioWraparound = "wraparound"
)

// FixedSSRC is the single SSRC used by generated scenarios.
const FixedSSRC uint32 = 0xC0FFEE

// PayloadType is the dynamic payload type used in the generated stream.
const PayloadType uint8 = 96

// Scenario is a deterministic RTP packet stream plus its expectations.
type Scenario struct {
	Name    string
	SSRC    uint32
	Packets []*rtp.Packet
	// ExpectedTailSeq is the sequence number the stream was generated
	// to end with. It differs from the last sent packet when the final
	// packet(s) are intentionally lost; the runner reports it to the
	// sequence checker so end-of-stream tail losses stay deterministic.
	ExpectedTailSeq uint16
	// Expected counts used by the report validator.
	WantComplete    int
	WantIncomplete  int
	WantGaps        int
	WantTransitions int
}

// New resolves a scenario name into a generated scenario.
// An unknown name returns an error so the CLI can exit with code 2.
func New(name string) (*Scenario, error) {
	switch name {
	case ScenarioGenerated:
		return generated(), nil
	case ScenarioWraparound:
		return wraparound(), nil
	default:
		return nil, fmt.Errorf("unknown scenario %q (valid: %s, %s)",
			name, ScenarioGenerated, ScenarioWraparound)
	}
}

func packet(ssrc uint32, seq uint16, ts uint32, marker bool, payload byte) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    PayloadType,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           ssrc,
			Marker:         marker,
		},
		Payload: []byte{payload},
	}
}

// generated builds the acceptance scenario.
//
// Exactly three frames are produced on a single SSRC:
//
//   - Frame A (ts 1000, seq 65534..1) is complete and crosses the
//     65535 -> 0 wraparound, delivered in order so both transports
//     establish a stable sequence baseline.
//   - Frame B (ts 2000, seq 2..4) is complete but arrives out of
//     order (seq 3 before seq 2); the late fill reassembles the whole
//     frame without any sequence gap. Placing the reorder after a
//     stable baseline keeps it observable over UDP as well as TCP.
//   - Frame C (ts 3000) is the last, incomplete frame: seq 5 and
//     seq 7 arrive (seq 6 is the single lost packet) but the frame
//     never gets a marker. The 5 -> 7 jump exposes the gap to the
//     sequence checker while the frame stays incomplete.
//
// Totals: 2 complete frames, 1 incomplete frame, 1 sequence gap.
func generated() *Scenario {
	s := &Scenario{
		Name:            ScenarioGenerated,
		SSRC:            FixedSSRC,
		WantComplete:    2,
		WantIncomplete:  1,
		WantGaps:        1,
		WantTransitions: 6,
	}
	ssrc := FixedSSRC

	// Frame A (ts 1000): seq 65534,65535,0,1 in order across the wrap;
	// marker on seq 1.
	s.Packets = append(s.Packets,
		packet(ssrc, 65534, 1000, false, 0xA0),
		packet(ssrc, 65535, 1000, false, 0xA1),
		packet(ssrc, 0, 1000, false, 0xA2),
		packet(ssrc, 1, 1000, true, 0xA3),
	)

	// Frame B (ts 2000): seq 2,3,4 with 3 delivered before 2 (out of
	// order); seq 4 carries the marker, the frame still completes.
	s.Packets = append(s.Packets,
		packet(ssrc, 3, 2000, false, 0xB1),
		packet(ssrc, 2, 2000, false, 0xB0),
		packet(ssrc, 4, 2000, true, 0xB2),
	)

	// Frame C (ts 3000): only seq 5 is ever sent; seq 6 is the single
	// lost packet and is the stream's true tail sequence. The frame
	// never gets a marker and is incomplete with exactly one gap.
	s.Packets = append(s.Packets,
		packet(ssrc, 5, 3000, false, 0xC0),
	)
	s.ExpectedTailSeq = 6

	return s
}

// wraparound builds the independent small sample.
func wraparound() *Scenario {
	s := &Scenario{
		Name:            ScenarioWraparound,
		SSRC:            FixedSSRC,
		WantComplete:    1,
		WantIncomplete:  0,
		WantGaps:        0,
		WantTransitions: 6,
	}
	ts := uint32(500)
	for _, p := range []struct {
		seq    uint16
		marker bool
		payload byte
	}{
		{65534, false, 0x0A},
		{65535, false, 0x0B},
		{0, false, 0x0C},
		{1, true, 0x0D},
	} {
		s.Packets = append(s.Packets, packet(FixedSSRC, p.seq, ts, p.marker, p.payload))
	}
	s.ExpectedTailSeq = 1
	return s
}
