package flow

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func collect(t *testing.T, p *Pipeline, n int, produce func(context.Context, chan<- Job) error) (Stats, []Result, error) {
	t.Helper()
	var mu sync.Mutex
	var got []Result
	st, err := p.Run(context.Background(), produce,
		func(ctx context.Context, job Job) (any, error) {
			return job.Data.(int) * 2, nil
		},
		func(r Result) error {
			mu.Lock()
			got = append(got, r)
			mu.Unlock()
			return nil
		})
	return st, got, err
}

func rangeProducer(n int) func(context.Context, chan<- Job) error {
	return func(ctx context.Context, jobs chan<- Job) error {
		for i := 1; i <= n; i++ {
			select {
			case jobs <- Job{Seq: i, Data: i}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
}

func TestPipelineProcessesAllJobs(t *testing.T) {
	p, err := New(Config{Workers: 3, Queue: 2})
	if err != nil {
		t.Fatal(err)
	}
	st, got, err := collect(t, p, 20, rangeProducer(20))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 20 || st.Submitted != 20 || st.Completed != 20 || st.Failed != 0 {
		t.Fatalf("stats=%#v results=%d", st, len(got))
	}
	if st.QueuePeak > 2 {
		t.Fatalf("queue peak %d exceeds capacity 2", st.QueuePeak)
	}
}

func TestPipelinePreservesWork(t *testing.T) {
	p, _ := New(Config{Workers: 2, Queue: 1})
	_, got, err := collect(t, p, 5, rangeProducer(5))
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[int]bool)
	for _, r := range got {
		seen[r.Data.(int)] = true
	}
	for i := 2; i <= 10; i += 2 {
		if !seen[i] {
			t.Fatalf("missing result %d", i)
		}
	}
}

func TestPipelineConfigValidation(t *testing.T) {
	if _, err := New(Config{Workers: 0, Queue: 1}); err == nil {
		t.Fatal("expected workers error")
	}
	if _, err := New(Config{Workers: 1, Queue: 0}); err == nil {
		t.Fatal("expected queue error")
	}
}

func TestPipelineSlowConsumerBackpressure(t *testing.T) {
	p, _ := New(Config{Workers: 1, Queue: 1})
	var produced int32
	produce := func(ctx context.Context, jobs chan<- Job) error {
		for i := 1; i <= 10; i++ {
			atomic.AddInt32(&produced, 1)
			select {
			case jobs <- Job{Seq: i}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	// The consumer sleeps so the pipeline cannot run ahead by more than the
	// worker plus queue capacity.
	st, err := p.Run(context.Background(), produce,
		func(ctx context.Context, job Job) (any, error) { return nil, nil },
		func(r Result) error {
			time.Sleep(2 * time.Millisecond)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if st.QueuePeak > 1 {
		t.Fatalf("queue peak=%d", st.QueuePeak)
	}
	if int(atomic.LoadInt32(&produced)) != 10 {
		t.Fatalf("produced=%d", produced)
	}
}

func TestPipelineConsumerErrorCancelsProducerAndWorkers(t *testing.T) {
	p, _ := New(Config{Workers: 2, Queue: 2})
	want := errors.New("stop")
	var producerSawCancel atomic.Bool
	produce := func(ctx context.Context, jobs chan<- Job) error {
		for i := 1; i <= 1000; i++ {
			select {
			case jobs <- Job{Seq: i}:
			case <-ctx.Done():
				producerSawCancel.Store(true)
				return ctx.Err()
			}
		}
		return nil
	}
	calls := 0
	st, err := p.Run(context.Background(), produce,
		func(ctx context.Context, job Job) (any, error) { return nil, nil },
		func(r Result) error {
			calls++
			if calls == 3 {
				return want
			}
			return nil
		})
	if !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
	if !producerSawCancel.Load() {
		t.Fatal("cancellation did not reach the producer")
	}
	if st.Completed < 3 {
		t.Fatalf("stats=%#v", st)
	}
}

func TestPipelineProducerErrorIsReported(t *testing.T) {
	p, _ := New(Config{Workers: 2, Queue: 1})
	want := errors.New("producer failed")
	produce := func(ctx context.Context, jobs chan<- Job) error {
		for i := 1; i <= 3; i++ {
			jobs <- Job{Seq: i}
		}
		return want
	}
	_, err := p.Run(context.Background(), produce,
		func(ctx context.Context, job Job) (any, error) { return nil, nil },
		func(r Result) error { return nil })
	if !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
}

func TestPipelineContextCancellation(t *testing.T) {
	p, _ := New(Config{Workers: 1, Queue: 1})
	ctx, cancel := context.WithCancel(context.Background())
	produce := func(ctx context.Context, jobs chan<- Job) error {
		for i := 1; ; i++ {
			select {
			case jobs <- Job{Seq: i}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	calls := 0
	_, err := p.Run(ctx, produce,
		func(ctx context.Context, job Job) (any, error) { return nil, nil },
		func(r Result) error {
			calls++
			if calls == 2 {
				cancel()
			}
			return nil
		})
	if !IsCanceled(err) {
		t.Fatalf("err=%v", err)
	}
}

func TestMergerReleasesContiguousPrefixOnly(t *testing.T) {
	m := NewMerger()
	if got := m.Add(Result{Seq: 2}); len(got) != 0 {
		t.Fatalf("released %#v before seq 1", got)
	}
	if m.LastSeq() != 0 {
		t.Fatalf("last seq=%d", m.LastSeq())
	}
	got := m.Add(Result{Seq: 1})
	if len(got) != 2 || got[0].Seq != 1 || got[1].Seq != 2 {
		t.Fatalf("released %#v", got)
	}
	if m.LastSeq() != 2 || m.NextSeq() != 3 || m.Pending() != 0 {
		t.Fatalf("state last=%d next=%d pending=%d", m.LastSeq(), m.NextSeq(), m.Pending())
	}
}

func TestMergerBuffersGapAndReports(t *testing.T) {
	m := NewMerger()
	m.Add(Result{Seq: 1})
	m.Add(Result{Seq: 3})
	m.Add(Result{Seq: 5})
	if m.LastSeq() != 1 {
		t.Fatalf("last=%d", m.LastSeq())
	}
	gap := m.DrainGap()
	if len(gap) != 2 || gap[0] != 3 || gap[1] != 5 {
		t.Fatalf("gap=%#v", gap)
	}
	if err := m.ErrGap(); err == nil {
		t.Fatal("expected gap error")
	}
	m.Add(Result{Seq: 2})
	if m.LastSeq() != 3 {
		t.Fatalf("last=%d after filling gap", m.LastSeq())
	}
}

func TestMergerIgnoresDuplicateAndStale(t *testing.T) {
	m := NewMerger()
	m.Add(Result{Seq: 1})
	m.Add(Result{Seq: 2})
	if got := m.Add(Result{Seq: 2}); len(got) != 0 {
		t.Fatalf("duplicate released %#v", got)
	}
	if got := m.Add(Result{Seq: 1}); len(got) != 0 {
		t.Fatalf("stale released %#v", got)
	}
	if len(m.Released()) != 2 {
		t.Fatalf("released=%d", len(m.Released()))
	}
}

func TestMergerSurvivesOutOfOrderCompletion(t *testing.T) {
	p, _ := New(Config{Workers: 4, Queue: 2})
	m := NewMerger()
	produce := rangeProducer(30)
	_, err := p.Run(context.Background(), produce,
		func(ctx context.Context, job Job) (any, error) {
			// Reverse the completion order roughly by sleeping longer for
			// earlier jobs.
			time.Sleep(time.Duration(30-job.Seq) * 100 * time.Microsecond)
			return job.Seq, nil
		},
		func(r Result) error {
			m.Add(r)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if m.LastSeq() != 30 {
		t.Fatalf("last=%d gap=%v", m.LastSeq(), m.DrainGap())
	}
	for i, r := range m.Released() {
		if r.Seq != i+1 {
			t.Fatalf("released out of order at %d: %#v", i, r)
		}
	}
}

func TestIsCanceled(t *testing.T) {
	if IsCanceled(nil) || IsCanceled(errors.New("x")) {
		t.Fatal("false positives")
	}
	if !IsCanceled(context.Canceled) {
		t.Fatal("context.Canceled")
	}
	if !IsCanceled(&PipelineError{Canceled: true, Err: errors.New("x")}) {
		t.Fatal("pipeline error")
	}
}
