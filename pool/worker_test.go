package pool

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"
)

func okHandler(ctx context.Context, t Task) (any, error) {
	return t.Payload, nil
}

func TestSubmitAndDrain(t *testing.T) {
	ctx := context.Background()
	p := New(ctx, 2, 2, okHandler)
	for i := 0; i < 6; i++ {
		if err := p.Submit(ctx, Task{Sequence: i, Payload: i * 10}); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	got := make(map[int]int)
	for i := 0; i < 6; i++ {
		r := <-p.Results()
		if r.Err != nil {
			t.Fatalf("result %d error: %v", r.Sequence, r.Err)
		}
		got[r.Sequence] = r.Value.(int)
	}
	if err := p.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 || got[5] != 50 {
		t.Fatalf("results wrong: %+v", got)
	}
}

func TestQueueCapacityHardLimit(t *testing.T) {
	ctx := context.Background()
	block := make(chan struct{})
	var once sync.Once
	acquired := make(chan struct{})
	handler := func(ctx context.Context, t Task) (any, error) {
		once.Do(func() { close(acquired) }) // signal the first acquisition only
		<-block                              // hold the worker so the buffer stays full
		return nil, nil
	}
	p := New(ctx, 1, 2, handler)
	defer func() {
		close(block)
		_ = p.Shutdown()
	}()

	// Wait until the sole worker is parked inside task 0, then fill the
	// depth-2 queue with tasks 1 and 2.
	if err := p.TrySubmit(Task{Sequence: 0}); err != nil {
		t.Fatal(err)
	}
	<-acquired
	if err := p.TrySubmit(Task{Sequence: 1}); err != nil {
		t.Fatal(err)
	}
	if err := p.TrySubmit(Task{Sequence: 2}); err != nil {
		t.Fatal(err)
	}
	if err := p.TrySubmit(Task{Sequence: 3}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("fourth submit = %v, want ErrQueueFull", err)
	}
}

func TestConsumerFailureIsPerItem(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	handler := func(ctx context.Context, t Task) (any, error) {
		if t.Sequence == 1 {
			return nil, boom
		}
		return t.Sequence, nil
	}
	p := New(ctx, 2, 2, handler)
	for i := 0; i < 4; i++ {
		if err := p.Submit(ctx, Task{Sequence: i}); err != nil {
			t.Fatal(err)
		}
	}
	errs := 0
	for i := 0; i < 4; i++ {
		r := <-p.Results()
		if r.Sequence == 1 {
			if !errors.Is(r.Err, boom) {
				t.Fatalf("item 1 error = %v, want boom", r.Err)
			}
			errs++
		} else if r.Err != nil {
			t.Fatalf("item %d unexpectedly failed: %v", r.Sequence, r.Err)
		}
	}
	if errs != 1 {
		t.Fatalf("got %d failures, want exactly 1", errs)
	}
	if err := p.Shutdown(); err != nil {
		t.Fatal(err)
	}
}

func TestCancellationStopsWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{}, 4)
	handler := func(ctx context.Context, t Task) (any, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	p := New(ctx, 2, 2, handler)
	for i := 0; i < 2; i++ {
		if err := p.Submit(ctx, Task{Sequence: i}); err != nil {
			t.Fatal(err)
		}
	}
	<-entered
	<-entered // both workers are parked

	done := make(chan error, 1)
	go func() { done <- p.Shutdown() }()
	cancel() // cancellation must unblock workers so Shutdown can complete

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown after cancel: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not return within 1s of cancellation")
	}
}

func TestSubmitAfterShutdownRejected(t *testing.T) {
	ctx := context.Background()
	p := New(ctx, 1, 1, okHandler)
	if err := p.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if err := p.Submit(ctx, Task{}); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("submit after shutdown = %v, want ErrPoolClosed", err)
	}
	if err := p.TrySubmit(Task{}); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("try after shutdown = %v, want ErrPoolClosed", err)
	}
	if err := p.Shutdown(); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("double shutdown = %v, want ErrPoolClosed", err)
	}
}

func TestNoGoroutineLeakOnCancel(t *testing.T) {
	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	handler := func(ctx context.Context, t Task) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	p := New(ctx, 3, 3, handler)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(seq int) {
			defer wg.Done()
			_ = p.Submit(ctx, Task{Sequence: seq})
		}(i)
	}
	wg.Wait()
	cancel()
	if err := p.Shutdown(); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	trace := make([]byte, 1<<16)
	n := runtime.Stack(trace, true)
	t.Fatalf("goroutine leak: before=%d after=%d\n%s", before, runtime.NumGoroutine(), trace[:n])
}
