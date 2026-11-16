package manifest

import (
	"testing"

	"dashgen/internal/align"
	"dashgen/internal/demo"
	"dashgen/internal/timebase"
)

func boundsFromDemo(t *testing.T, repID string) []align.SegmentBounds {
	t.Helper()
	report, err := align.Build(demo.Build())
	if err != nil {
		t.Fatalf("align demo: %v", err)
	}
	for _, tl := range report.Timelines {
		if tl.RepID == repID {
			return tl.Bounds
		}
	}
	t.Fatalf("rep %q not found", repID)
	return nil
}

func TestBuildTimelineCompressesContiguous(t *testing.T) {
	bounds := boundsFromDemo(t, "r360") // six equal 100-tick segments
	tl, err := BuildTimeline(bounds)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Entries) != 1 {
		t.Fatalf("entries = %d, want 1 compressed entry: %+v", len(tl.Entries), tl.Entries)
	}
	e := tl.Entries[0]
	if e.Time == nil || *e.Time != 0 || e.Duration != 100 || e.Repeat != 5 {
		t.Fatalf("unexpected entry: %+v", e)
	}
	if got := TimelineSegmentCount(tl); got != 6 {
		t.Fatalf("expanded count = %d, want 6", got)
	}
}

func TestBuildTimelineGapForcesExplicitTime(t *testing.T) {
	bounds := []align.SegmentBounds{
		{Number: 1, StartTicks: 0, DurationTicks: 10, Gap: false},
		{Number: 2, StartTicks: 20, DurationTicks: 10, Gap: true},
	}
	tl, err := BuildTimeline(bounds)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Entries) != 2 {
		t.Fatalf("entries = %d, want 2 (gap must split)", len(tl.Entries))
	}
	if tl.Entries[1].Time == nil || *tl.Entries[1].Time != 20 {
		t.Fatalf("gap entry must carry explicit t=20: %+v", tl.Entries[1])
	}
	if got := TimelineSegmentCount(tl); got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
}

func TestBuildTimelineDurationChange(t *testing.T) {
	bounds := []align.SegmentBounds{
		{Number: 1, StartTicks: 0, DurationTicks: 10},
		{Number: 2, StartTicks: 10, DurationTicks: 10},
		{Number: 3, StartTicks: 20, DurationTicks: 12},
	}
	tl, err := BuildTimeline(bounds)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(tl.Entries))
	}
	if tl.Entries[0].Repeat != 1 || tl.Entries[1].Duration != 12 {
		t.Fatalf("unexpected entries: %+v", tl.Entries)
	}
	if got := TimelineSegmentCount(tl); got != 3 {
		t.Fatalf("count = %d, want 3", got)
	}
}

func TestBuildTimelineRejects(t *testing.T) {
	if _, err := BuildTimeline(nil); err == nil {
		t.Fatal("empty bounds must error")
	}
	bad := []align.SegmentBounds{{Number: 1, DurationTicks: 0}}
	if _, err := BuildTimeline(bad); err == nil {
		t.Fatal("zero duration must error")
	}
	overflow := []align.SegmentBounds{
		{Number: 1, StartTicks: 1<<63 - 1, DurationTicks: 5},
	}
	if _, err := BuildTimeline(overflow); err == nil {
		t.Fatal("overflow end must error")
	}
}

func TestFormatDuration(t *testing.T) {
	r, err := timebase.ParseDuration("12500ms")
	if err != nil {
		t.Fatal(err)
	}
	if got := FormatDuration(r); got != "PT12.500S" {
		t.Fatalf("duration = %q, want PT12.500S", got)
	}
}

func TestExpandTimelineRoundTrip(t *testing.T) {
	for _, id := range []string{"r360", "r720", "r1080"} {
		bounds := boundsFromDemo(t, id)
		tl, err := BuildTimeline(bounds)
		if err != nil {
			t.Fatalf("%s: build: %v", id, err)
		}
		ticks, err := ExpandTimeline(tl)
		if err != nil {
			t.Fatalf("%s: expand: %v", id, err)
		}
		if len(ticks) != len(bounds) {
			t.Fatalf("%s: expanded %d ticks, want %d", id, len(ticks), len(bounds))
		}
		for i, b := range bounds {
			if ticks[i].Start != b.StartTicks || ticks[i].Duration != b.DurationTicks {
				t.Fatalf("%s segment %d: expanded %+v, want start %d dur %d",
					id, b.Number, ticks[i], b.StartTicks, b.DurationTicks)
			}
		}
	}
}

func TestExpandTimelineGapAndErrors(t *testing.T) {
	bounds := []align.SegmentBounds{
		{Number: 1, StartTicks: 0, DurationTicks: 10},
		{Number: 2, StartTicks: 20, DurationTicks: 10, Gap: true},
		{Number: 3, StartTicks: 30, DurationTicks: 12},
	}
	tl, err := BuildTimeline(bounds)
	if err != nil {
		t.Fatal(err)
	}
	ticks, err := ExpandTimeline(tl)
	if err != nil {
		t.Fatal(err)
	}
	want := []TimelineTick{{0, 10}, {20, 10}, {30, 12}}
	for i, w := range want {
		if ticks[i] != w {
			t.Fatalf("tick %d = %+v, want %+v", i, ticks[i], w)
		}
	}
	if _, err := ExpandTimeline(nil); err == nil {
		t.Fatal("nil timeline must error")
	}
	if _, err := ExpandTimeline(&SegmentTimeline{}); err == nil {
		t.Fatal("empty timeline must error")
	}
	noTime := &SegmentTimeline{Entries: []TimelineEntry{{Duration: 10}}}
	if _, err := ExpandTimeline(noTime); err == nil {
		t.Fatal("entry without t must error")
	}
}
