package continuity

import (
	"context"
	"errors"
	"sync"
)

// The stream package moves demuxed samples through a bounded queue. Backpressure
// is expressed by blocking producers rather than dropping samples. A single
// sample larger than the configured byte limit fails explicitly, and cancelling
// the context releases every blocked reader and writer.

var (
	// ErrSampleTooLarge is returned when one sample exceeds the queue limit.
	ErrSampleTooLarge = errors.New("sample exceeds the queue byte limit")
	// ErrQueueClosed is returned after a closed queue is fully drained.
	ErrQueueClosed = errors.New("queue closed")
)

// QueueConfig bounds the streaming buffer.
type QueueConfig struct {
	// MaxBytes is the total bytes of queued samples allowed. Zero selects
	// DefaultQueueBytes.
	MaxBytes int64
	// MaxSamples additionally bounds the queued sample count. Zero means only
	// the byte bound applies.
	MaxSamples int
}

// DefaultQueueBytes is the demo's default buffer budget (1 MiB).
const DefaultQueueBytes int64 = 1 << 20

// StreamStats records queue history, including backpressure observations.
type StreamStats struct {
	Pushed    int
	Popped    int
	Blocked   int // pushes that had to wait for room
	PeakBytes int64
	Rejected  int
}

// Queue is a bounded, goroutine-safe sample queue.
type Queue struct {
	cfg    QueueConfig
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	cond   *sync.Cond
	items  []Sample
	bytes  int64
	closed bool
	stats  StreamStats
	done   chan struct{}
}

// NewQueue builds a queue bound to ctx. Cancelling ctx unblocks all waiters.
func NewQueue(ctx context.Context, cfg QueueConfig) (*Queue, error) {
	if cfg.MaxBytes == 0 {
		cfg.MaxBytes = DefaultQueueBytes
	}
	if cfg.MaxBytes < 0 {
		return nil, Usagef("queue MaxBytes must be positive")
	}
	if cfg.MaxSamples < 0 {
		return nil, Usagef("queue MaxSamples must be non-negative")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	q := &Queue{cfg: cfg, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	q.cond = sync.NewCond(&q.mu)
	go func() {
		select {
		case <-ctx.Done():
			q.mu.Lock()
			q.cond.Broadcast()
			q.mu.Unlock()
		case <-q.done:
		}
	}()
	return q, nil
}

// Close marks the queue closed, releases blocked callers and stops the
// cancellation watcher. Close is idempotent.
func (q *Queue) Close() {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	q.mu.Unlock()
	q.cancel()
	close(q.done)
	q.cond.Broadcast()
}

// Err returns the queue's context error, if any.
func (q *Queue) Err() error { return q.ctx.Err() }

// Stats returns a snapshot.
func (q *Queue) Stats() StreamStats {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.stats
}

// Len returns the queued sample count and total bytes.
func (q *Queue) Len() (int, int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items), q.bytes
}

// Push enqueues s, blocking while the byte or sample budget is full. It returns
// the context error on cancellation and a KindQueueLimit fault carrying the
// sample's position when a single sample cannot ever fit.
func (q *Queue) Push(s Sample) error {
	size := int64(len(s.Data))
	q.mu.Lock()
	defer q.mu.Unlock()
	for {
		if err := q.ctx.Err(); err != nil {
			return err
		}
		if q.closed {
			return ErrQueueClosed
		}
		if size > q.cfg.MaxBytes {
			q.stats.Rejected++
			return WrapError(ErrSampleTooLarge, KindQueueLimit,
				Location{File: s.File, Segment: s.Segment, Offset: s.ByteOffset, Sample: s.Index},
				"sample of %d bytes exceeds the %d-byte queue limit", size, q.cfg.MaxBytes)
		}
		overSamples := q.cfg.MaxSamples > 0 && len(q.items) >= q.cfg.MaxSamples
		overBytes := q.bytes+size > q.cfg.MaxBytes
		if !overSamples && !overBytes {
			q.items = append(q.items, s)
			q.bytes += size
			if q.bytes > q.stats.PeakBytes {
				q.stats.PeakBytes = q.bytes
			}
			q.stats.Pushed++
			q.cond.Broadcast()
			return nil
		}
		q.stats.Blocked++
		q.cond.Wait()
	}
}

// Pop removes the oldest sample, blocking while the queue is empty. It returns
// ErrQueueClosed once a closed queue is drained and the context error on
// cancellation.
func (q *Queue) Pop() (Sample, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for {
		if len(q.items) > 0 {
			s := q.items[0]
			q.items[0] = Sample{}
			q.items = q.items[1:]
			q.bytes -= int64(len(s.Data))
			q.stats.Popped++
			q.cond.Broadcast()
			return s, nil
		}
		if q.closed {
			return Sample{}, ErrQueueClosed
		}
		if err := q.ctx.Err(); err != nil {
			return Sample{}, err
		}
		q.cond.Wait()
	}
}

// Drain removes and returns everything currently buffered without blocking.
func (q *Queue) Drain() []Sample {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := append([]Sample(nil), q.items...)
	q.items = nil
	q.bytes = 0
	q.cond.Broadcast()
	return out
}
