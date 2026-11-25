package rtp

import (
	"testing"

	pionrtp "github.com/pion/rtp"
)

func pk(seq uint16, ts uint32) pionrtp.Packet {
	return pionrtp.Packet{Header: pionrtp.Header{SequenceNumber: seq, Timestamp: ts, SSRC: 17}, Payload: []byte{byte(seq)}}
}

func TestAnalyzeDuplicateAndReorder(t *testing.T) {
	r := Analyze([]pionrtp.Packet{pk(1, 10), pk(2, 20), pk(4, 40), pk(3, 30), pk(4, 40)}, 16, nil)
	if r.Counts.Duplicate != 1 {
		t.Fatalf("duplicates=%d", r.Counts.Duplicate)
	}
	if r.Counts.OutOfOrder != 1 || r.Counts.Lost != 0 {
		t.Fatalf("counts=%+v", r.Counts)
	}
	want := []uint64{1, 2, 3, 4}
	if len(r.Ordered) != 4 {
		t.Fatalf("ordered=%v", r.Ordered)
	}
	for i := range want {
		if r.Ordered[i] != want[i] {
			t.Fatalf("ordered=%v want %v", r.Ordered, want)
		}
	}
}

func TestAnalyzeLossAndLateOutsideWindow(t *testing.T) {
	r := Analyze([]pionrtp.Packet{pk(1, 1), pk(2, 2), pk(3, 3), pk(4, 4), pk(10, 10), pk(5, 5)}, 4, nil)
	if r.Counts.Late != 1 || len(r.LateFailures) != 1 {
		t.Fatalf("late=%d failures=%d", r.Counts.Late, len(r.LateFailures))
	}
	// Sequence 5 arrived late and is not counted lost; 6..9 never arrived.
	if r.Counts.Lost != 4 {
		t.Fatalf("lost=%d counts=%+v", r.Counts.Lost, r.Counts)
	}
	if r.LateFailures[0].ExtSequence != 5 || r.LateFailures[0].Kind != "late_outside_window" {
		t.Fatalf("bad late event: %+v", r.LateFailures[0])
	}
}

func TestSequenceNumberWraparound(t *testing.T) {
	r := Analyze([]pionrtp.Packet{pk(65534, 1), pk(65535, 2), pk(0, 3), pk(1, 4)}, 16, nil)
	if r.Counts.SequenceWraps != 1 {
		t.Fatalf("wraps=%d", r.Counts.SequenceWraps)
	}
	want := []uint64{65534, 65535, 65536, 65537}
	if len(r.Ordered) != 4 {
		t.Fatalf("ordered=%v", r.Ordered)
	}
	for i := range want {
		if r.Ordered[i] != want[i] {
			t.Fatalf("ordered=%v want %v", r.Ordered, want)
		}
	}
}

func TestPionMarshalParseRoundTrip(t *testing.T) {
	original := pk(17, 90000)
	b, err := original.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.SequenceNumber != 17 || parsed.Timestamp != 90000 {
		t.Fatalf("parsed=%+v", parsed.Header)
	}
}
