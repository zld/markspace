package analyze

import (
	"encoding/json"
	"testing"

	"mediad/media"
)

func TestRunContiguousPasses(t *testing.T) {
	segs := []Segment{
		{InputIndex: 0, Name: "a", SampleStart: 0, SampleEnd: 100, Bytes: 100},
		{InputIndex: 1, Name: "b", SampleStart: 100, SampleEnd: 250, Bytes: 150},
	}
	rep := Run(segs)
	if len(rep.Intervals) != 2 {
		t.Fatalf("got %d intervals", len(rep.Intervals))
	}
	if !rep.Intervals[0].QualityPass || !rep.Intervals[1].QualityPass {
		t.Fatal("contiguous clean segments must pass quality")
	}
	if rep.Intervals[1].ExpectedGap != 0 || rep.Intervals[1].ActualGap != 0 {
		t.Fatalf("gap = expected %d actual %d, want 0/0",
			rep.Intervals[1].ExpectedGap, rep.Intervals[1].ActualGap)
	}
	if rep.SamplesTotal != 250 {
		t.Fatalf("samples total = %d, want 250", rep.SamplesTotal)
	}
	if len(rep.Damage) != 0 {
		t.Fatalf("clean timeline must have no damage: %+v", rep.Damage)
	}
}

func TestRunGapAndOverlap(t *testing.T) {
	segs := []Segment{
		{InputIndex: 0, Name: "a", SampleStart: 0, SampleEnd: 100},
		{InputIndex: 1, Name: "b", SampleStart: 140, SampleEnd: 200}, // 40-sample gap
		{InputIndex: 2, Name: "c", SampleStart: 190, SampleEnd: 240}, // 10-sample overlap
	}
	rep := Run(segs)
	if rep.Intervals[1].ActualGap != 40 || rep.Intervals[1].ExpectedGap != 0 {
		t.Fatalf("gap measures wrong: %+v", rep.Intervals[1])
	}
	if rep.Intervals[2].ActualGap != -10 {
		t.Fatalf("overlap measure = %d, want -10", rep.Intervals[2].ActualGap)
	}
	var cats []media.Category
	for _, d := range rep.Damage {
		cats = append(cats, d.Category)
	}
	if len(cats) != 2 || cats[0] != media.CatIntervalGap || cats[1] != media.CatIntervalOverlap {
		t.Fatalf("damage categories = %v, want [gap overlap]", cats)
	}
	// The gap fault must be anchored to the exact missing sample span.
	if rep.Damage[0].SampleStart != 100 || rep.Damage[0].SampleEnd != 140 {
		t.Fatalf("gap range = [%d,%d), want [100,140)",
			rep.Damage[0].SampleStart, rep.Damage[0].SampleEnd)
	}
	if !media.HasFatal(rep.Intervals[1].DamageRanges) {
		t.Fatal("gap must fail the quality gate")
	}
}

func TestRunDeterministicOrdering(t *testing.T) {
	// Submit in scrambled order; same starts must tie-break on input index.
	segs := []Segment{
		{InputIndex: 2, Name: "c", SampleStart: 200, SampleEnd: 300},
		{InputIndex: 0, Name: "a", SampleStart: 0, SampleEnd: 100},
		{InputIndex: 1, Name: "b", SampleStart: 100, SampleEnd: 200},
	}
	first := Run(segs)
	second := Run(segs)
	j1, _ := json.Marshal(first)
	j2, _ := json.Marshal(second)
	if string(j1) != string(j2) {
		t.Fatalf("repeated runs differ:\n%s\n%s", j1, j2)
	}
	got := []int{first.Intervals[0].InputIndex, first.Intervals[1].InputIndex, first.Intervals[2].InputIndex}
	want := []int{0, 1, 2}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("ordering = %v, want %v", got, want)
		}
	}
}

func TestRunTieBreakByInputIndex(t *testing.T) {
	segs := []Segment{
		{InputIndex: 5, Name: "later-index", SampleStart: 0, SampleEnd: 50},
		{InputIndex: 2, Name: "earlier-index", SampleStart: 0, SampleEnd: 60},
	}
	rep := Run(segs)
	if rep.Intervals[0].InputIndex != 2 || rep.Intervals[1].InputIndex != 5 {
		t.Fatalf("same-start tie-break wrong: %d then %d",
			rep.Intervals[0].InputIndex, rep.Intervals[1].InputIndex)
	}
}

func TestRunByteFaultInheritsSampleSpan(t *testing.T) {
	segs := []Segment{
		{
			InputIndex: 0, Name: "damaged", SampleStart: 0, SampleEnd: 100,
			Faults: []media.Fault{media.NewByteFault(media.CatPartialFrame, 76, 79, "tail")},
		},
	}
	rep := Run(segs)
	if rep.Damage[0].SampleStart != 0 || rep.Damage[0].SampleEnd != 100 {
		t.Fatalf("byte-only fault should inherit segment span, got %+v", rep.Damage[0])
	}
	if rep.Intervals[0].QualityPass {
		t.Fatal("partial-frame segment must fail quality")
	}
}

func TestRunFaultsSortedCanonically(t *testing.T) {
	segs := []Segment{{
		InputIndex: 0, Name: "x", SampleStart: 0, SampleEnd: 100,
		Faults: []media.Fault{
			media.NewSampleFault(media.CatIntervalOverlap, 50, 60, "later"),
			media.NewSampleFault(media.CatIntervalGap, 10, 20, "earlier"),
		},
	}}
	rep := Run(segs)
	if rep.Intervals[0].DamageRanges[0].Category != media.CatIntervalGap {
		t.Fatalf("fault order not canonical: %+v", rep.Intervals[0].DamageRanges)
	}
}
