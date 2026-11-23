package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"mediaarchive/internal/timeline"
	"mediaarchive/internal/track"
)

func TestPoolClampsConfig(t *testing.T) {
	p := NewPool(PoolConfig{Workers: 100000, Queue: -5})
	cfg := p.Config()
	if cfg.Workers != MaxWorkers || cfg.Queue != 1 {
		t.Fatalf("clamp wrong: %+v", cfg)
	}
}

func TestPoolRunsAllJobs(t *testing.T) {
	var done int64
	pool := NewPool(DefaultPoolConfig())
	jobs := make(chan Job, 8)
	go func() {
		for i := 0; i < 40; i++ {
			jobs <- func(context.Context) error {
				atomic.AddInt64(&done, 1)
				return nil
			}
		}
		close(jobs)
	}()
	if err := pool.Run(context.Background(), jobs); err != nil {
		t.Fatalf("Run returned %v", err)
	}
	if done != 40 {
		t.Fatalf("done=%d want 40", done)
	}
}

func TestPoolCancellationPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pool := NewPool(PoolConfig{Workers: 2, Queue: 4})
	jobs := make(chan Job)
	errCh := make(chan error, 1)
	go func() { errCh <- pool.Run(ctx, jobs) }()

	started := make(chan struct{})
	jobs <- func(c context.Context) error {
		close(started)
		<-c.Done() // block until cancellation reaches the worker
		return nil
	}
	<-started
	cancel()
	// Contract: once cancelled the producer stops submitting and closes the
	// jobs channel; the pool drains and unwinds rather than deadlocking.
	close(jobs)
	err := <-errCh
	if err == nil {
		t.Fatal("cancelled pool must return an error")
	}
	if !errors.Is(err, timeline.ErrCancelled) {
		t.Fatalf("want ErrCancelled, got %v", err)
	}
}

func TestPoolCancelledProducerStillDrained(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pool := NewPool(PoolConfig{Workers: 2, Queue: 8})
	jobs := make(chan Job, 8)
	errCh := make(chan error, 1)
	go func() { errCh <- pool.Run(ctx, jobs) }()

	started := make(chan struct{})
	jobs <- func(c context.Context) error {
		close(started)
		<-c.Done()
		return nil
	}
	<-started
	// Queue several jobs, then cancel; the pool must release them so the
	// producer can finish and close.
	for i := 0; i < 5; i++ {
		jobs <- func(context.Context) error { return nil }
	}
	cancel()
	close(jobs)
	if err := <-errCh; !errors.Is(err, timeline.ErrCancelled) {
		t.Fatalf("want ErrCancelled, got %v", err)
	}
}

func TestPoolWriterFailureReturnsFirstRootCause(t *testing.T) {
	root := errors.New("archive writer disk full")
	pool := NewPool(PoolConfig{Workers: 2, Queue: 2})
	jobs := make(chan Job)
	errCh := make(chan error, 1)
	go func() { errCh <- pool.Run(context.Background(), jobs) }()

	jobs <- func(context.Context) error { return root }
	// Keep producing; a healthy pool stops dispatch and drains the queue.
	for i := 0; i < 10; i++ {
		jobs <- func(context.Context) error { return errors.New("later failure") }
	}
	close(jobs)
	if err := <-errCh; !errors.Is(err, root) {
		t.Fatalf("want first root cause, got %v", err)
	}
}

func TestPoolRepeatedRunsIndependent(t *testing.T) {
	pool := NewPool(PoolConfig{Workers: 3, Queue: 8})
	for run := 0; run < 3; run++ {
		var done int64
		jobs := make(chan Job, 16)
		go func() {
			for i := 0; i < 20; i++ {
				i := i
				jobs <- func(context.Context) error {
					atomic.AddInt64(&done, int64(i+1))
					return nil
				}
			}
			close(jobs)
		}()
		if err := pool.Run(context.Background(), jobs); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if done != 210 { // sum 1..20
			t.Fatalf("run %d done=%d want 210", run, done)
		}
	}
}

func TestFeedHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	queue := make(chan Job, 1)
	queue <- func(context.Context) error { return nil }
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	ok := Feed(ctx, queue, []Job{
		func(context.Context) error { return nil },
		func(context.Context) error { return nil },
	})
	if ok {
		t.Fatal("Feed must report false on cancellation")
	}
}

func TestMergeDeterministicOrdering(t *testing.T) {
	// Inputs are per-track monotonic in source order, but interleaved across
	// tracks; the merger imposes the global deterministic order.
	evs := []Event{
		{Track: 1, TimeNanos: 50, SourceSeq: 0, Kind: track.KindVideo},
		{Track: 2, TimeNanos: 100, SourceSeq: 1, Kind: track.KindAudio},
		{Track: 1, TimeNanos: 100, SourceSeq: 2, Kind: track.KindVideo},
		{Track: 1, TimeNanos: 200, SourceSeq: 3, Kind: track.KindVideo},
	}
	out := MustMerge(evs)
	// Expected order: time 50, then time 100 tie resolved by track (1<2),
	// then track 1 seq 2, then time 200.
	wantSeq := []int64{0, 2, 1, 3}
	for i, w := range wantSeq {
		if out[i].SourceSeq != w {
			t.Fatalf("merge order=%v want seq %v", out, wantSeq)
		}
	}
	// A different arrival permutation of the same events must produce the
	// identical ordering byte-for-byte (same source sequence numbers).
	ref := MustMerge([]Event{evs[3], evs[1], evs[2], evs[0]})
	for i := range ref {
		if ref[i].SourceSeq != out[i].SourceSeq {
			t.Fatal("merge is not deterministic across input permutations")
		}
	}
}

func TestMergeSameTimeSameTrackStableBySeq(t *testing.T) {
	// Equal time and track: SourceSeq is the final tie-breaker.
	evs := []Event{
		{Track: 1, TimeNanos: 100, SourceSeq: 9},
		{Track: 1, TimeNanos: 100, SourceSeq: 4},
		{Track: 1, TimeNanos: 100, SourceSeq: 7},
	}
	out := MustMerge(evs)
	if out[0].SourceSeq != 4 || out[1].SourceSeq != 7 || out[2].SourceSeq != 9 {
		t.Fatalf("tie-break order=%v", out)
	}
}

func TestMergeRejectsTrackTimeRegression(t *testing.T) {
	evs := []Event{
		{Track: 1, TimeNanos: 100, SourceSeq: 0},
		{Track: 1, TimeNanos: 90, SourceSeq: 1},
	}
	if err := ValidateSourceOrder(evs); !errors.Is(err, timeline.ErrOutOfBounds) {
		t.Fatalf("want monotonic error, got %v", err)
	}
	// Merge itself still succeeds on the same events because sorting
	// resolves the interleaving deterministically.
	if _, err := Merge(evs); err != nil {
		t.Fatalf("Merge must remain permutation-agnostic: %v", err)
	}
}

func TestMergeTimeRangeAndCounts(t *testing.T) {
	evs := MustMerge([]Event{
		{Track: 1, TimeNanos: 10, SourceSeq: 0},
		{Track: 1, TimeNanos: 20, SourceSeq: 1},
		{Track: 2, TimeNanos: 25, SourceSeq: 2},
	})
	first, last := TimeRange(evs)
	if first != 10 || last != 25 {
		t.Fatalf("range=%d,%d", first, last)
	}
	counts := CountByTrack(evs)
	if counts[1] != 2 || counts[2] != 1 {
		t.Fatalf("counts=%v", counts)
	}
}

func TestPoolCancelErrClassification(t *testing.T) {
	// A job that itself returns the shared cancellation sentinel.
	pool := NewPool(DefaultPoolConfig())
	jobs := make(chan Job, 1)
	jobs <- func(context.Context) error {
		return timeline.NewError("pipeline", "job", -1, timeline.RecoverNone,
			"stopped", timeline.ErrCancelled)
	}
	close(jobs)
	err := pool.Run(context.Background(), jobs)
	if !errors.Is(err, timeline.ErrCancelled) {
		t.Fatalf("want ErrCancelled, got %v", fmt.Sprint(err))
	}
}
