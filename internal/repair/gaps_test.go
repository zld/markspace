package repair

import (
	"testing"

	"replaycheck/internal/fault"
)

func TestMergeSameSourceOverlaps(t *testing.T) {
	ix := New([]Gap{
		{Source: "rtp", Start: 100, End: 200},
		{Source: "rtp", Start: 150, End: 250},
		{Source: "rtp", Start: 400, End: 500},
	})
	if got := ix.Count("rtp"); got != 2 {
		t.Fatalf("merged count = %d, want 2", got)
	}
	if ix.Gaps[0].Start != 100 || ix.Gaps[0].End != 250 {
		t.Fatalf("first merged gap = %+v", ix.Gaps[0])
	}
	if got := ix.TotalTicks("rtp"); got != 250 {
		t.Fatalf("total ticks = %d, want 250", got)
	}
}

func TestAdjacentMerge(t *testing.T) {
	ix := New([]Gap{
		{Source: "wav", Start: 0, End: 10},
		{Source: "wav", Start: 10, End: 20},
	})
	if len(ix.Gaps) != 1 {
		t.Fatalf("adjacent gaps not merged: %+v", ix.Gaps)
	}
}

func TestDifferentSourcesStaySeparate(t *testing.T) {
	ix := New([]Gap{
		{Source: "rtp", Start: 0, End: 100},
		{Source: "wav", Start: 0, End: 50},
	})
	if len(ix.Gaps) != 2 {
		t.Fatalf("sources merged: %+v", ix.Gaps)
	}
	if _, ok := ix.OverlapsSource(25, 30, "wav"); !ok {
		t.Fatal("expected wav overlap")
	}
	if _, ok := ix.OverlapsSource(75, 80, "wav"); ok {
		t.Fatal("unexpected wav overlap")
	}
	if _, ok := ix.Overlaps(75, 80); !ok {
		t.Fatal("expected any-source overlap")
	}
}

func TestFirstBadByteAndEmptyIntervals(t *testing.T) {
	ix := New([]Gap{
		{Source: "wav", Start: 10, End: 10, ByteOffset: 999}, // dropped
		{Source: "wav", Start: 0, End: 1, ByteOffset: 44},
		{Source: "rtp", Start: 0, End: 1, ByteOffset: -1},
	})
	if got := ix.FirstBadByte(); got != 44 {
		t.Fatalf("first bad byte = %d, want 44", got)
	}
	if ix.Count("") != 2 {
		t.Fatalf("count = %d, want 2", ix.Count(""))
	}
}

func TestKindPreserved(t *testing.T) {
	ix := New([]Gap{
		{Source: "rtp", Start: 0, End: 10, Kind: fault.KindPacketLoss},
	})
	g, ok := ix.Overlaps(0, 1)
	if !ok || g.Kind != fault.KindPacketLoss {
		t.Fatalf("gap = %+v, %v", g, ok)
	}
}

func TestEmptyIndex(t *testing.T) {
	ix := New(nil)
	if len(ix.Gaps) != 0 || ix.FirstBadByte() != -1 {
		t.Fatalf("empty index not empty: %+v", ix)
	}
	if _, ok := ix.Overlaps(0, 100); ok {
		t.Fatal("empty index should not overlap")
	}
}
