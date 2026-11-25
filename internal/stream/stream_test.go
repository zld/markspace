package stream

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"mediapipeline/internal/fault"
)

func TestBoundedQueueEnforcesCapacity(t *testing.T) {
	q, err := NewBoundedQueue(2)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := q.Enqueue(ctx, Item{Index: i}); err != nil {
			t.Fatal(err)
		}
	}
	if q.Peak() != 2 {
		t.Fatalf("peak = %d, want 2", q.Peak())
	}

	// Third enqueue must block until a dequeue frees a slot.
	done := make(chan error, 1)
	go func() { done <- q.Enqueue(ctx, Item{Index: 2}) }()
	select {
	case <-done:
		t.Fatal("enqueue on full queue must block")
	case <-time.After(30 * time.Millisecond):
	}
	item, ok, err := q.Dequeue(ctx)
	if err != nil || !ok || item.Index != 0 {
		t.Fatalf("unexpected dequeue: %v %v %v", item, ok, err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked enqueue should complete after dequeue")
	}
	if q.Peak() > 2 {
		t.Fatalf("capacity violated, peak = %d", q.Peak())
	}
}

func TestBoundedQueueCancelation(t *testing.T) {
	q, _ := NewBoundedQueue(1)
	ctx, cancel := context.WithCancel(context.Background())
	if err := q.Enqueue(ctx, Item{Index: 0}); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := q.Enqueue(ctx, Item{Index: 1}); !fault.Is(err, fault.CodeCanceled) {
		t.Fatalf("expected canceled error, got %v", err)
	}

	// Dequeue cancellation on an empty queue.
	q2, _ := NewBoundedQueue(1)
	ctx2, cancel2 := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel2()
	}()
	if _, _, err := q2.Dequeue(ctx2); !fault.Is(err, fault.CodeCanceled) {
		t.Fatalf("expected canceled dequeue, got %v", err)
	}
}

func TestPipelineProcessesAllItems(t *testing.T) {
	var sum int32
	proc := func(_ context.Context, item Item) error {
		atomic.AddInt32(&sum, int32(item.Index))
		return nil
	}
	p, err := NewPipeline(2, 2, proc)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := p.Run(context.Background(), func(ctx context.Context, q *BoundedQueue) error {
		for i := 1; i <= 10; i++ {
			if err := q.Enqueue(ctx, Item{Index: i, Key: "rep0"}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Processed != 10 || sum != 55 {
		t.Fatalf("processed=%d sum=%d", stats.Processed, sum)
	}
	if stats.QueuePeak > stats.Capacity {
		t.Fatalf("peak %d exceeds capacity %d", stats.QueuePeak, stats.Capacity)
	}
}

func TestPipelineStopsOnProcessorError(t *testing.T) {
	var processed int32
	proc := func(_ context.Context, item Item) error {
		if item.Index == 3 {
			return fault.New(fault.CodeTruncatedBox, "boom")
		}
		atomic.AddInt32(&processed, 1)
		return nil
	}
	p, _ := NewPipeline(1, 1, proc)
	_, err := p.Run(context.Background(), func(ctx context.Context, q *BoundedQueue) error {
		for i := 1; i <= 5; i++ {
			if err := q.Enqueue(ctx, Item{Index: i}); err != nil {
				return err
			}
		}
		return nil
	})
	if !fault.Is(err, fault.CodeTruncatedBox) {
		t.Fatalf("processor error must propagate, got %v", err)
	}
	if atomic.LoadInt32(&processed) > 3 {
		t.Fatalf("pipeline must stop after the failing item, processed %d", processed)
	}
}

func TestPipelineRejectsBadConfig(t *testing.T) {
	if _, err := NewPipeline(0, 1, func(context.Context, Item) error { return nil }); err == nil {
		t.Fatal("zero capacity must be rejected")
	}
	if _, err := NewPipeline(1, 0, func(context.Context, Item) error { return nil }); err == nil {
		t.Fatal("zero workers must be rejected")
	}
	if _, err := NewPipeline(1, 1, nil); err == nil {
		t.Fatal("nil processor must be rejected")
	}
}
