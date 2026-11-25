package media

import (
	"errors"
	"testing"
)

func TestClockRoundTripWithinOneSample(t *testing.T) {
	c := NewClock(48000)
	for _, samples := range []int64{0, 1, 7, 999, 48000, 48001, 1234567} {
		ts := c.SamplesToTimestamp(samples, 90000)
		back := c.TimestampToSamples(int64(ts), 90000)
		if AbsInt64(back-samples) > 1 {
			t.Fatalf("round trip drift %d -> %d -> %d", samples, ts, back)
		}
	}
	// Identical rate must be exact.
	for _, samples := range []int64{0, 1, 1000, 48000} {
		ts := c.SamplesToTimestamp(samples, 48000)
		if back := c.TimestampToSamples(int64(ts), 48000); back != samples {
			t.Fatalf("same-rate conversion not exact: %d -> %d -> %d", samples, ts, back)
		}
	}
}

func TestDivRoundHalfAwayFromZero(t *testing.T) {
	cases := []struct {
		n, d, want int64
	}{
		{5, 2, 3}, {-5, 2, -3}, {4, 2, 2}, {-4, 2, -2}, {1, 3, 0}, {-1, 3, 0},
	}
	for _, tc := range cases {
		if got := divRound(tc.n, tc.d); got != tc.want {
			t.Fatalf("divRound(%d,%d)=%d want %d", tc.n, tc.d, got, tc.want)
		}
	}
}

func TestErrorCategory(t *testing.T) {
	err := WrapError(CatInvalidInput, "bad wav", errors.New("eof"))
	if CategoryOf(err) != CatInvalidInput {
		t.Fatal("category lost")
	}
	wrapped := errors.New("outer: " + err.Error())
	if CategoryOf(wrapped) != CatUnrecoverable {
		t.Fatal("plain error should be unrecoverable")
	}
	var me *Error
	if !errors.As(err, &me) || me.Cat != CatInvalidInput {
		t.Fatal("errors.As failed")
	}
}
