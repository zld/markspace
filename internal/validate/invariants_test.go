package validate

import (
	"testing"

	"mediad/internal/clock"
	"mediad/internal/core"
	"mediad/internal/diagnose"
)

func baseInput() Input {
	in := Input{
		Sizes:        map[string]int64{"a.mp4": 1000},
		AssetFormats: map[string]string{"a.mp4": "mp4"},
		Timeline: &clock.Timeline{
			Master: "v",
			Tracks: []clock.Track{{Name: "v", Kind: clock.KindVideo, Asset: "a.mp4"}},
			Points: []clock.Point{
				{Track: "v", Master: core.Zero(), Span: core.Span{Start: 0, End: 4}},
				{Track: "v", Master: core.Rat(1, 1), Span: core.Span{Start: 4, End: 8}},
			},
		},
		Indexes: []IndexRef{{
			Asset: "a.mp4", TrackID: 1, Timescale: 1000, FrameCount: 2,
			MinGap: core.Rat(1, 1), MaxGap: core.Rat(1, 1),
			Keyframes: []KeyRef{
				{Number: 1, DTS: 0, Gap: core.Zero(), Span: core.Span{Start: 100, End: 200}, Offset: 100, Trusted: true},
				{Number: 2, DTS: 1000, Gap: core.Rat(1, 1), Span: core.Span{Start: 200, End: 300}, Offset: 200, Trusted: true},
			},
		}},
		Ranges: []RangeRef{{Asset: "a.mp4", Format: "mp4", Span: core.Span{Start: 500, End: 600}}},
	}
	return in
}

func TestCleanInputPasses(t *testing.T) {
	res := All(baseInput())
	if res.Failed != 0 {
		t.Fatalf("unexpected failures: %+v", res.FailedChecks())
	}
	if res.Total == 0 {
		t.Fatal("no checks ran")
	}
}

func TestTimelineRegressionFails(t *testing.T) {
	in := baseInput()
	in.Timeline.Points[1].Master = core.Rat(-1, 1)
	res := All(in)
	if res.Failed == 0 {
		t.Fatal("want monotonic failure")
	}
	found := false
	for _, c := range res.FailedChecks() {
		if c.Name == "timeline_monotonic" {
			found = true
		}
	}
	if !found {
		t.Fatalf("checks=%+v", res.FailedChecks())
	}
}

func TestKeyframeSpanOutsideAssetFails(t *testing.T) {
	in := baseInput()
	in.Indexes[0].Keyframes[1].Span = core.Span{Start: 900, End: 1200}
	res := All(in)
	found := false
	for _, c := range res.FailedChecks() {
		if c.Name == "index_reference_valid" {
			found = true
		}
	}
	if !found {
		t.Fatalf("failures=%+v", res.FailedChecks())
	}
}

func TestRangeOutsideAssetFails(t *testing.T) {
	in := baseInput()
	in.Ranges[0].Span = core.Span{Start: 900, End: 1200}
	res := All(in)
	found := false
	for _, c := range res.FailedChecks() {
		if c.Name == "corrupt_range_half_open" {
			found = true
		}
	}
	if !found {
		t.Fatalf("failures=%+v", res.FailedChecks())
	}
}

func TestIssueSpanOutsideAssetFails(t *testing.T) {
	in := baseInput()
	in.Issues = diagnose.Issues{{
		Category: diagnose.CatTruncatedBox, Stage: diagnose.StageIngest,
		Format: "mp4", Asset: "a.mp4", Span: core.Span{Start: 990, End: 1100},
	}}
	res := All(in)
	found := false
	for _, c := range res.FailedChecks() {
		if c.Name == "issue_offset_legal" {
			found = true
		}
	}
	if !found {
		t.Fatalf("failures=%+v", res.FailedChecks())
	}
}

func TestNonPositiveKeyframeGapFails(t *testing.T) {
	in := baseInput()
	in.Indexes[0].Keyframes[1].Gap = core.Zero()
	res := All(in)
	found := false
	for _, c := range res.FailedChecks() {
		if c.Name == "keyframe_gap_valid" {
			found = true
		}
	}
	if !found {
		t.Fatalf("failures=%+v", res.FailedChecks())
	}
}

func TestResultIsDeterministic(t *testing.T) {
	a := All(baseInput())
	b := All(baseInput())
	if len(a.Checks) != len(b.Checks) {
		t.Fatalf("check count differs")
	}
	for i := range a.Checks {
		if a.Checks[i] != b.Checks[i] {
			t.Fatalf("check %d differs: %+v vs %+v", i, a.Checks[i], b.Checks[i])
		}
	}
}

func TestDriftAuditRejectsInconsistentPPB(t *testing.T) {
	d := DriftRef{
		Asset: "a.drift", Node: "n", Samples: 10,
		Slope: core.Rat(1001, 1000), Intercept: core.Rat(1, 100), RatePPB: 5000,
		Applied: true,
	}
	res := driftCheck(d)
	if res.Passed {
		t.Fatalf("inconsistent ppb accepted: %+v", res)
	}
	d.RatePPB = 1_000_000
	if !driftCheck(d).Passed {
		t.Fatal("consistent ppb rejected")
	}
}

func TestDriftAuditRejectsDegenerateFit(t *testing.T) {
	for _, d := range []DriftRef{
		{Samples: 1, Slope: core.Rat(1, 1), RatePPB: 0},
		{Samples: 5, Slope: core.Rat(0, 1), RatePPB: 0},
		{Samples: 5, Slope: core.Rat(1, 1), Intercept: core.Rat(100000, 1), RatePPB: 0},
	} {
		if driftCheck(d).Passed {
			t.Fatalf("degenerate drift accepted: %+v", d)
		}
	}
}
