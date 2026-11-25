// Package pipe connects parsing, indexing, persistence and recovery with bounded flow.
package pipe

import (
	"context"
	"sync"

	"mediacore/internal/diagnostic"
)

// Queue is a bounded channel with an observable maximum depth for tests.
type Queue[T any] struct {
	ch       chan T
	mu       sync.Mutex
	peak     int
	enqueued int64
}

func NewQueue[T any](capacity int) *Queue[T] {
	if capacity < 1 {
		capacity = 1
	}
	return &Queue[T]{ch: make(chan T, capacity)}
}

func (q *Queue[T]) Send(ctx context.Context, v T) error {
	select {
	case q.ch <- v:
		q.mu.Lock()
		n := len(q.ch)
		if n > q.peak {
			q.peak = n
		}
		q.enqueued++
		q.mu.Unlock()
		return nil
	case <-ctx.Done():
		return diagnostic.Wrap(diagnostic.ErrCancelled, diagnostic.Location{Stage: "pipe"}, ctx.Err())
	}
}

func (q *Queue[T]) Recv(ctx context.Context) (T, bool, error) {
	var zero T
	select {
	case v, ok := <-q.ch:
		if !ok {
			return zero, false, nil
		}
		return v, true, nil
	case <-ctx.Done():
		return zero, false, diagnostic.Wrap(diagnostic.ErrCancelled, diagnostic.Location{Stage: "pipe"}, ctx.Err())
	}
}

func (q *Queue[T]) Close()            { close(q.ch) }
func (q *Queue[T]) Channel() <-chan T { return q.ch }
func (q *Queue[T]) Len() int          { return len(q.ch) }

func (q *Queue[T]) Peak() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.peak
}

func (q *Queue[T]) Enqueued() int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.enqueued
}

// ByteBudget is a checked weighted buffer semaphore.
type ByteBudget struct {
	mu       sync.Mutex
	cond     *sync.Cond
	capacity int64
	current  int64
	peak     int64
}

func NewByteBudget(capacityBytes int64) *ByteBudget {
	if capacityBytes <= 0 {
		capacityBytes = 64 << 20
	}
	b := &ByteBudget{capacity: capacityBytes}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *ByteBudget) Capacity() int64 { return b.capacity }
func (b *ByteBudget) Current() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.current
}
func (b *ByteBudget) Peak() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.peak
}

// Acquire blocks until n bytes are available or ctx is canceled.
// Large shards must be read in chunks no larger than the budget.
func (b *ByteBudget) Acquire(ctx context.Context, n int64) error {
	if n <= 0 {
		return nil
	}
	if n > b.capacity {
		return diagnostic.New(diagnostic.ErrIllegalSample, diagnostic.Location{Stage: "pipe", Field: "buffer_budget"},
			"requested buffer exceeds memory cap; read the shard in chunks")
	}
	stopWatcher := make(chan struct{})
	defer close(stopWatcher)
	go func() {
		select {
		case <-ctx.Done():
			b.cond.Broadcast()
		case <-stopWatcher:
		}
	}()
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.current+n > b.capacity {
		if err := ctx.Err(); err != nil {
			return diagnostic.Wrap(diagnostic.ErrCancelled, diagnostic.Location{Stage: "pipe"}, err)
		}
		b.cond.Wait()
	}
	if err := ctx.Err(); err != nil {
		return diagnostic.Wrap(diagnostic.ErrCancelled, diagnostic.Location{Stage: "pipe"}, err)
	}
	b.current += n
	if b.current > b.peak {
		b.peak = b.current
	}
	return nil
}

func (b *ByteBudget) Release(n int64) {
	if n <= 0 {
		return
	}
	b.mu.Lock()
	b.current -= n
	if b.current < 0 {
		b.current = 0
	}
	b.cond.Broadcast()
	b.mu.Unlock()
}
