package fault

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorFormatting(t *testing.T) {
	err := At(StageRTCP, "parse report block", 128, ErrTruncated).WithSeq(4242)
	got := err.Error()
	want := "rtcp parse report block offset=128 seq=4242: truncated input"
	if got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if stage, ok := StageOf(err); !ok || stage != StageRTCP {
		t.Fatalf("StageOf = %v, %v", stage, ok)
	}
	off, ok := OffsetOf(err)
	if !ok || off != 128 {
		t.Fatalf("OffsetOf = %d, %v", off, ok)
	}
	if !errors.Is(err, ErrTruncated) {
		t.Fatal("errors.Is should match ErrTruncated")
	}
	if Is(err, ErrLimit, ErrTruncated) != true {
		t.Fatal("Is should match any sentinel")
	}
}

func TestWrappedError(t *testing.T) {
	inner := New(StageIngest, "open", ErrLimit)
	wrapped := fmt.Errorf("stage run: %w", inner)
	stage, ok := StageOf(wrapped)
	if !ok || stage != StageIngest {
		t.Fatalf("StageOf(wrapped) = %v, %v", stage, ok)
	}
	if off, ok := OffsetOf(inner); !ok || off != -1 {
		t.Fatalf("OffsetOf = %d, %v", off, ok)
	}
	var nilErr *Error
	if nilErr.Error() != "<nil>" {
		t.Fatalf("nil Error() = %q", nilErr.Error())
	}
}
