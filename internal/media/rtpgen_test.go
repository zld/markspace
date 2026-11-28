package media

import (
	"bytes"
	"testing"

	"github.com/pion/rtp"
)

func TestRTPRoundTripThroughPion(t *testing.T) {
	stream, err := BuildRTP(RTPSpec{
		Asset: "a", Track: 7, ClockRate: 90_000, StartRTP: 1_000,
		StartSeq: 65_530, Packets: 40, Payload: 32, TickEvery: 2,
		ReorderAhead: 5, ReorderDelay: 2, ReorderableGaps: 3, Duplicates: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := stream.DecodePackets()
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != len(stream.Packets) {
		t.Fatalf("decoded %d of %d", len(decoded), len(stream.Packets))
	}
	// Re-marshalling the decoded packet must reproduce the original bytes.
	for i, got := range decoded {
		if !bytes.Equal(got.Raw, stream.Packets[i].Raw) {
			t.Fatalf("packet %d wire bytes changed", i)
		}
		var pkt rtp.Packet
		if err := pkt.Unmarshal(stream.Packets[i].Raw); err != nil {
			t.Fatalf("pion cannot parse fixture %d: %v", i, err)
		}
		if pkt.SequenceNumber != stream.Packets[i].Sequence {
			t.Fatalf("sequence mismatch at %d", i)
		}
		if pkt.SSRC != 7 {
			t.Fatalf("ssrc = %d", pkt.SSRC)
		}
	}
}

func TestRTPFixtureCounts(t *testing.T) {
	stream, err := BuildRTP(RTPSpec{
		Asset: "a", Track: 1, ClockRate: 90_000, StartRTP: 0,
		StartSeq: 0, Packets: 200, Payload: 16, TickEvery: 1,
		ReorderAhead: 10, ReorderDelay: 2, ReorderableGaps: 5,
		UnrecoverableGaps: 1, UnrecoverableRun: 20, Duplicates: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stream.Missing) != 20 {
		t.Fatalf("missing = %d, want the unrecoverable run", len(stream.Missing))
	}
	if stream.Duplicates != 4 {
		t.Fatalf("duplicates = %d", stream.Duplicates)
	}
	if stream.ExpectedLossRecoverable <= 0 {
		t.Fatal("expected recoverable loss fixtures")
	}
	if stream.ExpectedLossTotal != stream.ExpectedLossRecoverable+len(stream.Missing) {
		t.Fatalf("loss accounting = %d recoverable %d missing", stream.ExpectedLossRecoverable, len(stream.Missing))
	}
	var late, reordered int
	for _, p := range stream.Packets {
		switch p.Source {
		case 1:
			reordered++
		case 3:
			late++
		}
	}
	if reordered == 0 || late == 0 {
		t.Fatalf("reordered=%d late=%d", reordered, late)
	}
}

func TestRTPDeterministic(t *testing.T) {
	spec := RTPSpec{Asset: "a", Track: 1, ClockRate: 48_000, StartRTP: 5,
		StartSeq: 9, Packets: 100, Payload: 24, TickEvery: 3,
		ReorderAhead: 7, ReorderDelay: 2, ReorderableGaps: 4, Duplicates: 1}
	a, err := BuildRTP(spec)
	if err != nil {
		t.Fatal(err)
	}
	b, err := BuildRTP(spec)
	if err != nil {
		t.Fatal(err)
	}
	for i := range a.Packets {
		if !bytes.Equal(a.Packets[i].Raw, b.Packets[i].Raw) {
			t.Fatalf("fixture differs at packet %d", i)
		}
	}
}

func TestUnmarshalRejectsGarbage(t *testing.T) {
	if _, err := UnmarshalRTP([]byte{0xde, 0xad}); err == nil {
		t.Fatal("expected parse failure for a truncated packet")
	}
}
