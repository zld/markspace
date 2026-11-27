package mediaerr

import (
	"errors"
	"testing"
)

func TestByteRangeMerge(t *testing.T) {
	a := ByteRange{Start: 10, End: 20}
	b := ByteRange{Start: 20, End: 30} // adjacent
	if !a.OverlapsOrAdjacent(b) {
		t.Fatal("adjacent ranges should merge")
	}
	if got := a.Merge(b); got != (ByteRange{Start: 10, End: 30}) {
		t.Fatalf("merge=%v", got)
	}
	c := ByteRange{Start: 31, End: 40}
	if a.OverlapsOrAdjacent(c) {
		t.Fatal("disjoint ranges must not merge")
	}
	if (ByteRange{Start: 5, End: 1}).Valid() {
		t.Fatal("inverted range must be invalid")
	}
}

func TestMediaErrorWrapAndAs(t *testing.T) {
	root := errors.New("disk full")
	me := Wrap(root, StageTxn, "v1", "checkpoint failed").
		WithBytes(ByteRange{Start: 0, End: 9})
	if !errors.Is(me, root) {
		t.Fatal("Unwrap chain broken")
	}
	got, ok := AsMedia(me)
	if !ok || got.Stage != StageTxn || got.Byte == nil {
		t.Fatalf("AsMedia failed: %+v", got)
	}
	if ExitCode(me) != ExitCommitOrRecover {
		t.Fatalf("txn exit code got %d", ExitCode(me))
	}
	if ExitCode(New(StageParse, "v", "bad nal")) != ExitInputOrIndex {
		t.Fatal("parse errors map to exit code 2")
	}
	if ExitCode(nil) != ExitOK || ExitCode(errors.New("plain")) != ExitGeneric {
		t.Fatal("base exit codes wrong")
	}
}

func TestMergeFaults(t *testing.T) {
	a := New(StageParse, "v", "x").WithBytes(ByteRange{Start: 0, End: 10}).
		WithTime(TimeRange{FromTicks: 0, ToTicks: 1})
	b := New(StageParse, "v", "y").WithBytes(ByteRange{Start: 10, End: 18}).
		WithTime(TimeRange{FromTicks: 1, ToTicks: 2})
	if !Mergeable(a, b) {
		t.Fatal("same-track adjacent parse faults must merge")
	}
	m := Merge(a, b)
	if *m.Byte != (ByteRange{Start: 0, End: 18}) || m.Time.ToTicks != 2 {
		t.Fatalf("merged fault wrong: %+v", m)
	}
	c := New(StageIndex, "v", "z").WithBytes(ByteRange{Start: 10, End: 18})
	if Mergeable(a, c) {
		t.Fatal("different stages must not merge")
	}
}
