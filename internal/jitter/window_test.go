package jitter

import (
	"testing"

	"jbbench/internal/protocol"
)

func mkFrame(no uint32) Frame {
	return Frame{Pkt: protocol.Packet{FrameNo: no, Seq: uint16(no)}, Arrival: 0}
}

func TestInOrderPushPop(t *testing.T) {
	w := NewReorderWindow(8, DropRejectNew)
	for i := uint32(0); i < 5; i++ {
		if ok, r := w.Push(mkFrame(i)); !ok {
			t.Fatalf("frame %d rejected: %s", i, r)
		}
	}
	for i := uint32(0); i < 5; i++ {
		f, ok := w.Pop()
		if !ok || f.Pkt.FrameNo != i {
			t.Fatalf("pop %d got ok=%v frame=%+v", i, ok, f)
		}
	}
	if _, ok := w.Pop(); ok {
		t.Fatal("pop past end must report a gap")
	}
}

func TestReorderAndDuplicate(t *testing.T) {
	w := NewReorderWindow(8, DropRejectNew)
	w.Push(mkFrame(0))
	w.Push(mkFrame(2)) // gap at 1, out of order
	w.Push(mkFrame(1)) // fills the gap
	if ok, r := w.Push(mkFrame(1)); ok || r != "duplicate" {
		t.Fatalf("duplicate: ok=%v reason=%q", ok, r)
	}
	st := w.Stats()
	if st.DuplicateDrops != 1 || st.Reordered != 2 || st.Accepted != 3 {
		t.Fatalf("stats = %+v", st)
	}
	for i := uint32(0); i < 3; i++ {
		if _, ok := w.Pop(); !ok {
			t.Fatalf("gap at %d after reorder fill", i)
		}
	}
}

func TestLateDropAfterCursor(t *testing.T) {
	w := NewReorderWindow(8, DropRejectNew)
	w.Push(mkFrame(0))
	w.Push(mkFrame(1))
	w.Pop() // cursor -> 1
	w.Pop() // cursor -> 2
	if ok, r := w.Push(mkFrame(0)); ok || r != "late" {
		t.Fatalf("late frame: ok=%v reason=%q", ok, r)
	}
	if w.Stats().LateDrops != 1 {
		t.Fatal("late drop not counted")
	}
}

func TestOverflowRejectAndEvict(t *testing.T) {
	// Capacity 3: frames more than 2 ahead of the cursor are out of range.
	rw := NewReorderWindow(3, DropRejectNew)
	rw.Push(mkFrame(0))
	if ok, r := rw.Push(mkFrame(5)); ok || r != "overflow" {
		t.Fatalf("reject policy: ok=%v reason=%q", ok, r)
	}
	if rw.Stats().OverflowDrops != 1 {
		t.Fatal("overflow not counted")
	}

	ew := NewReorderWindow(3, DropEvictOld)
	ew.Push(mkFrame(0))
	ew.Push(mkFrame(1))
	if ok, _ := ew.Push(mkFrame(4)); !ok {
		t.Fatal("evict policy must accept the new frame")
	}
	if ew.Stats().OverflowDrops != 1 || ew.Len() != 2 {
		t.Fatalf("evict stats/len wrong: %+v len=%d", ew.Stats(), ew.Len())
	}
	if ew.Has(0) {
		t.Fatal("oldest frame should have been evicted")
	}
}

func TestGapStateAndPending(t *testing.T) {
	w := NewReorderWindow(16, DropRejectNew)
	w.Push(mkFrame(0))
	w.Push(mkFrame(3)) // 1,2 missing
	w.Pop()            // consume cursor frame 0; gap now sits at the cursor
	no, gap := w.FirstMissing()
	if !gap || no != 1 {
		t.Fatalf("FirstMissing = (%d,%v), want (1,true)", no, gap)
	}
	if w.PendingGaps() != 2 {
		t.Fatalf("PendingGaps = %d, want 2", w.PendingGaps())
	}
	if _, ok := w.Pop(); ok {
		t.Fatal("frame 1 is missing, Pop must fail")
	}
	w.Skip() // conceal 1
	w.Skip() // conceal 2
	f, ok := w.Pop()
	if !ok || f.Pkt.FrameNo != 3 {
		t.Fatal("frame 3 should be playable after skips")
	}
	if w.PendingGaps() != 0 {
		t.Fatal("all gaps resolved after skips")
	}
}

func TestWindowHighFrameNumbers(t *testing.T) {
	// Frame numbers are uint32; ordering must remain correct at very high
	// values (runs never approach 2^32 frames, so wrap does not occur).
	w := NewReorderWindow(8, DropRejectNew)
	base := uint32(0xFFFFFFF0)
	w.Push(mkFrame(base))
	w.Push(mkFrame(base + 2))
	w.Push(mkFrame(base + 1))
	for i := uint32(0); i < 3; i++ {
		f, ok := w.Pop()
		if !ok || f.Pkt.FrameNo != base+i {
			t.Fatalf("pop %d: ok=%v frame=%d", i, ok, f.Pkt.FrameNo)
		}
	}
}
