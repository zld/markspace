package stream

import (
	"context"
	"sync"

	"mediacore/internal/clock"
)

// Semaphore is a byte-counted bounded buffer accounting primitive. Acquire
// blocks (while honoring ctx) until enough budget is free; the runner calls
// Release with the same byte count once a batch is consumed. Peak is tracked
// as evidence for the peak_buffer_bytes report.
type Semaphore struct {
	mu    sync.Mutex
	cond  *sync.Cond
	used  int64
	peak  int64
	limit int64
}

// NewSemaphore builds a semaphore with the given hard byte budget.
func NewSemaphore(limit int64) *Semaphore {
	if limit <= 0 {
		panic("stream: semaphore limit must be positive")
	}
	s := &Semaphore{limit: limit}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// watchContext wakes waiters when the context ends. The returned stop
// function releases the watcher once Acquire has returned.
func (s *Semaphore) watchContext(ctx context.Context) (stop func()) {
	stopCh := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			s.cond.Broadcast()
		case <-stopCh:
		}
	}()
	return func() { close(stopCh) }
}

// Acquire reserves n bytes. A request larger than the limit is rejected as a
// parameter error rather than deadlocking forever.
func (s *Semaphore) Acquire(ctx context.Context, n int64) error {
	if n > s.limit {
		return clock.ErrInvalidArgument("stream: batch of %d bytes exceeds in-flight budget %d", n, s.limit)
	}
	stop := s.watchContext(ctx)
	defer stop()

	s.mu.Lock()
	defer s.mu.Unlock()
	for s.used+n > s.limit {
		if err := ctx.Err(); err != nil {
			return clock.ErrCanceled("stream.backpressure", err)
		}
		s.cond.Wait()
		if err := ctx.Err(); err != nil {
			return clock.ErrCanceled("stream.backpressure", err)
		}
	}
	s.used += n
	if s.used > s.peak {
		s.peak = s.used
	}
	return nil
}

// Release returns n bytes to the pool and wakes one blocked producer.
func (s *Semaphore) Release(n int64) {
	s.mu.Lock()
	s.used -= n
	if s.used < 0 {
		s.mu.Unlock()
		panic("stream: semaphore released below zero")
	}
	s.cond.Signal()
	s.mu.Unlock()
}

// Peak returns the highest simultaneously-reserved byte count observed.
func (s *Semaphore) Peak() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peak
}

// Used returns the currently reserved bytes (tests/metrics).
func (s *Semaphore) Used() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used
}
