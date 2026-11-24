package model

import (
	"strings"
	"testing"
)

func TestTimebaseConversion(t *testing.T) {
	tb := Timebase{Timescale: 90000}
	if got := tb.TickToNanos(90000); got != 1_000_000_000 {
		t.Fatalf("one second: got %d", got)
	}
	if got := tb.TickToNanos(1); got != 11111 {
		t.Fatalf("one tick floors to 11111ns: got %d", got)
	}
	if got := tb.NanosToTick(1_000_000_000); got != 90000 {
		t.Fatalf("nanos to tick: got %d", got)
	}
	if got := tb.TickError(11111); got != 0 {
		t.Fatalf("sub-tick error floors to zero: got %d", got)
	}
	if got := tb.TickError(22223); got != 2 {
		t.Fatalf("two-tick error: got %d", got)
	}
}

func TestTimelineValidateFragment(t *testing.T) {
	tl := New()
	tt := &TrackTimeline{
		Key:      "v1",
		Declared: NanosRange{StartNS: 0, EndNS: 1_000_000_000},
		Segments: []Segment{{TrackKey: "v1", StartTick: 0, Duration: 90000}},
	}
	if err := tl.AddTrack(tt); err != nil {
		t.Fatal(err)
	}
	if err := tl.ValidateFragment("v1", Range{StartTick: 0, Duration: 3000}); err != nil {
		t.Fatalf("inside timeline: %v", err)
	}
	if err := tl.ValidateFragment("v1", Range{StartTick: 89000, Duration: 3000}); err == nil {
		t.Fatal("expected out-of-range fragment to be rejected")
	} else if !strings.Contains(err.Error(), "outside the explicit timeline") {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := tl.ValidateFragment("nope", Range{}); err == nil {
		t.Fatal("expected unknown track to be rejected")
	}
}

func TestTimelineExtentsAndDuplicate(t *testing.T) {
	tl := New()
	if err := tl.AddTrack(&TrackTimeline{Key: "a", Declared: NanosRange{StartNS: 100, EndNS: 200}}); err != nil {
		t.Fatal(err)
	}
	if err := tl.AddTrack(&TrackTimeline{Key: "b", Declared: NanosRange{StartNS: 50, EndNS: 500}}); err != nil {
		t.Fatal(err)
	}
	if tl.OriginNS != 50 || tl.EndNS != 500 {
		t.Fatalf("extent = [%d,%d]", tl.OriginNS, tl.EndNS)
	}
	if err := tl.AddTrack(&TrackTimeline{Key: "a"}); err == nil {
		t.Fatal("expected duplicate track error")
	}
	if got := tl.SortedKeys(); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("sorted keys: %v", got)
	}
}

func TestRangeContains(t *testing.T) {
	outer := Range{StartTick: 10, Duration: 10}
	if !outer.Contains(Range{StartTick: 10, Duration: 5}) {
		t.Fatal("start-aligned child must be contained")
	}
	if !outer.Contains(Range{StartTick: 15, Duration: 5}) {
		t.Fatal("end-aligned child must be contained")
	}
	if outer.Contains(Range{StartTick: 15, Duration: 6}) {
		t.Fatal("overhang must not be contained")
	}
}
