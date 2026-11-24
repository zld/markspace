package stream

import (
	"context"
	"testing"
	"time"
)

func TestQueueBackpressureAndPeak(t *testing.T) {
	q := New(4096) // 4 KiB budget
	ctx := context.Background()

	// Fill the budget.
	if err := q.Push(ctx, Chunk{Data: make([]byte, 4096)}); err != nil {
		t.Fatalf("push within budget: %v", err)
	}
	// TryPush must fail fast with backpressure while full.
	if err := q.TryPush(ctx, Chunk{Data: make([]byte, 1024)}); err != ErrBackpressure {
		t.Fatalf("TryPush on full queue = %v, want ErrBackpressure", err)
	}
	// Blocking Push must wait until the consumer drains.
	pushed := make(chan struct{})
	go func() {
		if err := q.Push(ctx, Chunk{Data: make([]byte, 1024)}); err != nil {
			t.Errorf("blocking push: %v", err)
		}
		close(pushed)
	}()
	select {
	case <-pushed:
		t.Fatal("push must block while the queue is full")
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := q.Pop(ctx); err != nil {
		t.Fatalf("pop: %v", err)
	}
	select {
	case <-pushed:
	case <-time.After(time.Second):
		t.Fatal("push did not unblock after drain")
	}
	if got := q.Peak(); got < 4096 {
		t.Fatalf("peak = %d, want >= 4096", got)
	}
	if got := q.Peak(); got > 8192 {
		t.Fatalf("peak = %d exceeds budget 4096+slack", got)
	}
}

func TestQueueCancelUnblocksGoroutines(t *testing.T) {
	q := New(1024)
	ctx, cancel := context.WithCancel(context.Background())

	if err := q.Push(ctx, Chunk{Data: make([]byte, 1024)}); err != nil {
		t.Fatalf("fill queue: %v", err)
	}
	done := make(chan error, 2)
	// A producer blocked on a full queue must wake up on cancel.
	go func() { done <- q.Push(ctx, Chunk{Data: make([]byte, 1024)}) }()
	// A consumer blocked on an empty queue must wake up on cancel.
	q2 := New(1024)
	go func() {
		_, err := q2.Pop(ctx)
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("goroutine must return an error after cancel")
			}
		case <-time.After(time.Second):
			t.Fatal("goroutine did not exit after cancel")
		}
	}
}

func TestQueueCloseDrains(t *testing.T) {
	q := New(4096)
	ctx := context.Background()
	q.Push(ctx, Chunk{Data: []byte("a")})
	q.Close()
	// After close, remaining data is still drained, then ErrClosed.
	if _, err := q.Pop(ctx); err != nil {
		t.Fatalf("drain after close: %v", err)
	}
	if _, err := q.Pop(ctx); err != ErrClosed {
		t.Fatalf("pop on drained closed queue = %v, want ErrClosed", err)
	}
	if err := q.Push(ctx, Chunk{Data: []byte("b")}); err != ErrClosed {
		t.Fatalf("push on closed queue = %v, want ErrClosed", err)
	}
}
