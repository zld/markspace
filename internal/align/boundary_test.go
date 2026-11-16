package align

import (
	"errors"
	"strings"
	"testing"

	"dashgen/internal/demo"
	"dashgen/internal/plan"
	"dashgen/internal/timebase"
)

func TestBuildDemoMetrics(t *testing.T) {
	report, err := Build(demo.Build())
	if err != nil {
		t.Fatalf("demo plan must align cleanly, got: %v", err)
	}
	if len(report.Timelines) != 3 {
		t.Fatalf("timelines = %d, want 3", len(report.Timelines))
	}
	if got := timebase.FormatMillis(report.MaxStartOffset); got != "12.500" {
		t.Fatalf("max start offset = %s, want 12.500", got)
	}
	if got := timebase.FormatMillis(report.MaxDurationDelta); got != "1.000" {
		t.Fatalf("max duration delta = %s, want 1.000", got)
	}
	if len(report.SegmentNumbers) != 6 || report.SegmentNumbers[0] != 1 || report.SegmentNumbers[5] != 6 {
		t.Fatalf("segment numbers = %v, want 1..6", report.SegmentNumbers)
	}
	// Demo r360 is gap-free; Gap flags stay false.
	for _, tl := range report.Timelines {
		for _, b := range tl.Bounds {
			if b.Gap {
				t.Fatalf("%s segment %d unexpectedly flagged as gap", tl.RepID, b.Number)
			}
		}
	}
}

func TestGapFlag(t *testing.T) {
	p := &plan.Plan{Representations: []plan.Representation{{
		ID: "r", BandwidthBPS: 1, Timescale: 100,
		Segments: []plan.Segment{
			{Number: 1, StartTicks: 0, DurationTicks: 10},
			{Number: 2, StartTicks: 20, DurationTicks: 10}, // 10-tick hole
		},
	}}}
	report, err := Build(p)
	if err != nil {
		t.Fatalf("gaps are not errors: %v", err)
	}
	if !report.Timelines[0].Bounds[1].Gap {
		t.Fatal("segment 2 must be flagged as a gap for explicit S time")
	}
	if report.Timelines[0].Bounds[0].Gap {
		t.Fatal("first segment cannot be a gap")
	}
}

func TestOutOfOrder(t *testing.T) {
	p := &plan.Plan{Representations: []plan.Representation{{
		ID: "r", BandwidthBPS: 1, Timescale: 100,
		Segments: []plan.Segment{
			{Number: 1, StartTicks: 10, DurationTicks: 10},
			{Number: 2, StartTicks: 5, DurationTicks: 10},
		},
	}}}
	_, err := Build(p)
	var te *TimelineError
	if !errors.As(err, &te) || te.Kind != KindOutOfOrder {
		t.Fatalf("want out_of_order error, got %v", err)
	}
}

func TestOverlap(t *testing.T) {
	p := &plan.Plan{Representations: []plan.Representation{{
		ID: "r", BandwidthBPS: 1, Timescale: 100,
		Segments: []plan.Segment{
			{Number: 1, StartTicks: 0, DurationTicks: 10},
			{Number: 2, StartTicks: 8, DurationTicks: 10},
		},
	}}}
	_, err := Build(p)
	var te *TimelineError
	if !errors.As(err, &te) || te.Kind != KindOverlap {
		t.Fatalf("want overlap error, got %v", err)
	}
	if !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("error should explain overlap: %v", err)
	}
}

func TestMissingSegment(t *testing.T) {
	p := demo.Build()
	p.Representations[0].Segments = p.Representations[0].Segments[:5] // drop number 6
	_, err := Build(p)
	var me *MissingError
	if !errors.As(err, &me) {
		t.Fatalf("want missing error, got %v", err)
	}
	if me.Number != 6 || len(me.Missing) != 1 || me.Missing[0] != "r360" {
		t.Fatalf("unexpected missing detail: %+v", me)
	}
}

func TestCrossTimescaleExactness(t *testing.T) {
	// 3 ticks @300 tps == 10ms; 900 ticks @90000 tps == 10ms exactly.
	p := &plan.Plan{Representations: []plan.Representation{
		{
			ID: "a", BandwidthBPS: 1, Timescale: 300,
			Segments: []plan.Segment{{Number: 1, StartTicks: 0, DurationTicks: 3}},
		},
		{
			ID: "b", BandwidthBPS: 1, Timescale: 90000,
			Segments: []plan.Segment{{Number: 1, StartTicks: 0, DurationTicks: 900}},
		},
	}}
	report, err := Build(p)
	if err != nil {
		t.Fatal(err)
	}
	if !report.MaxStartOffset.IsZero() || !report.MaxDurationDelta.IsZero() {
		t.Fatalf("equivalent boundaries must compare equal: %s %s",
			timebase.FormatMillis(report.MaxStartOffset),
			timebase.FormatMillis(report.MaxDurationDelta))
	}
}
