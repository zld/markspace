package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"pwf/internal/fixture"
	"pwf/internal/quality"
)

func healthySources(t *testing.T, dir string) []Source {
	t.Helper()
	op := fixture.OpusStreamSpec("cam_a", 12)
	op.DropSeqs = []uint16{103}
	if _, _, err := fixture.BuildRTPStream(dir, op); err != nil {
		t.Fatal(err)
	}
	aac := fixture.AACStreamSpec("cam_b", 10)
	if _, _, err := fixture.BuildRTPStream(dir, aac); err != nil {
		t.Fatal(err)
	}
	mp4Path := filepath.Join(dir, "cam_c.mp4")
	sizes := make([]uint32, 12)
	for i := range sizes {
		sizes[i] = 36
	}
	if err := fixture.WriteMP4File(mp4Path, 44100, 1024, sizes); err != nil {
		t.Fatal(err)
	}
	return []Source{
		{Track: "cam_a", Kind: KindRTP, Path: filepath.Join(dir, "cam_a.rtp"), Codec: "opus", ClockHz: 48000},
		{Track: "cam_b", Kind: KindRTP, Path: filepath.Join(dir, "cam_b.rtp"), Codec: "aac", ClockHz: 44100},
		{Track: "cam_c", Kind: KindMP4, Path: mp4Path, Codec: "aac"},
	}
}

func TestRunFullPipeline(t *testing.T) {
	dir := t.TempDir()
	sources := healthySources(t, dir)
	out, err := Run(Options{
		WorkDir: filepath.Join(dir, "work"), Sources: sources,
		Thresholds: quality.DefaultThresholds(), Workers: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Measurements) != 3 {
		t.Fatalf("expected 3 measurements, got %d", len(out.Measurements))
	}
	if out.Manifest == nil || len(out.Manifest.RunSHA256) != 64 {
		t.Fatal("full run must produce a 64-char manifest digest")
	}
	if out.Plan == nil || len(out.Plan.Tracks) != 3 {
		t.Fatal("plan must cover every track")
	}
}

func TestResumeProducesIdenticalManifest(t *testing.T) {
	dir := t.TempDir()
	sources := healthySources(t, dir)
	// Reference full run in its own directory, used only to obtain a stable
	// stop key and the expected final manifest digest.
	full, err := Run(Options{WorkDir: filepath.Join(dir, "full"), Sources: sources,
		Thresholds: quality.DefaultThresholds(), Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	stopKey := full.Results[1].Key

	// The interrupted and resumed runs share a separate, initially empty
	// work directory so the stop node is genuinely executed there.
	work := filepath.Join(dir, "interrupted")
	interrupted, err := Run(Options{WorkDir: work, Sources: sources,
		Thresholds: quality.DefaultThresholds(), Workers: 2, StopAfterKey: stopKey})
	if err != nil {
		t.Fatal(err)
	}
	if !interrupted.Interrupted {
		t.Fatal("run with StopAfterKey must report interruption")
	}

	resumed, err := Run(Options{WorkDir: work, Sources: sources,
		Thresholds: quality.DefaultThresholds(), Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ReusedNodes < 2 {
		t.Fatalf("resume must reuse at least 2 nodes, got %d", resumed.ReusedNodes)
	}
	if resumed.Manifest.RunSHA256 != full.Manifest.RunSHA256 {
		t.Fatalf("resume digest %s != full digest %s",
			resumed.Manifest.RunSHA256, full.Manifest.RunSHA256)
	}
}

func TestResumeRecomputesWhenCheckpointTampered(t *testing.T) {
	dir := t.TempDir()
	sources := healthySources(t, dir)
	work := filepath.Join(dir, "work")
	first, err := Run(Options{WorkDir: work, Sources: sources,
		Thresholds: quality.DefaultThresholds(), Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	// Tamper one output; resume must recompute it but still converge to the
	// same manifest digest.
	outputs := filepath.Join(work, "checkpoint", "outputs")
	entries, _ := os.ReadDir(outputs)
	if len(entries) == 0 {
		t.Fatal("no outputs to tamper")
	}
	target := filepath.Join(outputs, entries[0].Name())
	if err := os.WriteFile(target, []byte("corrupted-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	resumed, err := Run(Options{WorkDir: work, Sources: sources,
		Thresholds: quality.DefaultThresholds(), Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Manifest.RunSHA256 != first.Manifest.RunSHA256 {
		t.Fatal("recomputed run must converge to the same manifest digest")
	}
}
