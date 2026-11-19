package jitter

import (
	"testing"

	"jitterlab/internal/protocol"
)

func pkt(seq uint32) protocol.Packet {
	return protocol.Packet{StreamID: 1, Seq: seq, SendTimeMs: int64(seq) * 20, Type: protocol.FrameAudio}
}

func TestSeqLessWrap(t *testing.T) {
	if !SeqLess(0xFFFFFFFE, 1) {
		t.Fatal("wrap: -2 should precede 1")
	}
	if SeqLess(1, 0xFFFFFFFE) {
		t.Fatal("wrap: 1 should not precede -2")
	}
	if SeqLess(5, 5) {
		t.Fatal("equal seqs")
	}
}

func TestWindowReorderAndDuplicate(t *testing.T) {
	w := NewWindow(8)
	if r := w.Insert(pkt(3)); r != Inserted {
		t.Fatalf("first insert: %v", r)
	}
	if r := w.Insert(pkt(5)); r != Inserted {
		t.Fatalf("forward insert: %v", r)
	}
	if r := w.Insert(pkt(5)); r != Duplicate {
		t.Fatalf("buffered dup: %v", r)
	}
	if r := w.Insert(pkt(4)); r != Inserted {
		t.Fatalf("fill gap: %v", r)
	}
	for _, want := range []uint32{3, 4, 5} {
		p, ok := w.Pop()
		if !ok || p.Seq != want {
			t.Fatalf("pop got %v,%v want %d", p.Seq, ok, want)
		}
	}
	// Re-arrival of a played seq is a duplicate.
	if r := w.Insert(pkt(4)); r != Duplicate {
		t.Fatalf("played re-arrival: %v", r)
	}
}

func TestWindowLateAfterSkip(t *testing.T) {
	w := NewWindow(8)
	w.Insert(pkt(10))
	w.Skip() // 10 concealed at deadline
	if r := w.Insert(pkt(10)); r != Late {
		t.Fatalf("concealed re-arrival: %v", r)
	}
}

func TestWindowFullAndBounded(t *testing.T) {
	w := NewWindow(4)
	w.Insert(pkt(0))
	for s := uint32(2); s <= 4; s++ { // leave seq 1 missing
		if r := w.Insert(pkt(s)); r != Inserted {
			t.Fatalf("insert %d: %v", s, r)
		}
	}
	if r := w.Insert(pkt(5)); r != Full {
		t.Fatalf("overflow: %v", r)
	}
	if w.Len() > 4 {
		t.Fatalf("window unbounded: %d", w.Len())
	}
}

func TestWindowWrapAround(t *testing.T) {
	w := NewWindow(8)
	w.Insert(pkt(0xFFFFFFFE))
	w.Insert(pkt(0xFFFFFFFF))
	w.Insert(pkt(0)) // wraps past 2^32-1
	w.Insert(pkt(1))
	for _, want := range []uint32{0xFFFFFFFE, 0xFFFFFFFF, 0, 1} {
		if !w.Has(w.Base()) {
			w.Skip()
		}
		p, ok := w.Pop()
		if !ok {
			t.Fatalf("want seq %d", want)
		}
		if p.Seq != want {
			t.Fatalf("got %d want %d", p.Seq, want)
		}
	}
}

func TestWindowForwardJumpResets(t *testing.T) {
	w := NewWindow(8)
	w.Insert(pkt(0))
	if r := w.Insert(pkt(maxForward + 10)); r != Inserted || w.Resets != 1 {
		t.Fatalf("jump: r=%v resets=%d", r, w.Resets)
	}
	if w.Base() != maxForward+10 || w.Len() != 1 {
		t.Fatalf("resync failed: base=%d len=%d", w.Base(), w.Len())
	}
}
