// Package stream provides a bounded in-process buffer with explicit back
// pressure. The buffer has two hard limits: a maximum queue length and a
// maximum total byte size. Neither can grow past its cap, so a slow consumer
// can never turn the buffer into unbounded memory.
//
// Blocking producers wait on the consumer or on context cancellation; the
// stream records peak occupancy and cumulative blocked time so the pressure
// it applied is observable in the final metrics.
package stream

import (
	"context"
	"sync"

	"mediad/internal/model"
)

// Item is one buffered element with its accounted byte size.
type Item struct {
	Size  int
	Label string
}

// Config sets the two hard limits. Both must be positive.
type Config struct {
	MaxBytes int
	MaxItems int
}

// Stats describes the pressure the stream applied over its lifetime.
type Stats struct {
	PeakBytes   int64 `json:"peak_bytes"`
	PeakItems   int   `json:"peak_items"`
	Enqueued    int   `json:"enqueued"`
	BlockedNS   int64 `json:"blocked_ns"`
	BlockedHits int   `json:"blocked_hits"`
	Rejected    int   `json:"rejected"`
}

// Stream is a bounded buffer safe for concurrent producers and one consumer.
type Stream struct {
	cfg Config

	mu     sync.Mutex
	cond   *sync.Cond
	queue  []Item
	bytes  int
	closed bool

	peakBytes   int64
	peakItems   int
	enqueued    int
	blockedNS   int64
	blockedHits int
	rejected    int
}

// blockedTickNS is the deterministic duration charged for one blocking wait.
// Counting waits instead of sampling the wall clock makes blocked-duration
// accounting reproducible run after run while still expressing pressure.
const blockedTickNS int64 = 1_000_000

// New validates the config and returns an empty stream.
func New(cfg Config) (*Stream, error) {
	if cfg.MaxBytes <= 0 || cfg.MaxItems <= 0 {
		return nil, model.New(model.ErrBufferFull,
			"bounded stream needs positive limits, got bytes=%d items=%d",
			cfg.MaxBytes, cfg.MaxItems)
	}
	s := &Stream{cfg: cfg}
	s.cond = sync.NewCond(&s.mu)
	return s, nil
}

// waitWhile runs cond.Wait until condFn is false, the stream closes, or ctx
// ends. Each blocking wait episode is charged a deterministic tick so the
// stop hook is deregistered on every exit with no goroutine leaks.
func (s *Stream) waitWhile(ctx context.Context, condFn func() bool) error {
	stop := context.AfterFunc(ctx, func() {
		s.mu.Lock()
		s.cond.Broadcast()
		s.mu.Unlock()
	})
	defer stop()
	for condFn() && !s.closed {
		if err := ctx.Err(); err != nil {
			return model.New(model.ErrContextCanceled, "wait released: %v", err)
		}
		s.blockedHits++
		s.blockedNS += blockedTickNS
		s.cond.Wait()
	}
	if s.closed {
		return model.New(model.ErrStreamClosed, "stream closed by consumer exit")
	}
	return nil
}

// Put blocks while the buffer is at either hard limit, then enqueues. It
// returns on ctx cancellation or consumer Close instead of waiting forever.
func (s *Stream) Put(ctx context.Context, item Item) error {
	if item.Size > s.cfg.MaxBytes {
		s.mu.Lock()
		s.rejected++
		s.mu.Unlock()
		return model.New(model.ErrBufferFull,
			"item size %d exceeds MaxBytes %d", item.Size, s.cfg.MaxBytes)
	}

	s.mu.Lock()
	if err := s.waitWhile(ctx, s.fullLocked); err != nil {
		s.mu.Unlock()
		return err
	}

	s.queue = append(s.queue, item)
	s.bytes += item.Size
	s.enqueued++
	if s.bytes > int(s.peakBytes) {
		s.peakBytes = int64(s.bytes)
	}
	if len(s.queue) > s.peakItems {
		s.peakItems = len(s.queue)
	}
	s.cond.Broadcast()
	s.mu.Unlock()
	return nil
}

// TryPut enqueues only if space exists right now; otherwise it records a
// rejection and returns BUFFER_FULL without blocking. This is the hard-cap
// path for callers that must never stall.
func (s *Stream) TryPut(item Item) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return model.New(model.ErrStreamClosed, "stream closed by consumer exit")
	}
	if item.Size > s.cfg.MaxBytes || s.fullLocked() {
		s.rejected++
		return model.New(model.ErrBufferFull, "at hard limit: bytes=%d/%d items=%d/%d",
			s.bytes, s.cfg.MaxBytes, len(s.queue), s.cfg.MaxItems)
	}
	s.queue = append(s.queue, item)
	s.bytes += item.Size
	s.enqueued++
	if s.bytes > int(s.peakBytes) {
		s.peakBytes = int64(s.bytes)
	}
	if len(s.queue) > s.peakItems {
		s.peakItems = len(s.queue)
	}
	s.cond.Broadcast()
	return nil
}

// Get blocks until an item is available, the stream is closed and drained, or
// ctx is canceled. The boolean is false at clean end-of-stream.
func (s *Stream) Get(ctx context.Context) (Item, bool, error) {
	s.mu.Lock()
	waitErr := s.waitWhile(ctx, func() bool { return len(s.queue) == 0 })
	if len(s.queue) == 0 {
		s.mu.Unlock()
		// Closed and drained: normal end-of-stream, not a failure.
		if model.HasCode(waitErr, model.ErrStreamClosed) {
			return Item{}, false, nil
		}
		if waitErr != nil {
			return Item{}, false, waitErr
		}
		return Item{}, false, nil
	}
	item := s.queue[0]
	s.queue = s.queue[1:]
	s.bytes -= item.Size
	s.cond.Broadcast() // wake a blocked producer
	s.mu.Unlock()
	return item, true, nil
}

// Close marks the stream finished and releases every blocked producer.
func (s *Stream) Close() {
	s.mu.Lock()
	s.closed = true
	s.cond.Broadcast()
	s.mu.Unlock()
}

// Snapshot returns cumulative pressure counters.
func (s *Stream) Snapshot() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Stats{
		PeakBytes: s.peakBytes, PeakItems: s.peakItems,
		Enqueued: s.enqueued, BlockedNS: s.blockedNS,
		BlockedHits: s.blockedHits, Rejected: s.rejected,
	}
}

func (s *Stream) fullLocked() bool {
	return s.bytes >= s.cfg.MaxBytes || len(s.queue) >= s.cfg.MaxItems
}
