package pipeline

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/example/tsrepair/internal/model"
)

func makeItems(n int) <-chan Item {
	ch := make(chan Item, n)
	for i := 0; i < n; i++ {
		ch <- Item{Seq: int64(i), Offset: int64(i * 188), Boundary: i%5 == 0}
	}
	close(ch)
	return ch
}

func TestPipelineProcessesAllAndBoundsDepth(t *testing.T) {
	var mu sync.Mutex
	var got []int64
	stage := Stage{Name: "passthrough", Run: func(_ context.Context, in Item) (Item, error) {
		mu.Lock()
		got = append(got, in.Seq)
		mu.Unlock()
		return in, nil
	}}
	r := NewRunner(1, false, stage, stage)
	stats, err := r.Run(context.Background(), makeItems(10))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if stats.Processed != 10 || len(got) != 20 {
		t.Fatalf("processed=%d stage-calls=%d", stats.Processed, len(got))
	}
	if stats.MaxQueueDepth > 1 {
		t.Fatalf("queue depth %d exceeds capacity 1", stats.MaxQueueDepth)
	}
	if r.LastGoodBoundary() != 5 {
		t.Fatalf("last boundary=%d want 5", r.LastGoodBoundary())
	}
}

func TestSlowConsumerBackPressures(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	slow := Stage{Name: "slow", Run: func(ctx context.Context, in Item) (Item, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return in, nil
	}}
	r := NewRunner(1, false, slow)
	src := make(chan Item, 8)
	done := make(chan struct{})
	go func() {
		_, _ = r.Run(context.Background(), src)
		close(done)
	}()
	for i := 0; i < 6; i++ {
		src <- Item{Seq: int64(i)}
	}
	<-started
	// While the consumer is blocked the queue must stay at capacity, not grow.
	if d := r.queues[0].MaxDepth(); d > 1 {
		t.Fatalf("back-pressure failed, depth=%d", d)
	}
	close(release)
	close(src)
	<-done
}

func TestFatalStageErrorStopsPipeline(t *testing.T) {
	boom := errors.New("boom")
	stage := Stage{Name: "fail", Run: func(_ context.Context, in Item) (Item, error) {
		if in.Seq == 3 {
			return Item{}, model.New(model.StageIndex, model.SeverityFatal, "fatal test").Wrap(boom)
		}
		return in, nil
	}}
	r := NewRunner(2, false, stage)
	_, err := r.Run(context.Background(), makeItems(8))
	if err == nil || !model.IsFatal(err) {
		t.Fatalf("fatal error must propagate, got %v", err)
	}
	if !errors.Is(err, boom) {
		t.Fatal("wrapped cause lost")
	}
}

func TestRecoverableErrorDropsAndContinues(t *testing.T) {
	var got []int64
	stage := Stage{Name: "flaky", Run: func(_ context.Context, in Item) (Item, error) {
		if in.Seq == 2 {
			return Item{}, model.New(model.StageTimebase, model.SeverityRecoverable, "drop me")
		}
		got = append(got, in.Seq)
		return in, nil
	}}
	r := NewRunner(2, true, stage)
	stats, err := r.Run(context.Background(), makeItems(6))
	if err != nil {
		t.Fatalf("recover mode must not fail: %v", err)
	}
	if stats.Dropped != 1 || stats.Processed != 5 {
		t.Fatalf("stats wrong: %+v", stats)
	}
	for _, s := range got {
		if s == 2 {
			t.Fatal("dropped item must not be confirmed")
		}
	}
}

func TestCancellationUnblocksStages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	block := Stage{Name: "block", Run: func(ctx context.Context, in Item) (Item, error) {
		<-ctx.Done()
		return Item{}, ctx.Err()
	}}
	r := NewRunner(1, false, block)
	src := make(chan Item)
	done := make(chan error, 1)
	go func() {
		_, err := r.Run(ctx, src)
		done <- err
	}()
	src <- Item{Seq: 0}
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not unblock the stage")
	}
}
