package stream

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"mediamux/media"
)

// ErrQueueFull is returned to a producer that asked for non-blocking mode and
// found the queue at capacity.
var ErrQueueFull = errors.New("stream: queue full")

// ErrSlowConsumer is returned when a producer is blocked by a full queue for
// longer than the configured timeout. That is the observable symptom of a slow
// consumer: the branch stops, and everything acknowledged before the block
// stays committed.
var ErrSlowConsumer = errors.New("stream: slow consumer")

// errDrained signals a closed, empty queue.
var errDrained = errors.New("stream: queue drained")

// IsDrained reports whether err means the queue closed and is empty.
func IsDrained(err error) bool { return errors.Is(err, errDrained) }

// IsSlowConsumer reports whether err is a slow-consumer timeout.
func IsSlowConsumer(err error) bool { return errors.Is(err, ErrSlowConsumer) }

// QueueStats is the observable state of a bounded queue.
type QueueStats struct {
	Capacity     int   `json:"capacity"`
	Depth        int   `json:"depth"`
	Enqueued     int64 `json:"enqueued"`
	Dequeued     int64 `json:"dequeued"`
	BlockedNanos int64 `json:"blocked_nanos"`
	Dropped      int64 `json:"dropped"`
	TimedOut     int64 `json:"timed_out"`
}

// Queue is a fixed-capacity FIFO. Producers block when it is full, which is
// the back pressure that keeps memory bounded. If a producer stays blocked
// longer than slowTimeout, the branch is stopped with ErrSlowConsumer.
type Queue[T any] struct {
	ch      chan T
	done    chan struct{}
	once    sync.Once
	timeout time.Duration
	enq     atomic.Int64
	deq     atomic.Int64
	blocked atomic.Int64
	dropped atomic.Int64
	timed   atomic.Int64
}

// NewQueue builds a bounded queue. capacity must be positive; slowTimeout
// zero means a blocked producer waits indefinitely.
func NewQueue[T any](capacity int, slowTimeout time.Duration) (*Queue[T], error) {
	if capacity <= 0 {
		return nil, media.NewError(media.CodeInput, media.PhasePipeline, "queue capacity must be positive").AtOffset(0)
	}
	if slowTimeout < 0 {
		return nil, media.NewError(media.CodeInput, media.PhasePipeline, "slow-consumer timeout must not be negative").AtOffset(0)
	}
	return &Queue[T]{
		ch:      make(chan T, capacity),
		done:    make(chan struct{}),
		timeout: slowTimeout,
	}, nil
}

// Push blocks until there is room, the queue closes, or ctx ends. Time spent
// blocked is accumulated as back-pressure evidence.
func (q *Queue[T]) Push(ctx context.Context, item T) error {
	start := time.Now()
	var timeout <-chan time.Time
	var timer *time.Timer
	if q.timeout > 0 {
		timer = time.NewTimer(q.timeout)
		defer timer.Stop()
		timeout = timer.C
	}
	select {
	case q.ch <- item:
	case <-ctx.Done():
		q.Drop()
		return media.NewError(media.CodeCanceled, media.PhasePipeline, "producer canceled while blocked").Because(ctx.Err())
	case <-q.done:
		q.Drop()
		return media.NewError(media.CodeCanceled, media.PhasePipeline, "queue closed")
	case <-timeout:
		q.timed.Add(1)
		q.Drop()
		q.Close()
		return fmt.Errorf("%w: producer blocked for %s with a full queue", ErrSlowConsumer, q.timeout)
	}
	q.blocked.Add(time.Since(start).Nanoseconds())
	q.enq.Add(1)
	return nil
}

// TryPush enqueues without blocking; it returns ErrQueueFull at capacity.
func (q *Queue[T]) TryPush(item T) error {
	select {
	case <-q.done:
		return media.NewError(media.CodeCanceled, media.PhasePipeline, "queue closed")
	default:
	}
	select {
	case q.ch <- item:
		q.enq.Add(1)
		return nil
	default:
		q.dropped.Add(1)
		return ErrQueueFull
	}
}

// Pop waits for the next item. It drains buffered items after Close before
// reporting IsDrained.
func (q *Queue[T]) Pop(ctx context.Context) (T, error) {
	var zero T
	select {
	case item := <-q.ch:
		q.deq.Add(1)
		return item, nil
	case <-q.done:
		select {
		case item := <-q.ch:
			q.deq.Add(1)
			return item, nil
		default:
			return zero, errDrained
		}
	case <-ctx.Done():
		return zero, media.NewError(media.CodeCanceled, media.PhasePipeline, "consumer canceled").Because(ctx.Err())
	}
}

// Close wakes all blocked producers and consumers. It is idempotent and never
// closes the item channel, so a racing Push cannot panic.
func (q *Queue[T]) Close() error {
	q.once.Do(func() { close(q.done) })
	return nil
}

// Drop records an item the producer failed to enqueue.
func (q *Queue[T]) Drop() { q.dropped.Add(1) }

// Stats snapshots the queue for reporting.
func (q *Queue[T]) Stats() QueueStats {
	return QueueStats{
		Capacity:     cap(q.ch),
		Depth:        len(q.ch),
		Enqueued:     q.enq.Load(),
		Dequeued:     q.deq.Load(),
		BlockedNanos: q.blocked.Load(),
		Dropped:      q.dropped.Load(),
		TimedOut:     q.timed.Load(),
	}
}
