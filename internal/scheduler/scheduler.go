// Package scheduler runs segment tasks on a bounded worker pool with retries,
// cooperative cancellation and deterministic failure injection.
package scheduler

import (
	"context"
	"errors"
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

// permanentError is the structural marker for non-retryable failures. Any
// error implementing it (e.g. pipeline's injected faults) bypasses retries,
// avoiding an import cycle with the pipeline package.
type permanentError interface {
	IsPermanent() bool
}

func isPermanent(err error) bool {
	var pe permanentError
	return errors.As(err, &pe)
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
	cfg       Config
	proc      Processor
	onCommit  func(*model.Segment)
	mu        sync.Mutex
	cond      *sync.Cond
	queue     []*model.Segment // segments waiting to run
	inFlight  int              // segments currently owned by a worker
	segs      []*model.Segment
	wg        sync.WaitGroup
	committed atomic.Int64
	stopping  bool
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
func (s *Scheduler) OnCommit(fn func(*model.Segment)) { s.onCommit = fn }

// Run dispatches segments until all are terminal, the context is cancelled, or
// FailAfter fires. Segments already committed before Run are counted as reused
// and never re-dispatched.
func (s *Scheduler) Run(ctx context.Context) Result {
	var res Result
	s.mu.Lock()
	for _, seg := range s.segs {
		if seg.State == model.StateCommitted {
			res.Reused++
			s.committed.Add(1)
		} else {
			// Every non-terminal segment enters the queue, whatever its
			// persisted state (queued, paused, retryable, ...).
			seg.State = model.StateQueued
			s.queue = append(s.queue, seg)
		}
	}
	s.mu.Unlock()

	for w := 0; w < s.cfg.Workers; w++ {
		s.wg.Add(1)
		go s.worker(ctx)
	}
	// Wake workers parked on the condition when the context is cancelled.
	stopWatch := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			s.stopping = true
			s.cond.Broadcast()
			s.mu.Unlock()
		case <-stopWatch:
		}
	}()
	s.wg.Wait()
	close(stopWatch)

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

func (s *Scheduler) interruptFired() bool {
	return s.cfg.FailAfter > 0 && s.committed.Load() >= int64(s.cfg.FailAfter)
}

// next blocks until a queued segment is available or work is fully drained.
func (s *Scheduler) next(ctx context.Context) *model.Segment {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.queue) == 0 {
		if s.stopping || s.interruptFired() || ctx.Err() != nil || s.inFlight == 0 {
			return nil
		}
		s.cond.Wait()
	}
	seg := s.queue[0]
	s.queue = s.queue[1:]
	if err := task.Move(seg, model.StateRunning); err != nil {
		// Illegal move is a programming error; fail the segment explicitly.
		_ = task.Move(seg, model.StateFailed)
		seg.LastError = err.Error()
		return nil
	}
	s.inFlight++
	return seg
}

func (s *Scheduler) worker(ctx context.Context) {
	defer s.wg.Done()
	for {
		if ctx.Err() != nil {
			return
		}
		seg := s.next(ctx)
		if seg == nil {
			return
		}
		s.execute(ctx, seg)
		s.mu.Lock()
		s.inFlight--
		// When the fail-after threshold is reached, park every queued segment
		// so a resume picks it up; then wake peers so they observe the stop.
		if s.interruptFired() {
			s.stopping = true
			for _, q := range s.queue {
				_ = task.Move(q, model.StatePaused)
			}
			s.queue = nil
		}
		s.cond.Broadcast()
		s.mu.Unlock()
	}
}

// execute runs the processor with bounded retries and drives state moves.
func (s *Scheduler) execute(ctx context.Context, seg *model.Segment) {
	var lastErr error
	for attempt := 0; attempt <= s.cfg.MaxRetries; attempt++ {
		if ctx.Err() != nil {
			s.markRecoverable(seg, "cancelled before encode: "+ctx.Err().Error())
			return
		}
		if attempt > 0 {
			// Re-enter running for a retry attempt.
			if err := task.Move(seg, model.StateQueued); err != nil {
				s.failSegment(seg, err)
				return
			}
			if err := task.Move(seg, model.StateRunning); err != nil {
				s.failSegment(seg, err)
				return
			}
		}
		err := s.proc.Process(ctx, seg)
		if err == nil {
			if ctx.Err() != nil {
				s.markRecoverable(seg, "cancelled after encode: "+ctx.Err().Error())
				return
			}
			s.mu.Lock()
			// Process performs its quality checks while "running"; a clean
			// result passes through validating before the terminal commit.
			if merr := task.Move(seg, model.StateValidating); merr != nil {
				s.mu.Unlock()
				s.failSegment(seg, merr)
				return
			}
			_ = task.Move(seg, model.StateCommitted)
			s.committed.Add(1)
			if s.onCommit != nil {
				s.onCommit(seg)
			}
			s.mu.Unlock()
			return
		}
		if ctx.Err() != nil {
			s.markRecoverable(seg, "cancelled during encode: "+err.Error())
			return
		}
		lastErr = err
		// Injected and other permanent failures are deterministic: retrying
		// either fails identically or silently succeeds on a one-shot fault.
		if isPermanent(err) {
			break
		}
		if attempt < s.cfg.MaxRetries {
			s.mu.Lock()
			_ = task.Move(seg, model.StateRetryable)
			seg.LastError = err.Error()
			s.mu.Unlock()
		}
	}
	s.failSegment(seg, lastErr)
}

func (s *Scheduler) failSegment(seg *model.Segment, lastErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = task.Move(seg, model.StateFailed)
	if isPermanent(lastErr) {
		seg.LastError = fmt.Sprintf("permanent failure (not retried): %v", lastErr)
	} else {
		seg.LastError = fmt.Sprintf("exhausted %d retries: %v", s.cfg.MaxRetries, lastErr)
	}
}

// markRecoverable parks unfinished work so a later resume can pick it up.
func (s *Scheduler) markRecoverable(seg *model.Segment, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch seg.State {
	case model.StateRunning, model.StateValidating, model.StateQueued:
		_ = task.Move(seg, model.StatePaused)
	}
	seg.LastError = reason
}
