package continuity

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func qSample(idx int, size int) Sample {
	return Sample{
		Segment: "s.m4s", File: "s.m4s", Index: idx,
		ByteOffset: int64(idx), ByteLength: int64(size),
		Data: make([]byte, size),
	}
}

func TestQueueBackpressureBlocksProducer(t *testing.T) {
	ctx := context.Background()
	q, err := NewQueue(ctx, QueueConfig{MaxBytes: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if err := q.Push(qSample(0, 10)); err != nil {
		t.Fatal(err)
	}
	if err := q.Push(qSample(1, 10)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- q.Push(qSample(2, 15)) }()
	select {
	case err := <-done:
		t.Fatalf("push should block for space, returned %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	// The consumer frees room; the blocked producer proceeds without loss.
	s, err := q.Pop()
	if err != nil || s.Index != 0 {
		t.Fatalf("pop=%+v,%v", s, err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("blocked push failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked push was not released")
	}
	if st := q.Stats(); st.Blocked == 0 || st.Pushed != 3 {
		t.Fatalf("stats=%+v", st)
	}
	// No sample was dropped: 1 popped + 2 still queued.
	if n, _ := q.Len(); n != 2 {
		t.Fatalf("queued=%d", n)
	}
}

func TestQueueCancellationReleasesBlockedPush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	q, err := NewQueue(ctx, QueueConfig{MaxBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if err := q.Push(qSample(0, 10)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- q.Push(qSample(1, 10)) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked push was not released by cancellation")
	}
}

func TestQueueCancellationReleasesBlockedPop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	q, err := NewQueue(ctx, QueueConfig{MaxBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	done := make(chan error, 1)
	go func() {
		_, err := q.Pop()
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked pop was not released by cancellation")
	}
}

func TestQueueSingleSampleTooLargeFails(t *testing.T) {
	q, err := NewQueue(context.Background(), QueueConfig{MaxBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	err = q.Push(qSample(3, 9))
	if err == nil {
		t.Fatal("oversized sample must fail")
	}
	if !errors.Is(err, ErrSampleTooLarge) || !IsKind(err, KindQueueLimit) {
		t.Fatalf("err=%v", err)
	}
	if loc := LocationOf(err); loc.Sample != 3 {
		t.Fatalf("loc=%+v", loc)
	}
	if n, b := q.Len(); n != 0 || b != 0 {
		t.Fatalf("failed push must not enqueue: %d,%d", n, b)
	}
}

func TestQueueCloseDrains(t *testing.T) {
	q, err := NewQueue(context.Background(), QueueConfig{MaxBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	q.Push(qSample(0, 10))
	q.Push(qSample(1, 10))
	q.Close()
	s, err := q.Pop()
	if err != nil || s.Index != 0 {
		t.Fatalf("closed queue must drain existing samples: %+v,%v", s, err)
	}
	if _, err := q.Pop(); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Pop(); !errors.Is(err, ErrQueueClosed) {
		t.Fatalf("err=%v", err)
	}
}

func TestQueueSampleBound(t *testing.T) {
	q, err := NewQueue(context.Background(), QueueConfig{MaxBytes: 1 << 20, MaxSamples: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if err := q.Push(qSample(0, 1)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- q.Push(qSample(1, 1)) }()
	select {
	case err := <-done:
		t.Fatalf("sample bound should block, got %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	q.Pop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("sample-bound push was not released")
	}
}

func TestQueueConcurrentOrdering(t *testing.T) {
	q, err := NewQueue(context.Background(), QueueConfig{MaxBytes: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	const n = 50
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			if err := q.Push(qSample(i, 8)); err != nil {
				t.Errorf("push %d: %v", i, err)
				return
			}
		}
		q.Close()
	}()
	last := -1
	for {
		s, err := q.Pop()
		if errors.Is(err, ErrQueueClosed) {
			break
		}
		if err != nil {
			t.Fatalf("pop: %v", err)
		}
		if s.Index <= last {
			t.Fatalf("out-of-order pop: %d after %d", s.Index, last)
		}
		last = s.Index
	}
	wg.Wait()
	if last != n-1 {
		t.Fatalf("last=%d want %d", last, n-1)
	}
	if peak := q.Stats().PeakBytes; peak > 64 {
		t.Fatalf("peak bytes %d exceeded limit", peak)
	}
}

func TestQueueContextCancelBeforePush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	q, err := NewQueue(ctx, QueueConfig{MaxBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	cancel()
	if err := q.Push(qSample(0, 4)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestQueueBackpressureDoesNotDropSamples(t *testing.T) {
	// A producer pushing 100 samples through a 32-byte buffer must deliver all
	// 100 when a consumer drains slowly.
	q, err := NewQueue(context.Background(), QueueConfig{MaxBytes: 32})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	const n = 100
	produced := make(chan error, 1)
	go func() {
		for i := 0; i < n; i++ {
			if err := q.Push(qSample(i, 8)); err != nil {
				produced <- err
				return
			}
		}
		q.Close()
		produced <- nil
	}()
	seen := 0
	for {
		_, err := q.Pop()
		if errors.Is(err, ErrQueueClosed) {
			break
		}
		if err != nil {
			t.Fatalf("pop: %v", err)
		}
		seen++
	}
	if err := <-produced; err != nil {
		t.Fatal(err)
	}
	if seen != n {
		t.Fatalf("delivered %d want %d", seen, n)
	}
	if st := q.Stats(); st.Blocked == 0 || st.Rejected != 0 {
		t.Fatalf("expected backpressure, no rejections: %+v", st)
	}
}
