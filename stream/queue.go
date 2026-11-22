package stream

import (
	"context"
	"sync"

	"medpipe/timeline"
)

// MaxQueueBlocks is the hard ceiling on any queue's capacity.
const MaxQueueBlocks = 64

// QueuedBlock is one work item handed from a producer to a consumer.
type QueuedBlock struct {
	TrackID  string
	Sequence int
	Payload  []byte
	Start    int64 // output-grid sample position
}

// BoundedQueue is a capacity-bounded FIFO with blocking backpressure.
// At most capacity blocks may wait inside the queue; producers block until a
// slot frees, the context is cancelled, or Close is called.
type BoundedQueue struct {
	mu       sync.Mutex
	cond     *sync.Cond
	slots    []QueuedBlock
	capacity int
	closed   bool
	peak     int
	totalIn  int64
	totalOut int64
}

// NewBoundedQueue validates the capacity against the hard ceiling.
func NewBoundedQueue(capacity int) (*BoundedQueue, error) {
	if capacity <= 0 {
		return nil, timeline.NewError(timeline.ErrCodeIllegalBlockLength, timeline.StageQueue, "",
			"queue capacity must be positive, got %d", capacity)
	}
	if capacity > MaxQueueBlocks {
		return nil, timeline.NewError(timeline.ErrCodeIllegalBlockLength, timeline.StageQueue, "",
			"queue capacity %d exceeds hard limit %d", capacity, MaxQueueBlocks)
	}
	q := &BoundedQueue{capacity: capacity}
	q.cond = sync.NewCond(&q.mu)
	return q, nil
}

// Push blocks until a slot is available. It returns an error when the context
// is cancelled or the queue has been closed.
func (q *BoundedQueue) Push(ctx context.Context, b QueuedBlock) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.slots) >= q.capacity && !q.closed {
		// Wake when either the queue drains or the context is cancelled.
		done := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				q.cond.Broadcast()
			case <-done:
			}
		}()
		q.cond.Wait()
		close(done)
		if ctx.Err() != nil {
			return timeline.NewError(timeline.ErrCodeCancelled, timeline.StageQueue, b.TrackID,
				"push cancelled: %v", ctx.Err())
		}
	}
	if q.closed {
		return timeline.NewError(timeline.ErrCodeQueueClosed, timeline.StageQueue, b.TrackID,
			"queue is closed")
	}
	q.slots = append(q.slots, b)
	q.totalIn++
	if len(q.slots) > q.peak {
		q.peak = len(q.slots)
	}
	q.cond.Broadcast()
	return nil
}

// Pop blocks until a block is available. On Close with an empty queue it
// returns false so consumers can terminate their loop.
func (q *BoundedQueue) Pop(ctx context.Context) (QueuedBlock, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.slots) == 0 && !q.closed {
		done := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				q.cond.Broadcast()
			case <-done:
			}
		}()
		q.cond.Wait()
		close(done)
		if ctx.Err() != nil {
			return QueuedBlock{}, false,
				timeline.NewError(timeline.ErrCodeCancelled, timeline.StageQueue, "",
					"pop cancelled: %v", ctx.Err())
		}
	}
	if len(q.slots) == 0 {
		return QueuedBlock{}, false, nil // drained after close
	}
	b := q.slots[0]
	q.slots = q.slots[1:]
	q.totalOut++
	q.cond.Broadcast()
	return b, true, nil
}

// Close marks the queue closed; blocked producers unblock with an error and
// consumers drain remaining items before seeing the closed signal.
func (q *BoundedQueue) Close() {
	q.mu.Lock()
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()
}

// Stats is a point-in-time snapshot of the bounded-memory counters.
type Stats struct {
	Capacity    int
	Depth       int
	PeakDepth   int
	Pushed      int64
	Popped      int64
	InFlightCap int // capacity + at most two blocks in transit
}

// Stats returns the counters. InFlightCap is the documented memory ceiling:
// queue slots plus at most one block being pushed and one being popped.
func (q *BoundedQueue) Stats() Stats {
	q.mu.Lock()
	defer q.mu.Unlock()
	return Stats{
		Capacity:    q.capacity,
		Depth:       len(q.slots),
		PeakDepth:   q.peak,
		Pushed:      q.totalIn,
		Popped:      q.totalOut,
		InFlightCap: q.capacity + 2,
	}
}
