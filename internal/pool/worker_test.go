package pool

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPoolLimits(t *testing.T) {
	p := New[int, int](Config{Workers: 99, Queue: 99}, func(context.Context, int) (int, error) {
		return 0, nil
	})
	if p.WorkerCount() != MaxWorkers {
		t.Fatalf("workers=%d want %d", p.WorkerCount(), MaxWorkers)
	}
	if p.QueueSize() != QueueCapacity {
		t.Fatalf("queue=%d want %d", p.QueueSize(), QueueCapacity)
	}
}

func TestPoolReturnsOrderedResults(t *testing.T) {
	jobs := []int{1, 2, 3, 4, 5}
	p := New[int, int](Config{Workers: 4, Queue: 8}, func(_ context.Context, job int) (int, error) {
		time.Sleep(time.Duration(6-job) * time.Millisecond)
		return job * job, nil
	})
	got, err := p.Run(context.Background(), jobs)
	if err != nil {
		t.Fatal(err)
	}
	for i, job := range jobs {
		if got[i] != job*job {
			t.Fatalf("result %d=%d want %d", i, got[i], job*job)
		}
	}
}

func TestPoolCancelPropagates(t *testing.T) {
	started := make(chan struct{}, MaxWorkers)
	p := New[int, int](Config{Workers: 2, Queue: 1}, func(ctx context.Context, job int) (int, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return 0, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := p.Run(ctx, []int{1, 2, 3, 4, 5})
		done <- err
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("workers did not start")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pool did not stop after cancellation")
	}
}

func TestPoolStopsAfterHandlerError(t *testing.T) {
	wantErr := errors.New("boom")
	p := New[int, int](Config{}, func(ctx context.Context, job int) (int, error) {
		if job == 2 {
			return 0, wantErr
		}
		select {
		case <-time.After(50 * time.Millisecond):
			return job, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	})
	_, err := p.Run(context.Background(), []int{1, 2, 3})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err=%v want %v", err, wantErr)
	}
}
