package pipeline

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mediasync/internal/fault"
)

func TestConfigClampedToCaps(t *testing.T) {
	p := NewPool(context.Background(), MaxWorkers*10, MaxQueue*10, nil)
	if cap(p.jobs) != MaxQueue {
		t.Fatalf("queue cap %d want %d", cap(p.jobs), MaxQueue)
	}
	if p.Workers() != MaxWorkers {
		t.Fatalf("workers %d want cap %d", p.Workers(), MaxWorkers)
	}
	p.Close()

	p2 := NewPool(context.Background(), 0, 0, nil)
	if cap(p2.jobs) != 1 || p2.Workers() != 1 {
		t.Fatalf("minimums not applied: jobs=%d workers=%d", cap(p2.jobs), p2.Workers())
	}
	p2.Close()
}

func TestMergeStableByIndex(t *testing.T) {
	// High indexes finish first; the merger must still emit input order.
	processor := func(_ context.Context, job Job) (any, error) {
		time.Sleep(time.Duration(20-job.Index) * time.Millisecond)
		return job.Input, nil
	}
	jobs := []Job{
		{Index: 0, Input: "a"},
		{Index: 1, Input: "b"},
		{Index: 2, Input: "c"},
		{Index: 3, Input: "d"},
	}
	results, err := Run(context.Background(), 4, 8, processor, jobs)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 4 {
		t.Fatalf("want 4 results, got %d", len(results))
	}
	// Arrival order must be demonstrably shuffled at least sometimes; assert
	// the merge output is index order regardless.
	for i, r := range results {
		if r.Index != i {
			t.Fatalf("position %d got index %d; merge must be input-order stable", i, r.Index)
		}
		if r.Err != nil || r.Canceled {
			t.Fatalf("result %d must be a clean success: %+v", i, r)
		}
	}
}

func TestP95NearestRank(t *testing.T) {
	results := make([]Result, 20)
	for i := range results {
		results[i] = Result{Index: i, Latency: int64(i + 1)}
	}
	// Nearest rank ceil(0.95*20)=19 -> 19th smallest = 19.
	if got := P95LatencyNanos(results); got != 19 {
		t.Fatalf("p95 = %d want 19", got)
	}
	if P95LatencyNanos(nil) != 0 {
		t.Fatal("empty p95 must be zero")
	}
}

func TestCancelStopsSubmissionAndDrains(t *testing.T) {
	var started atomic.Int32
	block := make(chan struct{})
	processor := func(ctx context.Context, job Job) (any, error) {
		started.Add(1)
		select {
		case <-block:
			return job.Input, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	pool := NewPool(ctx, 2, 4, processor)

	if err := pool.Submit(ctx, Job{Index: 0}); err != nil {
		t.Fatal(err)
	}
	if err := pool.Submit(ctx, Job{Index: 1}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for started.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if started.Load() != 2 {
		t.Fatal("workers did not start in time")
	}

	cancel()
	if err := pool.Submit(ctx, Job{Index: 2}); err == nil {
		t.Fatal("submit after cancel must fail")
	} else if f, ok := fault.AsFault(err); !ok || f.Code != fault.ErrCanceled {
		t.Fatalf("want canceled fault, got %v", err)
	}

	close(block)
	pool.Close() // must return: in-flight workers and sink all exit
	// On cancellation workers may either deliver a canceled result or drop
	// it while exiting; either is acceptable. The invariant is that nothing
	// delivered can be marked successful.
	for _, r := range pool.Collected() {
		if r.Err == nil && !r.Canceled {
			t.Fatalf("no result may be marked successful after cancellation: %+v", r)
		}
	}
}

func TestCanceledResultsExplicitlyMarked(t *testing.T) {
	// When cancellation lands while workers are still deliverable, every
	// collected result must carry the canceled flag (never a silent success).
	ctx, cancel := context.WithCancel(context.Background())
	processor := func(ctx context.Context, job Job) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	pool := NewPool(ctx, 2, 4, processor)
	_ = pool.Submit(ctx, Job{Index: 0})
	_ = pool.Submit(ctx, Job{Index: 1})
	cancel()
	pool.Close()
	for _, r := range pool.Collected() {
		if !r.Canceled {
			t.Fatalf("post-cancel result must be flagged canceled: %+v", r)
		}
	}
}

func TestRunPropagatesProcessorError(t *testing.T) {
	boom := errors.New("boom")
	processor := func(_ context.Context, job Job) (any, error) {
		if job.Index == 1 {
			return nil, boom
		}
		return "ok", nil
	}
	jobs := []Job{{Index: 0}, {Index: 1}, {Index: 2}}
	results, err := Run(context.Background(), 2, 4, processor, jobs)
	if err != nil {
		t.Fatalf("Run must surface per-job errors in results, got %v", err)
	}
	if !errors.Is(results[1].Err, boom) {
		t.Fatalf("want propagated boom error, got %v", results[1].Err)
	}
	if results[0].Err != nil || results[2].Err != nil {
		t.Fatal("sibling jobs must remain successful")
	}
}

func TestNoGoroutineLeakOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	processor := func(ctx context.Context, job Job) (any, error) {
		wg.Done()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	pool := NewPool(ctx, 3, 6, processor)
	wg.Add(3)
	for i := range 3 {
		if err := pool.Submit(ctx, Job{Index: i}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	cancel()
	done := make(chan struct{})
	go func() { pool.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pool.Close hung after cancellation; workers leaked")
	}
}

func TestMoreJobsThanWorkersNoDeadlock(t *testing.T) {
	// Regression: queue/results backpressure must not deadlock when jobs
	// outnumber workers and nobody reads concurrently.
	var n atomic.Int32
	processor := func(_ context.Context, job Job) (any, error) {
		n.Add(1)
		return job.Index, nil
	}
	jobs := make([]Job, 50)
	for i := range jobs {
		jobs[i] = Job{Index: i}
	}
	results, err := Run(context.Background(), 2, 4, processor, jobs)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 50 || n.Load() != 50 {
		t.Fatalf("want 50 completed jobs, got results=%d processed=%d", len(results), n.Load())
	}
}
