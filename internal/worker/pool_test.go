package worker

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func job(dir, id, params string, runs *atomic.Int64) Job {
	out := filepath.Join(dir, id+".out")
	return Job{
		ID:         id,
		ParamsHash: params,
		OutputPath: out,
		Run: func(ctx context.Context) (Result, error) {
			runs.Add(1)
			if err := os.WriteFile(out, []byte("data-"+id), 0o644); err != nil {
				return Result{}, err
			}
			return Result{SHA256: "hash-" + id, SizeBytes: 5, DurationMS: 8000}, nil
		},
	}
}

func TestPoolRunsJobsConcurrently(t *testing.T) {
	dir := t.TempDir()
	var runs atomic.Int64
	p := New(2)
	jobs := []Job{job(dir, "a", "p1", &runs), job(dir, "b", "p1", &runs)}
	res, err := p.Run(context.Background(), jobs)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || runs.Load() != 2 {
		t.Fatalf("results=%d runs=%d", len(res), runs.Load())
	}
	if res[0].JobID != "a" || res[1].JobID != "b" {
		t.Fatalf("results out of order: %+v", res)
	}
	if res[0].Reused || res[1].Reused {
		t.Fatal("first run must not be marked reused")
	}
}

func TestPoolRerunReusesMarkers(t *testing.T) {
	dir := t.TempDir()
	var runs atomic.Int64
	p := New(2)
	jobs := []Job{job(dir, "a", "p1", &runs), job(dir, "b", "p1", &runs)}
	if _, err := p.Run(context.Background(), jobs); err != nil {
		t.Fatal(err)
	}
	// Second run with identical params: nothing re-executed.
	res, err := p.Run(context.Background(), jobs)
	if err != nil {
		t.Fatal(err)
	}
	if runs.Load() != 2 {
		t.Fatalf("re-executed jobs: runs=%d", runs.Load())
	}
	for _, r := range res {
		if !r.Reused {
			t.Fatalf("job %s not marked reused", r.JobID)
		}
	}
	// Changed params invalidate the marker.
	jobs[0] = job(dir, "a", "p2", &runs)
	if _, err := p.Run(context.Background(), jobs); err != nil {
		t.Fatal(err)
	}
	if runs.Load() != 3 {
		t.Fatalf("param change must re-run exactly one job: runs=%d", runs.Load())
	}
}

func TestPoolDeduplicatesJobIDs(t *testing.T) {
	dir := t.TempDir()
	var runs atomic.Int64
	p := New(4)
	j := job(dir, "a", "p1", &runs)
	res, err := p.Run(context.Background(), []Job{j, j, j})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || runs.Load() != 1 {
		t.Fatalf("duplicate jobs counted: results=%d runs=%d", len(res), runs.Load())
	}
}

func TestPoolEmptyIDRejected(t *testing.T) {
	p := New(1)
	if _, err := p.Run(context.Background(), []Job{{ID: ""}}); err == nil {
		t.Fatal("expected error for empty job ID")
	}
}
