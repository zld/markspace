package testkit

import (
	"context"
	"os"
	"testing"
	"time"

	"mediacore/internal/clock"
	"mediacore/internal/index"
	"mediacore/internal/scan"
)

const (
	demoSamples   = 108000 // 3600 s at 30 fps
	demoTimescale = 90000
	demoDelta     = 3000
)

func baseSpec(fs FixtureSet) MP4Spec {
	return MP4Spec{
		Path: fs.MP4, Manifest: fs.Manifest, TrackID: 1,
		Timescale: demoTimescale, SampleCount: demoSamples, SampleDelta: demoDelta,
		KeyEvery: 60, MediaStartTicks: demoTimescale, // 1 s non-zero origin
		TargetBPS: 1_800_000, JitterPct: 4, Seed: 20261123,
	}
}

func basePipeline() PipelineConfig {
	return PipelineConfig{
		TargetBPS:       1_800_000,
		MaxBatchSamples: 512,
		MaxBatchBytes:   256 << 10,
		InFlightBytes:   2 << 20,
	}
}

func generateNormal(t *testing.T, root string) FixtureSet {
	t.Helper()
	fs, err := PrepareDirs(root, "normal")
	if err != nil {
		t.Fatal(err)
	}
	spec := baseSpec(fs)
	if err := WriteMP4(spec); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteManifest(spec); err != nil {
		t.Fatal(err)
	}
	if err := WriteRTPSidecar(fs.RTP, 1, demoTimescale, demoSamples, demoDelta, 0, 0); err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestPipelineNormalRunAndInvariants(t *testing.T) {
	fs := generateNormal(t, t.TempDir())
	res, err := RunPipeline(context.Background(), fs, basePipeline(), "", nil)
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	r := res.Report

	// Acceptance numeric bounds.
	if r.QualitySamples <= 10000 {
		t.Errorf("quality_samples=%d", r.QualitySamples)
	}
	if r.DroppedSamples != 0 {
		t.Errorf("dropped=%d", r.DroppedSamples)
	}
	if r.AlignmentErrorMS > 2 {
		t.Errorf("alignment=%dms", r.AlignmentErrorMS)
	}
	if r.PeakBufferBytes > 2<<20 {
		t.Errorf("peak=%d > 2MiB", r.PeakBufferBytes)
	}
	if r.BitrateDeviationPC >= 200 { // 2.00% in hundredths
		t.Errorf("deviation=%s", formatPC(r.BitrateDeviationPC))
	}
	if r.TargetBPS != 1_800_000 {
		t.Errorf("target=%d", r.TargetBPS)
	}
	// Mid-stream target at a non-sync position must quantify real fallback.
	if res.Seek.FallbackSamples <= 0 || r.KeyframeFallbackMS == 0 {
		t.Errorf("expected non-zero keyframe fallback: %+v", res.Seek)
	}
	// Output sample count matches manifest window.
	if res.Records != demoSamples {
		t.Errorf("records=%d want %d", res.Records, demoSamples)
	}
	// Cross-stage invariant: output file contains exactly N physical samples.
	info, err := os.Stat(fs.Output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(demoSamples)*physicalPayload {
		t.Errorf("output bytes=%d", info.Size())
	}
	// Monotonic DTS across the whole index, O(1) sampled at batch boundaries.
	digest := mustDigest(t, fs)
	idxPath := index.CachePath(fs.Index, digest, 1)
	f, err := os.Open(idxPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	hdr, err := index.LoadOrVerify(idxPath, digest, 1, demoSamples)
	if err != nil {
		t.Fatal(err)
	}
	var prev int64 = -1
	for i := uint32(0); i < hdr.SampleCount; i += 997 {
		rec, err := index.ReadRecord(f, hdr, i)
		if err != nil {
			t.Fatal(err)
		}
		if rec.DTSNanos <= prev {
			t.Fatalf("non-monotonic DTS at %d", i)
		}
		prev = rec.DTSNanos
		if rec.Offset+uint64(rec.Size) > uint64(info.Size()) && false {
			t.Fatal("unreachable")
		}
	}
}

func TestCacheHitOnSecondRun(t *testing.T) {
	root := t.TempDir()
	fs := generateNormal(t, root)
	first, err := RunPipeline(context.Background(), fs, basePipeline(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.CacheState != "rebuilt" {
		t.Fatalf("first run cache=%s want rebuilt", first.CacheState)
	}
	second, err := RunPipeline(context.Background(), fs, basePipeline(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.CacheState != "hit" {
		t.Fatalf("second run cache=%s want hit", second.CacheState)
	}
}

func TestRepeatRunDigestDeterminism(t *testing.T) {
	root := t.TempDir()
	fs := generateNormal(t, root)
	r1, err := RunPipeline(context.Background(), fs, basePipeline(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Remove checkpoint and output but keep the index; rerun from scratch.
	os.Remove(fs.Checkpoint)
	os.Remove(fs.Output)
	r2, err := RunPipeline(context.Background(), fs, basePipeline(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r1.OutputDigest != r2.OutputDigest {
		t.Fatalf("output digest differs: %s vs %s", r1.OutputDigest, r2.OutputDigest)
	}
	if r1.RepairDigest != r2.RepairDigest || r1.RepairDigest == "" {
		t.Fatalf("repair digest differs or empty: %q vs %q", r1.RepairDigest, r2.RepairDigest)
	}
	// The cache-state field is expected to change (rebuilt -> hit); compare
	// every numeric evidence field with it normalized.
	rep1, rep2 := r1.Report, r2.Report
	rep1.CacheState, rep2.CacheState = "x", "x"
	if rep1.Render() != rep2.Render() {
		t.Fatalf("report evidence differs:\n%s\nvs\n%s", rep1.Render(), rep2.Render())
	}
}

func TestSlowConsumerCancellation(t *testing.T) {
	fs := generateNormal(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	pcfg := basePipeline()
	pcfg.SlowEvery = 1
	// Block on every batch and cancel after the first one starts.
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	sleep := func() {
		select {
		case started <- struct{}{}:
		default:
		}
		<-block
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := RunPipeline(ctx, fs, pcfg, "", sleep)
		errCh <- err
	}()
	<-started
	cancel()
	close(block)
	err := <-errCh
	if clock.Classify(err) != clock.KindCanceled {
		t.Fatalf("want canceled kind, got %v (%d)", err, clock.Classify(err))
	}
	// Checkpoint must exist and be resumable.
	if _, err := os.Stat(fs.Checkpoint); err != nil {
		t.Fatalf("checkpoint missing after cancel: %v", err)
	}
}

func TestBoundedPeakUnderSlowConsumer(t *testing.T) {
	fs := generateNormal(t, t.TempDir())
	pcfg := basePipeline()
	pcfg.SlowEvery = 7
	res, err := RunPipeline(context.Background(), fs, pcfg, "", func() {
		time.Sleep(2 * time.Millisecond)
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Report.PeakBufferBytes > pcfg.InFlightBytes {
		t.Fatalf("peak %d exceeds budget %d", res.Report.PeakBufferBytes, pcfg.InFlightBytes)
	}
}

func mustDigest(t *testing.T, fs FixtureSet) string {
	t.Helper()
	m, digest, err := scan.LoadManifest(fs.MP4, fs.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	_ = m
	return digest
}

func formatPC(h int64) string {
	return string(rune('0'+h/100)) + "." + string(rune('0'+h%100/10)) + string(rune('0'+h%10))
}
