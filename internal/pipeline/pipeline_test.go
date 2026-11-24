package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"archivist/internal/fault"
	"archivist/internal/fixture"
	"archivist/internal/index"
)

func setup(t *testing.T, n int) (root string, inputs []string) {
	t.Helper()
	root = t.TempDir()
	dir := filepath.Join(root, "inputs")
	opts := fixture.DefaultOptions("clip", fixture.KindNormal)
	opts.SampleCount = n
	s, err := fixture.Build(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	return root, []string{s.Path}
}

func newRunner(t *testing.T, root string, queue int, interrupt int) *Runner {
	t.Helper()
	r, err := New(Options{
		QueueCapacity:  queue,
		WorkDir:        filepath.Join(root, "work"),
		ChunkSamples:   1,
		InterruptAfter: interrupt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRunProducesHashesAndProvenance(t *testing.T) {
	root, inputs := setup(t, 30)
	r := newRunner(t, root, 2, 0)
	results, err := r.Run(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d", len(results))
	}
	res := results[0]
	if res.Status != "ok" {
		t.Fatalf("status = %s (%s)", res.Status, res.Error)
	}
	if len(res.OutputSHA256) != 64 || len(res.ProvenanceSHA256) != 64 || len(res.IndexSHA256) != 64 {
		t.Fatalf("missing digests: %+v", res)
	}
	if res.IndexEntries != 30 || res.RepairedEntries != 0 {
		t.Fatalf("counts = %+v", res)
	}
	if res.CheckpointStage != StageProvenance {
		t.Fatalf("checkpoint stage = %s", res.CheckpointStage)
	}
	if _, err := os.Stat(res.ProvenancePath); err != nil {
		t.Fatal(err)
	}
}

func TestRerunIsIdempotent(t *testing.T) {
	root, inputs := setup(t, 20)
	r := newRunner(t, root, 2, 0)
	first, err := r.Run(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	// A second run reuses the checkpoint entirely.
	second, err := r.Run(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	if first[0].OutputSHA256 != second[0].OutputSHA256 {
		t.Fatalf("output digest changed: %s vs %s", first[0].OutputSHA256, second[0].OutputSHA256)
	}
	if first[0].ProvenanceSHA256 != second[0].ProvenanceSHA256 {
		t.Fatalf("provenance digest changed")
	}
	if len(second[0].ExecutedStages) != 0 {
		t.Fatalf("rerun re-executed %v", second[0].ExecutedStages)
	}
	if len(second[0].ResumedStages) != len(stageOrder) {
		t.Fatalf("resumed stages = %v", second[0].ResumedStages)
	}
}

func TestFreshWorkdirSameDigest(t *testing.T) {
	root, inputs := setup(t, 20)
	a := newRunner(t, root, 2, 0)
	b, err := New(Options{
		QueueCapacity: 2,
		WorkDir:       filepath.Join(root, "work2"),
		ChunkSamples:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ra, err := a.Run(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := b.Run(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	if ra[0].OutputSHA256 != rb[0].OutputSHA256 {
		t.Fatalf("output digest differs across work dirs")
	}
	if ra[0].ProvenanceSHA256 != rb[0].ProvenanceSHA256 {
		t.Fatalf("provenance digest differs across work dirs")
	}
}

func TestResumeAfterInterrupt(t *testing.T) {
	root, inputs := setup(t, 400)
	// Measure a complete run first for the 80% resume budget.
	full := newRunner(t, root, 2, 0)
	start := time.Now()
	fullRes, err := full.Run(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	fullElapsed := time.Since(start)

	root2 := t.TempDir()
	interrupted := newRunner(t, root2, 2, 3)
	res, err := interrupted.Run(context.Background(), inputs)
	if err == nil {
		t.Fatal("expected interruption error")
	}
	if f, ok := fault.AsFault(err); !ok || f.Kind != fault.KindCancel {
		t.Fatalf("err = %v", err)
	}
	if res[0].Status != "error" || !res[0].CheckpointKept {
		t.Fatalf("interrupted result = %+v", res[0])
	}
	if res[0].CheckpointStage != StageWrite {
		t.Fatalf("interrupted at %s", res[0].CheckpointStage)
	}

	// The checkpoint must exist on disk and record the two completed stages.
	cps, _ := filepath.Glob(filepath.Join(root2, "work", "checkpoints", "*.json"))
	if len(cps) != 1 {
		t.Fatalf("checkpoints = %v", cps)
	}
	cp, err := loadCheckpoint(cps[0])
	if err != nil || cp == nil {
		t.Fatalf("checkpoint load: %v", err)
	}
	if !cp.Has(StageScan) || !cp.Has(StageRepair) || !cp.Has(StageWrite) || cp.Has(StageIndex) {
		t.Fatalf("completed = %v", cp.Completed)
	}

	resumed := newRunner(t, root2, 2, 0)
	startResume := time.Now()
	res2, err := resumed.Run(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	resumeElapsed := time.Since(startResume)
	if res2[0].OutputSHA256 != fullRes[0].OutputSHA256 {
		t.Fatalf("resumed output digest %s != uninterrupted %s",
			res2[0].OutputSHA256, fullRes[0].OutputSHA256)
	}
	if res2[0].ProvenanceSHA256 != fullRes[0].ProvenanceSHA256 {
		t.Fatal("resumed provenance digest differs")
	}
	if len(res2[0].ExecutedStages) != 3 {
		t.Fatalf("resume executed %v", res2[0].ExecutedStages)
	}
	for _, s := range res2[0].ExecutedStages {
		if s == StageScan || s == StageRepair || s == StageWrite {
			t.Fatalf("resume re-executed %s", s)
		}
	}
	if resumeElapsed > fullElapsed*4/5 {
		t.Fatalf("resume took %v, full run %v (budget 80%%)", resumeElapsed, fullElapsed)
	}
}

func TestCorruptInputStopsWithoutSuccess(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "inputs")
	opts := fixture.DefaultOptions("bad", fixture.KindBadLength)
	opts.SampleCount = 12
	s, err := fixture.BuildCorrupt(dir, opts, fixture.KindBadLength)
	if err != nil {
		t.Fatal(err)
	}
	r := newRunner(t, root, 2, 0)
	res, err := r.Run(context.Background(), []string{s.Path})
	if err == nil {
		t.Fatal("expected corruption error")
	}
	if fault.ExitCode(err) != 2 {
		t.Fatalf("exit = %d (%v)", fault.ExitCode(err), err)
	}
	if res[0].Status == "ok" {
		t.Fatal("corrupt input was marked ok")
	}
	if res[0].ErrorOffset != s.CorruptAt {
		t.Fatalf("error offset = %d want %d", res[0].ErrorOffset, s.CorruptAt)
	}
	if res[0].OutputSHA256 != "" {
		t.Fatal("corrupt input produced an output digest")
	}
	// No output file may be published for a failed input.
	if _, err := os.Stat(filepath.Join(root, "work", "output", "bad.mp4")); !os.IsNotExist(err) {
		t.Fatalf("unexpected output file: %v", err)
	}
	// The checkpoint is retained so a repaired input can resume.
	if _, err := os.Stat(res[0].CheckpointPath); err != nil {
		t.Fatalf("checkpoint not kept: %v", err)
	}
}

func TestTamperedOutputForcesRebuild(t *testing.T) {
	root, inputs := setup(t, 15)
	r := newRunner(t, root, 2, 0)
	first, err := r.Run(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the published output; the next run must notice and rebuild it.
	if err := os.WriteFile(first[0].OutputPath, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := r.Run(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	if second[0].OutputSHA256 != first[0].OutputSHA256 {
		t.Fatalf("rebuilt output digest %s != %s", second[0].OutputSHA256, first[0].OutputSHA256)
	}
	// Write, index, verify and provenance must all re-run; scan/repair may
	// remain valid because they depend only on the input bytes.
	if len(second[0].ExecutedStages) == 0 {
		t.Fatal("tampered output was accepted")
	}
	for _, s := range second[0].ExecutedStages {
		if s == StageScan || s == StageRepair {
			t.Fatalf("re-executed pure stage %s", s)
		}
	}
}

func TestQueueBoundsConcurrency(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "inputs")
	var inputs []string
	for _, name := range []string{"a", "b", "c", "d", "e", "f"} {
		opts := fixture.DefaultOptions(name, fixture.KindNormal)
		opts.SampleCount = 10
		s, err := fixture.Build(dir, opts)
		if err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, s.Path)
	}
	r := newRunner(t, root, 2, 0)
	results, err := r.Run(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(inputs) {
		t.Fatalf("results = %d", len(results))
	}
	seen := map[string]bool{}
	for i, res := range results {
		if res == nil {
			t.Fatalf("result %d missing", i)
		}
		if res.Name != inputName(inputs[i]) {
			t.Fatalf("result %d name = %s, want %s (order must follow inputs)", i, res.Name, inputName(inputs[i]))
		}
		seen[res.OutputSHA256] = true
	}
	if len(seen) != len(inputs) {
		t.Fatal("distinct inputs produced identical digests")
	}
}

func TestQueueCapacityRequired(t *testing.T) {
	if _, err := New(Options{QueueCapacity: 0, WorkDir: t.TempDir()}); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestCheckpointRejectsChangedParams(t *testing.T) {
	root, inputs := setup(t, 10)
	r := newRunner(t, root, 2, 0)
	if _, err := r.Run(context.Background(), inputs); err != nil {
		t.Fatal(err)
	}
	// A different chunk size is a different output contract, so the run must
	// start over rather than reuse the previous checkpoint.
	alt, err := New(Options{
		QueueCapacity: 2,
		WorkDir:       filepath.Join(root, "work"),
		ChunkSamples:  4,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := alt.Run(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	if len(res[0].ResumedStages) != 0 {
		t.Fatalf("reused checkpoint across parameter change: %v", res[0].ResumedStages)
	}
	if len(res[0].ExecutedStages) != len(stageOrder) {
		t.Fatalf("executed = %v", res[0].ExecutedStages)
	}
	cps, _ := filepath.Glob(filepath.Join(root, "work", "checkpoints", "*.json"))
	if len(cps) != 2 {
		t.Fatalf("expected one checkpoint per parameter set, got %v", cps)
	}
}

func TestIndexArtifactIsQueryable(t *testing.T) {
	root, inputs := setup(t, 60)
	r := newRunner(t, root, 2, 0)
	res, err := r.Run(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	ix, err := index.Load(res[0].IndexPath)
	if err != nil {
		t.Fatal(err)
	}
	hit, err := ix.Query(1, 45000)
	if err != nil {
		t.Fatal(err)
	}
	if !hit.Available || hit.PTS > 45000 || 45000-hit.PTS > 3000 {
		t.Fatalf("hit = %+v", hit)
	}
}
