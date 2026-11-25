package clock

import "testing"

func TestSeqAndTSDeltaWraparound(t *testing.T) {
	if d := SeqDelta(0xFFFF, 0x0001); d != 2 {
		t.Fatalf("seq wrap forward: got %d", d)
	}
	if d := SeqDelta(0x0001, 0xFFFF); d != -2 {
		t.Fatalf("seq wrap backward: got %d", d)
	}
	if d := TS32Delta(0xFFFFFFF0, 0x00000010); d != 32 {
		t.Fatalf("timestamp wrap forward: got %d", d)
	}
	if d := TS32Delta(10, 10); d != 0 {
		t.Fatalf("identical timestamps must have zero delta, got %d", d)
	}
}

func TestObserveSeqAbsoluteIndex(t *testing.T) {
	ts := NewTimestamp(48000, 1)
	first := ts.ObserveSeq(0xFFFE)
	if first != 0xFFFE {
		t.Fatalf("first absolute index = %d", first)
	}
	if got := ts.ObserveSeq(0xFFFF); got != 0xFFFF {
		t.Fatalf("second absolute index = %d", got)
	}
	if got := ts.ObserveSeq(0x0000); got != 0x10000 {
		t.Fatalf("post-wrap absolute index = %d", got)
	}
	if got := ts.ObserveSeq(0x0001); got != 0x10001 {
		t.Fatalf("post-wrap second index = %d", got)
	}
	// Duplicate sequence numbers resolve to the same absolute index.
	if got := ts.ObserveSeq(0x0001); got != 0x10001 {
		t.Fatalf("duplicate must keep index, got %d", got)
	}
}

func TestObserveTSFrameWraparound(t *testing.T) {
	ts := NewTimestamp(48000, 1)
	ts.ObserveTS(0xFFFFFFF0)
	ts.ObserveTS(0x00000000) // wraps: +16 units
	if got := ts.AbsTimestamp(); got != 0x100000000 {
		t.Fatalf("absolute timestamp after wrap = %d", got)
	}
	if got := ts.AbsFrame(); got != 0x100000000 {
		t.Fatalf("absolute frame after wrap = %d", got)
	}
	// A retransmitted/duplicate timestamp must not move the cursor.
	ts.ObserveTS(0x00000000)
	if got := ts.AbsFrame(); got != 0x100000000 {
		t.Fatalf("duplicate timestamp moved frame cursor to %d", got)
	}
}

func TestAdvanceAndFrameRange(t *testing.T) {
	ts := NewTimestamp(8000, 1)
	if got := ts.Advance(100); got != 100 {
		t.Fatalf("advance = %d", got)
	}
	if got := ts.Advance(24); got != 124 {
		t.Fatalf("second advance = %d", got)
	}
	r := FrameRange{Start: 10, End: 34}
	if r.Len() != 24 {
		t.Fatalf("range length = %d", r.Len())
	}
}
