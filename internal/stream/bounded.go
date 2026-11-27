// Package stream provides a context-aware bounded queue that applies
// back-pressure to producers when full and never grows past its capacity.
// A slow consumer therefore cannot inflate memory; cancellation releases
// every blocked reader and writer.
package stream

import (
	"context"
	"sync"

	"github.com/local/tracecheck/internal/clock"
)

// Item is the unit of work carried by the queue; it wraps the block-derived
// payload together with the metadata needed downstream.
type Item struct {
	SegmentIndex int
	SourceStart  int64
	SourceEnd    int64
	Content      []byte
}

// Bounded is a fixed-capacity channel-backed queue plus a peak tracker.
// Memory is bounded by capacity*maxItemSize; producers block on Put when
// the queue is full instead of allocating additional buffers.
type Bounded struct {
	ch      chan Item
	maxSize int
	peak    int
	mu      sync.Mutex
	closed  bool
}

// NewBounded constructs a queue with the given item capacity and per-item
// byte ceiling (single-block upper bound).
func NewBounded(capacity int, maxItemSize int) *Bounded {
	if capacity <= 0 {
		capacity = 1
	}
	return &Bounded{
		ch:      make(chan Item, capacity),
		maxSize: maxItemSize,
	}
}

// Put enqueues an item, applying back-pressure by blocking while the queue
// is full. It returns clock.CodeCancelled if ctx fires while blocked.
// Oversized items are rejected with CodeOutOfRange so the single-block
// ceiling is enforced.
func (q *Bounded) Put(ctx context.Context, item Item) error {
	if len(item.Content) > q.maxSize {
		return clock.NewError(clock.CodeOutOfRange, item.SourceStart, "",
			"item exceeds single-block size ceiling")
	}
	select {
	case q.ch <- item:
		// Count only items actually buffered so the peak can never exceed
		// capacity even when producers are blocked in back-pressure.
		q.enqueued()
		return nil
	case <-ctx.Done():
		return clock.NewError(clock.CodeCancelled, item.SourceStart, "", ctx.Err().Error())
	}
}

// TryPut enqueues without blocking. It returns false when the queue is full
// or closed, leaving the caller to apply its own back-pressure policy.
func (q *Bounded) TryPut(item Item) bool {
	if len(item.Content) > q.maxSize {
		return false
	}
	select {
	case q.ch <- item:
		q.enqueued()
		return true
	default:
		return false
	}
}

// Get dequeues an item, blocking until one is available, the queue closes
// and drains, or ctx fires. The boolean is false when no more items will
// ever arrive (closed and empty).
func (q *Bounded) Get(ctx context.Context) (Item, bool, error) {
	select {
	case item, ok := <-q.ch:
		if !ok {
			return Item{}, false, nil
		}
		return item, true, nil
	case <-ctx.Done():
		return Item{}, false, clock.NewError(clock.CodeCancelled, 0, "", ctx.Err().Error())
	}
}

// Close signals that no further items will be produced. Pending items
// remain readable until drained.
func (q *Bounded) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.closed {
		q.closed = true
		close(q.ch)
	}
}

// Peak returns the highest number of items ever held simultaneously.
func (q *Bounded) Peak() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.peak
}

// Capacity returns the configured item capacity.
func (q *Bounded) Capacity() int { return cap(q.ch) }

// enqueued records the queue depth after a successful send. The peak is
// observed from len(q.ch), which can never exceed the channel capacity, so
// a blocked (back-pressured) producer cannot inflate the peak.
func (q *Bounded) enqueued() {
	q.mu.Lock()
	if n := len(q.ch); n > q.peak {
		q.peak = n
	}
	q.mu.Unlock()
}
