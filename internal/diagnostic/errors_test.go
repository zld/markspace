package diagnostic_test

import (
	"errors"
	"testing"

	"mediacore/internal/diagnostic"
)

func TestExitCodeMapping(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, 0},
		{diagnostic.Wrap(diagnostic.ErrTruncated, diagnostic.Location{}, "x"), 2},
		{diagnostic.Wrap(diagnostic.ErrIllegalSample, diagnostic.Location{}, "x"), 2},
		{diagnostic.Wrap(diagnostic.ErrCheckpoint, diagnostic.Location{}, "x"), 3},
		{errors.New("plain"), 1},
	}
	for _, tc := range cases {
		if got := diagnostic.ExitCode(tc.err); got != tc.want {
			t.Fatalf("ExitCode(%v)=%d want %d", tc.err, got, tc.want)
		}
	}
}

func TestPositionOfPlainError(t *testing.T) {
	p := diagnostic.PositionOf(errors.New("plain"))
	if p.Message != "plain" || p.ByteOffset != -1 {
		t.Fatalf("position=%+v", p)
	}
}
