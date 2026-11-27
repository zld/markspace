package pool

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestLimitsValidated(t *testing.T) {
	fn := func(context.Context, int) (int, error) { return 0, nil }
	if _, err := New(context.Background(), 0, 1, fn); err == nil {
		t.Fatal("workers=0 accepted")
	}
	if _, err := New(context.Background(), MaxWorkers+1, 1, fn); err == nil {
		t.Fatal("workers>max accepted")
	}
	if _, err := New(context.Background(), 1, 0, fn); err == nil {
		t.Fatal("queue=0 accepted")
	}
	if _, err := New(context.Background(), 1, MaxQueue+1, fn); err == nil {
		t.Fatal("queue>max accepted")
	}
}

func TestProcessAll(t *testing.T) {
	p, err := New(context.Background(), 4, 8, func(ctx context.Context, n int) (int, error) {
		return n * 2, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for i := 0; i < 50; i++ {
			if err := p.Submit(context.Background(), i); err != nil {
				return
			}
		}
		p.Close()
	}()
	sum := 0
	count := 0
	for res := range p.Results() {
		if res.Err != nil {
			t.Fatalf("result error: %v", res.Err)
		}
		sum += res.Value
		count++
	}
	if count != 50 || sum != 2*(49*50/2) {
		t.Fatalf("count=%d sum=%d", count, sum)
	}
	if p.Err() != nil {
		t.Fatalf("pool err = %v", p.Err())
	}
}

func TestWorkerFailurePropagates(t *testing.T) {
	boom := errors.New("boom")
	p, err := New(context.Background(), 2, 4, func(ctx context.Context, n int) (int, error) {
		if n == 3 {
			return 0, boom
		}
		return n, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for i := 0; i < 20; i++ {
			if err := p.Submit(context.Background(), i); err != nil {
				return // submissions stop after the failure
			}
		}
		p.Close()
	}()
	for range p.Results() { // drain completed results
	}
	if !errors.Is(p.Err(), boom) {
		t.Fatalf("pool err = %v, want boom", p.Err())
	}
}

func TestCancelStopsAndDrains(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 64)
	p, err := New(ctx, 2, 4, func(ctx context.Context, n int) (int, error) {
		started <- struct{}{}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(50 * time.Millisecond):
			return n, nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			if err := p.Submit(context.Background(), i); err != nil {
				return
			}
		}
	}()
	<-started
	cancel() // cancel propagates to producer, queue and workers
	done := make(chan struct{})
	go func() {
		for range p.Results() {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("results not drained after cancel: deadlock")
	}
	wg.Wait()
	if err := p.Submit(context.Background(), 1); !errors.Is(err, ErrClosed) && err == nil {
		t.Fatalf("submit after cancel = %v", err)
	}
}

func TestSubmitAfterClose(t *testing.T) {
	p, err := New(context.Background(), 1, 2, func(ctx context.Context, n int) (int, error) {
		return n, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	if err := p.Submit(context.Background(), 1); !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
	// Close is idempotent.
	p.Close()
}
