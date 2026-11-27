package stream

import (
	"context"
	"testing"
	"time"

	"github.com/local/tracecheck/internal/clock"
)

func itemWith(n int) Item {
	return Item{SegmentIndex: 1, SourceStart: 0, SourceEnd: int64(n), Content: make([]byte, n)}
}

func TestPutGetDrain(t *testing.T) {
	q := NewBounded(2, 100)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := q.Put(ctx, itemWith(10)); err != nil {
			t.Fatal(err)
		}
	}
	if q.Peak() != 2 {
		t.Fatalf("peak = %d, want 2", q.Peak())
	}
	for i := 0; i < 2; i++ {
		if _, ok, err := q.Get(ctx); err != nil || !ok {
			t.Fatalf("get %d: ok=%v err=%v", i, ok, err)
		}
	}
	q.Close()
	if _, ok, _ := q.Get(ctx); ok {
		t.Fatal("closed drained queue must report ok=false")
	}
}

func TestBackPressureAndPeakBound(t *testing.T) {
	const cap = 4
	q := NewBounded(cap, 10_000)
	ctx := context.Background()

	// Slow consumer: the producer offers faster than the consumer drains,
	// so the queue fills and the producer blocks on back-pressure.
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		for i := 0; i < 50; i++ {
			if err := q.Put(ctx, itemWith(100)); err != nil {
				return
			}
		}
	}()

	received := 0
	for received < 50 {
		_, ok, err := q.Get(ctx)
		if err != nil || !ok {
			break
		}
		received++
		time.Sleep(2 * time.Millisecond) // deliberately slow
	}
	<-producerDone // producer fully stopped before Close: no send-on-closed
	q.Close()

	if received != 50 {
		t.Fatalf("consumed %d of 50 items", received)
	}
	if q.Peak() > cap {
		t.Fatalf("peak %d exceeded capacity %d under slow consumer", q.Peak(), cap)
	}
}

func TestCancellationReleasesBlockedPut(t *testing.T) {
	q := NewBounded(1, 100)
	ctx, cancel := context.WithCancel(context.Background())
	if err := q.Put(ctx, itemWith(1)); err != nil {
		t.Fatal(err)
	}
	putErr := make(chan error, 1)
	go func() {
		putErr <- q.Put(ctx, itemWith(1)) // blocks: queue full
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-putErr:
		if clock.AsTraceError(err) == nil || clock.AsTraceError(err).Code != clock.CodeCancelled {
			t.Fatalf("expected cancellation error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked Put was not released by cancellation")
	}
}

func TestCancellationReleasesBlockedGet(t *testing.T) {
	q := NewBounded(1, 100)
	ctx, cancel := context.WithCancel(context.Background())
	getErr := make(chan error, 1)
	go func() {
		_, _, err := q.Get(ctx) // blocks: empty queue
		getErr <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-getErr:
		if clock.AsTraceError(err) == nil || clock.AsTraceError(err).Code != clock.CodeCancelled {
			t.Fatalf("expected cancellation error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked Get was not released by cancellation")
	}
}

func TestOversizedItemRejected(t *testing.T) {
	q := NewBounded(2, 10)
	err := q.Put(context.Background(), itemWith(11))
	te := clock.AsTraceError(err)
	if te == nil || te.Code != clock.CodeOutOfRange {
		t.Fatalf("expected out-of-range, got %v", err)
	}
	if q.TryPut(itemWith(11)) {
		t.Fatal("TryPut must reject oversized item")
	}
}

func TestTryPutNonBlocking(t *testing.T) {
	q := NewBounded(1, 100)
	if !q.TryPut(itemWith(1)) {
		t.Fatal("first TryPut should succeed")
	}
	if q.TryPut(itemWith(1)) {
		t.Fatal("second TryPut on full queue must fail")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	q := NewBounded(1, 10)
	q.Close()
	q.Close() // must not panic on double close
	if _, ok, _ := q.Get(context.Background()); ok {
		t.Fatal("closed queue must be empty")
	}
}
