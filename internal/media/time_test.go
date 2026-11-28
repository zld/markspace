package media

import (
	"strings"
	"testing"
)

func TestToNanosRoundsAndKeepsErrorUnderOneMillisecond(t *testing.T) {
	tb, err := NewTimebase(90000, "x.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	// 3003 ticks at 90 kHz is 33.36666... ms.
	ns, err := tb.ToNanos(3003)
	if err != nil {
		t.Fatal(err)
	}
	const want = 33_366_667
	if ns != want {
		t.Fatalf("ToNanos(3003)=%d want %d", ns, want)
	}
	// Accumulating one thousand such segments must not drift more than 1 ms.
	var total int64
	for i := 0; i < 1000; i++ {
		total += ns
	}
	if err := CheckDrift(33_366_666*1000, total, "test"); err != nil {
		t.Fatal(err)
	}
}

func TestZeroTimescaleRejected(t *testing.T) {
	_, err := NewTimebase(0, "dash.mpd")
	if err == nil {
		t.Fatal("expected zero timescale error")
	}
	if !strings.Contains(err.Error(), "zero timescale") || !strings.Contains(err.Error(), "dash.mpd") {
		t.Fatalf("error lacks location: %v", err)
	}
}

func TestBuildTimelineDetectsDriftBeyondOneMillisecond(t *testing.T) {
	tb := Timebase{Timescale: 1000}
	segs, err := BuildTimeline([]int64{1_000_000_000, 2_000_000_000}, tb, 3_000_000_001)
	if err != nil {
		t.Fatalf("1ns declared drift must be tolerated: %v", err)
	}
	if segs[1].Start != 1_000_000_000 || segs[1].End() != 3_000_000_000 {
		t.Fatalf("unexpected boundaries: %+v", segs)
	}
	if _, err := BuildTimeline([]int64{1_000_000_000, 2_000_000_000}, tb, 3_002_000_000); err == nil {
		t.Fatal("2ms declared drift must be rejected")
	}
}

func TestToNanosOverflowStopsConversion(t *testing.T) {
	tb := Timebase{Timescale: 1}
	if _, err := tb.ToNanos(1 << 62); err == nil {
		t.Fatal("expected overflow error")
	}
}
