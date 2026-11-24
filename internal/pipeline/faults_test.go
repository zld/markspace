package pipeline_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	idxpkg "media-archiver/internal/index"
	"media-archiver/internal/media"
	"media-archiver/internal/pipeline"
)

func stageStatus(m pipeline.Manifest, name media.Stage) string {
	for _, s := range m.Stages {
		if s.Name == name {
			return s.Status
		}
	}
	return ""
}

func idxLoadForTest(path string) (*idxpkg.Index, error) { return idxpkg.Load(path) }

func TestTruncateFlowsThroughStagesAndReportsExactOffset(t *testing.T) {
	wd, cfg := preparePipeline(t, "truncate")
	expected, err := pipeline.LoadExpected(wd)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := pipeline.Run(*cfg)
	if media.ExitCode(err) != 2 {
		t.Fatalf("exit=%d err=%v", media.ExitCode(err), err)
	}
	me := media.AsError(err)
	if me == nil || me.Code != media.CodeTruncated || me.Offset != expected.CorruptOffset {
		t.Fatalf("error=%+v want offset %d", me, expected.CorruptOffset)
	}
	if rep == nil || rep.CorruptOffset != expected.CorruptOffset || rep.BadBlocks != 1 {
		t.Fatalf("report=%+v", rep)
	}
	m := readManifest(t, wd)
	if m.Status != "rejected" {
		t.Fatalf("manifest status=%s, must not look successful", m.Status)
	}
	for _, stage := range []media.Stage{media.StageParse, media.StageTransform, media.StageIndex} {
		if got := stageStatus(m, stage); got != "completed" {
			t.Fatalf("stage %s status=%s want completed", stage, got)
		}
	}
	if got := stageStatus(m, media.StageVerify); got != "failed" {
		t.Fatalf("verify status=%s want failed", got)
	}
}

func TestBackwardTimestampLookupMatchesExpected(t *testing.T) {
	_, cfg := preparePipeline(t, "backward")
	rep, err := pipeline.Run(*cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.RepairCount != 1 {
		t.Fatalf("repair count=%d decisions=%+v", rep.RepairCount, rep.Repairs)
	}
	d := rep.Repairs[0]
	if d.Kind != "backward_timestamp" || d.OriginalStartFrame == nil || *d.OriginalStartFrame != 480 || d.RepairedStartFrame != 1440 {
		t.Fatalf("unexpected repair: %+v", d)
	}
	if rep.Lookup == nil || rep.Lookup.Entry.BlockIndex != 6 || rep.Lookup.FrameOffset != 64 {
		t.Fatalf("lookup=%+v", rep.Lookup)
	}
}

func TestDuplicateBlockIsRepairedAndLocated(t *testing.T) {
	_, cfg := preparePipeline(t, "duplicate")
	rep, err := pipeline.Run(*cfg)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, d := range rep.Repairs {
		if d.Kind == "duplicate_timestamp" && d.BlockIndex == 4 {
			found = true
			if d.RepairedStartFrame != 960 {
				t.Fatalf("repaired frame=%d want960", d.RepairedStartFrame)
			}
		}
	}
	if !found {
		t.Fatalf("duplicate decision missing: %+v", rep.Repairs)
	}
	if rep.Lookup == nil || rep.Lookup.Entry.BlockIndex != 7 || rep.Lookup.FrameOffset != 64 {
		t.Fatalf("lookup=%+v", rep.Lookup)
	}
}

func TestCorruptIndexRebuildKeepsAudioDigest(t *testing.T) {
	wd, cfg := preparePipeline(t, "bad-index")
	first, err := pipeline.Run(*cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := pipeline.CorruptIndexForDemo(wd); err != nil {
		t.Fatal(err)
	}
	second, err := pipeline.Run(*cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !second.IndexRebuilt {
		t.Fatal("expected index rebuild")
	}
	if second.AudioSHA256 != first.AudioSHA256 {
		t.Fatalf("audio digest changed: %s -> %s", first.AudioSHA256, second.AudioSHA256)
	}
	if second.IndexSHA256 != first.IndexSHA256 {
		t.Fatalf("index hash changed after deterministic rebuild")
	}
	if _, err := idxLoadForTest(filepath.Join(wd, "index", "index.json")); err != nil {
		t.Fatalf("rebuilt index invalid: %v", err)
	}
}

func TestChangedInputInvalidatesAndRecomputes(t *testing.T) {
	wd, cfg := preparePipeline(t, "none")
	if _, err := pipeline.Run(*cfg); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(wd, "input", "sample.wav")
	f, err := os.OpenFile(input, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte{0}); err != nil {
		t.Fatal(err)
	}
	f.Close()

	rep, err := pipeline.Run(*cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"parse", "transform", "index", "verify"} {
		if !slices.Contains(rep.RecomputedStages, stage) {
			t.Fatalf("stage %s was not recomputed: %+v", stage, rep.RecomputedStages)
		}
	}
}
