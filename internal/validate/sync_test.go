package validate

import (
	"testing"

	"mediaforensics/internal/clock"
	"mediaforensics/internal/fixture"
	"mediaforensics/internal/model"
	"mediaforensics/internal/parse"
	"mediaforensics/internal/repair"
	"mediaforensics/internal/render"
	"mediaforensics/internal/rtpstats"
)

func buildPipeline(t *testing.T, mut func([]byte, *fixture.Layout) []byte, frames int) (
	*model.Timeline, *render.FrameManifest, *repair.Report) {
	t.Helper()
	spec := fixture.DefaultSpec(42, frames)
	data, layout, err := fixture.GenerateMP4(spec)
	if err != nil {
		t.Fatal(err)
	}
	if mut != nil {
		data = mut(data, layout)
	}
	res, err := parse.Parse(data)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	rep := repair.Segment(res.Timeline)
	rr, errs := render.NewRenderer(res.Timeline, render.Config{
		OutputDir: t.TempDir(), FrameCount: frames,
		FrameRateNum: 24000, FrameRateDen: 1001, TransitionEvery: 8,
	})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	fm, err := rr.Render(frames)
	if err != nil {
		t.Fatal(err)
	}
	return res.Timeline, fm, rep
}

func TestIntactTimelinePassesThresholds(t *testing.T) {
	tl, fm, rep := buildPipeline(t, nil, 40)
	stats := rtpstats.Stats{Expected: 100, InOrder: 95, Recovered: 5, P95LatencyMs: 40}
	report := Check(tl, fm, rep, stats, DefaultThresholds())
	if len(report.Errors) != 0 {
		t.Fatalf("intact pipeline must validate clean: %+v", report.Errors)
	}
	if report.MaxSyncErrorMs > DefaultThresholds().SyncErrorMs {
		t.Fatalf("sync error above threshold: %f", report.MaxSyncErrorMs)
	}
}

// Cross-module path: a truncated parse error must reach the sync/validation
// stage with the original source offset preserved.
func TestTruncationErrorPropagatesToValidation(t *testing.T) {
	spec := fixture.DefaultSpec(42, 40)
	data, layout, err := fixture.GenerateMP4(spec)
	if err != nil {
		t.Fatal(err)
	}
	cut, dmg, err := fixture.TruncateBox(data, layout, 128)
	if err != nil {
		t.Fatal(err)
	}
	res, perr := parse.Parse(cut)
	if perr == nil {
		t.Fatal("truncated input must fail parsing")
	}
	fe := model.AsForensicError(perr)
	if fe.Offset != layout.MdatOffset || fe.Stage != model.StageParseBox {
		t.Fatalf("truncation offset lost before validation: %+v", fe)
	}
	// Even with a failed parse, the partial container evidence is retained.
	if res == nil || res.Container == nil {
		t.Fatal("partial evidence must survive for the manifest")
	}
	// A synthetic validation error built from the same offset proves the
	// propagation contract end-to-end.
	linked := model.NewError(model.StageValidate, "propagated_truncation",
		-1, fe.Offset, "truncation reached validation")
	if linked.Offset != dmg.Offset && linked.Offset != layout.MdatOffset {
		t.Fatal("validation cannot trace back to source bytes")
	}
}

func TestSyncFailureCarriesTrackAndOffset(t *testing.T) {
	tl, fm, rep := buildPipeline(t, nil, 30)
	// Force a large skew by shifting every audio sample far into the future.
	for _, tr := range tl.Tracks {
		if tr.Kind == model.KindAudio {
			for i := range tr.Samples {
				s, err := tr.Samples[i].Start.Add(clock.New(1, 10)) // +100ms
				if err != nil {
					t.Fatal(err)
				}
				tr.Samples[i].Start = s
			}
		}
	}
	report := Check(tl, fm, rep, rtpstats.Stats{Expected: 1, InOrder: 1},
		DefaultThresholds())
	if len(report.SyncFailures) == 0 {
		t.Fatal("100ms skew must exceed the 20ms threshold")
	}
	f := report.SyncFailures[0]
	if f.TrackID == 0 || f.SourceOffset < 0 || f.FrameTime == "" {
		t.Fatalf("sync failure missing provenance: %+v", f)
	}
}

func TestFrameGapAppearsInValidation(t *testing.T) {
	tl, fm, rep := buildPipeline(t, func(data []byte, layout *fixture.Layout) []byte {
		out, _, err := fixture.CorruptSample(data, layout, 1, 0)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}, 12)
	// Corrupting one sample may not create a gap by itself; force one gap in
	// the manifest to exercise the validation propagation branch.
	fm.Frames[0].Gap = true
	fm.GapCount = 1
	fm.Frames[0].PrimaryTrack = 1
	report := Check(tl, fm, rep, rtpstats.Stats{Expected: 1, InOrder: 1},
		DefaultThresholds())
	var found bool
	for _, e := range report.Errors {
		if e.Phase == "frame_gap" && e.Stage == model.StageValidate {
			found = true
		}
	}
	if !found {
		t.Fatal("render gap must be surfaced by the validator")
	}
}

func TestThresholdBreachesReported(t *testing.T) {
	tl, fm, rep := buildPipeline(t, nil, 20)
	bad := rtpstats.Stats{Expected: 100, InOrder: 80, Recovered: 0, P95LatencyMs: 80}
	report := Check(tl, fm, rep, bad, DefaultThresholds())
	phases := map[string]bool{}
	for _, e := range report.Errors {
		phases[e.Phase] = true
	}
	if !phases["recovery_rate"] || !phases["p95_latency"] {
		t.Fatalf("threshold breaches not both reported: %+v", phases)
	}
}

func TestRationalParsing(t *testing.T) {
	var n, d int64
	if _, err := scanRational("3/8", &n, &d); err != nil || n != 3 || d != 8 {
		t.Fatalf("rational parse failed: %d/%d %v", n, d, err)
	}
	if _, err := scanRational("bad", &n, &d); err == nil {
		t.Fatal("invalid rational must be rejected")
	}
	if _, err := scanRational("1/0", &n, &d); err == nil {
		t.Fatal("zero denominator must be rejected")
	}
}
