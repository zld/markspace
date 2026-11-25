package stream

import (
	"context"
	"errors"
	"testing"
	"time"

	"mediapack/model"
)

func TestBoundedQueuePeakAndFIFO(t *testing.T) {
	q, err := NewBoundedQueue(2, 1024)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := q.Put(ctx, &model.Unit{ID: string(rune('a' + i)), Size: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if q.Len() != 2 || q.Peak() != 2 {
		t.Fatalf("expected full queue peak 2, len=%d peak=%d", q.Len(), q.Peak())
	}
	first, err := q.Get(ctx)
	if err != nil || first.ID != "a" {
		t.Fatalf("unexpected first unit %+v err=%v", first, err)
	}
	second, _ := q.Get(ctx)
	if second.ID != "b" {
		t.Fatalf("FIFO violated: %s", second.ID)
	}
}

func TestBoundedQueueBackpressureTimeoutIsRecoverable(t *testing.T) {
	q, err := NewBoundedQueue(1, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.PutTimeout(&model.Unit{ID: "one", Size: 1}, time.Second); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = q.PutTimeout(&model.Unit{ID: "two", Track: "video", Size: 1}, 20*time.Millisecond)
	if err == nil {
		t.Fatal("expected backpressure error")
	}
	if elapsed := time.Since(start); elapsed < 15*time.Millisecond || elapsed > 300*time.Millisecond {
		t.Fatalf("timeout did not apply: %v", elapsed)
	}
	var me *model.Error
	if !errors.As(err, &me) || me.Category != model.CategoryQueueBackpressure || !me.Recoverable {
		t.Fatalf("expected recoverable backpressure, got %v", err)
	}
	if q.Peak() > 1 {
		t.Fatalf("peak exceeded capacity: %d", q.Peak())
	}
}

func TestBoundedQueueOversizedUnitRejected(t *testing.T) {
	q, _ := NewBoundedQueue(1, 8)
	err := q.Put(context.Background(), &model.Unit{ID: "big", Size: 64})
	if !model.IsCategory(err, model.CategoryUnitTooLarge) {
		t.Fatalf("expected unit_too_large, got %v", err)
	}
	if q.Len() != 0 {
		t.Fatal("oversized unit must not enter queue")
	}
}

func TestBoundedQueueProducerBlocksUntilConsumer(t *testing.T) {
	q, _ := NewBoundedQueue(1, 1024)
	if err := q.Put(context.Background(), &model.Unit{ID: "held", Size: 1}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- q.Put(context.Background(), &model.Unit{ID: "waiting", Size: 1})
	}()
	select {
	case err := <-done:
		t.Fatalf("producer should block while full, returned %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	u, err := q.Get(context.Background())
	if err != nil || u.ID != "held" {
		t.Fatalf("unexpected get: %+v %v", u, err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked producer did not resume")
	}
}
