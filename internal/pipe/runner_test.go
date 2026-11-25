package pipe_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"mediacore/internal/diagnostic"
	"mediacore/internal/pipe"
)

func TestBoundedQueuePeakAndBackpressure(t *testing.T) {
	q := pipe.NewQueue[int](2)
	ctx := context.Background()
	if err := q.Send(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := q.Send(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if q.Peak() != 2 || q.Len() != 2 {
		t.Fatalf("peak=%d len=%d", q.Peak(), q.Len())
	}

	thirdDone := make(chan error, 1)
	go func() { thirdDone <- q.Send(context.Background(), 3) }()
	select {
	case err := <-thirdDone:
		t.Fatalf("third send did not block: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	v, ok, err := q.Recv(ctx)
	if err != nil || !ok || v != 1 {
		t.Fatalf("recv=%d,%v,%v", v, ok, err)
	}
	select {
	case err := <-thirdDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked sender did not resume after receive")
	}
	if q.Peak() != 2 {
		t.Fatalf("peak changed to %d", q.Peak())
	}
}

func TestQueueCancelUnblocksConsumerAndProducer(t *testing.T) {
	q := pipe.NewQueue[int](1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := q.Recv(ctx); !errors.Is(err, diagnostic.ErrCancelled) {
		t.Fatalf("canceled recv err=%v", err)
	}

	full := pipe.NewQueue[int](1)
	if err := full.Send(context.Background(), 9); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if err := full.Send(ctx2, 10); !errors.Is(err, diagnostic.ErrCancelled) {
		t.Fatalf("canceled send err=%v", err)
	}
}

func TestByteBudgetBoundsPeakAndCancellation(t *testing.T) {
	b := pipe.NewByteBudget(10)
	if err := b.Acquire(context.Background(), 6); err != nil {
		t.Fatal(err)
	}
	if b.Peak() != 6 || b.Current() != 6 {
		t.Fatalf("peak/current=%d/%d", b.Peak(), b.Current())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.Acquire(ctx, 5); !errors.Is(err, diagnostic.ErrCancelled) {
		t.Fatalf("budget cancel err=%v", err)
	}
	b.Release(6)
	if b.Current() != 0 {
		t.Fatalf("current after release=%d", b.Current())
	}
	if err := b.Acquire(context.Background(), 11); !errors.Is(err, diagnostic.ErrIllegalSample) {
		t.Fatalf("oversized chunk err=%v", err)
	}
}
