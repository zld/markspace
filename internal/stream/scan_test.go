package stream_test

import (
	"testing"

	"assetcheck/internal/errmodel"
	"assetcheck/internal/fixture"
	"assetcheck/internal/stream"
	"assetcheck/internal/wav"
)

func scanNamed(t *testing.T, name string) (*wav.Wav, *stream.Report) {
	t.Helper()
	data := fixture.NamedFixtures()[name]
	w, perr := wav.Parse(data)
	if perr != nil {
		t.Fatalf("parse %s: %v", name, perr)
	}
	rep, err := stream.Scan(w, data, stream.Budget{})
	if err != nil {
		t.Fatalf("scan %s: %v", name, err)
	}
	return w, rep
}

func TestGoodSampleCleanAndThresholdsPass(t *testing.T) {
	_, rep := scanNamed(t, "good.wav")
	if rep.TotalFrames != 96000 {
		t.Fatalf("total frames=%d", rep.TotalFrames)
	}
	if rep.CorruptFrames != 0 || rep.CorruptRatio != 0 || len(rep.CorruptRanges) != 0 {
		t.Fatalf("good sample must be clean: frames=%d ranges=%v", rep.CorruptFrames, rep.CorruptRanges)
	}
	if rep.Interval.MaxFrames != 512 || rep.Interval.MinFrames != 512 {
		t.Fatalf("interval stats wrong: %+v", rep.Interval)
	}
	if len(rep.Keyframes) != 188 {
		t.Fatalf("keyframes=%d, want 188", len(rep.Keyframes))
	}
	ev := stream.Evaluate(rep, stream.DefaultThresholds())
	if !ev.Pass {
		for _, it := range ev.Items {
			t.Errorf("item %s pass=%v actual=%v threshold=%v", it.Key, it.Pass, it.Actual, it.Threshold)
		}
	}
}

func TestCorruptSampleLocatesOneRangeAndFailsRatio(t *testing.T) {
	_, rep := scanNamed(t, "corrupt.wav")
	if len(rep.CorruptRanges) != 1 {
		t.Fatalf("want exactly 1 merged range, got %v", rep.CorruptRanges)
	}
	r := rep.CorruptRanges[0]
	if r.Start != 5120 || r.End != 5632 {
		t.Fatalf("corrupt range = [%d,%d), want [5120,5632)", r.Start, r.End)
	}
	if rep.CorruptFrames != 512 {
		t.Fatalf("corrupt frames=%d", rep.CorruptFrames)
	}
	if rep.CorruptRatio <= 0.001 {
		t.Fatalf("ratio %.6f should exceed 0.001", rep.CorruptRatio)
	}
	var found bool
	for _, f := range rep.Findings {
		if f.Code == errmodel.ErrBlockChecksum && f.Frame == 5120 {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing E2001 at frame 5120: %v", rep.Findings)
	}
	ev := stream.Evaluate(rep, stream.DefaultThresholds())
	if ev.Pass {
		t.Fatal("evaluation must fail on corrupt ratio")
	}
}

func TestTruncatedSampleHasNonEmptyRangeAndOverLimit(t *testing.T) {
	w, rep := scanNamed(t, "truncated.wav")
	if !w.DataTruncated {
		t.Fatal("fixture should have out-of-bounds data chunk")
	}
	if len(rep.CorruptRanges) == 0 || rep.CorruptRanges[0].Frames() <= 0 {
		t.Fatalf("expected non-empty corrupt range, got %v", rep.CorruptRanges)
	}
	if rep.CorruptRatio <= 0.001 {
		t.Fatalf("truncated ratio %.6f must exceed 0.001", rep.CorruptRatio)
	}
	ev := stream.Evaluate(rep, stream.DefaultThresholds())
	if ev.Pass {
		t.Fatal("truncated sample must violate thresholds")
	}
}

func TestDuplicateBoundaryReported(t *testing.T) {
	_, rep := scanNamed(t, "dupbound.wav")
	if len(rep.DuplicateBoundaries) == 0 || rep.DuplicateBoundaries[0] != 1536 {
		t.Fatalf("duplicates=%v, want [1536]", rep.DuplicateBoundaries)
	}
	var code bool
	for _, f := range rep.Findings {
		if f.Code == errmodel.ErrDuplicateBoundary {
			code = true
		}
	}
	if !code {
		t.Fatal("missing E2003 finding")
	}
	// Dedup must keep interval statistics regular.
	if rep.Interval.MaxFrames != 512 {
		t.Fatalf("duplicate leaked into intervals: max=%d", rep.Interval.MaxFrames)
	}
}

func TestScanBudgetStopsAtLastFrame(t *testing.T) {
	data := fixture.NamedFixtures()["good.wav"]
	w, err := wav.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := stream.Scan(w, data, stream.Budget{MaxScanFrames: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.BudgetExceeded || rep.LastFrame != 999 {
		t.Fatalf("budget flag=%v last=%d", rep.BudgetExceeded, rep.LastFrame)
	}
}
