package pipeline

import (
	"context"
	"testing"
	"time"

	"avcheck/internal/media"
)

func TestQueueBackpressureAndPeak(t *testing.T) {
	q := NewQueue[int](4, "t")
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		if err := q.Send(ctx, i); err != nil {
			t.Fatal(err)
		}
	}
	// queue full: send must block
	done := make(chan error, 1)
	go func() { done <- q.Send(ctx, 99) }()
	select {
	case <-done:
		t.Fatal("send on full queue must block")
	case <-time.After(50 * time.Millisecond):
	}
	if v, ok := q.Recv(ctx); !ok || v != 0 {
		t.Fatalf("recv %v %v", v, ok)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if q.Peak() != 4 || q.Cap() != 4 {
		t.Fatalf("peak=%d cap=%d", q.Peak(), q.Cap())
	}
}

func TestQueueCancel(t *testing.T) {
	q := NewQueue[int](1, "t")
	ctx, cancel := context.WithCancel(context.Background())
	if err := q.Send(ctx, 1); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- q.Send(ctx, 2) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	err := <-done
	me, ok := err.(*media.Error)
	if !ok || me.Kind != media.KindCanceled {
		t.Fatalf("expected canceled error, got %v", err)
	}
}

// runPipeline pushes n items through map/reduce stages and returns the sum.
func runPipeline(ctx context.Context, n, capacity int) (sum int, peak int64, err error) {
	q1 := NewQueue[int](capacity, "gen")
	q2 := NewQueue[int](capacity, "sq")
	stages := []func(context.Context) error{
		func(ctx context.Context) error {
			defer q1.Close()
			for i := 0; i < n; i++ {
				if err := q1.Send(ctx, i); err != nil {
					return err
				}
			}
			return nil
		},
		func(ctx context.Context) error {
			defer q2.Close()
			for {
				v, ok := q1.Recv(ctx)
				if !ok {
					return nil
				}
				if err := q2.Send(ctx, v*v); err != nil {
					return err
				}
			}
		},
		func(ctx context.Context) error {
			for {
				v, ok := q2.Recv(ctx)
				if !ok {
					return nil
				}
				sum += v
			}
		},
	}
	err = Run(ctx, stages...)
	return sum, max(q1.Peak(), q2.Peak()), err
}

func TestPipelineRepeatRuns(t *testing.T) {
	var first int
	for run := 0; run < 3; run++ {
		sum, peak, err := runPipeline(context.Background(), 500, 8)
		if err != nil {
			t.Fatal(err)
		}
		if peak > 8 {
			t.Fatalf("peak %d exceeds capacity", peak)
		}
		if run == 0 {
			first = sum
		} else if sum != first {
			t.Fatalf("run %d sum %d != %d", run, sum, first)
		}
	}
}

func TestPipelineAbortAndResume(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	q := NewQueue[int](2, "abort")
	producer := func(ctx context.Context) error {
		defer q.Close()
		for i := 0; i < 1000; i++ {
			if err := q.Send(ctx, i); err != nil {
				return err
			}
		}
		return nil
	}
	consumer := func(ctx context.Context) error {
		for i := 0; ; i++ {
			if i == 5 {
				cancel() // abort mid-stream
			}
			if _, ok := q.Recv(ctx); !ok {
				return media.NewError(media.KindCanceled, 0, 0, -1, "aborted by consumer")
			}
		}
	}
	err := Run(ctx, producer, consumer)
	if err == nil {
		t.Fatal("expected abort error")
	}
	// resume: fresh queues on a fresh context must complete
	sum, _, err := runPipeline(context.Background(), 100, 4)
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	want := 0
	for i := 0; i < 100; i++ {
		want += i * i
	}
	if sum != want {
		t.Fatalf("resumed sum %d want %d", sum, want)
	}
}
