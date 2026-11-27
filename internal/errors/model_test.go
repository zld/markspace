package errors

import (
	"fmt"
	"testing"
)

func TestNewAndAt(t *testing.T) {
	plain := New("stream", CodeInvalidEncoding, "bad nal %d", 7)
	if plain.Offset != -1 || plain.Code != CodeInvalidEncoding {
		t.Fatalf("unexpected plain error: %+v", plain)
	}
	if got := plain.Error(); got != "stream/INVALID_ENCODING: bad nal 7" {
		t.Fatalf("unexpected message %q", got)
	}

	anchored := At("stream", CodeTruncatedUnit, 42, "unit ends early")
	if anchored.Offset != 42 {
		t.Fatalf("offset = %d, want 42", anchored.Offset)
	}
	if got := anchored.Error(); got != "stream/TRUNCATED_UNIT at byte 42: unit ends early" {
		t.Fatalf("unexpected message %q", got)
	}
}

func TestWrapAndAs(t *testing.T) {
	root := fmt.Errorf("io failure")
	pe := New("txn", CodeTxn, "wal broken").Wrap(root)
	if pe.Unwrap() != root {
		t.Fatal("wrapped cause not retained")
	}
	if got := AsError(fmt.Errorf("context: %w", pe)); got != pe {
		t.Fatal("AsError did not traverse wrap chain")
	}
	if AsError(root) != nil {
		t.Fatal("AsError should return nil for non-project errors")
	}
	if AsError(nil) != nil {
		t.Fatal("AsError(nil) should return nil")
	}
}

func TestWithOffset(t *testing.T) {
	pe := New("stream", CodeOffsetOutOfRange, "x").WithOffset(99)
	if pe.Offset != 99 {
		t.Fatalf("offset = %d, want 99", pe.Offset)
	}
}
