package fixture

import (
	"testing"

	"github.com/example/tsrepair/internal/ts"
)

func TestGenerateDeterministicAndAligned(t *testing.T) {
	g1, g2 := NewGenerator(7), NewGenerator(7)
	s1 := g1.Generate(40, nil)
	s2 := g2.Generate(40, nil)
	if string(s1.Data) != string(s2.Data) {
		t.Fatal("same seed must produce identical bytes")
	}
	if len(s1.CompleteData)%ts.PacketSize != 0 {
		t.Fatal("complete data must be whole 188-byte packets")
	}
	if s1.TruncatedBytes != 0 || len(s1.KeyframeOffsets) == 0 {
		t.Fatalf("clean stream invariants wrong: %+v", s1)
	}
	for _, off := range s1.KeyframeOffsets {
		if s1.CompleteData[off] != ts.SyncByte {
			t.Fatalf("keyframe offset %d not on a sync byte", off)
		}
	}
}

func TestAllFaultsInjected(t *testing.T) {
	s := NewGenerator(7).Generate(60, AllFaults)
	if s.TruncatedBytes == 0 {
		t.Fatal("truncated-tail fault must leave dangling bytes")
	}
	if len(s.Data)-s.TruncatedBytes != len(s.CompleteData) {
		t.Fatal("truncation accounting inconsistent")
	}
	// Complete data must still split into whole packets.
	if len(s.CompleteData)%ts.PacketSize != 0 {
		t.Fatal("complete portion must stay packet aligned")
	}
	// Every packet of the complete portion must parse.
	count := 0
	_, err := ts.SplitPackets(s.CompleteData, func(*ts.Packet) error {
		count++
		return nil
	})
	if err != nil {
		t.Fatalf("complete stream must parse: %v", err)
	}
	if count != s.TotalPackets {
		t.Fatalf("parsed %d packets, ground truth %d", count, s.TotalPackets)
	}
}

func TestUnknownLayoutMarkerPresent(t *testing.T) {
	s := NewGenerator(7).Generate(20, []string{FaultAudioLayout})
	var found bool
	_, _ = ts.SplitPackets(s.CompleteData, func(p *ts.Packet) error {
		for _, b := range p.Payload {
			if b == 0x7F {
				found = true
			}
		}
		return nil
	})
	if !found {
		t.Fatal("audio-layout fault marker not found in any payload")
	}
}

func TestFaultStreamsStayDeterministic(t *testing.T) {
	a := NewGenerator(11).Generate(50, AllFaults)
	b := NewGenerator(11).Generate(50, AllFaults)
	if string(a.Data) != string(b.Data) {
		t.Fatal("fault streams must rerun byte-identically")
	}
}
