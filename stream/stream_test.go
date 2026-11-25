package stream

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zld/media-regress/internal/error"
)

func item(i int, size int, ts uint32) Item {
	return Item{Index: i, TrackID: 2, Timestamp: ts, Payload: make([]byte, size), SourceFrames: 8000}
}

func TestConfigValidation(t *testing.T) {
	if _, err := NewQueue(Config{MaxBlocks: 0, MaxBytes: 10}); err == nil {
		t.Fatal("zero blocks must fail")
	}
	if _, err := NewQueue(Config{MaxBlocks: 4, MaxBytes: 0}); err == nil {
		t.Fatal("zero bytes must fail")
	}
}

func TestQueueBoundsBlocks(t *testing.T) {
	q, err := NewQueue(Config{MaxBlocks: 2, MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := q.Push(ctx, item(1, 10, 0)); err != nil {
		t.Fatal(err)
	}
	if err := q.Push(ctx, item(2, 10, 8000)); err != nil {
		t.Fatal(err)
	}
	// The third push must block; cancel it.
	cctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	err = q.Push(cctx, item(3, 10, 16000))
	if err == nil {
		t.Fatal("push beyond block bound must block and fail on timeout")
	}
	if e, ok := merr.As(err); !ok || e.Kind != merr.KindTimeout {
		t.Fatalf("expected timeout, got %v", err)
	}
	stats := q.StatsSnapshot()
	if stats.QueuePeakBlocks != 2 {
		t.Fatalf("peak blocks %d", stats.QueuePeakBlocks)
	}
}

func TestQueueBoundsBytes(t *testing.T) {
	q, err := NewQueue(Config{MaxBlocks: 100, MaxBytes: 25})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := q.Push(ctx, item(1, 20, 0)); err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := q.Push(cctx, item(2, 20, 8000)); err == nil {
		t.Fatal("byte bound must block")
	}
	stats := q.StatsSnapshot()
	if stats.QueuePeakBytes > 25 {
		t.Fatalf("peak bytes %d exceeds limit", stats.QueuePeakBytes)
	}
}

func TestQueueConsumerUnblocksProducer(t *testing.T) {
	q, err := NewQueue(Config{MaxBlocks: 1, MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := q.Push(ctx, item(1, 10, 0)); err != nil {
		t.Fatal(err)
	}
	pushed := make(chan error, 1)
	go func() { pushed <- q.Push(ctx, item(2, 10, 8000)) }()
	// Give the producer a chance to block, then drain one item.
	time.Sleep(10 * time.Millisecond)
	if _, err := q.Pop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-pushed:
		if err != nil {
			t.Fatalf("push should succeed after drain: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("producer stayed blocked after room appeared")
	}
}

func TestQueueCloseDrains(t *testing.T) {
	q, err := NewQueue(Config{MaxBlocks: 4, MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_ = q.Push(ctx, item(1, 1, 0))
	q.Close()
	if _, err := q.Pop(ctx); err != nil {
		t.Fatalf("queued item must still pop: %v", err)
	}
	if _, err := q.Pop(ctx); err != ErrDrained {
		t.Fatalf("got %v want ErrDrained", err)
	}
	if err := q.Push(ctx, item(2, 1, 0)); err == nil {
		t.Fatal("push after close must fail")
	}
}

func TestQueueLastCommitted(t *testing.T) {
	q, _ := NewQueue(Config{MaxBlocks: 4, MaxBytes: 1 << 20})
	if q.LastCommitted() != -1 {
		t.Fatal("empty queue commits -1")
	}
	ctx := context.Background()
	_ = q.Push(ctx, item(1, 1, 0))
	_ = q.Push(ctx, item(2, 1, 8000))
	_, _ = q.Pop(ctx)
	if q.LastCommitted() != 1 {
		t.Fatalf("committed %d want 1", q.LastCommitted())
	}
}

func TestPipelineDeliversInOrderWithSequences(t *testing.T) {
	var got []Item
	var mu atomic.Int64
	p, err := NewPipeline(Config{MaxBlocks: 2, MaxBytes: 1 << 20}, func(ctx context.Context, d Delivery) error {
		got = append(got, d.Item)
		mu.Add(1)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	runDone := make(chan error, 1)
	go func() { runDone <- p.Run(ctx) }()
	for i := 1; i <= 5; i++ {
		if err := p.Push(ctx, item(i, 8, uint32(i-1)*8000)); err != nil {
			t.Fatal(err)
		}
	}
	p.Close()
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d deliveries", len(got))
	}
	for i, it := range got {
		if it.Index != i+1 {
			t.Fatalf("delivery %d has index %d", i, it.Index)
		}
		if it.Sequence != uint16(i) {
			t.Fatalf("delivery %d has sequence %d", i, it.Sequence)
		}
	}
	if p.LastCommitted() != 5 {
		t.Fatalf("committed %d", p.LastCommitted())
	}
}

func TestPipelineRejectsRegressingTimestamp(t *testing.T) {
	p, _ := NewPipeline(Config{MaxBlocks: 4, MaxBytes: 1 << 20}, nil)
	ctx := context.Background()
	if err := p.Push(ctx, item(1, 1, 8000)); err != nil {
		t.Fatal(err)
	}
	err := p.Push(ctx, item(2, 1, 4000))
	if err == nil {
		t.Fatal("regressing timestamp must fail")
	}
	if e, ok := merr.As(err); !ok || e.Stage != merr.StageStream || e.TrackID != 2 || e.InputOffset != 2 {
		t.Fatalf("unexpected structured error: %v", err)
	}
}

func TestPipelineSlowConsumerBackpressure(t *testing.T) {
	p, _ := NewPipeline(Config{MaxBlocks: 2, MaxBytes: 1 << 20}, func(ctx context.Context, d Delivery) error {
		time.Sleep(5 * time.Millisecond)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	for i := 1; i <= 4; i++ {
		if err := p.Push(ctx, item(i, 8, uint32(i-1)*8000)); err != nil {
			t.Fatal(err)
		}
	}
	stats := p.Stats()
	if stats.BlockedPushes == 0 {
		t.Fatal("slow consumer must have throttled the producer")
	}
	if stats.QueuePeakBlocks > 2 {
		t.Fatalf("peak blocks %d exceeds limit", stats.QueuePeakBlocks)
	}
	p.Close()
}

func TestPipelineCancelStopsIntake(t *testing.T) {
	p, _ := NewPipeline(Config{MaxBlocks: 1, MaxBytes: 1 << 20}, func(ctx context.Context, d Delivery) error {
		<-ctx.Done()
		return ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- p.Run(ctx) }()
	if err := p.Push(ctx, item(1, 8, 0)); err != nil {
		t.Fatal(err)
	}
	// This push blocks until cancellation.
	time.Sleep(10 * time.Millisecond)
	cancel()
	if err := p.Push(ctx, item(2, 8, 8000)); err == nil {
		t.Fatal("push after cancel must fail")
	}
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("pipeline did not stop on cancellation")
	}
	if p.LastCommitted() > 1 {
		t.Fatalf("committed %d after cancel", p.LastCommitted())
	}
}

func TestQueueStatsPeakBytesNeverExceeds(t *testing.T) {
	q, _ := NewQueue(Config{MaxBlocks: 10, MaxBytes: 64})
	ctx := context.Background()
	for i := 0; i < 6; i++ {
		q.Push(ctx, item(i, 16, uint32(i)*8000))
		if _, err := q.Pop(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := q.StatsSnapshot().QueuePeakBytes; got > 64 {
		t.Fatalf("peak bytes %d exceeds 64", got)
	}
}
