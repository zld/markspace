package clock

import (
	"testing"

	"mediad/internal/core"
)

func TestFitDriftRecoversKnownLine(t *testing.T) {
	// node = 1.000001 * source + 0.25 exactly.
	slope := core.Rat(1000001, 1000000)
	intercept := core.Rat(1, 4)
	var samples []DriftSample
	for i := int64(0); i < 10; i++ {
		src := core.Rat(i, 1)
		samples = append(samples, DriftSample{Source: src, Node: slope.Mul(src).Add(intercept)})
	}
	m, err := FitDrift("edge-a", samples)
	if err != nil {
		t.Fatal(err)
	}
	m.Applied = true
	if m.Slope.Cmp(slope) != 0 || m.Intercept.Cmp(intercept) != 0 {
		t.Fatalf("fit slope=%s intercept=%s", m.Slope, m.Intercept)
	}
	if m.RatePPB != 1000 {
		t.Fatalf("ppb=%d", m.RatePPB)
	}
	if !m.MaxResidual.Zero() {
		t.Fatalf("residual=%s", m.MaxResidual)
	}
	// Correct inverts the fit: a node reading maps back to source time.
	nodeAt5 := slope.Mul(core.Rat(5, 1)).Add(intercept)
	if got := m.Correct(nodeAt5); got.Cmp(core.Rat(5, 1)) != 0 {
		t.Fatalf("correct=%s want 5/1", got)
	}
}

func TestFitDriftNeedsDistinctSources(t *testing.T) {
	_, err := FitDrift("n", []DriftSample{
		{Source: core.Rat(1, 1), Node: core.Rat(1, 1)},
		{Source: core.Rat(1, 1), Node: core.Rat(2, 1)},
	})
	if err == nil {
		t.Fatal("want error for degenerate samples")
	}
}

func TestDriftNonMonotonicIsClamped(t *testing.T) {
	model := DriftModel{Slope: core.Rat(1, 1), Intercept: core.Zero(), Applied: true}
	c := NewCorrector(model)
	if _, clamped := c.Correct(core.Rat(5, 1), false); clamped {
		t.Fatal("first point must not clamp")
	}
	got, clamped := c.Correct(core.Rat(4, 1), false)
	if !clamped || got.Cmp(core.Rat(5, 1)) != 0 {
		t.Fatalf("got=%s clamped=%v", got, clamped)
	}
	if c.Clamps() != 1 {
		t.Fatalf("clamps=%d", c.Clamps())
	}
}

func TestExtenderAcrossWrap(t *testing.T) {
	var e Extender
	start := uint32(0xFFFFFE00)
	var prev int64
	for i := uint32(0); i < 1000; i++ {
		ext, wrapped := e.Extend(start + i*200)
		if i > 0 && ext <= prev {
			t.Fatalf("not monotonic at %d: %d <= %d", i, ext, prev)
		}
		if i == 0 && wrapped {
			t.Fatal("first sample cannot wrap")
		}
		prev = ext
	}
	if e.Wraps() != 1 {
		t.Fatalf("wraps=%d", e.Wraps())
	}
}

func TestExtenderRejectsBackwardsJump(t *testing.T) {
	// A backwards step from a small raw value cannot be a rollover.
	var e Extender
	e.Extend(1000)
	ext, wrapped := e.Extend(500)
	if wrapped || ext != 500 {
		t.Fatalf("small backwards step misclassified: ext=%d wrapped=%v", ext, wrapped)
	}
	// A large negative signed delta is likewise a genuine regression.
	var f Extender
	f.Extend(1_000_000)
	ext2, wrapped2 := f.Extend(0)
	if wrapped2 || ext2 >= 1_000_000 {
		t.Fatalf("regression misclassified: ext=%d wrapped=%v", ext2, wrapped2)
	}
}

func TestTimelineNormalizesSubtitleAndVFRVideo(t *testing.T) {
	b := NewBuilder("video")
	if err := b.AddTrack(Track{Name: "video", Kind: KindVideo, Asset: "clip.mp4", Scale: core.Rat(1, 30000), Origin: core.Zero()}); err != nil {
		t.Fatal(err)
	}
	if err := b.AddTrack(Track{Name: "subs", Kind: KindSubtitle, Asset: "clip.srt", Scale: core.Rat(1, 1000), Origin: core.Zero()}); err != nil {
		t.Fatal(err)
	}
	// VFR video: 1001-tick then 1501-tick gaps at 30k timescale.
	for _, dts := range []int64{0, 1001, 2502, 3503, 5004} {
		if _, err := b.AddPoint("video", core.Rat(dts, 1), false, core.Span{Start: dts, End: dts + 4}); err != nil {
			t.Fatal(err)
		}
	}
	for _, ms := range []int64{500, 1500, 3000} {
		if _, err := b.AddPoint("subs", core.Rat(ms, 1), false, core.Span{Start: ms, End: ms + 8}); err != nil {
			t.Fatal(err)
		}
	}
	tl, err := b.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := tl.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(tl.Points) != 8 {
		t.Fatalf("points=%d", len(tl.Points))
	}
	for i := 1; i < len(tl.Points); i++ {
		if tl.Points[i].Master.Cmp(tl.Points[i-1].Master) < 0 {
			t.Fatalf("point %d regressed", i)
		}
	}
	vid := tl.TrackByName("video")
	if vid.First.String() != "0/1" || vid.Last.String() != "417/2500" {
		t.Fatalf("video first=%s last=%s", vid.First, vid.Last)
	}
}

func TestTimelineAppliesDriftAndKeepsMonotonic(t *testing.T) {
	slope := core.Rat(1000002, 1000000) // node runs 2 ppm fast
	var samples []DriftSample
	for i := int64(0); i <= 20; i++ {
		src := core.Rat(i, 2)
		samples = append(samples, DriftSample{Source: src, Node: slope.Mul(src)})
	}
	model, err := FitDrift("edge-b", samples)
	if err != nil {
		t.Fatal(err)
	}
	model.Applied = true
	b := NewBuilder("video")
	if err := b.AddTrack(Track{Name: "video", Kind: KindVideo, Asset: "v.mp4", Scale: core.Rat(1, 1000), Origin: core.Zero(), Drift: &model}); err != nil {
		t.Fatal(err)
	}
	// Node clock reads 10 ms fast per 10 s; corrected values must land on the
	// source grid and never regress.
	var prev core.Rational
	for i := int64(0); i <= 20; i++ {
		node := slope.Mul(core.Rat(i, 2))
		p, err := b.AddPoint("video", node.Mul(core.Rat(1000, 1)), false, core.Span{Start: i, End: i + 1})
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 && p.Master.Cmp(prev) < 0 {
			t.Fatalf("regressed at %d: %s < %s", i, p.Master, prev)
		}
		prev = p.Master
	}
	tl, err := b.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if tl.DriftPPB["video"] != 2000 {
		t.Fatalf("ppb=%v", tl.DriftPPB)
	}
}

func TestTimelineRejectsUnknownTrack(t *testing.T) {
	b := NewBuilder("x")
	if _, err := b.AddPoint("nope", core.Zero(), false, core.Span{}); err == nil {
		t.Fatal("want unknown track error")
	}
}
