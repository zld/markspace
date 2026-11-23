package pipeline

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPoolAccountingsAndBackpressure(t *testing.T) {
	var inFlight, peak int64
	var mu sync.Mutex
	pool, err := NewPool(context.Background(), 3, 2, func(ctx context.Context, j Job) Result {
		n := atomic.AddInt64(&inFlight, 1)
		mu.Lock()
		if n > peak {
			peak = n
		}
		mu.Unlock()
		time.Sleep(2 * time.Millisecond)
		atomic.AddInt64(&inFlight, -1)
		return Result{Index: j.Index}
	})
	if err != nil {
		t.Fatal(err)
	}
	const jobs = 40
	done := make(chan struct{})
	got := make(map[int]bool, jobs)
	go func() {
		defer close(done)
		for r := range pool.Results() {
			got[r.Index] = true
		}
	}()
	for i := 0; i < jobs; i++ {
		if err := pool.Submit(Job{Index: i}); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	pool.Close()
	<-done
	st := pool.Stats()
	if st.Submitted != jobs {
		t.Errorf("submitted = %d, want %d", st.Submitted, jobs)
	}
	if st.Submitted != st.Completed+st.Failed+st.Cancelled {
		t.Errorf("accounting broken: %+v", st)
	}
	if len(got) != jobs {
		t.Errorf("received %d results, want %d", len(got), jobs)
	}
	if peak > 3 {
		t.Errorf("in-flight peak %d exceeded worker count 3", peak)
	}
	if st.PeakQueue > st.QueueCap {
		t.Errorf("peak queue %d exceeded capacity %d", st.PeakQueue, st.QueueCap)
	}
	if err := pool.Submit(Job{Index: 999}); !errors.Is(err, ErrPoolClosed) {
		t.Errorf("submit after close = %v, want ErrPoolClosed", err)
	}
}

func TestPoolCancellationReclaimsAllWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var started int64
	pool, err := NewPool(ctx, 4, 4, func(jobCtx context.Context, j Job) Result {
		atomic.AddInt64(&started, 1)
		<-jobCtx.Done()
		return Result{Index: j.Index}
	})
	if err != nil {
		t.Fatal(err)
	}
	const jobs = 12
	done := make(chan struct{})
	var cancelled int
	go func() {
		defer close(done)
		for r := range pool.Results() {
			if r.Status == StatusCancelled {
				cancelled++
			}
		}
	}()
	// Submitting from a goroutine keeps this test from deadlocking on a full
	// queue while every worker is parked in the handler.
	go func() {
		for i := 0; i < jobs; i++ {
			if err := pool.Submit(Job{Index: i}); err != nil {
				return
			}
		}
	}()
	// Wait until at least one worker is actually blocked, then cancel.
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt64(&started) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	pool.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("result stream did not close after cancellation (goroutine leak)")
	}
	if cancelled == 0 {
		t.Error("no cancelled results reported")
	}
}
