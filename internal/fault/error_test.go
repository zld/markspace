package fault

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorFormatWithOffset(t *testing.T) {
	err := New(ErrTruncatedUnit, 37, "unit ends at %d, need %d", 37, 64)
	want := "E_TRUNCATED_UNIT offset=37: unit ends at 37, need 64"
	if err.Error() != want {
		t.Fatalf("got %q want %q", err.Error(), want)
	}
}

func TestErrorFormatNoOffset(t *testing.T) {
	err := New(ErrMissingTrack, NoOffset, "track for h265 absent")
	if got := err.Error(); got != "E_MISSING_TRACK: track for h265 absent" {
		t.Fatalf("got %q", got)
	}
}

func TestHasCodeThroughWrap(t *testing.T) {
	base := New(ErrBadHeader, 12, "forbidden bit")
	wrapped := fmt.Errorf("parse h264: %w", base)
	if !HasCode(wrapped, ErrBadHeader) {
		t.Fatal("expected code match through wrap")
	}
	if HasCode(wrapped, ErrTruncatedUnit) {
		t.Fatal("unexpected code match")
	}
	if HasCode(errors.New("plain"), ErrBadHeader) {
		t.Fatal("plain error must not match")
	}
}

func TestExitCodeContract(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, 0},
		{New(ErrBadHeader, 0, "x"), 2},
		{New(ErrIndexOutOfRange, 9, "x"), 2},
		{New(ErrInterrupted, NoOffset, "x"), 3},
		{New(ErrBudgetExceeded, NoOffset, "x"), 3},
	}
	for i, c := range cases {
		if got := ExitCode(c.err); got != c.want {
			t.Errorf("case %d: got %d want %d", i, got, c.want)
		}
	}
}
