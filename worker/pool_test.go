package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestPoolBoundsConcurrency(t *testing.T) {
	var cur, maxSeen atomic.Int64
	tasks := make([]Task, 20)
	for i := range tasks {
		tasks[i] = Task{ID: "t", Run: func(ctx context.Context) error {
			n := cur.Add(1)
			for {
				m := maxSeen.Load()
				if n <= m || maxSeen.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			cur.Add(-1)
			return nil
		}}
	}
	p := &Pool{Size: 3}
	if err := p.Run(context.Background(), tasks); err != nil {
		t.Fatal(err)
	}
	if maxSeen.Load() > 3 {
		t.Fatalf("concurrency peaked at %d, limit 3", maxSeen.Load())
	}
	if maxSeen.Load() == 0 {
		t.Fatal("tasks never ran")
	}
}

func TestPoolFirstErrorCancels(t *testing.T) {
	boom := errors.New("boom")
	var ran atomic.Int64
	tasks := make([]Task, 10)
	for i := range tasks {
		i := i
		tasks[i] = Task{ID: "t", Run: func(ctx context.Context) error {
			if i == 0 {
				return boom
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(50 * time.Millisecond):
				ran.Add(1)
				return nil
			}
		}}
	}
	p := &Pool{Size: 2}
	if err := p.Run(context.Background(), tasks); !errors.Is(err, boom) {
		t.Fatalf("want boom, got %v", err)
	}
	if ran.Load() >= 9 {
		t.Fatalf("cancellation did not stop remaining tasks (%d ran)", ran.Load())
	}
}
