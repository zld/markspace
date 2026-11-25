// Package stream schedules fragment processing through a capacity-bounded
// queue. When consumers fall behind, producers block on Enqueue, which gives
// natural back-pressure and guarantees the in-memory queue never exceeds the
// configured capacity. Cancellation and corrupted inputs stop the pipeline
// before any checkpoint advances.
package stream

import (
	"context"
	"sync"

	"mediapipeline/internal/fault"
)

// Item is one unit of work flowing through the pipeline. The payload type is
// generic so the scheduling layer stays independent of the media model.
type Item struct {
	// Index is the deterministic input index.
	Index int
	// Key identifies the representation/source the item belongs to.
	Key string
	// Payload carries the subsystem-specific data.
	Payload any
}

// BoundedQueue is a context-aware FIFO queue with a hard capacity.
type BoundedQueue struct {
	mu       sync.Mutex
	cond     *sync.Cond
	items    []Item
	capacity int
	closed   bool
	peak     int
}

// NewBoundedQueue creates a queue that never holds more than capacity items.
func NewBoundedQueue(capacity int) (*BoundedQueue, error) {
	if capacity < 1 {
		return nil, fault.New(fault.CodeInvalidArgument, "queue capacity must be >= 1")
	}
	q := &BoundedQueue{capacity: capacity, items: make([]Item, 0, capacity)}
	q.cond = sync.NewCond(&q.mu)
	return q, nil
}

// Enqueue blocks while the queue is full or until ctx is canceled. A
// canceled context returns fault.CodeCanceled and the item is not stored.
func (q *BoundedQueue) Enqueue(ctx context.Context, item Item) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) >= q.capacity && !q.closed {
		if err := q.waitContext(ctx); err != nil {
			return err
		}
	}
	if q.closed {
		return fault.New(fault.CodeCanceled, "queue closed, enqueue rejected")
	}
	q.items = append(q.items, item)
	if len(q.items) > q.peak {
		q.peak = len(q.items)
	}
	q.cond.Broadcast()
	return nil
}

// Dequeue blocks until an item is available, the queue is closed and drained,
// or ctx is canceled. The boolean is false only when no more items will ever
// arrive.
func (q *BoundedQueue) Dequeue(ctx context.Context) (Item, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && !q.closed {
		if err := q.waitContext(ctx); err != nil {
			return Item{}, false, err
		}
	}
	if len(q.items) == 0 {
		return Item{}, false, nil
	}
	item := q.items[0]
	q.items = q.items[1:]
	q.cond.Broadcast()
	return item, true, nil
}

// Close marks the queue finished; blocked consumers drain remaining items.
func (q *BoundedQueue) Close() {
	q.mu.Lock()
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()
}

// Peak returns the highest occupancy observed so far.
func (q *BoundedQueue) Peak() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.peak
}

// Len returns the current occupancy.
func (q *BoundedQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// waitContext must be called with the lock held. It wakes waiters when the
// context is done by spawning a short-lived watcher, while normal wake-ups
// come from Broadcast on Enqueue/Dequeue/Close.
func (q *BoundedQueue) waitContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fault.New(fault.CodeCanceled, "context finished: %v", err)
	}
	done := make(chan struct{})
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			q.cond.Broadcast()
		case <-stop:
		}
		close(done)
	}()
	q.cond.Wait()
	close(stop)
	<-done
	if err := ctx.Err(); err != nil {
		return fault.New(fault.CodeCanceled, "context finished: %v", err)
	}
	return nil
}
