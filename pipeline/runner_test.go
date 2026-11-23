package pipeline_test

import (
	"path/filepath"
	"testing"

	"mediacore/audio"
	"mediacore/cache"
	"mediacore/ingest"
	"mediacore/internal/sample"
	"mediacore/media"
	"mediacore/pipeline"
)

func testConfig(t *testing.T, dir string) pipeline.Config {
	t.Helper()
	video, idx := sample.H264(42, 6, 5) // 30 frames at 30 fps = 1 s
	full := true
	color := make([]ingest.ColorMeta, 6)
	for i := range color {
		color[i] = ingest.ColorMeta{Primaries: "bt709", Transfer: "bt709", Matrix: "bt709", FullRange: &full}
	}
	store, err := pipeline.NewStore(filepath.Join(dir, "ckpt"))
	if err != nil {
		t.Fatal(err)
	}
	return pipeline.Config{
		SourceID:     "capture:test",
		Codec:        ingest.CodecH264,
		Video:        video,
		Index:        &idx,
		Audio:        sample.Audio(42, 44100, 6, 1000),
		AudioMeta:    ingest.AudioMeta{Rate: 44100, Channels: 6, Layout: "5.1"},
		Color:        color,
		TargetLayout: audio.LayoutStereo,
		TargetRate:   48000,
		Wire:         sample.LossWire(42, 0.08, 5),
		Store:        store,
		Cache:        cache.New(),
		OutPath:      filepath.Join(dir, "out.bin"),
	}
}

func run(t *testing.T, cfg pipeline.Config) pipeline.Result {
	t.Helper()
	r, err := pipeline.NewRunner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res
}

func TestRunMetricsAndDeterminism(t *testing.T) {
	a := run(t, testConfig(t, t.TempDir()))
	b := run(t, testConfig(t, t.TempDir()))
	if a.OutputDigest != b.OutputDigest {
		t.Fatalf("same seed, different digests: %s vs %s", a.OutputDigest, b.OutputDigest)
	}
	if a.AVSyncP95Ms > 20 {
		t.Fatalf("av_sync_p95 = %d ms, want <= 20", a.AVSyncP95Ms)
	}
	if a.LossRecoveryPct < 90 {
		t.Fatalf("loss_recovery = %.1f%%, want >= 90", a.LossRecoveryPct)
	}
	if a.LatencyP95Ms > 80 {
		t.Fatalf("latency_p95 = %d ms, want <= 80", a.LatencyP95Ms)
	}
	if a.AudioOutFrames != 48000 {
		t.Fatalf("audio out = %d frames, want 48000", a.AudioOutFrames)
	}
	if a.VideoStats.Received == 0 || a.AudioStats.Received == 0 {
		t.Fatalf("no packets recovered: %+v %+v", a.VideoStats, a.AudioStats)
	}
}

func TestCrashResumeSameDigest(t *testing.T) {
	clean := run(t, testConfig(t, t.TempDir()))

	dir := t.TempDir()
	cfg := testConfig(t, dir)
	cfg.CrashAfter = 12 // die mid-stream, after a GOP checkpoint
	crashed := run(t, cfg)
	if !crashed.Crashed {
		t.Fatal("expected simulated crash")
	}
	// resume: same dir, no crash
	cfg2 := testConfig(t, dir)
	resumed := run(t, cfg2)
	if resumed.OutputDigest != clean.OutputDigest {
		t.Fatalf("resumed digest %s != clean %s", resumed.OutputDigest, clean.OutputDigest)
	}
	// checkpoint record must agree with the final digest
	store, _ := pipeline.NewStore(filepath.Join(dir, "ckpt"))
	cp, found, err := store.Load()
	if err != nil || !found {
		t.Fatalf("no checkpoint after resume: %v", err)
	}
	if cp.Stage != "done" || cp.Digest != resumed.OutputDigest {
		t.Fatalf("checkpoint inconsistent: %+v vs %s", cp, resumed.OutputDigest)
	}
}

func TestCorruptIndexPropagatesAndRecovers(t *testing.T) {
	clean := run(t, testConfig(t, t.TempDir()))

	cfg := testConfig(t, t.TempDir())
	cfg.Index.Offsets[0] = cfg.Index.Offsets[0] + 1 // break the declared index
	res := run(t, cfg)
	if res.ErrorCode != media.CodeIndexInvalid {
		t.Fatalf("error_code = %s, want index_invalid", res.ErrorCode)
	}
	if !res.Recovered {
		t.Fatal("pipeline must recover by rebuilding the index from the stream")
	}
	if res.OutputDigest != clean.OutputDigest {
		t.Fatalf("recovered digest %s != clean %s", res.OutputDigest, clean.OutputDigest)
	}
}

func TestCacheForgedHitPropagatesAndRecomputes(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t, dir)
	shared := cache.New()
	cfg.Cache = shared
	clean := run(t, cfg)

	// second run presents a different source identity: every cached hit
	// fails source verification, gets quarantined, and is recomputed
	cfg2 := testConfig(t, dir)
	cfg2.Cache = shared
	cfg2.Store, _ = pipeline.NewStore(filepath.Join(dir, "ckpt2"))
	cfg2.OutPath = filepath.Join(dir, "out2.bin")
	cfg2.SourceID = "capture:other"
	res := run(t, cfg2)
	if len(shared.Quarantined()) == 0 {
		t.Fatal("forged hits were not quarantined")
	}
	if res.OutputDigest != clean.OutputDigest {
		t.Fatalf("recomputed digest %s != clean %s", res.OutputDigest, clean.OutputDigest)
	}
}

func TestColorChangeReflectedInSummary(t *testing.T) {
	cfg := testConfig(t, t.TempDir())
	full := true
	cfg.Color[3] = ingest.ColorMeta{Primaries: "bt2020", Transfer: "pq", Matrix: "bt2020nc", FullRange: &full}
	res := run(t, cfg)
	if res.ColorSummary == "" || res.ColorSummary[:7] != "changed" {
		t.Fatalf("color summary = %q, want changed|...", res.ColorSummary)
	}
}
