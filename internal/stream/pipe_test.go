package stream_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"example.com/archiveverify/internal/model"
	"example.com/archiveverify/internal/stream"
)

func TestPipeProcessesAllWithinMemoryBound(t *testing.T) {
	p := stream.NewPipe(stream.Options{Queue: 2, MemoryBytes: 4096, Workers: 1})
	items := []stream.Item{{ID: 1, Size: 1024}, {ID: 2, Size: 1024}, {ID: 3, Size: 1024}}
	st, err := p.Run(context.Background(), items, func(context.Context, stream.Item) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if st.Processed != 3 {
		t.Fatalf("processed=%d want 3", st.Processed)
	}
	if st.PeakMemoryBytes > 4096 {
		t.Fatalf("peak memory=%d exceeds budget", st.PeakMemoryBytes)
	}
}

func TestPipeSlowConsumerAppliesBackpressure(t *testing.T) {
	gate := make(chan struct{})
	time.AfterFunc(30*time.Millisecond, func() { close(gate) })
	p := stream.NewPipe(stream.Options{Queue: 8, MemoryBytes: 3 * 1024, Workers: 1})
	items := make([]stream.Item, 0, 8)
	for i := 0; i < 8; i++ {
		items = append(items, stream.Item{ID: i, Size: 1024})
	}
	st, err := p.Run(context.Background(), items, func(ctx context.Context, item stream.Item) error {
		if item.ID == 0 {
			select {
			case <-gate:
			case <-ctx.Done():
				return context.Cause(ctx)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Processed != 8 {
		t.Fatalf("processed=%d want 8", st.Processed)
	}
	if st.BackpressureEvents == 0 {
		t.Fatal("slow consumer did not produce accounted backpressure")
	}
	if st.PeakMemoryBytes > 3*1024 {
		t.Fatalf("peak=%d exceeds memory budget", st.PeakMemoryBytes)
	}
}

func TestPipeOversizedItemIsResourceLimitExitThree(t *testing.T) {
	p := stream.NewPipe(stream.Options{Queue: 1, MemoryBytes: 64})
	_, err := p.Run(context.Background(), []stream.Item{{ID: 1, Size: 128}},
		func(context.Context, stream.Item) error { return nil })
	if model.ExitCode(err) != 3 || !errors.Is(err, model.ErrResourceLimit) {
		t.Fatalf("err=%v exit=%d", err, model.ExitCode(err))
	}
}

func TestPipeCancellationUsesSameCause(t *testing.T) {
	cause := errors.New("operator cancel cause")
	ctx, cancel := context.WithCancelCause(context.Background())
	time.AfterFunc(20*time.Millisecond, func() { cancel(cause) })
	p := stream.NewPipe(stream.Options{Queue: 4, MemoryBytes: 8 << 20, Workers: 2})
	items := []stream.Item{{ID: 1, Size: 1}, {ID: 2, Size: 1}, {ID: 3, Size: 1}}
	_, err := p.Run(ctx, items, func(ctx context.Context, _ stream.Item) error {
		<-ctx.Done()
		return context.Cause(ctx)
	})
	if !errors.Is(err, cause) {
		t.Fatalf("error=%v want cause %v", err, cause)
	}
	if model.ExitCode(err) != 3 {
		t.Fatalf("exit code=%d want 3", model.ExitCode(err))
	}
}
