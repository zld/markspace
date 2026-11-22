package pipeline

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func makeTasks(n int) []Task {
	out := make([]Task, n)
	for i := range out {
		out[i] = Task{
			Track: uint32(i % 3), Seq: uint64(i / 3),
			NS: int64((i*37)%97) + int64(i)*100, Index: int64(i),
			Data: []byte{byte(i)},
		}
	}
	return out
}

func identityProcess(_ context.Context, t Task) Result {
	return Result{Task: t, Out: append([]byte(nil), t.Data...)}
}

func TestPoolBoundsAndBackPressure(t *testing.T) {
	ctx := context.Background()
	pool, err := NewPool(ctx, 2, 4, identityProcess)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewPool(ctx, 0, 4, identityProcess); err == nil {
		t.Fatal("zero workers must fail")
	}
	if _, err := NewPool(ctx, 2, MaxQueue+1, identityProcess); err == nil {
		t.Fatal("oversize queue must fail")
	}
	var inFlight, maxInFlight int32
	var mu sync.Mutex
	seen := map[int64]bool{}
	process := func(ctx context.Context, tk Task) Result {
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			m := atomic.LoadInt32(&maxInFlight)
			if cur <= m || atomic.CompareAndSwapInt32(&maxInFlight, m, cur) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		mu.Lock()
		seen[tk.Index] = true
		mu.Unlock()
		return Result{Task: tk}
	}
	pool2, _ := NewPool(ctx, 2, 4, process)
	tasks := makeTasks(50)
	for _, tk := range tasks {
		if err := pool2.Submit(tk); err != nil {
			t.Fatal(err)
		}
	}
	pool2.Close()
	if len(seen) != len(tasks) {
		t.Fatalf("processed %d of %d", len(seen), len(tasks))
	}
	if maxInFlight > 2 {
		t.Fatalf("concurrency exceeded workers: %d", maxInFlight)
	}
	pool.Close()
}

func TestDeterministicMergeOrder(t *testing.T) {
	run := func() []Result {
		var sink []Result
		merged, err := Run(context.Background(), 4, 8,
			sliceSource(makeTasks(60)), identityProcess,
			func(_ context.Context, r Result) error { sink = append(sink, r); return nil })
		if err != nil {
			t.Fatal(err)
		}
		return merged
	}
	a, b := run(), run()
	if len(a) != 60 || len(b) != 60 {
		t.Fatalf("lengths %d %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Task.Index != b[i].Task.Index {
			t.Fatalf("nondeterministic order at %d", i)
		}
		if i > 0 && orderedLess(a[i], a[i-1]) {
			t.Fatalf("merge order violated at %d", i)
		}
	}
}

func TestCancellationPropagatesReaderToWriter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var started int32
	var observed int32
	process := func(ctx context.Context, tk Task) Result {
		atomic.AddInt32(&started, 1)
		<-ctx.Done() // workers block until cancellation reaches them
		atomic.AddInt32(&observed, 1)
		return Result{Task: tk, Err: ctx.Err()}
	}
	src := func(ctx context.Context) (Task, error) {
		// Emit until cancelled; Submit must unblock on cancellation.
		i := int64(0)
		for {
			select {
			case <-ctx.Done():
				return Task{}, ctx.Err()
			default:
			}
			i++
			return Task{Index: i, NS: i}, nil
		}
	}
	sink := func(context.Context, Result) error { return nil }

	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, 4, 8, src, process, sink)
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled run must return an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not propagate; run hung")
	}
	if atomic.LoadInt32(&observed) == 0 {
		t.Fatal("no worker observed the cancelled context")
	}
}

func TestNoTasksOutlivePool(t *testing.T) {
	ctx := context.Background()
	var active int32
	process := func(ctx context.Context, tk Task) Result {
		atomic.AddInt32(&active, 1)
		time.Sleep(2 * time.Millisecond)
		atomic.AddInt32(&active, -1)
		return Result{Task: tk}
	}
	pool, _ := NewPool(ctx, 4, 16, process)
	for _, tk := range makeTasks(100) {
		if err := pool.Submit(tk); err != nil {
			t.Fatal(err)
		}
	}
	pool.Close()
	select {
	case <-pool.Closed():
	default:
		t.Fatal("Closed channel not signaled")
	}
	if got := atomic.LoadInt32(&active); got != 0 {
		t.Fatalf("%d tasks still active after Close", got)
	}
}

func TestWorkerErrorSuppressesSink(t *testing.T) {
	var sinkCalls int32
	process := func(_ context.Context, tk Task) Result {
		if tk.Index == 25 {
			return Result{Task: tk, Err: errors.New("simulated corruption")}
		}
		return Result{Task: tk}
	}
	sink := func(context.Context, Result) error {
		atomic.AddInt32(&sinkCalls, 1)
		return nil
	}
	_, err := Run(context.Background(), 3, 8, sliceSource(makeTasks(40)), process, sink)
	if err == nil {
		t.Fatal("corrupt intermediate result must fail the run")
	}
	if got := atomic.LoadInt32(&sinkCalls); got != 0 {
		t.Fatalf("sink published %d results before failure; must publish none", got)
	}
}

func TestSourceErrorStopsPipeline(t *testing.T) {
	boom := errors.New("reader failure")
	src := func(context.Context) (Task, error) {
		return Task{}, boom
	}
	_, err := Run(context.Background(), 2, 4, src, identityProcess,
		func(context.Context, Result) error { return nil })
	if !errors.Is(err, boom) {
		t.Fatalf("want reader failure, got %v", err)
	}
}

func TestSinkErrorStopsPipeline(t *testing.T) {
	boom := errors.New("writer failure")
	var calls int
	sink := func(context.Context, Result) error {
		calls++
		if calls == 3 {
			return boom
		}
		return nil
	}
	_, err := Run(context.Background(), 2, 4, sliceSource(makeTasks(20)), identityProcess, sink)
	if !errors.Is(err, boom) {
		t.Fatalf("want sink failure, got %v", err)
	}
}

// sliceSource returns a deterministic Source over a task slice.
func sliceSource(tasks []Task) Source {
	i := 0
	var mu sync.Mutex
	return func(context.Context) (Task, error) {
		mu.Lock()
		defer mu.Unlock()
		if i >= len(tasks) {
			return Task{}, io.EOF
		}
		tk := tasks[i]
		i++
		return tk, nil
	}
}
