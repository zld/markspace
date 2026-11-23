package pool

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"mediakernel/internal/mediaerr"
)

func TestConfigValidation(t *testing.T) {
	if _, err := New(context.Background(), 0, 4); err == nil {
		t.Fatal("zero workers must fail")
	}
	if _, err := New(context.Background(), 2, 0); err == nil {
		t.Fatal("zero queue must fail")
	}
	if _, err := New(context.Background(), mediaerr.MaxWorkers+1, 4); err == nil {
		t.Fatal("worker cap must be enforced")
	}
	if _, err := New(context.Background(), 2, mediaerr.MaxQueue+1); err == nil {
		t.Fatal("queue cap must be enforced")
	}
}

func TestAllJobsRunAndCleanExit(t *testing.T) {
	before := runtime.NumGoroutine()
	ctx := context.Background()
	p, err := New(ctx, 4, 8)
	if err != nil {
		t.Fatal(err)
	}
	var done int64
	const n = 50
	for i := 0; i < n; i++ {
		if err := p.Submit(ctx, func(context.Context) error {
			atomic.AddInt64(&done, 1)
			return nil
		}); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if done != n {
		t.Fatalf("ran %d jobs, want %d", done, n)
	}
	// Submit after close must fail rather than panic or block forever.
	if err := p.Submit(ctx, func(context.Context) error { return nil }); err == nil {
		t.Fatal("submit after Wait must fail")
	}
	assertGoroutinesSettled(t, before)
}

func TestCancelPropagation(t *testing.T) {
	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	p, err := New(ctx, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 2)
	// Occupy both workers with long jobs.
	for i := 0; i < 2; i++ {
		if err := p.Submit(ctx, func(ctx context.Context) error {
			started <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		}); err != nil {
			t.Fatalf("submit blocking job: %v", err)
		}
	}
	<-started
	<-started
	cancel()
	// A submit after cancel must return immediately.
	done := make(chan error, 1)
	go func() { done <- p.Submit(context.Background(), func(context.Context) error { return nil }) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("submit during cancellation must error")
		}
	case <-time.After(time.Second):
		t.Fatal("submit did not unblock within 1s after cancel")
	}
	// Wait must return promptly with a cancellation-class error.
	waitErr := waitWithTimeout(t, p, time.Second)
	if waitErr == nil {
		t.Fatal("expected error from canceled pool")
	}
	if me, ok := mediaerr.As(waitErr); ok {
		if me.Category != mediaerr.CatCanceled {
			t.Fatalf("category = %s, want canceled", me.Category)
		}
	} else if !errors.Is(waitErr, context.Canceled) {
		t.Fatalf("unexpected error %v", waitErr)
	}
	assertGoroutinesSettled(t, before)
}

func TestJobErrorReportedOnce(t *testing.T) {
	ctx := context.Background()
	p, _ := New(ctx, 3, 4)
	boom := mediaerr.New(mediaerr.CatRemux, "boom", "synthetic failure")
	for i := 0; i < 6; i++ {
		i := i
		_ = p.Submit(ctx, func(context.Context) error {
			if i%2 == 0 {
				return boom
			}
			return nil
		})
	}
	err := p.Wait()
	if err == nil {
		t.Fatal("first job error must surface")
	}
	// Wait is safe to call repeatedly and stays idempotent.
	if err2 := p.Wait(); err2 != err {
		t.Fatalf("repeated Wait changed the error: %v vs %v", err, err2)
	}
}

func TestBoundedQueueBackPressure(t *testing.T) {
	ctx := context.Background()
	p, _ := New(ctx, 1, 1)
	hold := make(chan struct{})
	// The single worker is blocked; the one-slot queue then fills, so the
	// next Submit must block until cancellation rather than grow memory.
	_ = p.Submit(ctx, func(context.Context) error { <-hold; return nil })
	_ = p.Submit(ctx, func(context.Context) error { return nil })
	subCtx, cancel := context.WithCancel(ctx)
	canceled := make(chan struct{})
	go func() {
		_ = p.Submit(subCtx, func(context.Context) error { return nil })
		close(canceled)
	}()
	select {
	case <-canceled:
		t.Fatal("submit should block while the bounded queue is full")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("blocked submit did not observe cancellation")
	}
	close(hold)
	_ = p.Wait()
}

func waitWithTimeout(t *testing.T, p *Pool, d time.Duration) error {
	t.Helper()
	type res struct{ err error }
	ch := make(chan res, 1)
	go func() { ch <- res{p.Wait()} }()
	select {
	case r := <-ch:
		return r.err
	case <-time.After(d):
		t.Fatalf("Wait did not return within %s", d)
		return nil
	}
}

func assertGoroutinesSettled(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("goroutine leak: baseline=%d now=%d", baseline, runtime.NumGoroutine())
}
