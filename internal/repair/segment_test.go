package repair

import (
	"testing"

	"mediaforensics/internal/fixture"
	"mediaforensics/internal/model"
	"mediaforensics/internal/parse"
)

func parseTimeline(t *testing.T, mut func([]byte, *fixture.Layout) []byte) (*model.Timeline, *fixture.Layout) {
	t.Helper()
	spec := fixture.DefaultSpec(7, 24)
	data, layout, err := fixture.GenerateMP4(spec)
	if err != nil {
		t.Fatal(err)
	}
	if mut != nil {
		data = mut(data, layout)
	}
	res, err := parse.Parse(data)
	if err != nil {
		t.Fatalf("unexpected parse failure: %v", err)
	}
	return res.Timeline, layout
}

func TestSegmentIntactTimeline(t *testing.T) {
	tl, _ := parseTimeline(t, nil)
	rep := Segment(tl)
	if len(rep.Regions) != 0 || rep.Excluded != 0 {
		t.Fatalf("intact input must have no regions: %+v", rep.Regions)
	}
	for id, cov := range rep.TrackCoverage {
		if cov.Valid != cov.Total {
			t.Fatalf("track %d lost samples without damage: %+v", id, cov)
		}
	}
}

func TestCorruptSampleRegionAndGuardMargin(t *testing.T) {
	tl, _ := parseTimeline(t, func(data []byte, layout *fixture.Layout) []byte {
		out, _, err := fixture.CorruptSample(data, layout, 1, 10)
		if err != nil {
			t.Fatal(err)
		}
		return out
	})
	rep := Segment(tl)
	if len(rep.Regions) != 1 {
		t.Fatalf("want one coalesced region, got %d", len(rep.Regions))
	}
	rg := rep.Regions[0]
	if rg.Kind != RegionCorrupt || rg.FirstSample != 10 || rg.LastSample != 10 {
		t.Fatalf("region metadata wrong: %+v", rg)
	}
	tr := tl.Track(1)
	// Samples 9 and 11 become low-confidence safety margins; sample 10 is none.
	if tr.Samples[9].Confidence != model.ConfidenceLow ||
		tr.Samples[11].Confidence != model.ConfidenceLow {
		t.Fatal("adjacent samples must be downgraded as safety margin")
	}
	intervals := SafeIntervals(tr)
	for _, iv := range intervals {
		if iv[0] <= 10 && iv[1] > 10 {
			t.Fatalf("damaged sample must not lie inside a safe interval: %+v", intervals)
		}
	}
	if rep.Errors[0].Offset != rg.SourceOffset {
		t.Fatal("repair error must preserve source offset")
	}
}

func TestTruncatedRegionClassified(t *testing.T) {
	tl, _ := parseTimeline(t, nil)
	// Simulate truncation by voiding the last three audio samples' offsets.
	audio := tl.Track(3)
	for i := len(audio.Samples) - 3; i < len(audio.Samples); i++ {
		audio.Samples[i].Offset = -1
		audio.Samples[i].Confidence = model.ConfidenceNone
	}
	rep := Segment(tl)
	var found bool
	for _, rg := range rep.Regions {
		if rg.TrackID == 3 && rg.Kind == RegionGap {
			found = true
			if rg.LastSample != len(audio.Samples)-1 {
				t.Fatalf("gap must cover tail samples: %+v", rg)
			}
		}
	}
	if !found {
		t.Fatalf("truncated tail not classified as gap: %+v", rep.Regions)
	}
}

func TestDamagedDataNeverOnValidTimeline(t *testing.T) {
	tl, _ := parseTimeline(t, func(data []byte, layout *fixture.Layout) []byte {
		out, _, err := fixture.CorruptSample(data, layout, 2, 0)
		if err != nil {
			t.Fatal(err)
		}
		return out
	})
	Segment(tl)
	for _, tr := range tl.Tracks {
		for _, s := range tr.ValidSamples() {
			if s.Corrupt || s.Confidence == model.ConfidenceLow || s.Confidence == model.ConfidenceNone {
				t.Fatalf("damaged/guarded sample leaked onto timeline: %+v", s)
			}
		}
	}
}

func TestRepairDeterministic(t *testing.T) {
	build := func() *model.Timeline {
		tl, _ := parseTimeline(t, func(data []byte, layout *fixture.Layout) []byte {
			out, _, _ := fixture.CorruptSample(data, layout, 1, 5)
			return out
		})
		Segment(tl)
		return tl
	}
	a, b := build(), build()
	for i := range a.Tracks {
		for j := range a.Tracks[i].Samples {
			x, y := a.Tracks[i].Samples[j], b.Tracks[i].Samples[j]
			if x.Confidence != y.Confidence || x.Basis != y.Basis {
				t.Fatalf("repair result differs at track %d sample %d", i, j)
			}
		}
	}
}
