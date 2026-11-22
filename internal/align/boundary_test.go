package align

import (
	"errors"
	"testing"

	"dashcheck/internal/model"
)

func tol(s string) model.Rat {
	r, err := model.ParseRat(s)
	if err != nil {
		panic(err)
	}
	return r
}

func demoDesc() *model.Description {
	return &model.Description{
		ContentID: "c1",
		Representations: []model.RepresentationInput{
			{
				ID: "hd", Bandwidth: 3000, Width: 1920, Height: 1080, Timescale: 1000,
				Segments: []model.SegmentRef{
					{Number: 1, StartTick: 0, Duration: 2000, RandomAccess: true},
					{Number: 2, StartTick: 2000, Duration: 2000, RandomAccess: true},
					{Number: 3, StartTick: 4000, Duration: 1200, RandomAccess: true},
					{Number: 4, StartTick: 5200, Duration: 800, RandomAccess: true},
				},
			},
			{
				ID: "sd", Bandwidth: 800, Width: 854, Height: 480, Timescale: 1000,
				Segments: []model.SegmentRef{
					{Number: 1, StartTick: 0, Duration: 2000, RandomAccess: true},
					{Number: 2, StartTick: 2000, Duration: 2000, RandomAccess: true},
					{Number: 3, StartTick: 4000, Duration: 1000, RandomAccess: true},
					{Number: 4, StartTick: 5000, Duration: 1000, RandomAccess: true},
				},
			},
		},
	}
}

func TestAlignHappyPathThreeSwitchPoints(t *testing.T) {
	res, err := Align(demoDesc(), tol("1/1000"), false)
	if err != nil {
		t.Fatalf("expected alignment success, got %v", err)
	}
	if got := len(res.SwitchPoints); got != 3 {
		t.Fatalf("expected 3 switch points, got %d", got)
	}
	wantTicks := []int64{0, 2000, 4000}
	for i, sp := range res.SwitchPoints {
		if sp.BoundaryTick["hd"] != wantTicks[i] || sp.BoundaryTick["sd"] != wantTicks[i] {
			t.Errorf("switch point %d ticks = hd:%d sd:%d, want %d on both",
				i, sp.BoundaryTick["hd"], sp.BoundaryTick["sd"], wantTicks[i])
		}
		if c, _ := sp.SkewMs.Compare(model.ZeroRat); c != 0 {
			t.Errorf("switch point %d skew = %s, want 0", i, sp.SkewMs.String())
		}
	}
	if len(res.Rejections) != 0 {
		t.Errorf("expected no rejections, got %d", len(res.Rejections))
	}
}

func TestAlignTailDivergenceIsNotRejected(t *testing.T) {
	res, err := Align(demoDesc(), tol("1/1000"), false)
	if err != nil {
		t.Fatalf("tail divergence must not fail alignment: %v", err)
	}
	// Tail starts 5200 vs 5000 (200 ms apart) must never appear as a
	// rejection or a fourth switch point.
	for _, r := range res.Rejections {
		if r.Tick == 5200 || r.Tick == 5000 {
			t.Errorf("tail boundary wrongly rejected: %+v", r)
		}
	}
	for _, sp := range res.SwitchPoints {
		if sp.BoundaryTick["hd"] == 5200 {
			t.Errorf("tail boundary must not become a switch point: %+v", sp)
		}
	}
}

func TestAlignSkewBeyondToleranceFailsAndKeepsOriginalTicks(t *testing.T) {
	d := demoDesc()
	d.Representations[1].Segments = []model.SegmentRef{
		{Number: 1, StartTick: 0, Duration: 2000, RandomAccess: true},
		{Number: 2, StartTick: 2000, Duration: 2000, RandomAccess: true},
		{Number: 3, StartTick: 4200, Duration: 2000, RandomAccess: true},
		{Number: 4, StartTick: 6200, Duration: 1000, RandomAccess: true},
	}
	res, err := Align(d, tol("1/20"), false)
	if err == nil {
		t.Fatal("expected alignment error for 200 ms skew under 50 ms tolerance")
	}
	var me *model.Error
	if !errors.As(err, &me) || me.Category != model.CategoryAlignment {
		t.Fatalf("expected categorized alignment error, got %v", err)
	}
	if got := len(res.SwitchPoints); got != 2 {
		t.Fatalf("expected 2 surviving switch points, got %d", got)
	}
	found := false
	for _, r := range res.Rejections {
		if r.Representation == "sd" && r.Tick == 4200 {
			found = true
			if c, _ := r.SkewMs.Compare(tol("200")); c != 0 {
				t.Errorf("skew = %s ms, want 200", r.SkewMs.String())
			}
		}
	}
	if !found {
		t.Error("expected a rejection naming sd at original tick 4200")
	}
	// The later segment must keep its real 6200 tick instead of being
	// index-shifted onto the hd timeline.
	for _, b := range res.Renditions[1].Boundaries {
		if b.Number == 4 && b.Tick != 6200 {
			t.Errorf("segment 4 tick = %d, want original 6200", b.Tick)
		}
	}
}

func TestAlignSkewAtToleranceBoundaryIsInclusive(t *testing.T) {
	d := demoDesc()
	d.Representations[1].Segments[2].StartTick = 4200
	res, err := Align(d, tol("1/5"), false)
	if err != nil {
		t.Fatalf("200 ms skew under 1/5 s tolerance must pass, got %v", err)
	}
	if got := len(res.SwitchPoints); got != 3 {
		t.Fatalf("expected 3 switch points at inclusive boundary, got %d", got)
	}
	if c, _ := res.MaxSkewMs.Compare(tol("200")); c != 0 {
		t.Errorf("max skew = %s, want 200", res.MaxSkewMs.String())
	}
}

func TestAlignNonRandomAccessBoundaryRejected(t *testing.T) {
	d := demoDesc()
	d.Representations[0].Segments[2].RandomAccess = false
	res, err := Align(d, tol("1/1000"), false)
	if err == nil {
		t.Fatal("expected rejection when a matched boundary is not an RA point")
	}
	sawRA := false
	for _, r := range res.Rejections {
		if r.Reason == model.CodeBoundaryNotRA && r.Representation == "hd" {
			sawRA = true
		}
	}
	if !sawRA {
		t.Error("expected BOUNDARY_NOT_RANDOM_ACCESS rejection for hd")
	}
}

func TestAlignDifferentTimescalesUseRationalBase(t *testing.T) {
	d := demoDesc()
	// sd runs at 90 kHz ticks describing the same 0/2s/4s boundaries.
	d.Representations[1].Timescale = 90000
	d.Representations[1].Segments = []model.SegmentRef{
		{Number: 1, StartTick: 0, Duration: 180000, RandomAccess: true},
		{Number: 2, StartTick: 180000, Duration: 180000, RandomAccess: true},
		{Number: 3, StartTick: 360000, Duration: 90000, RandomAccess: true},
		{Number: 4, StartTick: 450000, Duration: 90000, RandomAccess: true},
	}
	res, err := Align(d, tol("1/1000"), false)
	if err != nil {
		t.Fatalf("cross-timescale alignment failed: %v", err)
	}
	if got := len(res.SwitchPoints); got != 3 {
		t.Fatalf("expected 3 switch points, got %d", got)
	}
	if res.SwitchPoints[2].BoundaryTick["sd"] != 360000 {
		t.Errorf("sd tick at 4s = %d, want 360000",
			res.SwitchPoints[2].BoundaryTick["sd"])
	}
}

func TestAlignNonContiguousNumbersStillMatchByTime(t *testing.T) {
	d := demoDesc()
	// Renumber sd segments with a gap: matching must stay time-based.
	d.Representations[1].Segments[1].Number = 7
	d.Representations[1].Segments[2].Number = 8
	d.Representations[1].Segments[3].Number = 9
	res, err := Align(d, tol("1/1000"), false)
	if err != nil {
		t.Fatalf("alignment by time failed: %v", err)
	}
	if got := len(res.SwitchPoints); got != 3 {
		t.Fatalf("expected 3 switch points, got %d", got)
	}
}

func TestAlignNoCommonSwitchPoint(t *testing.T) {
	d := demoDesc()
	// Shift every sd boundary far beyond tolerance of the hd timeline.
	d.Representations[1].Segments[0].StartTick = 30000
	d.Representations[1].Segments[1].StartTick = 39000
	d.Representations[1].Segments[2].StartTick = 49000
	d.Representations[1].Segments[3].StartTick = 55000
	res, err := Align(d, tol("1/1000"), false)
	if err == nil {
		t.Fatal("expected no-common-switch-point error")
	}
	var me *model.Error
	if !errors.As(err, &me) || me.Code != model.CodeNoSwitchPoint {
		t.Fatalf("expected code %s, got %v", model.CodeNoSwitchPoint, err)
	}
	if len(res.SwitchPoints) != 0 {
		t.Errorf("expected zero switch points, got %d", len(res.SwitchPoints))
	}
}

func TestNormalizeCoverageAndRanges(t *testing.T) {
	rens, err := Normalize(demoDesc())
	if err != nil {
		t.Fatal(err)
	}
	if rens[0].CoverageTick != 6000 || rens[1].CoverageTick != 6000 {
		t.Errorf("coverage = %d/%d, want 6000/6000",
			rens[0].CoverageTick, rens[1].CoverageTick)
	}
	if c, _ := rens[0].Duration.Compare(tol("6")); c != 0 {
		t.Errorf("hd duration = %s, want 6", rens[0].Duration.String())
	}
}
