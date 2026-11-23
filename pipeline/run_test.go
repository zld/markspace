package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zld/media-check/media"
	"github.com/zld/media-check/transport"
)

func TestRunProducesDeterministicManifest(t *testing.T) {
	dir := t.TempDir()
	src := writeFixture(t, dir)
	rep, err := Run(context.Background(), runOpts(src, filepath.Join(dir, "run1")))
	if err != nil {
		t.Fatalf("run1: %v", err)
	}
	if rep.Map.Tracks != 2 {
		t.Errorf("tracks = %d", rep.Map.Tracks)
	}
	if !rep.Map.Complete {
		t.Errorf("run incomplete: %+v", rep.Map.Stats)
	}
	if len(rep.Map.Entries) != 8 {
		t.Errorf("segments = %d, want 8", len(rep.Map.Entries))
	}
	if rep.Map.SHA256 == "" {
		t.Error("manifest has no digest")
	}
	// A second run into a different directory, with different pool sizing,
	// must produce the same digest.
	rep2, err := Run(context.Background(), RunOptions{
		Source: src, OutDir: filepath.Join(dir, "run2"), Workers: 1, Queue: 2,
		SamplesPer: 6, Targets: testTargets, Seed: 41, Resume: true,
	})
	if err != nil {
		t.Fatalf("run2: %v", err)
	}
	if rep2.Map.SHA256 != rep.Map.SHA256 {
		t.Errorf("manifest digests differ: %s != %s", rep.Map.SHA256, rep2.Map.SHA256)
	}
	if !rep2.Map.Complete {
		t.Error("second run incomplete")
	}
}

func TestRunCancellationRecordsTasks(t *testing.T) {
	dir := t.TempDir()
	src := writeFixture(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel almost immediately; the manifest must still be written, must not
	// claim completeness, and must account for every submitted segment.
	go func() {
		time.Sleep(2 * time.Millisecond)
		cancel()
	}()
	rep, err := Run(ctx, runOpts(src, filepath.Join(dir, "run")))
	if err != nil {
		t.Fatalf("cancelled run returned error: %v", err)
	}
	if rep.Map.Complete {
		t.Error("cancelled run reported complete")
	}
	// A cancellation before the producer starts is legitimate and submits
	// nothing; otherwise every submitted job must be recorded, and the run
	// must report cancelled tasks rather than silently dropping them.
	if len(rep.Map.Entries) > 0 && rep.Map.Cancelled == 0 {
		t.Error("cancelled run recorded entries but no cancelled tasks")
	}
	if rep.Map.Stats.Submitted != len(rep.Map.Entries) {
		t.Errorf("submitted %d jobs but manifest has %d entries", rep.Map.Stats.Submitted, len(rep.Map.Entries))
	}
	if rep.Map.Segments != len(rep.Segments) {
		t.Errorf("manifest segments = %d, planned = %d", rep.Map.Segments, len(rep.Segments))
	}
	// No manifest-only claim may survive for a cancelled entry.
	for _, e := range rep.Map.Entries {
		if e.Status == StatusCancelled && e.SHA256 != "" {
			t.Errorf("cancelled entry %d retains a payload hash", e.Index)
		}
	}
}

func TestIndexHitRatioAndRandomSeek(t *testing.T) {
	dir := t.TempDir()
	src := writeFixture(t, dir)
	rep, err := Run(context.Background(), runOpts(src, filepath.Join(dir, "run")))
	if err != nil {
		t.Fatal(err)
	}
	tracks, err := rep.TrackMap.MappedTracks(rep.Probe)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	idx, err := media.BuildIndex(tracks, rep.Segments)
	if err != nil {
		t.Fatal(err)
	}
	if idx.Len() == 0 {
		t.Fatal("empty index")
	}
	ratio, checked, err := idx.VerifyRandom(f, tracks, 41, 64)
	if err != nil {
		t.Fatal(err)
	}
	if ratio != 1 {
		t.Errorf("keyframe hit ratio = %f, want 1.0", ratio)
	}
	if len(checked) == 0 {
		t.Error("no keyframes were checked")
	}
	// A shifted index must be detected.
	idx.Keyframes[0].Offset += 4
	if _, _, err := idx.VerifyRandom(f, tracks, 41, 64); err == nil {
		// verification reports misses via the ratio, not an error
		if r, _, _ := idx.VerifyRandom(f, tracks, 41, 64); r == 1 {
			t.Error("corrupted index still reported a perfect hit ratio")
		}
	}
}

func TestRunRTPReportFromDrill(t *testing.T) {
	drill, err := transport.DefaultRTPDrill(41)
	if err != nil {
		t.Fatal(err)
	}
	stats, gaps, _, err := drill.Replay(4, 64, 0x4d454449)
	if err != nil {
		t.Fatal(err)
	}
	report := &RTPReport{
		Received: stats.Received, Duplicates: stats.Duplicates,
		Reordered: stats.Reordered, Lost: stats.Lost,
	}
	if report.Duplicates != 3 || report.Reordered != 5 || report.Lost != 2 {
		t.Errorf("rtp report = %+v", report)
	}
	if len(gaps) != 1 || gaps[0].Start != 0x10000+10 {
		t.Errorf("gaps = %+v", gaps)
	}
}

func TestRunRejectsEscapingOutput(t *testing.T) {
	dir := t.TempDir()
	opts := runOpts("", filepath.Join(dir, "..", "outside"))
	if _, err := Run(context.Background(), opts); err == nil {
		t.Fatal("escaping out dir accepted")
	}
}

func TestPipelineWritesValidManifestJSON(t *testing.T) {
	dir := t.TempDir()
	src := writeFixture(t, dir)
	out := filepath.Join(dir, "run")
	rep, err := Run(context.Background(), runOpts(src, out))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(rep.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var man Manifest
	if err := json.Unmarshal(raw, &man); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if err := man.VerifyManifest(); err != nil {
		t.Fatal(err)
	}
	// The manifest must not leak absolute paths; artifacts are relative.
	for _, e := range man.Entries {
		if filepath.IsAbs(e.Artifact) {
			t.Errorf("absolute artifact path in manifest: %q", e.Artifact)
		}
	}
}
