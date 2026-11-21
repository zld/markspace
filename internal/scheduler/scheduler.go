// Package scheduler runs segment tasks on a bounded worker pool with retries,
// cooperative cancellation and deterministic failure injection.
package scheduler

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"bitrack/internal/model"
	"bitrack/internal/task"
)

// Processor executes the full encode+validate pipeline for one segment.
// Implementations own FFmpeg invocation, the closed loop and PSNR checks.
type Processor interface {
	Process(ctx context.Context, seg *model.Segment) error
}

// Config tunes the scheduler. FailAfter > 0 deterministically interrupts the
// run once that many segments have reached the committed state, simulating a
// process crash mid-batch for recovery testing.
type Config struct {
	Workers       int
	MaxRetries    int
	FailAfter     int
	InterruptKind string
}

// Result summarizes what the scheduler did.
type Result struct {
	Processed   int
	Reused      int
	Failed      []int
	Interrupted bool
}

// Scheduler coordinates task state transitions. It is single-use: call Run once.
type Scheduler struct {
	cfg         Config
	proc        Processor
	onCommit    func(*model.Segment)
	mu          sync.Mutex
	cond        *sync.Cond
	segs        []*model.Segment
	wg          sync.WaitGroup
	commitCount atomic.Int64
}

// New builds a scheduler over the given segments.
func New(cfg Config, proc Processor, segs []*model.Segment) *Scheduler {
	if cfg.Workers <= 0 {
		cfg.Workers = 1
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	s := &Scheduler{cfg: cfg, proc: proc, segs: segs}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// OnCommit registers a callback fired after every successful commit. It runs
// synchronously inside the worker while the scheduler lock is held, so the
// checkpoint can never observe a half-updated segment.
func (s *Scheduler) OnCommit(fn func(*model.Segment)) { s.onCommit = fn } // Run dispatches segments until all are terminal, the context is cancelled, or
// FailAfter fires. Segments already committed before Run are counted as reused
// and never re-dispatched.
func (s *Scheduler) Run(ctx context.Context) Result {
	var res Result
	for _, seg := range s.segs {
		if seg.State == model.StateCommitted {
			res.Reused++
			s.commitCount.Add(1)
		}
	}
	for w := 0; w < s.cfg.Workers; w++ {
		s.wg.Add(1)
		go s.worker(ctx)
	}

	// Wake workers whenever a task becomes available or state changes.
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		s.cond.Broadcast()
		<-done
	}

	for _, seg := range s.segs {
		switch seg.State {
		case model.StateCommitted:
			if !wasReused(seg) {
				res.Processed++
			}
		case model.StateFailed:
			res.Failed = append(res.Failed, seg.Index)
		}
	}
	s.mu.Lock()
	res.Interrupted = s.interruptFired() || ctx.Err() != nil
	s.mu.Unlock()
	return res
}

// wasReused reports whether the segment was already committed at scheduling.
func wasReused(seg *model.Segment) bool {
	for _, a := range seg.Attempts {
		if a.Reason == "reused-from-checkpoint" {
			return true
		}
	}
	return false
}

func (s *Scheduler) worker(ctx context.Context) {
	defer s.wg.Done()
	for {
		seg := s.pick()
		if seg == nil {
			return
		}
		if !s.claim(seg) {
			continue
		}
		s.execute(ctx, seg)
	}
}

// pick returns the next queued segment, or nil when work is exhausted or the
// run must stop. It blocks while remaining segments are still in flight.
func (s *Scheduler) pick() *model.Segment {
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if s.interruptFired() {
			return nil
		}
		for _, seg := range s.segs {
			if seg.State == model.StateQueued {
				return seg
			}
		}
		// No queued work right now: wait for an in-flight segment to finish
		// and possibly re-queue itself as retryable, otherwise terminate.
		if s.anyInFlight() {
			s.cond.Wait()
			continue
		}
		return nil
	}
}

func (s *Scheduler) anyInFlight() bool {
	for _, seg := range s.segs {
		if seg.State == model.StateRunning ||
			seg.State == model.StateValidating ||
			seg.State == model.StateRetryable ||
			seg.State == model.StatePaused {
			return true
		}
	}
	return false
}

func (s *Scheduler) interruptFired() bool {
	return s.cfg.FailAfter > 0 && s.commitCount.Load() >= int64(s.cfg.FailAfter)
}

// claim moves a queued segment into running; a lost race returns false.
func (s *Scheduler) claim(seg *model.Segment) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seg.State != model.StateQueued || s.interruptFired() {
		s.cond.Broadcast()
		return false
	}
	if err := task.Move(seg, model.StateRunning); err != nil {
		// State machine rejection is a programming error; fail the segment.
		_ = task.Move(seg, model.StateFailed)
		seg.LastError = err.Error()
		return false
	}
	return true
}

// execute runs the processor with bounded retries and drives state moves.
func (s *Scheduler) execute(ctx context.Context, seg *model.Segment) {
	defer s.cond.Broadcast()
	var lastErr error
	for attempt := 0; attempt <= s.cfg.MaxRetries; attempt++ {
		if ctx.Err() != nil || s.interruptFired() {
			s.markRecoverable(seg, "cancelled before encode: "+ctx.Err().Error())
			return
		}
		// Re-enter running after a retryable attempt.
		if seg.State == model.StateRetryable {
			_ = task.Move(seg, model.StateQueued)
			_ = task.Move(seg, model.StateRunning)
		}
		err := s.proc.Process(ctx, seg)
		if err == nil {
			s.mu.Lock()
			_ = task.Move(seg, model.StateCommitted)
			s.commitCount.Add(1)
			if s.onCommit != nil {
				s.onCommit(seg)
			}
			s.mu.Unlock()
			if s.interruptFired() {
				// Deterministic crash point: committed work is durable, the
				// rest stays queued for resume.
				s.mu.Lock()
				s.pauseRemaining()
				s.mu.Unlock()
			}
			return
		}
		lastErr = err
		if attempt < s.cfg.MaxRetries {
			s.mu.Lock()
			_ = task.Move(seg, model.StateRetryable)
			seg.LastError = err.Error()
			s.mu.Unlock()
		}
	}
	s.mu.Lock()
	_ = task.Move(seg, model.StateFailed)
	seg.LastError = fmt.Sprintf("exhausted %d retries: %v", s.cfg.MaxRetries, lastErr)
	s.mu.Unlock()
}

// markRecoverable parks unfinished work so resume can pick it up.
func (s *Scheduler) markRecoverable(seg *model.Segment, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seg.State == model.StateRunning || seg.State == model.StateValidating {
		_ = task.Move(seg, model.StatePaused)
	} else if seg.State == model.StateQueued {
		_ = task.Move(seg, model.StatePaused)
	}
	seg.LastError = reason
}

// pauseRemaining moves every not-yet-committed segment to paused.
func (s *Scheduler) pauseRemaining() {
	for _, seg := range s.segs {
		if seg.State == model.StateQueued || seg.State == model.StateRunning ||
			seg.State == model.StateValidating || seg.State == model.StateRetryable {
			_ = task.Move(seg, model.StatePaused)
		}
	}
}
