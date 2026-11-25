package errmodel

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorCodeFormattingAndOffsets(t *testing.T) {
	e := New(ErrChunkOutOfBounds, "size %d past end", 9).AtOffset(128).AtFrame(7)
	want := "[E1012] offset=128 frame=7: size 9 past end"
	if got := e.Error(); got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
	if e.Code.Category() != CatFormat {
		t.Fatal("1xxx must be format category")
	}
	if New(ErrWALCorrupt, "x").Code.Category() != CatTxn {
		t.Fatal("5xxx must be txn category")
	}
	if New(ErrThresholdCorrupt, "x").Code.Category() != CatBudget {
		t.Fatal("6xxx must be budget category")
	}
}

func TestAsErrorUnwrapAndExitCodes(t *testing.T) {
	inner := New(ErrNonFrameTail, "tail")
	wrapped := fmt.Errorf("parse failed: %w", inner)
	got, ok := AsError(wrapped)
	if !ok || got.Code != ErrNonFrameTail {
		t.Fatalf("AsError did not unwrap: %v ok=%v", got, ok)
	}
	cases := []struct {
		err  error
		code int
	}{
		{nil, 0},
		{New(ErrThresholdMaxGap, "x"), 2},
		{New(ErrBadRIFF, "x"), 3},
		{New(ErrInjected, "x"), 4},
		{errors.New("plain"), 1},
	}
	for _, c := range cases {
		if got := ExitCode(c.err); got != c.code {
			t.Fatalf("ExitCode(%v)=%d, want %d", c.err, got, c.code)
		}
	}
}
