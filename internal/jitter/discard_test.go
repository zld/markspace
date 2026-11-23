package jitter

import "testing"

func TestDiscardGapAdvancesFloorSafety(t *testing.T) {
	b := newBuf(t, 2, 64)
	for seq := uint16(1); seq <= 6; seq++ {
		b.Push(pkt(seq, 0))
	}
	b.Push(pkt(8, 0)) // holes at 7, then 8 is resident
	if b.Floor() != 7 {
		t.Fatalf("floor = %d want 7", b.Floor())
	}
	if _, err := b.DiscardGap(7); err == nil {
		t.Fatal("empty gap accepted")
	}
	if _, err := b.DiscardGap(9); err == nil {
		t.Fatal("gap crossing a resident packet must fail")
	}
	released, err := b.DiscardGap(8)
	if err != nil {
		t.Fatal(err)
	}
	if len(released) != 1 || released[0].ExtSeq != 8 {
		t.Fatalf("released = %v", seqs(released))
	}
	if b.Floor() != 9 {
		t.Fatalf("floor = %d want 9", b.Floor())
	}
}
