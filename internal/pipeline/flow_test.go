package pipeline

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"mediapipe/internal/model"
)

func intStage(ctx context.Context, in <-chan int, out chan<- int) error {
	for {
		select {
		case <-ctx.Done():
			return ctxErr(ctx)
		case v, ok := <-in:
			if !ok {
				return nil
			}
			out <- v
		}
	}
}

func TestRunAppliesStagesInOrder(t *testing.T) {
	mk := func(name string, f func(int) int) Stage[int] {
		return Stage[int]{Name: name, Work: func(_ context.Context, v int) ([]int, error) {
			return []int{f(v)}, nil
		}}
	}
	res := Run(context.Background(), Config{Buffer: 1}, []Stage[int]{
		mk("a", func(v int) int { return v + 1 }),
		mk("b", func(v int) int { return v * 2 }),
	}, []int{1, 2, 3})
	if res.Err != nil {
		t.Fatalf("run: %v", res.Err)
	}
	want := []int{4, 6, 8}
	if len(res.Items) != 3 {
		t.Fatalf("items = %v", res.Items)
	}
	for i := range want {
		if res.Items[i] != want[i] {
			t.Fatalf("items = %v, want %v", res.Items, want)
		}
	}
	if res.Stats.StageLatency["a"].Count != 3 {
		t.Fatalf("latency stats = %+v", res.Stats.StageLatency)
	}
}

func TestSlowConsumerBackpressure(t *testing.T) {
	var inFlight int32
	var peak int32
	// A consumer that sleeps per item, with only 1 slot of buffering, must
	// keep the number of items in flight bounded rather than absorbing the
	// whole input.
	slow := Stage[int]{Name: "slow", Work: func(_ context.Context, v int) ([]int, error) {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		time.Sleep(200 * time.Microsecond)
		atomic.AddInt32(&inFlight, -1)
		return []int{v}, nil
	}}
	fast := Stage[int]{Name: "fast", Work: func(_ context.Context, v int) ([]int, error) { return []int{v}, nil }}
	input := make([]int, 200)
	res := Run(context.Background(), Config{Buffer: 1}, []Stage[int]{fast, slow}, input)
	if res.Err != nil {
		t.Fatalf("run: %v", res.Err)
	}
	if len(res.Items) != 200 {
		t.Fatalf("delivered %d items", len(res.Items))
	}
	if got := atomic.LoadInt32(&peak); got > 3 {
		t.Fatalf("backpressure failed: %d items in flight with buffer 1", got)
	}
}

func TestStageFailureStopsEveryStageWithSameCause(t *testing.T) {
	cause := errors.New("boom")
	var afterRan int32
	failing := Stage[int]{Name: "fail", Work: func(_ context.Context, v int) ([]int, error) {
		if v == 5 {
			return nil, model.New(model.KindCorrupt, model.StageMP4Scan, "a.mp4", "scan", cause).WithOffset(4096)
		}
		return []int{v}, nil
	}}
	after := Stage[int]{Name: "after", Work: func(_ context.Context, v int) ([]int, error) {
		atomic.AddInt32(&afterRan, 1)
		return []int{v}, nil
	}}
	input := make([]int, 100)
	for i := range input {
		input[i] = i
	}
	res := Run(context.Background(), Config{Buffer: 1}, []Stage[int]{
		{Name: "pre", Work: func(_ context.Context, v int) ([]int, error) { return []int{v}, nil }},
		failing, after,
	}, input)
	if res.Err == nil {
		t.Fatal("expected failure")
	}
	var me *model.Error
	if !errors.As(res.Err, &me) {
		t.Fatalf("want structured error, got %v", res.Err)
	}
	if me.Stage != model.StageMP4Scan || me.Offset != 4096 {
		t.Fatalf("cause lost: %+v", me)
	}
	if !errors.Is(res.Err, cause) {
		t.Fatal("root cause chain broken")
	}
	// The downstream stage must not have processed the remaining items.
	if atomic.LoadInt32(&afterRan) >= 100 {
		t.Fatalf("downstream stage kept running after failure: %d", afterRan)
	}
	if model.ExitCodeOf(res.Err) != 2 {
		t.Fatalf("exit = %d", model.ExitCodeOf(res.Err))
	}
}

func TestCancellationPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	blocking := Stage[int]{Name: "block", Work: func(ctx context.Context, v int) ([]int, error) {
		select {
		case <-ctx.Done():
			return nil, ctxErr(ctx)
		case <-time.After(10 * time.Second):
			return []int{v}, nil
		}
	}}
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	res := Run(ctx, Config{Buffer: 1}, []Stage[int]{blocking}, []int{1, 2, 3})
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("cancellation took %v", elapsed)
	}
	if !res.ErrCancelled() {
		t.Fatalf("want cancelled, got %v", res.Err)
	}
	var me *model.Error
	if !errors.As(res.Err, &me) || me.Kind != model.KindCancelled {
		t.Fatalf("error = %v", res.Err)
	}
	if !errors.Is(CauseOf(res.Err), context.Canceled) {
		t.Fatalf("root cause = %v", CauseOf(res.Err))
	}
}

func TestValidateStages(t *testing.T) {
	if err := ValidateStages([]Stage[int]{}); err == nil {
		t.Fatal("empty chain should be invalid")
	}
	dup := []Stage[int]{
		{Name: "a", Work: func(context.Context, int) ([]int, error) { return nil, nil }},
		{Name: "a", Work: func(context.Context, int) ([]int, error) { return nil, nil }},
	}
	if err := ValidateStages(dup); err == nil {
		t.Fatal("duplicate names should be invalid")
	}
}

func TestLatencySummaries(t *testing.T) {
	s := summarise([]int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20})
	if s.Count != 20 || s.MaxUS != 20 || s.MeanUS != 10 {
		t.Fatalf("summary = %+v", s)
	}
	if s.P95US != 20 {
		t.Fatalf("p95 = %d", s.P95US)
	}
	if summarise(nil).Count != 0 {
		t.Fatal("empty summary should be zero")
	}
}
