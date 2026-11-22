// Package pipeline connects recovery, timeline synthesis and off-screen
// export with bounded queues. Every queue has a fixed capacity: a slow
// consumer blocks the producer instead of growing memory, and cancellation
// or a downstream error unwinds every stage and releases all buffers.
package pipeline

import (
	"context"
	"sync"

	"github.com/medialab/medialab/internal/clock"
)

// Queue is a fixed-capacity, context-aware FIFO. It holds no goroutines and
// therefore has no cleanup requirements beyond draining its items.
type Queue[T any] struct {
	items chan T
	peak  int
	mu    sync.Mutex
}

// NewQueue creates a queue with the given positive capacity.
func NewQueue[T any](capacity int) *Queue[T] {
	if capacity <= 0 {
		capacity = 1
	}
	return &Queue[T]{items: make(chan T, capacity)}
}

// Put blocks while the queue is full, propagating context cancellation. On
// success it records the new fill level for the peak-watermark report.
func (q *Queue[T]) Put(ctx context.Context, item T) error {
	select {
	case q.items <- item:
		q.observe(len(q.items))
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Get blocks until an item is available, the queue is closed and drained, or
// the context is cancelled.
func (q *Queue[T]) Get(ctx context.Context) (T, bool, error) {
	var zero T
	select {
	case item, ok := <-q.items:
		return item, ok, nil
	case <-ctx.Done():
		return zero, false, ctx.Err()
	}
}

// Close marks the queue complete; buffered items remain readable.
func (q *Queue[T]) Close() { close(q.items) }

// Len returns the current fill level.
func (q *Queue[T]) Len() int { return len(q.items) }

// Peak returns the highest fill level ever observed.
func (q *Queue[T]) Peak() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.peak
}

func (q *Queue[T]) observe(n int) {
	q.mu.Lock()
	if n > q.peak {
		q.peak = n
	}
	q.mu.Unlock()
}

// Stage is one processing step connected to its inbound queue.
type Stage[T any] func(ctx context.Context, in *Queue[T]) error

// Runner executes a producer feeding a bounded queue consumed by one stage.
type Runner[T any] struct {
	queue *Queue[T]
}

// NewRunner wires a queue of the given capacity.
func NewRunner[T any](capacity int) *Runner[T] {
	return &Runner[T]{queue: NewQueue[T](capacity)}
}

// Queue exposes the inbound queue so the producer can enqueue.
func (r *Runner[T]) Queue() *Queue[T] { return r.queue }

// Run executes produce then consume. The producer must close the queue when
// its work ends. Cancellation or a stage error cancels the shared context so
// a blocked producer stops immediately; the producer error (if any) is still
// captured rather than lost.
func (r *Runner[T]) Run(ctx context.Context, produce func(context.Context) error, stage Stage[T]) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var prodErr, stageErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer func() {
			r.queue.Close()
			if p := recover(); p != nil {
				prodErr = clock.NewError(clock.KindOverflow, "pipeline", "", 0,
					"producer panic")
			}
		}()
		prodErr = produce(ctx)
	}()
	go func() {
		defer wg.Done()
		defer cancel()
		stageErr = stage(ctx, r.queue)
	}()
	wg.Wait()

	if stageErr != nil {
		return stageErr
	}
	if prodErr != nil {
		return prodErr
	}
	return nil
}

// Peak reports the maximum queue fill observed during the run.
func (r *Runner[T]) Peak() int { return r.queue.Peak() }
