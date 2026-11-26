package batch_test

import (
	"os"
	"path/filepath"
	"testing"

	"mediarepair/internal/batch"
	"mediarepair/internal/cache"
	"mediarepair/internal/index"
	"mediarepair/internal/media"
	"mediarepair/internal/source"
	"mediarepair/internal/testfixture"
)

func runStandard(t *testing.T, interruptAfter int, crashBefore int) (string, *batch.Result, error) {
	t.Helper()
	root := t.TempDir()
	if _, err := testfixture.CreateStandard(root); err != nil {
		t.Fatal(err)
	}
	res, err := batch.Run(batch.Options{
		Root:                   root,
		BlockSize:              4,
		Cache:                  cache.New(),
		InterruptAfterBlocks:   interruptAfter,
		CrashBeforeCommitBlock: crashBefore,
	})
	return root, res, err
}

func TestBatchCompleteAndIdempotentHash(t *testing.T) {
	root, first, err := runStandard(t, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Index.Records) != 12 || len(first.SegmentHashes) != 3 {
		t.Fatalf("records=%d segments=%d", len(first.Index.Records), len(first.SegmentHashes))
	}
	if first.Repair.RepairedCount != 2 || first.PixelMeanRGB != "93,95.5,108.5" {
		t.Fatalf("repaired=%d pixels=%s", first.Repair.RepairedCount, first.PixelMeanRGB)
	}
	if first.Audio == nil || first.Audio.PeakDBFS > -6.0 || first.Audio.PeakDBFS < -6.04 {
		t.Fatalf("audio dbfs=%v", first.Audio.PeakDBFS)
	}
	if first.AlignMaxUS > 11.12 {
		t.Fatalf("align=%.3f", first.AlignMaxUS)
	}

	second, err := batch.Run(batch.Options{Root: root, BlockSize: 4, Cache: cache.New()})
	if err != nil {
		t.Fatal(err)
	}
	if second.IndexSHA256 != first.IndexSHA256 {
		t.Fatalf("index hash changed:\n%s\n%s", first.IndexSHA256, second.IndexSHA256)
	}
	for i := range first.SegmentHashes {
		if first.SegmentHashes[i] != second.SegmentHashes[i] {
			t.Fatalf("segment %d hash differs", i)
		}
	}
}

func TestInterruptAfterBlockAndResumeMatchesCleanRun(t *testing.T) {
	cleanRoot := t.TempDir()
	if _, err := testfixture.CreateStandard(cleanRoot); err != nil {
		t.Fatal(err)
	}
	clean, err := batch.Run(batch.Options{Root: cleanRoot, BlockSize: 4})
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	if _, err := testfixture.CreateStandard(root); err != nil {
		t.Fatal(err)
	}
	_, err = batch.Run(batch.Options{Root: root, BlockSize: 4, InterruptAfterBlocks: 1})
	if !media.HasCode(err, media.CodeInterrupt) {
		t.Fatalf("interrupt err=%v", err)
	}
	cp, err := batch.LoadCheckpoint(root)
	if err != nil || cp == nil || cp.CommittedBlocks != 1 {
		t.Fatalf("checkpoint=%+v err=%v", cp, err)
	}
	partialIdx, err := index.Load(filepath.Join(root, "work", "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(partialIdx.Records) != 4 {
		t.Fatalf("partial index records=%d want 4", len(partialIdx.Records))
	}

	resumed, err := batch.Run(batch.Options{Root: root, BlockSize: 4})
	if err != nil {
		t.Fatal(err)
	}
	if !resumed.Resumed || len(resumed.Index.Records) != 12 {
		t.Fatalf("resumed=%+v", resumed)
	}
	if resumed.IndexSHA256 != clean.IndexSHA256 {
		t.Fatalf("resumed hash %s != clean %s", resumed.IndexSHA256, clean.IndexSHA256)
	}
	for i := range clean.SegmentHashes {
		if resumed.SegmentHashes[i] != clean.SegmentHashes[i] {
			t.Fatalf("segment %d mismatch", i)
		}
	}
}

func TestCrashBeforeCommitLeavesTailAndResumeDiscardsIt(t *testing.T) {
	root := t.TempDir()
	if _, err := testfixture.CreateStandard(root); err != nil {
		t.Fatal(err)
	}
	_, err := batch.Run(batch.Options{Root: root, BlockSize: 4, CrashBeforeCommitBlock: 1})
	if !media.HasCode(err, media.CodeInterrupt) {
		t.Fatalf("crash err=%v", err)
	}
	cp, _ := batch.LoadCheckpoint(root)
	if cp == nil || cp.CommittedBlocks != 1 {
		t.Fatalf("committed=%+v", cp)
	}
	stage := filepath.Join(root, "work", "stage", "segment-001.tmp")
	if !fileExists(stage) {
		t.Fatalf("expected uncommitted stage tail at %s", stage)
	}

	res, err := batch.Run(batch.Options{Root: root, BlockSize: 4})
	if err != nil {
		t.Fatal(err)
	}
	if fileExists(stage) {
		t.Fatal("stale stage tail was not consumed/removed")
	}
	if len(res.Index.Records) != 12 || !res.Checkpoint.Complete {
		t.Fatalf("resume result incomplete: %+v", res.Checkpoint)
	}
}

func TestInputChangeDuringResumeRejected(t *testing.T) {
	root := t.TempDir()
	fx, err := testfixture.CreateStandard(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = batch.Run(batch.Options{Root: root, BlockSize: 4, InterruptAfterBlocks: 1})
	if !media.HasCode(err, media.CodeInterrupt) {
		t.Fatal(err)
	}
	fx.Manifest.Frames[2].Color.Primaries = "bt2020-changed"
	if err := source.SaveManifest(filepath.Join(root, "manifest.json"), fx.Manifest); err != nil {
		t.Fatal(err)
	}
	_, err = batch.Run(batch.Options{Root: root, BlockSize: 4})
	if !media.HasCode(err, media.CodeInputChanged) {
		t.Fatalf("change err=%v", err)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
