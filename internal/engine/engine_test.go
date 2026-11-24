package engine

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"mediarun/internal/audio"
	"mediarun/internal/fixture"
	"mediarun/internal/pipeline"
)

func newTestRunner(t *testing.T) *Runner {
	t.Helper()
	n, err := audio.NewNormalizer()
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	return NewRunner(n)
}

func ingestNormal(t *testing.T) *IngestReport {
	t.Helper()
	st := fixture.Build(fixture.DefaultSpec(fixture.KindNormal))
	rep, err := IngestTS(st.TS, fixture.AudioPID, fixture.VideoPID)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func baseConfig(t *testing.T, job string, rep *IngestReport) Config {
	dir := t.TempDir()
	return Config{
		JobID:          job,
		InputDigest:    "fixture-normal",
		InputSize:      0,
		PacketCount:    rep.Packets,
		InputPath:      "self://normal",
		WorkDir:        filepath.Join(dir, "work"),
		CheckpointPath: filepath.Join(dir, "checkpoint.json"),
		CacheDir:       filepath.Join(dir, "cache"),
	}
}

func TestRun_FullNormal(t *testing.T) {
	r := newTestRunner(t)
	rep := ingestNormal(t)
	cfg := baseConfig(t, "full", rep)

	res, err := r.Run(context.Background(), rep, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.SegmentsTotal != len(rep.Segments) || res.SegmentsNow != len(rep.Segments) {
		t.Fatalf("segment counts: total=%d now=%d want=%d",
			res.SegmentsTotal, res.SegmentsNow, len(rep.Segments))
	}
	if res.FinalDigest == "" || res.OutputPath == "" {
		t.Fatal("output or digest missing")
	}
	if res.AVSyncMS > 20 {
		t.Fatalf("av sync drift %d ms exceeds 20", res.AVSyncMS)
	}
	if res.P95ProcessMS > 2000 {
		t.Fatalf("p95 processing %d ms exceeds 2000 per segment", res.P95ProcessMS)
	}
	prov, err := pipeline.LoadProvenance(filepath.Join(cfg.WorkDir, "provenance.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !prov.Valid {
		t.Fatal("provenance must be valid after a clean run")
	}
}

func TestRun_ResumeIsIdempotentAndDigestStable(t *testing.T) {
	r := newTestRunner(t)

	// Reference: an uninterrupted full run.
	fullRep := ingestNormal(t)
	fullCfg := baseConfig(t, "full", fullRep)
	full, err := r.Run(context.Background(), fullRep, fullCfg)
	if err != nil {
		t.Fatal(err)
	}

	// Interrupted run: stop after two segments.
	intRep := ingestNormal(t)
	intCfg := baseConfig(t, "interrupted", intRep)
	intCfg.InterruptAfter = 2
	first, err := r.Run(context.Background(), intRep, intCfg)
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("want ErrInterrupted, got %v", err)
	}
	if first.SegmentsNow != 2 || first.Interrupted != true {
		t.Fatalf("interrupt accounting wrong: %+v", first)
	}

	// Resume without the interrupt flag: it must skip the two done segments
	// and never re-emit them.
	resumeCfg := intCfg
	resumeCfg.InterruptAfter = 0
	resumed, err := r.Run(context.Background(), intRep, resumeCfg)
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if resumed.SegmentsResumed != 2 {
		t.Fatalf("resumed segments = %d, want 2", resumed.SegmentsResumed)
	}
	if resumed.DuplicateSegment != 0 {
		t.Fatalf("duplicate segments after resume: %d", resumed.DuplicateSegment)
	}
	if resumed.SegmentsNow != len(intRep.Segments)-2 {
		t.Fatalf("resume processed %d, want %d",
			resumed.SegmentsNow, len(intRep.Segments)-2)
	}
	if resumed.FinalDigest != full.FinalDigest {
		t.Fatalf("resume digest %s != full run digest %s",
			resumed.FinalDigest, full.FinalDigest)
	}
}

func TestRun_CacheHitVerifiesArtifact(t *testing.T) {
	r := newTestRunner(t)

	firstRep := ingestNormal(t)
	cfg := baseConfig(t, "cache-a", firstRep)
	first, err := r.Run(context.Background(), firstRep, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if first.CacheHits != 0 || first.CacheMisses == 0 {
		t.Fatalf("first run must be all misses: hits=%d misses=%d",
			first.CacheHits, first.CacheMisses)
	}

	// Second run, fresh work dir and checkpoint but the SAME cache: every
	// segment should be served from a verified cache hit.
	secondRep := ingestNormal(t)
	secondCfg := baseConfig(t, "cache-b", secondRep)
	secondCfg.CacheDir = cfg.CacheDir
	second, err := r.Run(context.Background(), secondRep, secondCfg)
	if err != nil {
		t.Fatal(err)
	}
	if second.CacheHits != len(secondRep.Segments) || second.CacheMisses != 0 {
		t.Fatalf("second run must be all hits: hits=%d misses=%d",
			second.CacheHits, second.CacheMisses)
	}
	if second.FinalDigest != first.FinalDigest {
		t.Fatal("cached artifacts must reproduce the identical digest")
	}
}

func TestRun_RejectsEmptyIngest(t *testing.T) {
	r := newTestRunner(t)
	cfg := baseConfig(t, "empty", &IngestReport{})
	if _, err := r.Run(context.Background(), &IngestReport{}, cfg); err == nil {
		t.Fatal("run with no segments must fail")
	}
}

func TestRun_PathEscapeRejected(t *testing.T) {
	r := newTestRunner(t)
	rep := ingestNormal(t)
	cfg := baseConfig(t, "escape", rep)
	// An explicit parent-reference component must be refused before any write.
	// Build it by concatenation (filepath.Join would resolve the ".." away).
	cfg.CheckpointPath = cfg.WorkDir + string(filepath.Separator) + ".." +
		string(filepath.Separator) + "outside.json"
	if _, err := r.Run(context.Background(), rep, cfg); !errors.Is(err, pipeline.ErrPathEscape) {
		t.Fatalf("want ErrPathEscape, got %v", err)
	}
}

func TestPercentile95(t *testing.T) {
	if got := percentile95(nil); got != 0 {
		t.Fatalf("empty p95 = %d", got)
	}
	lat := []int64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	if got := percentile95(lat); got != 100 {
		t.Fatalf("p95 = %d, want 100", got)
	}
}

func TestMaxAVSync_CleanStream(t *testing.T) {
	segs := []Segment{
		{Index: 0, FirstPCRMS: 0},
		{Index: 1, FirstPCRMS: 1000},
		{Index: 2, FirstPCRMS: 2000},
	}
	if got := maxAVSync(segs, 1000); got != 0 {
		t.Fatalf("clean stream drift = %d, want 0", got)
	}
	drift := append([]Segment(nil), segs...)
	drift = append(drift, Segment{Index: 3, FirstPCRMS: 3012})
	if got := maxAVSync(drift, 1000); got != 12 {
		t.Fatalf("drift = %d, want 12", got)
	}
}

func TestIngestRTP_RecordsSharedEvents(t *testing.T) {
	st := fixture.Build(fixture.DefaultSpec(fixture.KindLoss))
	rep, err := IngestRTP(st.RTP, 8, fixture.AudioPID, fixture.VideoPID)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, e := range rep.Events {
		kinds[e.Kind]++
	}
	if kinds["loss"] == 0 {
		t.Fatalf("genuine RTP loss must be a shared event: %v", kinds)
	}
	if kinds["rtp_dup"] == 0 {
		t.Fatalf("RTP duplicate must be recorded distinctly: %v", kinds)
	}
	if rep.RecoveredPackets == 0 || rep.Lost == 0 {
		t.Fatalf("expected both recovery and loss, got %+v", rep)
	}
}

func TestIngestTS_PCRWrapNoAmbiguity(t *testing.T) {
	st := fixture.Build(fixture.DefaultSpec(fixture.KindPCRWrap))
	rep, err := IngestTS(st.TS, fixture.AudioPID, fixture.VideoPID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rep.Events {
		if e.Kind == "ambiguous_ts" {
			t.Fatalf("PCR wrap produced an ambiguous event: %s", e.Describe())
		}
	}
	if len(rep.Segments) == 0 {
		t.Fatal("no segments reassembled after PCR wrap")
	}
}
