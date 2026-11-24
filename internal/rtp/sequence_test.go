package rtp

import "testing"

func TestTrackerClassifiesDuplicateReorderAndGap(t *testing.T) {
	tr := NewTracker(1)
	steps := []struct {
		seq  uint16
		want Disposition
	}{
		{10, DispositionNew},
		{11, DispositionNew},
		{13, DispositionNew}, // gap at 12
		{12, DispositionOutOfOrder},
		{11, DispositionDuplicate},
	}
	var missing []uint64
	for _, step := range steps {
		got := tr.Accept(step.seq)
		if got.Kind != step.want {
			t.Fatalf("seq %d kind=%d want %d", step.seq, got.Kind, step.want)
		}
		missing = append(missing, got.Missing...)
	}
	if len(missing) != 1 || missing[0] != 12 {
		t.Fatalf("missing=%v", missing)
	}
}

func TestTrackerUnwrapsSequenceWrap(t *testing.T) {
	tr := NewTracker(2)
	tr.Accept(65534)
	tr.Accept(65535)
	got := tr.Accept(0)
	if got.ExtSequence != 65536 {
		t.Fatalf("ext=%d want 65536", got.ExtSequence)
	}
	if got.Kind != DispositionNew {
		t.Fatalf("kind=%d", got.Kind)
	}
	if dup := tr.Accept(0); dup.Kind != DispositionDuplicate {
		t.Fatalf("wrapped duplicate kind=%d", dup.Kind)
	}
}

func TestRestoreCheckpointState(t *testing.T) {
	tr := NewTracker(3)
	tr.MarkSeen(99)
	if !tr.Seen(99) || tr.MaxExt != 99 {
		t.Fatal("checkpoint state not restored")
	}
	if got := tr.Accept(100); got.Kind != DispositionNew {
		t.Fatalf("post-checkpoint seq kind=%d", got.Kind)
	}
}
