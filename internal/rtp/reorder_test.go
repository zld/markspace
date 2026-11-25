package rtp

import (
	"testing"

	"tracecheck/internal/model"
)

func rec(seq uint16, arrival model.Tick) model.PacketRecord {
	return model.PacketRecord{
		Stream:      1,
		Codec:       model.CodecOpus,
		Sequence:    seq,
		ArrivalTick: arrival,
	}
}

func TestReorderReordersAndDrains(t *testing.T) {
	b := NewReorderBuffer(4)
	b.Push(rec(0, 0))
	b.Push(rec(2, 20))
	b.Push(rec(1, 10)) // late, fills the hole
	if b.Reorders() != 1 || b.Duplicates() != 0 {
		t.Fatalf("counters wrong: reorder=%d dup=%d", b.Reorders(), b.Duplicates())
	}
	if got := b.Drain(); len(got) != 0 {
		t.Fatalf("nothing should drain inside the window, got %d", len(got))
	}
	b.Push(rec(6, 60)) // window now releases through 1
	got := b.Drain()
	if len(got) != 2 || got[0].Sequence != 0 || got[1].Sequence != 1 {
		t.Fatalf("drained order wrong: %+v", got)
	}
	if len(b.Gaps()) != 0 {
		t.Fatalf("filled hole must not be a gap: %+v", b.Gaps())
	}
	rest := b.Flush()
	if len(rest) != 2 || rest[0].Sequence != 2 || rest[1].Sequence != 6 {
		t.Fatalf("flush must return the buffered tail: %+v", rest)
	}
}

func TestReorderDuplicatesCountedOnce(t *testing.T) {
	b := NewReorderBuffer(4)
	b.Push(rec(0, 0))
	b.Push(rec(1, 10))
	b.Push(rec(0, 5)) // duplicate
	got := b.Flush()
	if b.Duplicates() != 1 {
		t.Fatalf("duplicate count = %d", b.Duplicates())
	}
	if len(got) != 2 {
		t.Fatalf("duplicate must be stored once, got %d packets", len(got))
	}
}

func TestReorderBurstLossBecomesGap(t *testing.T) {
	b := NewReorderBuffer(4)
	b.Push(rec(10, 100))
	b.Push(rec(14, 140)) // 11,12,13 lost
	if got := b.Drain(); len(got) != 0 {
		t.Fatalf("gap must stay open inside window: %+v", got)
	}
	b.Push(rec(20, 200)) // window passes the missing runs
	b.Drain()
	gaps := b.Gaps()
	if len(gaps) != 2 || gaps[0].Count != 3 || gaps[1].Count != 1 {
		t.Fatalf("want gap runs of 3 and 1, got %+v", gaps)
	}
	if gaps[0].FromExtended != 11 || gaps[0].ToExtended != 13 {
		t.Fatalf("gap range wrong: %+v", gaps[0])
	}
	if gaps[0].Span.Start != 100 || gaps[0].Span.End != 140 {
		t.Fatalf("gap span must use neighbor arrivals: %+v", gaps[0].Span)
	}
	if gaps[1].Span.Start != 140 || gaps[1].Span.End != 200 {
		t.Fatalf("second gap span wrong: %+v", gaps[1].Span)
	}
}

func TestReorderFlushReportsTrailingLoss(t *testing.T) {
	b := NewReorderBuffer(4)
	b.Push(rec(0, 0))
	b.Push(rec(3, 30)) // 1,2 missing at stream end
	got := b.Flush()
	if len(got) != 2 {
		t.Fatalf("flush keeps observed packets, got %d", len(got))
	}
	gaps := b.Gaps()
	if len(gaps) != 1 || gaps[0].Count != 2 {
		t.Fatalf("trailing gap wrong: %+v", gaps)
	}
}

func TestReorderWrapKeepsExtendedOrder(t *testing.T) {
	b := NewReorderBuffer(16)
	for seq := uint16(65530); seq != 4; seq++ {
		b.Push(rec(seq, model.Tick(seq)*10))
	}
	got := b.Flush()
	if len(got) != 10 {
		t.Fatalf("want 10 packets across wrap, got %d", len(got))
	}
	if got[0].ExtendedSequence != 65530 || got[9].ExtendedSequence != (1<<16)+3 {
		t.Fatalf("extended order wrong: %d..%d", got[0].ExtendedSequence, got[9].ExtendedSequence)
	}
	if b.Wraps() != 1 {
		t.Fatalf("wrap count = %d", b.Wraps())
	}
}

func TestReorderMultipleGapsMergedPerRun(t *testing.T) {
	b := NewReorderBuffer(2)
	b.Push(rec(0, 0))
	b.Push(rec(2, 20)) // 1 missing
	b.Push(rec(5, 50)) // 3,4 missing
	b.Flush()
	gaps := b.Gaps()
	if len(gaps) != 2 || gaps[0].Count != 1 || gaps[1].Count != 2 {
		t.Fatalf("want two distinct gap runs, got %+v", gaps)
	}
}
