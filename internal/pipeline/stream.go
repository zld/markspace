// Package pipeline connects stages with bounded queues so backpressure
// blocks upstream and memory stays constant regardless of input length.
package pipeline

import (
	"context"
	"sync"
	"sync/atomic"

	"avcheck/internal/media"
)

// Queue is a bounded, metered channel between two stages.
type Queue[T any] struct {
	name string
	ch   chan T
	cur  atomic.Int64
	peak atomic.Int64
}

// NewQueue builds a queue with fixed capacity.
func NewQueue[T any](capacity int, name string) *Queue[T] {
	if capacity < 1 {
		capacity = 1
	}
	return &Queue[T]{name: name, ch: make(chan T, capacity)}
}

// Send blocks while the queue is full; cancellation unblocks it with a
// classified error so upstream stages can stop cleanly.
func (q *Queue[T]) Send(ctx context.Context, v T) error {
	select {
	case q.ch <- v:
		n := q.cur.Add(1)
		for {
			p := q.peak.Load()
			if n <= p || q.peak.CompareAndSwap(p, n) {
				break
			}
		}
		return nil
	case <-ctx.Done():
		return media.NewError(media.KindCanceled, 0, 0, -1, "queue %s send canceled", q.name)
	}
}

// Recv returns the next item; ok is false once the queue is closed and
// drained, or when the context is canceled.
func (q *Queue[T]) Recv(ctx context.Context) (v T, ok bool) {
	select {
	case v, ok := <-q.ch:
		if ok {
			q.cur.Add(-1)
		}
		return v, ok
	case <-ctx.Done():
		return v, false
	}
}

// Close signals end of stream; pending items remain receivable.
func (q *Queue[T]) Close() { close(q.ch) }

// Peak returns the maximum occupancy observed.
func (q *Queue[T]) Peak() int64 { return q.peak.Load() }

// Cap returns the fixed capacity.
func (q *Queue[T]) Cap() int { return cap(q.ch) }

// Run executes stages concurrently; the first non-cancellation error
// cancels the rest and is returned.
func Run(ctx context.Context, stages ...func(context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errs := make([]error, len(stages))
	for i, s := range stages {
		wg.Add(1)
		go func(i int, s func(context.Context) error) {
			defer wg.Done()
			if err := s(ctx); err != nil {
				errs[i] = err
				cancel()
			}
		}(i, s)
	}
	wg.Wait()
	for _, err := range errs {
		if err == nil {
			continue
		}
		if me, ok := err.(*media.Error); ok && me.Kind == media.KindCanceled {
			continue
		}
		return err
	}
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
