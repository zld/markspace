package demo

import (
	"testing"
)

func TestStandardFaultCatalogue(t *testing.T) {
	m := Generate(12, 41)

	// 12 frames * 3 packets, minus the one removed fragment, plus one
	// duplicate emission.
	if want := 12*3 - 1 + 1; len(m.Packets) != want {
		t.Fatalf("emitted packets = %d, want %d", len(m.Packets), want)
	}
	if m.LostPackets != 1 || m.DuplicateSets != 1 || m.ReorderSets != 1 {
		t.Fatalf("fault sets wrong: %+v", m)
	}
	if m.ExpectedGaps != 1 || m.ExpectedDuplicates != 1 ||
		m.ExpectedFramesReassembled != 11 || m.ExpectedDropped != 1 {
		t.Fatalf("expected stats wrong: %+v", m)
	}
}

func TestTerminalGapAndWrapAround(t *testing.T) {
	m := Generate(12, 41)
	// First packet starts at 65530; the stream crosses the 16-bit boundary.
	if m.Packets[0].SequenceNumber != StartSeq {
		t.Fatalf("first seq = %d, want %d", m.Packets[0].SequenceNumber, StartSeq)
	}
	// Build the unique emitted sequence numbers and confirm exactly one
	// expected number is absent (the removed terminal fragment).
	emitted := map[uint16]struct{}{}
	for _, p := range m.Packets {
		emitted[p.SequenceNumber] = struct{}{}
	}
	missing := 0
	for i := 0; i < 12*3; i++ {
		seq := uint16(int(StartSeq) + i)
		if _, ok := emitted[seq]; !ok {
			missing++
		}
	}
	if missing != 1 {
		t.Fatalf("want exactly one missing sequence number, got %d", missing)
	}
	// The stream still crosses seq 0 via wrap-around.
	if _, ok := emitted[0]; !ok {
		t.Fatal("stream must wrap past seq 0")
	}
}

func TestDeterministicAcrossRuns(t *testing.T) {
	a := Generate(12, 41)
	b := Generate(12, 41)
	if len(a.Packets) != len(b.Packets) {
		t.Fatal("same seed must yield same length")
	}
	for i := range a.Packets {
		pa, pb := a.Packets[i], b.Packets[i]
		if pa.SequenceNumber != pb.SequenceNumber ||
			pa.Timestamp != pb.Timestamp ||
			string(pa.Payload) != string(pb.Payload) ||
			pa.Kind != pb.Kind {
			t.Fatalf("packet %d differs across runs", i)
		}
	}
}

func TestMarkerPerFrame(t *testing.T) {
	m := Generate(12, 41)
	markers := 0
	for _, p := range m.Packets {
		if p.Marker {
			markers++
		}
	}
	if markers != 12 {
		t.Fatalf("want one marker per source frame, got %d", markers)
	}
}
