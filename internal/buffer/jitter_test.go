package buffer

import (
	"testing"

	"mediaqc/internal/model"
)

func ev(seq uint64) model.Event {
	return model.Event{MaterialID: "m", TrackID: "t", Sequence: seq}
}

func pushAll(t *testing.T, b *Buffer, seqList ...uint64) []model.Event {
	t.Helper()
	var out []model.Event
	for _, s := range seqList {
		got, err := b.Push(ev(s))
		if err != nil {
			t.Fatalf("push %d: %v", s, err)
		}
		out = append(out, got...)
	}
	return out
}

func TestInOrderPassThrough(t *testing.T) {
	b, _ := New("m", "t", 8)
	out := pushAll(t, b, 0, 1, 2)
	if len(out) != 3 {
		t.Fatalf("in-order events must release immediately, got %d", len(out))
	}
}

func TestReorderWithinWindow(t *testing.T) {
	b, _ := New("m", "t", 8)
	// 0, then 2 (parked), then 1 fills the hole
	if out, _ := b.Push(ev(0)); len(out) != 1 {
		t.Fatal("seq 0 must release")
	}
	if out, _ := b.Push(ev(2)); len(out) != 0 {
		t.Fatal("seq 2 must wait for seq 1")
	}
	if b.PendingCount() != 1 {
		t.Fatal("expected one parked event")
	}
	out, err := b.Push(ev(1))
	if err != nil || len(out) != 2 || out[0].Sequence != 1 || out[1].Sequence != 2 {
		t.Fatalf("hole fill must release 1,2 in order, got %+v err=%v", out, err)
	}
	if b.Stats().Reordered != 1 {
		t.Fatal("reordered counter not incremented")
	}
}

func TestDuplicateSuppression(t *testing.T) {
	b, _ := New("m", "t", 8)
	_, _ = b.Push(ev(0))
	_, err := b.Push(ev(0))
	if err == nil || !model.HasCode(err, model.CodeDuplicateSeq) {
		t.Fatalf("want duplicate error, got %v", err)
	}
	// late packet after the head advanced is also a duplicate.
	_, _ = b.Push(ev(1))
	_, err = b.Push(ev(0))
	if err == nil || !model.HasCode(err, model.CodeDuplicateSeq) {
		t.Fatalf("late packet must be duplicate, got %v", err)
	}
	if b.Stats().Duplicates != 2 {
		t.Fatalf("duplicate count = %d, want 2", b.Stats().Duplicates)
	}
}

func TestOverWindowRejected(t *testing.T) {
	b, _ := New("m", "t", 4)
	_, _ = b.Push(ev(0))
	// window=4: sequences >= 4 from head 0 are outside the window
	_, err := b.Push(ev(5))
	if err == nil || !model.HasCode(err, model.CodeOutOfWindow) {
		t.Fatalf("want out-of-window error, got %v", err)
	}
	if model.IsRecoverable(err) {
		t.Fatal("out-of-window must be unrecoverable")
	}
	if b.Stats().RejectedOverWindow != 1 {
		t.Fatal("over-window counter not incremented")
	}
}

func TestBufferBounded(t *testing.T) {
	b, _ := New("m", "t", 4)
	// Head at 0, then keep a hole at seq 1 so the release head never advances.
	if _, err := b.Push(ev(0)); err != nil {
		t.Fatal(err)
	}
	for s := uint64(2); s < 5; s++ { // park 2,3,4 => all within window
		if _, err := b.Push(ev(s)); err != nil {
			t.Fatalf("seq %d should fit, got %v", s, err)
		}
	}
	if b.PendingCount() != 3 {
		t.Fatalf("parked count = %d, want 3", b.PendingCount())
	}
	// seq 5: 5-1=4 is not less than window 4 => breach
	if _, err := b.Push(ev(5)); err == nil {
		t.Fatal("seq 5 must breach the window")
	}
}

func TestFlushReportsGaps(t *testing.T) {
	b, _ := New("m", "t", 16)
	_, _ = b.Push(ev(0))
	_, _ = b.Push(ev(1))
	// seq 2,3 missing
	_, _ = b.Push(ev(4))
	out, stats := b.Flush()
	if len(out) != 1 || out[0].Sequence != 4 {
		t.Fatalf("flush should emit parked seq 4, got %+v", out)
	}
	if len(stats.UnrecoveredGaps) != 1 {
		t.Fatalf("want one gap, got %+v", stats.UnrecoveredGaps)
	}
	g := stats.UnrecoveredGaps[0]
	if g.FirstSeq != 2 || g.LastSeq != 3 || g.Count() != 2 {
		t.Fatalf("gap bounds wrong: %+v", g)
	}
	if g.DeclaredBy != model.StageBuffer {
		t.Fatal("gap must record the declaring stage")
	}
}

func TestInvalidWindowRejected(t *testing.T) {
	if _, err := New("m", "t", 0); err == nil {
		t.Fatal("zero window must be rejected")
	}
}
