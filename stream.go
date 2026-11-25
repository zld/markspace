package mediakernel

import (
	"context"
	"sync/atomic"
)

// QueueStats is a point-in-time snapshot of a BoundedQueue. Pending is the
// number of items currently buffered; it never exceeds Capacity.
type QueueStats struct {
	Capacity int `json:"capacity"`
	Enqueued int `json:"enqueued"`
	Dequeued int `json:"dequeued"`
	Pending  int `json:"pending"`
	Peak     int `json:"queue_peak"`
}

// BoundedQueue is a capacity-bounded FIFO with context-aware blocking. A
// producer calling Enqueue on a full queue blocks until a consumer frees a
// slot (backpressure) or the context is cancelled, so memory cannot grow
// beyond Capacity regardless of producer speed. It is safe for concurrent use.
type BoundedQueue[T any] struct {
	ch       chan T
	capacity int
	enqueued atomic.Int64
	dequeued atomic.Int64
	peak     atomic.Int64
}

// NewBoundedQueue creates a queue holding at most capacity items.
func NewBoundedQueue[T any](capacity int) *BoundedQueue[T] {
	if capacity < 1 {
		capacity = 1
	}
	return &BoundedQueue[T]{ch: make(chan T, capacity), capacity: capacity}
}

// Enqueue appends item, blocking while the queue is full. A cancelled context
// returns the structured cancellation error without touching the queue.
func (q *BoundedQueue[T]) Enqueue(ctx context.Context, item T) error {
	// Try a non-blocking send first so the uncontended path stays cheap.
	select {
	case q.ch <- item:
	default:
		select {
		case q.ch <- item:
		case <-ctx.Done():
			return ErrCancelled.WithCause(ctx.Err())
		}
	}
	q.enqueued.Add(1)
	if pending := int64(len(q.ch)); pending > q.peak.Load() {
		q.peak.Store(pending)
	}
	return nil
}

// Dequeue removes and returns the next item, blocking while empty. On context
// cancellation it returns the cancellation error and a zero value.
func (q *BoundedQueue[T]) Dequeue(ctx context.Context) (T, error) {
	var zero T
	select {
	case item := <-q.ch:
		q.dequeued.Add(1)
		return item, nil
	case <-ctx.Done():
		return zero, ErrCancelled.WithCause(ctx.Err())
	}
}

// Close signals that no further items will be enqueued. Consumers can range
// over the channel to drain remaining work before exiting.
func (q *BoundedQueue[T]) Close() { close(q.ch) }

// Channel exposes the underlying channel for range-based draining.
func (q *BoundedQueue[T]) Channel() <-chan T { return q.ch }

// Stats returns a consistent-enough snapshot (counters are read independently;
// the queue is concurrent so exact simultaneous consistency is not required).
func (q *BoundedQueue[T]) Stats() QueueStats {
	return QueueStats{
		Capacity: q.capacity,
		Enqueued: int(q.enqueued.Load()),
		Dequeued: int(q.dequeued.Load()),
		Pending:  len(q.ch),
		Peak:     int(q.peak.Load()),
	}
}

// WorkerCounters reports how far a pipeline stage got before it exited. It is
// emitted on cancellation so callers know what was processed versus pending.
type WorkerCounters struct {
	Processed int `json:"processed"`
	Pending   int `json:"pending"`
	Enqueued  int `json:"enqueued"`
}

// Counters is a concurrency-safe incrementing counter pair shared by stages.
type Counters struct {
	processed atomic.Int64
	enqueued  atomic.Int64
}

// IncProcessed records one finished unit of work.
func (c *Counters) IncProcessed() { c.processed.Add(1) }

// IncEnqueued records one unit of work accepted into the pipeline.
func (c *Counters) IncEnqueued() { c.enqueued.Add(1) }

// Snapshot returns the current counters.
func (c *Counters) Snapshot() (enqueued, processed int64) {
	return c.enqueued.Load(), c.processed.Load()
}
