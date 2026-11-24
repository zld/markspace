package pipeline_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	idxpkg "media-archiver/internal/index"
	"media-archiver/internal/media"
	"media-archiver/internal/pipeline"
)

func preparePipeline(t *testing.T, injection string) (string, *pipeline.Config) {
	t.Helper()
	wd := t.TempDir()
	expected, err := pipeline.PrepareDemoInput(wd, 17, injection)
	if err != nil {
		t.Fatal(err)
	}
	frame := expected.TargetFrame
	cfg := &pipeline.Config{
		WorkDir:     wd,
		Processing:  pipeline.DefaultProcessing(17, injection),
		LookupFrame: &frame,
	}
	return wd, cfg
}

func readManifest(t *testing.T, wd string) pipeline.Manifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(wd, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m pipeline.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestNormalRunIsDeterministicAndAligned(t *testing.T) {
	wd, cfg := preparePipeline(t, "none")
	first, err := pipeline.Run(*cfg)
	if err != nil {
		t.Fatal(err)
	}
	if first.TargetBitrateBPS != 1_536_000 {
		t.Fatalf("target=%d", first.TargetBitrateBPS)
	}
	if !first.Stats.BitrateWithinTolerance(0.1) {
		t.Fatalf("deviation=%f", first.BitrateDeviationPct)
	}
	if first.Frames != 2880 || first.Channels != 2 || first.IndexEntries != 12 {
		t.Fatalf("counts: frames=%d channels=%d entries=%d", first.Frames, first.Channels, first.IndexEntries)
	}
	if first.ManifestAlignmentNS > 20834 {
		t.Fatalf("alignment=%d exceeds one frame", first.ManifestAlignmentNS)
	}
	if first.Lookup == nil || first.Lookup.Entry.BlockIndex != 6 || first.Lookup.FrameOffset != 60 {
		t.Fatalf("lookup=%+v", first.Lookup)
	}
	idx, err := idxpkg.Load(filepath.Join(wd, "index", "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	timeHit, err := idx.LookupTimeNS(first.Lookup.TimeNS)
	if err != nil {
		t.Fatal(err)
	}
	if timeHit.Entry.BlockIndex != first.Lookup.Entry.BlockIndex || timeHit.FrameOffset != first.Lookup.FrameOffset {
		t.Fatalf("time lookup=%+v does not match frame lookup=%+v", timeHit, first.Lookup)
	}

	indexBytes1, err := os.ReadFile(filepath.Join(wd, "index", "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes1, err := os.ReadFile(filepath.Join(wd, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}

	second, err := pipeline.Run(*cfg)
	if err != nil {
		t.Fatal(err)
	}
	indexBytes2, _ := os.ReadFile(filepath.Join(wd, "index", "index.json"))
	manifestBytes2, _ := os.ReadFile(filepath.Join(wd, "manifest.json"))
	if string(indexBytes1) != string(indexBytes2) {
		t.Fatal("index.json is not byte-identical on repeat")
	}
	if string(manifestBytes1) != string(manifestBytes2) {
		t.Fatal("manifest.json is not byte-identical on repeat")
	}
	if second.IndexSHA256 != first.IndexSHA256 || second.ManifestSHA256 != first.ManifestSHA256 {
		t.Fatal("reported hashes changed on repeat")
	}
}

func TestInterruptAfterIndexResumesAndCompletes(t *testing.T) {
	wd, cfg := preparePipeline(t, "none")
	cfg.InterruptAfter = media.StageIndex
	rep, err := pipeline.Run(*cfg)
	if media.ExitCode(err) != 3 {
		t.Fatalf("err=%v exit=%d", err, media.ExitCode(err))
	}
	if rep == nil || rep.Status != "interrupted" {
		t.Fatalf("report=%+v", rep)
	}
	m := readManifest(t, wd)
	if m.Status != "interrupted" || m.LastCompletedStage != "index" {
		t.Fatalf("manifest status=%s last=%s", m.Status, m.LastCompletedStage)
	}

	resumeCfg := pipeline.Config{WorkDir: wd, Resume: true, LookupFrame: cfg.LookupFrame}
	rep, err = pipeline.Run(resumeCfg)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if rep.Status != "completed" || rep.ManifestSHA256 == "" {
		t.Fatalf("report=%+v", rep)
	}
	if rep.Lookup == nil || rep.Lookup.FrameOffset != 60 {
		t.Fatalf("resume lookup=%+v", rep.Lookup)
	}
	m = readManifest(t, wd)
	if m.Status != "completed" {
		t.Fatalf("manifest status=%s", m.Status)
	}
}
