package testdata

import "testing"

func TestGeneratedScenarioStructure(t *testing.T) {
	s, err := New(ScenarioGenerated)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Packets) != 10 {
		t.Fatalf("packet count = %d, want 10", len(s.Packets))
	}

	// Deterministic sequence/timestamp layout shared by both transports.
	type pkt struct {
		seq uint16
		ts  uint32
		m   bool
	}
	want := []pkt{
		{65534, 1000, false},
		{65532, 1000, false}, // reordered
		{65533, 1000, false},
		{65535, 1000, true}, // frame A marker
		{0, 2000, false},    // 65535 -> 0 wraparound
		{1, 2000, false},
		{2, 2000, true}, // frame B marker
		{3, 3000, false}, // frame C: seq 4 dropped, no marker
		{5, 4000, false}, // timestamp switch flushes C incomplete
		{6, 4000, true}, // frame D marker
	}
	for i, p := range s.Packets {
		if p.SSRC != FixedSSRC {
			t.Fatalf("packet %d: SSRC %x, want %x", i, p.SSRC, FixedSSRC)
		}
		if p.SequenceNumber != want[i].seq ||
			p.Timestamp != want[i].ts ||
			p.Marker != want[i].m {
			t.Fatalf("packet %d = seq %d ts %d marker %v, want %+v",
				i, p.SequenceNumber, p.Timestamp, p.Marker, want[i])
		}
	}
	if s.WantComplete != 2 || s.WantIncomplete != 1 ||
		s.WantGaps != 1 || s.WantTransitions != 6 {
		t.Fatalf("scenario expectations wrong: %+v", s)
	}
}

func TestWraparoundScenario(t *testing.T) {
	s, err := New(ScenarioWraparound)
	if err != nil {
		t.Fatal(err)
	}
	want := []uint16{65534, 65535, 0, 1}
	if len(s.Packets) != len(want) {
		t.Fatalf("packet count = %d, want 4", len(s.Packets))
	}
	for i, p := range s.Packets {
		if p.SequenceNumber != want[i] || p.Timestamp != 500 {
			t.Fatalf("packet %d = seq %d ts %d, want %d/500", i, p.SequenceNumber, p.Timestamp, want[i])
		}
		if p.Marker != (i == 3) {
			t.Fatalf("packet %d marker = %v", i, p.Marker)
		}
	}
	if s.WantComplete != 1 || s.WantIncomplete != 0 || s.WantGaps != 0 {
		t.Fatalf("wraparound expectations wrong: %+v", s)
	}
}

func TestUnknownScenario(t *testing.T) {
	if _, err := New("nope"); err == nil {
		t.Fatal("expected error for unknown scenario")
	}
}
