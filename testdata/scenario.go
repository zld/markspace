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
	// Expected counts used by the report validator.
	WantComplete   int
	WantIncomplete int
	WantGaps       int
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
// Frame A (ts 1000, seq 65532..65535) is delivered out of order but
// complete. Frame B (ts 2000, seq 0..2) is complete. Frame C
// (ts 3000) only ever contains seq 3: seq 4 is the single lost packet,
// and the arrival of seq 5 with a new timestamp flushes C as
// incomplete (the cross-frame gap). Frame D (ts 4000, seq 5..6) then
// reassembles complete, proving sequence checking is not reset by the
// bad frame. The stream crosses the 65535 -> 0 wraparound once.
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

	// Frame A: reordered, marker arrives last on seq 65535.
	s.Packets = append(s.Packets,
		packet(ssrc, 65534, 1000, false, 0xA2),
		packet(ssrc, 65532, 1000, false, 0xA0),
		packet(ssrc, 65533, 1000, false, 0xA1),
		packet(ssrc, 65535, 1000, true, 0xA3),
	)

	// Frame B: wraps around to seq 0, complete.
	s.Packets = append(s.Packets,
		packet(ssrc, 0, 2000, false, 0xB0),
		packet(ssrc, 1, 2000, false, 0xB1),
		packet(ssrc, 2, 2000, true, 0xB2),
	)

	// Frame C: only seq 3 ever arrives; seq 4 is dropped.
	s.Packets = append(s.Packets,
		packet(ssrc, 3, 3000, false, 0xC0),
	)

	// Frame D: seq 5 starts a new timestamp before C got a marker.
	// The assembler must flush C incomplete and keep D separate.
	s.Packets = append(s.Packets,
		packet(ssrc, 5, 4000, false, 0xD0),
		packet(ssrc, 6, 4000, true, 0xD1),
	)

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
	return s
}
