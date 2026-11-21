package demo

import (
	"testing"
)

func TestStandardFaultCatalogue(t *testing.T) {
	m := Generate(12, 41)

	// 12 frames * 3 packets, minus one lost fragment, plus one duplicate.
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

func TestSequenceWrapAround(t *testing.T) {
	m := Generate(12, 41)
	// First packet starts at 65530 and sequence numbers increment per
	// source packet; the stream must cross the 16-bit boundary.
	if m.Packets[0].SequenceNumber != StartSeq {
		t.Fatalf("first seq = %d, want %d", m.Packets[0].SequenceNumber, StartSeq)
	}
	seqs := make([]uint16, 0, len(m.Packets))
	for _, p := range m.Packets {
		seqs = append(seqs, p.SequenceNumber)
	}
	foundZero := false
	for _, s := range seqs {
		if s == 0 {
			foundZero = true
		}
	}
	if !foundZero {
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
