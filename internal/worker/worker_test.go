package worker_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"regress/internal/fault"
	"regress/internal/plan"
	"regress/internal/worker"
)

// fakeExec writes deterministic per-segment files and records concurrency.
type fakeExec struct {
	mu       sync.Mutex
	active   int32
	maxSeen  int32
	failKey  string
}

func (f *fakeExec) Execute(_ context.Context, j worker.Job, stageDir string) (*worker.Result, error) {
	cur := atomic.AddInt32(&f.active, 1)
	defer atomic.AddInt32(&f.active, -1)
	for {
		seen := atomic.LoadInt32(&f.maxSeen)
		if cur <= seen || atomic.CompareAndSwapInt32(&f.maxSeen, seen, cur) {
			break
		}
	}
	if j.Key == f.failKey {
		return nil, errors.New("injected execution failure")
	}
	path := filepath.Join(stageDir, "seg.bin")
	content := []byte(fmt.Sprintf("payload-%d", j.SegmentID))
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return nil, err
	}
	return &worker.Result{
		Job:         j,
		StagedPath:  path,
		OutputBytes: int64(len(content)),
		FrameCount:  12,
	}, nil
}

func makeJobs(n int) []worker.Job {
	jobs := make([]worker.Job, n)
	for i := range jobs {
		jobs[i] = worker.Job{
			Key:       fmt.Sprintf("seg-%d", i),
			SegmentID: i,
			Candidate: plan.Candidate{ID: 2, Label: "rung-2", WorkPerFrame: 250},
		}
	}
	return jobs
}

func TestPoolBoundedConcurrencyAndOrder(t *testing.T) {
	stage := t.TempDir()
	exec := &fakeExec{}
	pool := worker.NewPool(2, exec, stage)
	res, err := pool.Run(context.Background(), makeJobs(6))
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 6 {
		t.Fatalf("results = %d, want 6", len(res))
	}
	for i, r := range res {
		if r.Job.SegmentID != i {
			t.Fatalf("result order broken at %d: got segment %d", i, r.Job.SegmentID)
		}
		if r.Hash == "" {
			t.Fatal("hash not computed before return")
		}
	}
	if exec.maxSeen > 2 {
		t.Fatalf("max concurrency %d exceeded limit 2", exec.maxSeen)
	}
	if exec.maxSeen < 1 {
		t.Fatal("no work observed")
	}
}

func TestPoolFailureCleansStaging(t *testing.T) {
	stage := t.TempDir()
	exec := &fakeExec{failKey: "seg-3"}
	pool := worker.NewPool(3, exec, stage)
	_, err := pool.Run(context.Background(), makeJobs(5))
	if err == nil {
		t.Fatal("expected propagated failure")
	}
	// No half-product directory may survive to be referenced by a manifest.
	entries, _ := os.ReadDir(stage)
	for _, e := range entries {
		if e.Name() == "seg-3" {
			t.Fatal("failed job staging directory was not removed")
		}
	}
}

func TestHashRoundTripAndMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.bin")
	content := []byte("deterministic-bytes")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	h1, err := worker.HashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := worker.HashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatal("hash not stable across reads")
	}
	if err := worker.VerifyHash(path, h1); err != nil {
		t.Fatalf("identical content failed verification: %v", err)
	}
	if err := worker.VerifyHash(path, "deadbeef"); err == nil {
		t.Fatal("expected hash mismatch error")
	} else if !fault.HasCode(err, fault.CodeHashMismatch) {
		t.Fatalf("want hash-mismatch code, got %v", fault.Codes(err))
	}
}

func TestAtomicPublish(t *testing.T) {
	dir := t.TempDir()
	staged := filepath.Join(dir, "stage", "seg.bin")
	dest := filepath.Join(dir, "pub", "seg.bin")
	if err := os.MkdirAll(filepath.Dir(staged), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := worker.AtomicPublish(staged, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != "x" {
		t.Fatalf("published content mismatch: %q err=%v", got, err)
	}
	// No temp file left behind.
	if leftovers, _ := filepath.Glob(dest + ".tmp"); len(leftovers) != 0 {
		t.Fatal("temporary publish file was not renamed away")
	}
}
