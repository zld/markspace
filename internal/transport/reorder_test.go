package transport

import (
	"bytes"
	"testing"

	"archivecheck/internal/model"
)

func mkPacket(seq uint16, ts uint32, payload string) Packet {
	return Packet{Sequence: seq, Timestamp: ts, Payload: []byte(payload)}
}

func TestReorderOutOfOrderWithinWindow(t *testing.T) {
	r := NewReorder(Source{Segment: "s1"}, Options{})
	// Arrival order 3,1,2,4 with a window wide enough to hold them.
	arrivals := []Packet{
		mkPacket(102, 3000, "c"),
		mkPacket(100, 1000, "a"),
		mkPacket(101, 2000, "b"),
		mkPacket(103, 4000, "d"),
	}
	for _, p := range arrivals {
		if err := r.Push(p); err != nil {
			t.Fatal(err)
		}
	}
	res := r.Close()
	if res.Missing != 0 || len(res.Gaps) != 0 {
		t.Fatalf("unexpected gaps %+v", res.Gaps)
	}
	if res.Received != 4 {
		t.Fatalf("received=%d", res.Received)
	}
	for i, p := range res.Packets {
		if p.Sequence != uint16(100+i) {
			t.Fatalf("packet %d seq=%d", i, p.Sequence)
		}
	}
	if res.Reordered == 0 {
		t.Fatal("expected reordering to be counted")
	}
}

func TestReorderDuplicatesCountedNotWritten(t *testing.T) {
	r := NewReorder(Source{Segment: "s1"}, Options{})
	for _, p := range []Packet{
		mkPacket(10, 100, "a"),
		mkPacket(11, 200, "b"),
		mkPacket(10, 100, "a"),
		mkPacket(10, 100, "a"),
	} {
		if err := r.Push(p); err != nil {
			t.Fatal(err)
		}
	}
	res := r.Close()
	if res.Received != 2 {
		t.Fatalf("received=%d want 2", res.Received)
	}
	if res.DuplicateCount != 2 || len(res.Duplicates) != 1 {
		t.Fatalf("dups=%+v", res.Duplicates)
	}
	if !res.Duplicates[0].Identical || res.Duplicates[0].Count != 2 {
		t.Fatalf("dup %+v", res.Duplicates[0])
	}
	if _, ok := r.seen[10]; !ok {
		t.Fatal("duplicate should be recorded once")
	}
	if len(r.out) != 2 {
		t.Fatalf("out len=%d", len(r.out))
	}
}

func TestReorderBurstLossProducesGap(t *testing.T) {
	r := NewReorder(Source{Segment: "s1"}, Options{Window: 4})
	for _, seq := range []uint16{100, 101, 102, 107, 108} {
		if err := r.Push(mkPacket(seq, uint32(seq)*100, "x")); err != nil {
			t.Fatal(err)
		}
	}
	res := r.Close()
	if res.Missing != 4 {
		t.Fatalf("missing=%d want 4", res.Missing)
	}
	if len(res.Gaps) != 1 {
		t.Fatalf("gaps=%+v", res.Gaps)
	}
	g := res.Gaps[0]
	if g.Before != 102 || g.After != 107 || g.Count != 4 {
		t.Fatalf("gap %+v", g)
	}
	if g.FirstTimestamp != 10200 || g.LastTimestamp != 10700 {
		t.Fatalf("timestamps %+v", g)
	}
	for _, p := range res.Packets {
		if p.Sequence > 102 && p.Sequence < 107 {
			t.Fatalf("missing packet %d was fabricated", p.Sequence)
		}
	}
}

func TestReorderWaitsForWindowBeforeDeclaringGap(t *testing.T) {
	r := NewReorder(Source{Segment: "s1"}, Options{Window: 8})
	// 5 packets arrive out of order; the hole at 2 may still be filled.
	for _, seq := range []uint16{0, 1, 3, 4, 5, 6, 7, 8, 9} {
		if err := r.Push(mkPacket(seq, uint32(seq), "x")); err != nil {
			t.Fatal(err)
		}
	}
	if res := r.Result(); len(res.Gaps) != 0 {
		t.Fatalf("gap declared too early: %+v", res.Gaps)
	}
	// Late but inside the window: accepted in order, no gap.
	if err := r.Push(mkPacket(2, 2, "x")); err != nil {
		t.Fatal(err)
	}
	res := r.Close()
	if len(res.Gaps) != 0 || res.Missing != 0 {
		t.Fatalf("late arrival should close the hole: %+v", res.Gaps)
	}
	for i, p := range res.Packets {
		if p.Sequence != uint16(i) {
			t.Fatalf("packet %d=%d", i, p.Sequence)
		}
	}
}

func TestReorderSequenceWrap(t *testing.T) {
	r := NewReorder(Source{Segment: "s1"}, Options{})
	seqs := []uint16{65534, 65535, 0, 1, 2}
	for _, s := range seqs {
		if err := r.Push(mkPacket(s, uint32(s), "x")); err != nil {
			t.Fatal(err)
		}
	}
	res := r.Close()
	if res.Missing != 0 {
		t.Fatalf("wrap should not create a gap, got %+v", res.Gaps)
	}
	want := []uint64{65534, 65535, 65536, 65537, 65538}
	for i, p := range res.Packets {
		if p.Extended != want[i] {
			t.Fatalf("packet %d extended=%d want %d", i, p.Extended, want[i])
		}
	}
}

func TestReorderWrapWithGap(t *testing.T) {
	r := NewReorder(Source{Segment: "s1"}, Options{Window: 2})
	for _, s := range []uint16{65533, 65534, 1, 2} {
		if err := r.Push(mkPacket(s, uint32(s), "x")); err != nil {
			t.Fatal(err)
		}
	}
	res := r.Close()
	if res.Missing != 2 {
		t.Fatalf("missing=%d gaps=%+v", res.Missing, res.Gaps)
	}
	if g := res.Gaps[0]; g.Before != 65534 || g.After != 65537 {
		t.Fatalf("gap %+v", g)
	}
}

func TestReorderConflictingDuplicateIsFault(t *testing.T) {
	r := NewReorder(Source{Segment: "s7", File: "v/7.m4s"}, Options{})
	if err := r.Push(mkPacket(5, 500, "good")); err != nil {
		t.Fatal(err)
	}
	if err := r.Push(mkPacket(5, 500, "evil")); err != nil {
		t.Fatal(err)
	}
	res := r.Close()
	if len(res.Duplicates) != 1 || res.Duplicates[0].Identical {
		t.Fatalf("dup %+v", res.Duplicates)
	}
	if len(res.Duplicates[0].ConflictingDigests) != 2 {
		t.Fatalf("digests %+v", res.Duplicates[0].ConflictingDigests)
	}
	faults := r.Faults()
	if len(faults) != 1 || faults[0].Code != model.CodeTransportDuplicate {
		t.Fatalf("faults %+v", faults)
	}
	if faults[0].Source.Segment != "s7" {
		t.Fatalf("source %+v", faults[0].Source)
	}
}

func TestReorderBoundsBuffer(t *testing.T) {
	r := NewReorder(Source{Segment: "s1"}, Options{MaxPackets: 4})
	var err error
	for i := 0; i < 20; i++ {
		err = r.Push(mkPacket(uint16(i), uint32(i), "x"))
		if err != nil {
			break
		}
	}
	if err == nil {
		t.Fatal("expected the buffer bound to stop the stream")
	}
	f, ok := model.AsFault(err)
	if !ok || f.Code != model.CodeTransportSequence {
		t.Fatalf("err=%v", err)
	}
}

func TestReorderEmptyClose(t *testing.T) {
	r := NewReorder(Source{Segment: "s1"}, Options{})
	res := r.Close()
	if res.Received != 0 || len(res.Gaps) != 0 || len(res.Packets) != 0 {
		t.Fatalf("empty=%+v", res)
	}
}

func TestPayloadDigestStable(t *testing.T) {
	a := PayloadDigest([]byte("payload"))
	b := PayloadDigest([]byte("payload"))
	c := PayloadDigest([]byte("payloaD"))
	if a != b {
		t.Fatal("digest must be deterministic")
	}
	if a == c {
		t.Fatal("digest should distinguish payloads")
	}
	if len(a) != 16 || !bytes.Equal([]byte(a), bytes.ToLower([]byte(a))) {
		t.Fatalf("digest %q", a)
	}
}
