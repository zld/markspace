package pipeline

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBoundedQueueBackPressureAndDrain(t *testing.T) {
	q := NewBoundedQueue[int](2)
	ctx := context.Background()
	if err := q.Enqueue(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if q.Len() != 2 {
		t.Fatalf("len: %d", q.Len())
	}
	// A third enqueue must block until a dequeue frees capacity.
	done := make(chan error, 1)
	go func() { done <- q.Enqueue(ctx, 3) }()
	select {
	case <-done:
		t.Fatal("enqueue should block while the queue is full")
	case <-time.After(30 * time.Millisecond):
	}
	v, ok, err := q.Dequeue(ctx)
	if err != nil || !ok || v != 1 {
		t.Fatalf("dequeue: %d %v %v", v, ok, err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked enqueue never completed after dequeue")
	}
	q.Close()
	// Buffered items remain readable after close.
	seen := 0
	for {
		v, ok, err := q.Dequeue(ctx)
		if err != nil || !ok {
			break
		}
		seen++
		_ = v
	}
	if seen != 2 {
		t.Fatalf("drained %d items after close, want 2", seen)
	}
	if err := q.Enqueue(ctx, 9); !errors.Is(err, ErrQueueClosed) {
		t.Fatalf("enqueue after close: %v", err)
	}
}

func TestBoundedQueueCancellation(t *testing.T) {
	q := NewBoundedQueue[int](1)
	q.Enqueue(context.Background(), 1)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- q.Enqueue(ctx, 2) }()
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled enqueue did not return")
	}
}

func TestRunProcessesAllJobs(t *testing.T) {
	jobs := []TierJob{
		{Name: "a"}, {Name: "b"}, {Name: "c"},
	}
	var mu sync.Mutex
	seen := map[string]bool{}
	err := Run(context.Background(), jobs, 2, func(_ context.Context, j TierJob) error {
		mu.Lock()
		seen[j.Name] = true
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 3 {
		t.Fatalf("processed: %v", seen)
	}
}

func TestRunRespectsConcurrencyLimit(t *testing.T) {
	jobs := make([]TierJob, 12)
	for i := range jobs {
		jobs[i] = TierJob{Name: "j"}
	}
	var inFlight, maxFlight int32
	err := Run(context.Background(), jobs, 3, func(_ context.Context, _ TierJob) error {
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			m := atomic.LoadInt32(&maxFlight)
			if cur <= m || atomic.CompareAndSwapInt32(&maxFlight, m, cur) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if maxFlight > 3 {
		t.Fatalf("max in-flight %d exceeded limit 3", maxFlight)
	}
}

func TestRunFirstErrorCancelsOthers(t *testing.T) {
	jobs := make([]TierJob, 8)
	for i := range jobs {
		jobs[i] = TierJob{Name: "j"}
	}
	sentinel := errors.New("boom")
	var started int32
	err := Run(context.Background(), jobs, 4, func(ctx context.Context, _ TierJob) error {
		n := atomic.AddInt32(&started, 1)
		if n == 2 {
			return sentinel
		}
		<-ctx.Done() // remaining workers observe cancellation
		return ctx.Err()
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel error, got %v", err)
	}
	if atomic.LoadInt32(&started) > 4 {
		t.Fatalf("cancellation too late: %d workers started", started)
	}
}

func TestRunParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	jobs := []TierJob{{Name: "long"}}
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	err := Run(ctx, jobs, 1, func(ctx context.Context, _ TierJob) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation, got %v", err)
	}
}

// TestCloseOnFullQueueMustNotDeadlock guards the case where Close is called
// while an Enqueue is blocked on a saturated queue: Close must not wait on a
// lock held by the blocked sender.
func TestCloseOnFullQueueMustNotDeadlock(t *testing.T) {
	q := NewBoundedQueue[int](1)
	q.Enqueue(context.Background(), 1)
	blocked := make(chan error, 1)
	go func() { blocked <- q.Enqueue(context.Background(), 2) }()
	// Give the sender time to park on the full channel.
	time.Sleep(20 * time.Millisecond)
	done := make(chan struct{})
	go func() {
		q.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close deadlocked while an enqueue was blocked")
	}
	select {
	case err := <-blocked:
		if !errors.Is(err, ErrQueueClosed) {
			t.Fatalf("blocked enqueue: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked enqueue never returned after Close")
	}
}
