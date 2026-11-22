package pipeline_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"archive/internal/fixtures"
	"archive/internal/pipeline"
	"archive/internal/timeline"
)

func setup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, _, err := fixtures.Generate(dir, 17); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runAll(t *testing.T, dir string) *pipeline.Result {
	t.Helper()
	res, err := pipeline.Run(pipeline.Config{Out: dir, Policy: timeline.DefaultPolicy()})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res
}

func manifestBytes(t *testing.T, dir string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTwoPhaseResume(t *testing.T) {
	dir := setup(t)
	if _, err := pipeline.Run(pipeline.Config{Out: dir, Policy: timeline.DefaultPolicy(), StopAfter: "compose"}); err != nil {
		t.Fatalf("phase 1: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); !os.IsNotExist(err) {
		t.Fatal("manifest must not exist before the render stage")
	}
	res := runAll(t, dir)
	if res.ReusedStages != 2 {
		t.Fatalf("resume reused %d stages, want 2", res.ReusedStages)
	}
	if res.Segments != 8 || res.Frames != 24 {
		t.Fatalf("segments=%d frames=%d, want 8/24", res.Segments, res.Frames)
	}
	rep, err := pipeline.Verify(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() {
		t.Fatalf("verify failed: %+v", rep)
	}
}

func TestIdempotentRerun(t *testing.T) {
	dir := setup(t)
	runAll(t, dir)
	first := manifestBytes(t, dir)
	res := runAll(t, dir)
	if res.ReusedStages != 2 {
		t.Fatalf("rerun reused %d stages, want 2 (render always re-executes)", res.ReusedStages)
	}
	second := manifestBytes(t, dir)
	if string(first) != string(second) {
		t.Fatal("manifest differs across identical reruns")
	}
}

func TestConcurrentIdempotentRetry(t *testing.T) {
	dir := setup(t)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			_, errs[k] = pipeline.Run(pipeline.Config{Out: dir, Policy: timeline.DefaultPolicy()})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent run %d: %v", i, err)
		}
	}
	first := manifestBytes(t, dir)
	runAll(t, dir)
	if string(first) != string(manifestBytes(t, dir)) {
		t.Fatal("manifest changed after concurrent runs")
	}
	rep, err := pipeline.Verify(dir)
	if err != nil || !rep.OK() {
		t.Fatalf("verify after concurrency: %v %+v", err, rep)
	}
}

func TestCorruptFrameTriggersRecompute(t *testing.T) {
	dir := setup(t)
	runAll(t, dir)
	before := manifestBytes(t, dir)
	// Damage one frame on disk: the next run must not reuse it.
	frame := filepath.Join(dir, "frames", "seg-00-frame-000.png")
	f, err := os.OpenFile(frame, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("corruption")); err != nil {
		t.Fatal(err)
	}
	f.Close()
	rep, err := pipeline.Verify(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rep.HashMismatches == 0 {
		t.Fatal("verifier failed to detect the corrupted frame")
	}
	// Re-run: render re-executes and restores the canonical frame.
	runAll(t, dir)
	rep, err = pipeline.Verify(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() {
		t.Fatalf("verify after repair: %+v", rep)
	}
	if string(before) != string(manifestBytes(t, dir)) {
		t.Fatal("manifest changed after repair run")
	}
}

func TestStaleCheckpointInvalidated(t *testing.T) {
	dir := setup(t)
	runAll(t, dir)
	// Change the inputs: all checkpoints bound to the old input hash
	// must be invalidated and every stage recomputed.
	if _, _, err := fixtures.Generate(dir, 99); err != nil {
		t.Fatal(err)
	}
	res := runAll(t, dir)
	if res.ReusedStages != 0 {
		t.Fatalf("reused %d stages after input change, want 0", res.ReusedStages)
	}
	rep, err := pipeline.Verify(dir)
	if err != nil || !rep.OK() {
		t.Fatalf("verify after input change: %v %+v", err, rep)
	}
}
