package serr

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorFormatting(t *testing.T) {
	err := New(CodeTruncatedBlock, "block %d short", 3).At(42).WithPath([]string{"Segment", "Cluster"})
	want := "TRUNCATED_BLOCK @offset=42 path=Segment/Cluster: block 3 short"
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestNewLeavesOffsetUnset(t *testing.T) {
	err := New(CodeIO, "boom")
	if err.Offset != -1 {
		t.Fatalf("Offset = %d, want -1", err.Offset)
	}
	if got := OffsetOf(err); got != -1 {
		t.Fatalf("OffsetOf = %d", got)
	}
}

func TestIsMatchesCode(t *testing.T) {
	err := New(CodeCheckpointCorrupt, "bad")
	if !errors.Is(err, New(CodeCheckpointCorrupt, "other")) {
		t.Fatal("errors.Is should match on code")
	}
	if errors.Is(err, New(CodeIO, "x")) {
		t.Fatal("different code should not match")
	}
}

func TestCodeOfAndOffsetOf(t *testing.T) {
	if CodeOf(fmt.Errorf("plain")) != CodeIO {
		t.Fatal("plain errors map to IO_ERROR")
	}
	if OffsetOf(fmt.Errorf("plain")) != -1 {
		t.Fatal("plain errors have no offset")
	}
	wrapped := fmt.Errorf("wrap: %w", New(CodeNoAnchor, "x").At(7))
	code := CodeOf(wrapped)
	if code != CodeNoAnchor {
		t.Fatalf("wrapped code = %s", code)
	}
	if got := OffsetOf(wrapped); got != 7 {
		t.Fatalf("wrapped offset = %d", got)
	}
}

func TestWithPathCopies(t *testing.T) {
	path := []string{"A", "B"}
	err := New(CodeIO, "x").WithPath(path)
	path[0] = "mutated"
	if err.Path[0] != "A" {
		t.Fatal("WithPath must copy the slice")
	}
}
