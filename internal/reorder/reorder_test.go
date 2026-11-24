package reorder

import "testing"

func pk(seq uint16, offset int64) *Packet {
	return &Packet{Seq: seq, Offset: offset}
}

func seqs(events []Event) (seqs []uint64, kinds []EventKind) {
	for _, e := range events {
		seqs = append(seqs, e.ExtSeq)
		kinds = append(kinds, e.Kind)
	}
	return seqs, kinds
}

func TestWindowInOrder(t *testing.T) {
	w := NewWindow(8, 2)
	for i := uint16(0); i < 5; i++ {
		ev := w.Push(pk(i, int64(i)))
		if len(ev) != 1 || ev[0].Kind != KindPacket || ev[0].ExtSeq != uint64(i) {
			t.Fatalf("seq %d events = %+v", i, ev)
		}
	}
	if s := w.Stats(); s.Received != 5 || s.Reordered != 0 || s.Duplicates != 0 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestWindowDuplicateAndReorder(t *testing.T) {
	w := NewWindow(8, 2)
	w.Push(pk(0, 0))
	w.Push(pk(1, 1))
	w.Push(pk(2, 2))
	// Duplicate of an already delivered seq.
	if ev := w.Push(pk(1, 99)); ev != nil {
		t.Fatalf("duplicate produced events: %+v", ev)
	}
	// Out-of-order arrival within the window: seq 4 then 3.
	if ev := w.Push(pk(4, 4)); ev != nil {
		t.Fatalf("pending seq 4 should not emit yet: %+v", ev)
	}
	ev := w.Push(pk(3, 3))
	got, kinds := seqs(ev)
	if len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Fatalf("reorder emit = %v kinds %v", got, kinds)
	}
	s := w.Stats()
	if s.Duplicates != 1 || s.Reordered != 1 {
		t.Fatalf("stats = %+v, want dup=1 reorder=1", s)
	}
}

func TestWindowBurstLossAndLateDrop(t *testing.T) {
	w := NewWindow(4, 2)
	w.Push(pk(0, 0))
	// Jump far enough to expire seq 1..3 as one contiguous burst gap.
	ev := w.Push(pk(8, 8))
	var gapSeqs []uint64
	for _, e := range ev {
		if e.Kind == KindGap {
			gapSeqs = append(gapSeqs, e.ExtSeq)
		}
	}
	if len(gapSeqs) != 3 {
		t.Fatalf("gap events = %d (%+v), want 3", len(gapSeqs), ev)
	}
	if w.Loss().Missing() != 3 || w.Loss().Bursts() != 1 || w.Loss().MaxBurst() != 3 {
		t.Fatalf("loss = gaps %d missing %d bursts %d max %d",
			w.Loss().GapCount(), w.Loss().Missing(), w.Loss().Bursts(), w.Loss().MaxBurst())
	}
	// A packet for a seq already declared lost is a late drop, not a dup.
	if ev := w.Push(pk(2, 22)); ev != nil {
		t.Fatalf("late packet must not emit: %+v", ev)
	}
	if s := w.Stats(); s.LateDropped != 1 || s.Duplicates != 0 {
		t.Fatalf("stats = %+v, want late=1", s)
	}
}

func TestWindowFlushEOF(t *testing.T) {
	w := NewWindow(16, 2)
	w.Push(pk(0, 0))
	w.Push(pk(1, 1))
	w.Push(pk(3, 3)) // seq 2 still missing but inside the window
	if got := w.NextSeq(); got != 2 {
		t.Fatalf("frontier = %d, want 2", got)
	}
	ev := w.Flush()
	var gapCount, pktCount int
	for _, e := range ev {
		if e.Kind == KindGap {
			gapCount++
			if e.ExtSeq != 2 || e.Gap.From != 2 || e.Gap.To != 2 {
				t.Fatalf("EOF gap = %+v", e)
			}
		} else {
			pktCount++
		}
	}
	if gapCount != 1 || pktCount != 1 {
		t.Fatalf("flush = %d gaps %d packets, want 1/1", gapCount, pktCount)
	}
}

func TestWindowBoundaryDistance(t *testing.T) {
	// Size 4: a jump of exactly 4 stays buffered; a jump of 5 expires a hole.
	w := NewWindow(4, 2)
	w.Push(pk(0, 0))
	if ev := w.Push(pk(4, 4)); ev != nil {
		t.Fatalf("distance == size must not expire, got %+v", ev)
	}
	if w.Loss().GapCount() != 0 {
		t.Fatalf("gap declared inside window: %+v", w.Loss().Gaps())
	}
	w2 := NewWindow(4, 2)
	w2.Push(pk(0, 0))
	ev := w2.Push(pk(6, 6)) // distance 5 > size 4: seq 1 expires immediately
	if len(ev) == 0 || ev[0].Kind != KindGap || ev[0].ExtSeq != 1 {
		t.Fatalf("distance > size must expire oldest, got %+v", ev)
	}
}

func TestLossTrackerMergeAndBursts(t *testing.T) {
	l := NewLossTracker(3)
	l.Record(10, 11, 0)
	l.Record(12, 14, 0) // adjacent -> merged into one run of 5
	l.Record(30, 30, 0) // isolated short run, below burst threshold
	if l.GapCount() != 2 || l.Missing() != 6 || l.Bursts() != 1 || l.MaxBurst() != 5 {
		t.Fatalf("tracker = gaps %d missing %d bursts %d max %d",
			l.GapCount(), l.Missing(), l.Bursts(), l.MaxBurst())
	}
}
