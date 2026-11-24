package bitstream

import (
	"testing"

	"mediacheck/internal/media"
)

// lineFromStarts builds a video timeline at 1 ms/tick, marking every
// syncAt index as a keyframe.
func lineFromStarts(t *testing.T, id uint32, startsMs []int64, syncAt map[int]bool, inferred map[int]bool) *media.TrackLine {
	tb, err := media.NewTimeBase(1000)
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]media.RawSample, len(startsMs))
	for i, s := range startsMs {
		dur := int64(10)
		if i+1 < len(startsMs) {
			dur = startsMs[i+1] - s
		}
		src := media.SourceDecoded
		if inferred[i] {
			src = media.SourceInferred
		}
		raw[i] = media.RawSample{
			MediaTick:    s,
			DurationTick: uint32(dur),
			IsSync:       syncAt[i],
			Source:       src,
		}
	}
	line, err := media.BuildTrackLine(id, media.KindVideo, tb, raw, nil, len(inferred) > 0)
	if err != nil {
		t.Fatal(err)
	}
	return line
}

func TestAnalyzeRegularCadence(t *testing.T) {
	// 12 frames at 100 ms; keyframes every 4th frame -> 400 ms gaps.
	starts := make([]int64, 12)
	for i := range starts {
		starts[i] = int64(i) * 100
	}
	sync := map[int]bool{0: true, 4: true, 8: true}
	rep := Analyze(lineFromStarts(t, 1, starts, sync, nil))
	if rep.Stats.KeyframeCount != 3 {
		t.Fatalf("keyframes = %d, want 3", rep.Stats.KeyframeCount)
	}
	for _, g := range rep.Stats.GapsMs {
		if g != 400 {
			t.Errorf("gap = %d ms, want 400", g)
		}
	}
	if !rep.Stats.Deterministic {
		t.Error("fully decoded cadence must be deterministic")
	}
	if len(rep.Anomalies) != 0 {
		t.Errorf("regular cadence has anomalies: %+v", rep.Anomalies)
	}
	for _, b := range rep.Boundaries {
		if !b.Decodable {
			t.Error("decoded boundary must be decodable")
		}
	}
}

func TestAnalyzeDetectsCadenceBreak(t *testing.T) {
	// Five regular 400 ms keyframe gaps then one 1200 ms gap.
	starts := make([]int64, 7)
	for i := 0; i < 6; i++ {
		starts[i] = int64(i) * 400
	}
	starts[6] = starts[5] + 1200
	sync := map[int]bool{0: true, 1: true, 2: true, 3: true, 4: true, 5: true, 6: true}
	rep := Analyze(lineFromStarts(t, 1, starts, sync, nil))
	if len(rep.Anomalies) != 1 {
		t.Fatalf("anomalies = %d, want 1 (%+v)", len(rep.Anomalies), rep.Anomalies)
	}
	if rep.Anomalies[0].GapMs != 1200 {
		t.Errorf("anomaly gap = %d, want 1200", rep.Anomalies[0].GapMs)
	}
	if rep.Stats.MaxMs != 1200 {
		t.Errorf("max gap = %d, want 1200", rep.Stats.MaxMs)
	}
}

func TestAnalyzeInferredBoundariesNotDecodable(t *testing.T) {
	starts := []int64{0, 100, 200, 300, 400}
	sync := map[int]bool{0: true, 4: true}
	inferred := map[int]bool{4: true}
	rep := Analyze(lineFromStarts(t, 7, starts, sync, inferred))
	if rep.Stats.Deterministic {
		t.Fatal("gaps touching inferred points must be non-deterministic")
	}
	last := rep.Boundaries[len(rep.Boundaries)-1]
	if last.Decodable {
		t.Fatal("inferred boundary must not be reported decodable")
	}
}

func TestAnalyzeP95NearestRank(t *testing.T) {
	// 20 keyframes with gaps 100..2000 ms in 100 ms steps.
	gaps := make([]int64, 20)
	for i := range gaps {
		gaps[i] = int64(i+1) * 100
	}
	starts := make([]int64, 21)
	sync := map[int]bool{0: true}
	for i, g := range gaps {
		starts[i+1] = starts[i] + g
		sync[i+1] = true
	}
	rep := Analyze(lineFromStarts(t, 3, starts, sync, nil))
	// ceil(0.95*20)=19th ranked gap = 1900 ms.
	if rep.Stats.P95Ms != 1900 {
		t.Errorf("p95 = %d, want 1900", rep.Stats.P95Ms)
	}
	if rep.Stats.MinMs != 100 || rep.Stats.MaxMs != 2000 {
		t.Errorf("min/max = %d/%d, want 100/2000", rep.Stats.MinMs, rep.Stats.MaxMs)
	}
}

func TestAnalyzeSingleKeyframe(t *testing.T) {
	starts := []int64{0, 100, 200}
	rep := Analyze(lineFromStarts(t, 9, starts, map[int]bool{0: true}, nil))
	if rep.Stats.KeyframeCount != 1 || len(rep.Stats.GapsMs) != 0 {
		t.Fatal("single keyframe yields no gaps but must still count the frame")
	}
}
