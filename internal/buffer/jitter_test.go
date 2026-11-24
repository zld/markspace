package buffer

import (
	"testing"

	"mediasync/internal/model"
)

func ms(n int64) model.Nanos { return model.Nanos(n) * model.Nanos(model.NanosPerMilli) }

func mustNew(t *testing.T, cfg Config) *JitterBuffer {
	t.Helper()
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func entry(ext int64, msTime model.Nanos) Entry {
	return Entry{Ext: ext, TrackID: 1, Kind: model.KindVideo, MediaTime: msTime, Arrival: msTime}
}

func TestNewValidatesBounds(t *testing.T) {
	if _, err := New(Config{MaxPackets: 0}); err == nil {
		t.Error("zero MaxPackets must be rejected")
	}
	if _, err := New(Config{MaxPackets: 4, ReorderWindow: -1}); err == nil {
		t.Error("negative ReorderWindow must be rejected")
	}
}

func TestContiguousPrefixReleasedImmediately(t *testing.T) {
	b := mustNew(t, Config{MaxPackets: 8, ReorderWindow: 4, LateWindow: ms(500)})
	status, released, _ := b.Push(entry(1, ms(0)))
	if status != Buffered || len(released) != 1 {
		t.Fatalf("status=%v released=%d", status, len(released))
	}
	status, released, _ = b.Push(entry(2, ms(20)))
	if status != Buffered || len(released) != 1 {
		t.Fatalf("contiguous second packet should release: %v %d", status, len(released))
	}
}

func TestHoleHeldForReorderWindow(t *testing.T) {
	b := mustNew(t, Config{MaxPackets: 16, ReorderWindow: 3, LateWindow: ms(500)})
	b.Push(entry(1, ms(0)))
	// Drop 2, deliver 3..5: the prefix stops before the hole.
	_, released, _ := b.Push(entry(3, ms(40)))
	if len(released) != 0 {
		t.Fatalf("released across a hole: %v", released)
	}
	b.Push(entry(4, ms(60)))
	_, released, _ = b.Push(entry(5, ms(80)))
	if len(released) != 0 {
		t.Fatalf("still inside window, released %v", released)
	}
	// Once the high water mark is more than the window past the hole, the
	// held packets are forced out in order.
	_, released, _ = b.Push(entry(6, ms(100)))
	if len(released) != 4 {
		t.Fatalf("expected 4 forced releases, got %d: %v", len(released), released)
	}
	for i := 1; i < len(released); i++ {
		if released[i].Ext != released[i-1].Ext+1 {
			t.Errorf("release order not sequential: %v", released)
		}
	}
}

func TestGapFilledBeforeExpiry(t *testing.T) {
	b := mustNew(t, Config{MaxPackets: 16, ReorderWindow: 4, LateWindow: ms(500)})
	out := []Entry{}
	push := func(e Entry) {
		_, r, _ := b.Push(e)
		out = append(out, r...)
	}
	push(entry(1, ms(0)))
	push(entry(3, ms(40)))
	if len(out) != 1 {
		t.Fatalf("released too early: %v", out)
	}
	push(entry(2, ms(20))) // fills the hole
	if len(out) != 3 {
		t.Fatalf("expected ordered release after fill, got %d: %v", len(out), out)
	}
	if out[1].Ext != 2 || out[2].Ext != 3 {
		t.Errorf("order = %v", out)
	}
}

func TestLateByMediaTime(t *testing.T) {
	b := mustNew(t, Config{MaxPackets: 16, ReorderWindow: 2, LateWindow: ms(100)})
	b.Push(entry(10, ms(1000)))
	status, _, late := b.Push(entry(9, ms(500)))
	if status != Late || late == nil {
		t.Fatalf("expected late event, status=%v", status)
	}
	if late.Behind != ms(500) {
		t.Errorf("Behind = %d, want 500ms", late.Behind)
	}
	if b.Len() != 0 {
		t.Errorf("late entry must not be buffered, len=%d", b.Len())
	}
}

func TestWouldBeLateMatchesPush(t *testing.T) {
	b := mustNew(t, Config{MaxPackets: 16, ReorderWindow: 2, LateWindow: ms(100)})
	b.Push(entry(1, ms(1000)))
	if !b.WouldBeLate(entry(2, ms(800))) {
		t.Error("WouldBeLate should predict a late entry")
	}
	if b.WouldBeLate(entry(2, ms(950))) {
		t.Error("entry inside the window must not be predicted late")
	}
}

func TestMaxPacketsBound(t *testing.T) {
	b := mustNew(t, Config{MaxPackets: 2, ReorderWindow: 0, LateWindow: ms(500)})
	b.Push(entry(1, ms(0)))
	b.Push(entry(3, ms(40)))
	b.Push(entry(4, ms(60)))
	if b.Len() > 2 {
		t.Fatalf("buffer exceeded bound: len=%d", b.Len())
	}
}

func TestFlushDrainsInOrder(t *testing.T) {
	b := mustNew(t, Config{MaxPackets: 16, ReorderWindow: 8, LateWindow: ms(500)})
	b.Push(entry(2, ms(20)))
	b.Push(entry(4, ms(60))) // held behind the missing 3
	if b.Len() != 1 {
		t.Fatalf("expected one held entry, len=%d", b.Len())
	}
	out := b.Flush()
	if len(out) != 1 || out[0].Ext != 4 {
		t.Errorf("flush = %v", out)
	}
	if b.Len() != 0 {
		t.Errorf("flush left %d entries", b.Len())
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	b := mustNew(t, Config{MaxPackets: 16, ReorderWindow: 4, LateWindow: ms(500)})
	b.Push(entry(1, ms(0)))
	b.Push(entry(3, ms(40))) // held behind the missing 2
	snap := b.Snapshot()
	b2 := mustNew(t, Config{MaxPackets: 16, ReorderWindow: 4, LateWindow: ms(500)})
	b2.Restore(snap)
	if b2.Len() != 1 {
		t.Fatalf("restored len = %d, want 1", b2.Len())
	}
	if _, released, _ := b2.Push(entry(2, ms(20))); len(released) != 2 {
		t.Errorf("restored buffer did not release the filled run: %v", released)
	}
}

func TestSetAcceptAll(t *testing.T) {
	b := mustNew(t, Config{MaxPackets: 16, ReorderWindow: 0, LateWindow: ms(10)})
	b.Push(entry(1, ms(1000)))
	b.SetAcceptAll(true)
	if b.WouldBeLate(entry(2, ms(0))) {
		t.Error("accept-all must suppress late rejection")
	}
}
