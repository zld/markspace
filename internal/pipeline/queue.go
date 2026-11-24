// Package pipeline connects processing stages with bounded queues. A full
// queue blocks the upstream sender, so a slow consumer applies back-pressure
// all the way to the source. Context cancellation or a stage failure unblocks
// every waiting goroutine, and recovery resumes from the last valid boundary
// without resubmitting already confirmed items.
package pipeline

import (
	"context"
	"sync"
)

// Queue is a capacity-bounded FIFO guarded by a mutex and two conditions.
type Queue struct {
	mu      sync.Mutex
	notFull *sync.Cond
	notEmpty *sync.Cond
	items   []any
	cap     int
	closed  bool
	depth   int // high-water mark of len(items)
}

// NewQueue creates a queue with the given capacity (>= 1).
func NewQueue(capacity int) *Queue {
	if capacity < 1 {
		capacity = 1
	}
	q := &Queue{cap: capacity, items: make([]any, 0, capacity)}
	q.notFull = sync.NewCond(&q.mu)
	q.notEmpty = sync.NewCond(&q.mu)
	return q
}

// Put blocks while the queue is full. It returns false when ctx is canceled
// or the queue has been closed, without enqueuing.
func (q *Queue) Put(ctx context.Context, item any) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) >= q.cap && !q.closed {
		if canceled(ctx) {
			return false
		}
		q.wait(ctx, q.notFull)
	}
	if q.closed || canceled(ctx) {
		return false
	}
	q.items = append(q.items, item)
	if len(q.items) > q.depth {
		q.depth = len(q.items)
	}
	q.notEmpty.Signal()
	return true
}

// Get blocks until an item is available. The boolean is false when the queue
// is drained and closed, or ctx was canceled.
func (q *Queue) Get(ctx context.Context) (any, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && !q.closed {
		if canceled(ctx) {
			return nil, false
		}
		q.wait(ctx, q.notEmpty)
	}
	if len(q.items) == 0 {
		return nil, false
	}
	item := q.items[0]
	q.items = q.items[1:]
	q.notFull.Signal()
	return item, true
}

// Close marks the queue closed; blocked senders fail, blocked receivers drain
// remaining items and then get false.
func (q *Queue) Close() {
	q.mu.Lock()
	q.closed = true
	q.notFull.Broadcast()
	q.notEmpty.Broadcast()
	q.mu.Unlock()
}

// MaxDepth returns the largest queue length observed so far.
func (q *Queue) MaxDepth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.depth
}

// Len returns the current queued item count.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// canceled is a non-blocking context check.
func canceled(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

// wait sleeps on cond but wakes periodically to re-check ctx, so cancellation
// unblocks a stage even when no matching Signal arrives.
func (q *Queue) wait(ctx context.Context, cond *sync.Cond) {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			cond.Broadcast()
		case <-done:
		}
	}()
	cond.Wait()
	close(done)
}
