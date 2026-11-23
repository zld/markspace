package render

import (
	"testing"

	"mediad/timeline"
)

func TestFrameGeometryAndDeterministicRender(t *testing.T) {
	tl := &timeline.Timeline{Events: []timeline.Event{
		{Kind: timeline.KindSubtitle, TimeNS: 0, Text: "HELLO"},
		{Kind: timeline.KindChapter, TimeNS: 40_000_000, Text: "CH1"},
	}}
	g := NewGenerator(tl)
	bad := map[int64]bool{80_000_000: true}
	f1 := g.Render(0, 0, nil, 0)
	f2 := g.Render(0, 0, nil, 0)
	if len(f1.Pix) != Width*Height*4 {
		t.Fatalf("frame size=%d want %d", len(f1.Pix), Width*Height*4)
	}
	if FrameHash(f1) != FrameHash(f2) {
		t.Fatal("identical inputs must produce identical frame hashes")
	}
	// Full sequence re-render must be byte-for-byte stable across runs.
	first := make([]string, 5)
	for run := 0; run < 2; run++ {
		for i := int64(0); i < 5; i++ {
			f := g.Render(i, i*40_000_000, bad, 80_000_000)
			h := FrameHash(f)
			if run == 0 {
				first[i] = h
			} else if first[i] != h {
				t.Fatalf("frame %d hash changed between runs", i)
			}
		}
	}
	badFrame := g.Render(2, 80_000_000, bad, 80_000_000)
	if !badFrame.Bad || badFrame.BadTime != 80_000_000 {
		t.Fatal("bad event must be isolated and carry first bad time")
	}
	goodFrame := g.Render(1, 40_000_000, bad, 80_000_000)
	if goodFrame.Bad {
		t.Fatal("healthy frames must not be flagged")
	}
}

func TestPixelStatsStable(t *testing.T) {
	g := NewGenerator(&timeline.Timeline{})
	f := g.Render(7, 280_000_000, nil, 0)
	s1 := PixelStats(f.Pix)
	s2 := PixelStats(f.Pix)
	if s1 != s2 {
		t.Fatal("pixel stats must be deterministic")
	}
	if s1.Mean <= 0 || s1.Mean >= 255 {
		t.Fatalf("mean out of range: %+v", s1)
	}
	if s1.Variance < 0 {
		t.Fatal("variance cannot be negative")
	}
}

func TestBitratePlanningAndThreshold(t *testing.T) {
	// 2.4 Mbps at 25 fps => 12000 bytes/frame; 300 frames over 12s.
	perFrame := PlanBitrate(2_400_000, 25)
	if perFrame != 12_000 {
		t.Fatalf("per-frame budget=%d want 12000", perFrame)
	}
	total := int64(perFrame * 300)
	rep := AggregateBitrate(total, 12_000_000_000, 2_400_000)
	if rep.ActualBPS != 2_400_000 || rep.DeltaPercent != 0 {
		t.Fatalf("exact plan must hit target: %+v", rep)
	}
	if !rep.WithinThreshold(5) {
		t.Fatal("zero deviation must be within 5%")
	}
	over := AggregateBitrate(total*106/100, 12_000_000_000, 2_400_000)
	if over.WithinThreshold(5) {
		t.Fatalf("6%% over must fail threshold: %+v", over)
	}
	under := AggregateBitrate(total*96/100, 12_000_000_000, 2_400_000)
	if !under.WithinThreshold(5) || under.DeltaPercent >= 0 {
		t.Fatalf("4%% under must pass with negative delta: %+v", under)
	}
}

func TestMeasureCarriesHashAndStats(t *testing.T) {
	g := NewGenerator(&timeline.Timeline{})
	f := g.Render(3, 120_000_000, nil, 0)
	m := Measure(f, 12_000, 2_400_000, 40_000_000)
	if m.SHA256 == "" || len(m.SHA256) != 64 {
		t.Fatal("sha256 hex must be present")
	}
	if m.BitrateBPS != 2_400_000 {
		t.Fatalf("per-frame bitrate=%d want 2400000", m.BitrateBPS)
	}
	if m.Quality.Mean == 0 {
		t.Fatal("quality mean must be populated")
	}
}
