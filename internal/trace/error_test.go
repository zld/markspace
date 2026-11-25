package trace

import (
	"errors"
	"testing"
)

func TestErrorLocationsAndWrap(t *testing.T) {
	first := E(CodeBoxLengthOutOfBounds, StageIngest, "size %d", 4).
		WithTrack(2).WithOffset(128)
	wrapped := Wrap(StageCut, first)

	te, ok := As(wrapped)
	if !ok {
		t.Fatalf("Wrap did not preserve trace error: %T", wrapped)
	}
	if te.Code != CodeBoxLengthOutOfBounds {
		t.Fatalf("code = %q", te.Code)
	}
	if te.Stage != StageCut || te.Origin != StageIngest {
		t.Fatalf("stage/origin = %q/%q", te.Stage, te.Origin)
	}
	if !te.HasTrack || te.TrackID != 2 || !te.HasByteOffset || te.ByteOffset != 128 {
		t.Fatalf("location not preserved: %+v", te)
	}
	if len(te.Path) != 2 || te.Path[0] != StageIngest || te.Path[1] != StageCut {
		t.Fatalf("path = %v", te.Path)
	}
	if !errors.Is(wrapped, first) {
		t.Fatal("errors.Is must reach original error")
	}
}

func TestWrapForeignError(t *testing.T) {
	wrapped := Wrap(StageFingerprint, errors.New("boom"))
	te, ok := As(wrapped)
	if !ok || te.Code != CodeInternal || te.Stage != StageFingerprint {
		t.Fatalf("unexpected wrapped error: %#v", wrapped)
	}
}
