package rtp

import "testing"

func TestTrackerOrderedAndReordered(t *testing.T) {
	tr := NewSequenceTracker(16)
	first := tr.Push(100)
	if !first.First || first.Extended != 100 {
		t.Fatalf("first observation wrong: %+v", first)
	}
	second := tr.Push(102) // gap of one
	if second.Duplicate || second.Reordered || second.Extended != 102 {
		t.Fatalf("forward packet wrong: %+v", second)
	}
	late := tr.Push(101) // fills the gap out of order
	if !late.Reordered || late.Duplicate || late.Extended != 101 {
		t.Fatalf("reordered packet wrong: %+v", late)
	}
	dup := tr.Push(101)
	if !dup.Duplicate || dup.Extended != 101 {
		t.Fatalf("duplicate packet wrong: %+v", dup)
	}
	if tr.Expected() != 103 || tr.MaxExtended() != 102 {
		t.Fatalf("state wrong: expected=%d max=%d", tr.Expected(), tr.MaxExtended())
	}
}

func TestTrackerWrap(t *testing.T) {
	tr := NewSequenceTracker(128)
	tr.Push(65533)
	tr.Push(65534)
	tr.Push(65535)
	wrapped := tr.Push(0)
	if !wrapped.Wrapped || wrapped.Extended != 1<<16 {
		t.Fatalf("wrap packet wrong: %+v", wrapped)
	}
	next := tr.Push(1)
	if next.Extended != (1<<16)+1 || next.Wrapped {
		t.Fatalf("post-wrap packet wrong: %+v", next)
	}
	late := tr.Push(65535) // immediate previous number: still inside window
	if !late.Duplicate || late.Extended != 65535 {
		t.Fatalf("old-cycle duplicate wrong: %+v", late)
	}
	if !tr.WrapObserved() {
		t.Fatal("wrap must be recorded")
	}
}

func TestTrackerDuplicateAcrossWrap(t *testing.T) {
	tr := NewSequenceTracker(128)
	tr.Push(10)
	tr.Push(11)
	if obs := tr.Push(10); !obs.Duplicate {
		t.Fatalf("same-cycle duplicate wrong: %+v", obs)
	}
	// Walk a consecutive stream to the wrap boundary.
	for seq := uint16(12); seq != 0; seq++ {
		tr.Push(seq)
	}
	// seq wrapped naturally to 0 in a new cycle.
	zero := tr.Push(0)
	if zero.Duplicate || !zero.Wrapped || zero.Extended != 1<<16 {
		t.Fatalf("wrap-to-zero wrong: %+v", zero)
	}
	obs := tr.Push(10)
	if obs.Duplicate || obs.Wrapped || obs.Extended != (1<<16)+10 {
		t.Fatalf("new-cycle low number wrong: %+v", obs)
	}
	// The new-cycle 10 repeated is a duplicate again.
	if again := tr.Push(10); !again.Duplicate || again.Extended != (1<<16)+10 {
		t.Fatalf("new-cycle duplicate wrong: %+v", again)
	}
}

func TestTrackerWindowPruning(t *testing.T) {
	tr := NewSequenceTracker(4)
	for i := uint16(0); i < 10; i++ {
		tr.Push(i)
	}
	if tr.Seen(0) || tr.Seen(4) {
		t.Fatal("old entries must be pruned past the window")
	}
	if !tr.Seen(9) {
		t.Fatal("current entry must remain visible")
	}
}

func TestTrackerDefaultWindow(t *testing.T) {
	tr := NewSequenceTracker(0)
	tr.Push(0)
	if !tr.Seen(0) || tr.Window() != 32767 {
		t.Fatal("default window tracker must accept packets")
	}
}

func TestTrackerLargeJumpDoesNotWrap(t *testing.T) {
	tr := NewSequenceTracker(8)
	tr.Push(100)
	jump := tr.Push(500)
	if jump.Wrapped || jump.Extended != 500 {
		t.Fatalf("large jump must not be treated as wrap: %+v", jump)
	}
}
