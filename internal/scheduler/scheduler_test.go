package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"bitrack/internal/model"
)

// fakeProcessor drives deterministic outcomes per segment index.
type fakeProcessor struct {
	mu        sync.Mutex
	failOnce  map[int]int // segment index -> number of failing calls before success
	calls     map[int]int
	permanent map[int]bool
}

func (f *fakeProcessor) Process(ctx context.Context, seg *model.Segment) error {
	f.mu.Lock()
	f.calls[seg.Index]++
	calls := f.calls[seg.Index]
	if f.permanent[seg.Index] {
		f.mu.Unlock()
		return &permErr{errors.New("deterministic fault")}
	}
	if f.failOnce[seg.Index] >= calls {
		f.mu.Unlock()
		return errors.New("transient failure")
	}
	f.mu.Unlock()
	seg.PSNRDB, seg.ActualBits = 40, 1000
	return nil
}

type permErr struct{ error }

func (p *permErr) IsPermanent() bool { return true }

func newSegs(n int) []*model.Segment {
	segs := make([]*model.Segment, n)
	for i := range segs {
		segs[i] = &model.Segment{Index: i, State: model.StateQueued, TargetBits: 1000}
	}
	return segs
}

func TestAllSegmentsCommit(t *testing.T) {
	segs := newSegs(6)
	proc := &fakeProcessor{calls: map[int]int{}}
	s := New(Config{Workers: 3, MaxRetries: 2}, proc, segs)
	res := s.Run(context.Background())
	if len(res.Failed) != 0 || res.Processed != 6 {
		t.Fatalf("expected 6 processed, got processed=%d failed=%v", res.Processed, res.Failed)
	}
	for _, seg := range segs {
		if seg.State != model.StateCommitted {
			t.Errorf("segment %d state = %s, want committed", seg.Index, seg.State)
		}
	}
}

func TestTransientFailureRetriedThenCommitted(t *testing.T) {
	segs := newSegs(2)
	proc := &fakeProcessor{calls: map[int]int{}, failOnce: map[int]int{0: 1}}
	s := New(Config{Workers: 1, MaxRetries: 2}, proc, segs)
	res := s.Run(context.Background())
	if len(res.Failed) != 0 {
		t.Fatalf("retryable failure should recover, got %v", res.Failed)
	}
	if proc.calls[0] != 2 {
		t.Errorf("segment 0 processed %d times, want 2", proc.calls[0])
	}
	if segs[0].State != model.StateCommitted {
		t.Errorf("state = %s", segs[0].State)
	}
}

func TestPermanentFailureNotRetried(t *testing.T) {
	segs := newSegs(2)
	proc := &fakeProcessor{calls: map[int]int{}, permanent: map[int]bool{0: true}}
	s := New(Config{Workers: 1, MaxRetries: 3}, proc, segs)
	res := s.Run(context.Background())
	if len(res.Failed) != 1 || res.Failed[0] != 0 {
		t.Fatalf("segment 0 must fail, got %v", res.Failed)
	}
	if proc.calls[0] != 1 {
		t.Errorf("permanent fault called %d times, must not retry", proc.calls[0])
	}
	if segs[0].State != model.StateFailed {
		t.Errorf("state = %s, want failed", segs[0].State)
	}
}

func TestFailAfterPausesRemaining(t *testing.T) {
	segs := newSegs(5)
	proc := &fakeProcessor{calls: map[int]int{}}
	// A single worker makes the fail-after threshold deterministic.
	s := New(Config{Workers: 1, MaxRetries: 0, FailAfter: 2}, proc, segs)
	res := s.Run(context.Background())
	if !res.Interrupted {
		t.Fatal("fail-after should mark the run interrupted")
	}
	committed, paused := 0, 0
	for _, seg := range segs {
		switch seg.State {
		case model.StateCommitted:
			committed++
		case model.StatePaused:
			paused++
		}
	}
	if committed != 2 {
		t.Errorf("committed = %d, want exactly 2", committed)
	}
	if committed+paused != 5 {
		t.Errorf("every segment must be committed or paused, got %d/%d", committed, paused)
	}
}

func TestFailAfterUnderConcurrency(t *testing.T) {
	segs := newSegs(8)
	proc := &fakeProcessor{calls: map[int]int{}}
	// With concurrent workers, an in-flight segment may finish at the instant
	// the threshold is hit, so committed may exceed N but never go below it,
	// and no segment may be left in a non-recoverable state.
	s := New(Config{Workers: 3, MaxRetries: 0, FailAfter: 3}, proc, segs)
	res := s.Run(context.Background())
	if !res.Interrupted {
		t.Fatal("fail-after should mark the run interrupted")
	}
	committed := 0
	for _, seg := range segs {
		switch seg.State {
		case model.StateCommitted:
			committed++
		case model.StatePaused:
		default:
			t.Errorf("segment %d in non-recoverable state %s", seg.Index, seg.State)
		}
	}
	if committed < 3 {
		t.Errorf("committed = %d, must be at least the fail-after threshold 3", committed)
	}
}

func TestCancellationParksInFlight(t *testing.T) {
	segs := newSegs(3)
	proc := &blockingProcessor{block: make(chan struct{})}
	s := New(Config{Workers: 2, MaxRetries: 0}, proc, segs)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	// Give a worker time to enter the blocking Process call.
	time.Sleep(80 * time.Millisecond)
	cancel()
	close(proc.block) // release every blocked or future Process call
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not stop after cancellation")
	}
	for _, seg := range segs {
		if seg.State == model.StateRunning || seg.State == model.StateValidating {
			t.Errorf("segment %d left in-flight (%s) after cancel", seg.Index, seg.State)
		}
	}
}

type blockingProcessor struct{ block chan struct{} }

func (b *blockingProcessor) Process(ctx context.Context, seg *model.Segment) error {
	select {
	case <-b.block:
	case <-ctx.Done():
		return ctx.Err()
	}
	// After release, honor cancellation so retries do not re-enter work.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	seg.PSNRDB, seg.ActualBits = 40, 1000
	return nil
}

func TestPreCommittedSegmentsCountedReused(t *testing.T) {
	segs := newSegs(3)
	segs[0].State = model.StateCommitted
	segs[0].Attempts = []model.Attempt{{Reason: "reused-from-checkpoint"}}
	s := New(Config{Workers: 1}, &fakeProcessor{calls: map[int]int{}}, segs)
	res := s.Run(context.Background())
	if res.Reused != 1 || res.Processed != 2 {
		t.Errorf("reused=%d processed=%d, want 1 and 2", res.Reused, res.Processed)
	}
}
