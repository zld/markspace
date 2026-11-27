package errors

import (
	"fmt"
	"testing"
)

func TestStageAndOffset(t *testing.T) {
	err := Newf(StageParse, 42, "stco", "bad ref %d", 7)
	wrapped := fmt.Errorf("outer: %w", err)
	st, ok := StageOf(wrapped)
	if !ok || st != StageParse {
		t.Fatalf("stage=%v ok=%v", st, ok)
	}
	off, ok := OffsetOf(wrapped)
	if !ok || off != 42 {
		t.Fatalf("off=%d ok=%v", off, ok)
	}
	if got := err.Error(); got == "" {
		t.Fatal("empty message")
	}
}

func TestNoStage(t *testing.T) {
	if _, ok := StageOf(fmt.Errorf("plain")); ok {
		t.Fatal("unexpected stage")
	}
}
