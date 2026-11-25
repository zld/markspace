package mediaclock

import (
	"testing"
	"time"
)

func TestIntegerConversions(t *testing.T) {
	c, err := New(48000)
	if err != nil {
		t.Fatal(err)
	}
	// Exactly one second.
	if got := c.FramesToNanos(48000); got != int64(time.Second) {
		t.Fatalf("one second = %d ns, want %d", got, time.Second)
	}
	// Half second stays exact.
	if got := c.FramesToNanos(24000); got != 500_000_000 {
		t.Fatalf("half second = %d ns", got)
	}
	// Round trip floors and never drifts on exact boundaries. At 48 kHz a
	// frame is 20833.33... ns, so sub-nanosecond precision is intentionally
	// truncated and at most one frame can be lost on the floor trip.
	for _, f := range []int64{0, 1, 47999, 48000, 96000, 1234567} {
		ns := c.FramesToNanos(f)
		back := c.NanosToFramesFloor(ns)
		if back > f || f-back > 1 {
			t.Fatalf("round trip out of bounds: %d -> %dns -> %d", f, ns, back)
		}
	}
	// Multiples of three frames are exact at 48 kHz (gcd scaling).
	for _, f := range []int64{0, 3, 47997, 48000, 1234566} {
		if got := c.NanosToFramesFloor(c.FramesToNanos(f)); got != f {
			t.Fatalf("exact round trip drifted: %d -> %d", f, got)
		}
	}
}

func TestNewRejectsBadRate(t *testing.T) {
	if _, err := New(0); err == nil {
		t.Fatal("expected error for zero rate")
	}
}

func TestRangeAndMerge(t *testing.T) {
	a := Range{Start: 10, End: 20}
	if a.Frames() != 10 || !a.Contains(15) || a.Contains(20) {
		t.Fatal("range semantics wrong")
	}
	if !Overlaps(Range{0, 12}, a) || Overlaps(Range{0, 10}, a) {
		t.Fatal("overlap semantics wrong for half-open ranges")
	}
	merged := MergeUnions([]Range{{40, 50}, {10, 20}, {18, 30}, {70, 80}})
	want := []Range{{10, 30}, {40, 50}, {70, 80}}
	if len(merged) != len(want) {
		t.Fatalf("merged = %v, want %v", merged, want)
	}
	for i := range want {
		if merged[i] != want[i] {
			t.Fatalf("merged[%d] = %v, want %v (all %v)", i, merged[i], want[i], merged)
		}
	}
}
