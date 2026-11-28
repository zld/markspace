package model

import (
	"errors"
	"testing"
)

func TestFieldErrorOffsetAndPath(t *testing.T) {
	fe := OutOfRange("a.mxf", 128, "partition/KLV[2]/length", "length exceeds file")
	if fe.Kind != KindOutOfRange || fe.Offset != 128 || fe.Path == "" {
		t.Fatalf("field error not populated: %+v", fe)
	}
	if !IsKind(fe, KindOutOfRange) {
		t.Fatal("IsKind failed")
	}
	wrapped := WrapField(KindTruncated, "b.mp4", 40, "moov/mdat", "cut short", fe)
	var target *FieldError
	if !errors.As(wrapped, &target) || target.Offset != 40 {
		t.Fatal("errors.As did not reach outer FieldError")
	}
	inner, ok := AsField(errors.Unwrap(wrapped))
	if !ok || inner.Offset != 128 {
		t.Fatal("wrapped cause not recoverable")
	}
}

func TestStageTransitions(t *testing.T) {
	order := []Stage{StageCreated, StageParsed, StageComposed, StageExported, StageVerified}
	cur := StageCreated
	for _, next := range order[1:] {
		var err error
		if cur, err = Transition(cur, next); err != nil {
			t.Fatalf("legal transition failed: %v", err)
		}
	}
	// Re-entering the same stage is idempotent.
	if _, err := Transition(cur, StageVerified); err != nil {
		t.Fatalf("same-stage re-entry failed: %v", err)
	}
	// Jumping backwards/skipping stages must fail.
	if _, err := Transition(StageCreated, StageVerified); err == nil {
		t.Fatal("illegal jump was accepted")
	}
	if _, err := Transition(StageExported, StageParsed); err == nil {
		t.Fatal("backwards transition was accepted")
	}
	if !IsFinal(StageVerified) || IsFinal(StageExported) {
		t.Fatal("IsFinal wrong")
	}
}

func TestSourced(t *testing.T) {
	s := WithSource(42, SourceOnly("x.mp4", "tkhd/width", 100))
	if s.Value != 42 || s.Source.Offset != 100 {
		t.Fatalf("sourced value wrong: %+v", s)
	}
}
