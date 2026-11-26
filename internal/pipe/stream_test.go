package pipe

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunPeakNeverExceedsWorkersPlusQueue(t *testing.T) {
	cfg := Config{Workers: 2, Queue: 2}
	var active atomic.Int64
	var maxActive atomic.Int64

	stats, err := Run(context.Background(), cfg, 40,
		func(_ context.Context, i int) (Chunk[int], error) {
			return Chunk[int]{Index: i, Items: []int{i}}, nil
		},
		func(_ context.Context, c Chunk[int]) error {
			n := active.Add(1)
			for {
				m := maxActive.Load()
				if n <= m || maxActive.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			active.Add(-1)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Processed != 40 {
		t.Fatalf("processed=%d want 40", stats.Processed)
	}
	if stats.PeakChunks > int64(cfg.MaxInFlight()) {
		t.Fatalf("peak=%d bound=%d", stats.PeakChunks, cfg.MaxInFlight())
	}
	if maxActive.Load() > int64(cfg.Workers) {
		t.Fatalf("worker active=%d workers=%d", maxActive.Load(), cfg.Workers)
	}
}

func TestRunAppliesBackpressureBeforeNextProduce(t *testing.T) {
	cfg := Config{Workers: 1, Queue: 1}
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	var produceCalls atomic.Int64

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = Run(context.Background(), cfg, 20,
			func(_ context.Context, i int) (Chunk[struct{}], error) {
				produceCalls.Add(1)
				return Chunk[struct{}]{Index: i}, nil
			},
			func(_ context.Context, c Chunk[struct{}]) error {
				if c.Index == 0 {
					close(firstEntered)
					<-releaseFirst
				}
				return nil
			},
		)
	}()

	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first chunk did not start")
	}
	time.Sleep(20 * time.Millisecond)
	if got := produceCalls.Load(); got != 2 {
		t.Fatalf("produce calls while saturated=%d want 2 (one active + one queued)", got)
	}
	close(releaseFirst)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not finish after release")
	}
}

func TestRunStopsOnErrorAndCancel(t *testing.T) {
	wantErr := errors.New("boom")
	var produced atomic.Int64
	_, err := Run(context.Background(), Config{Workers: 2, Queue: 2}, 1000,
		func(_ context.Context, i int) (Chunk[int], error) {
			produced.Add(1)
			return Chunk[int]{Index: i}, nil
		},
		func(_ context.Context, c Chunk[int]) error {
			if c.Index == 3 {
				return wantErr
			}
			time.Sleep(time.Millisecond)
			return nil
		},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err=%v want %v", err, wantErr)
	}
	if produced.Load() > 10 {
		t.Fatalf("production did not stop promptly: produced=%d", produced.Load())
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stats, err := Run(ctx, Config{Workers: 1, Queue: 1}, 10,
		func(context.Context, int) (Chunk[int], error) { return Chunk[int]{}, nil },
		func(context.Context, Chunk[int]) error { return nil },
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel err=%v", err)
	}
	if stats.Processed != 0 {
		t.Fatalf("processed after canceled context=%d", stats.Processed)
	}
}
