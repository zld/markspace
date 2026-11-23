// Package stream provides the bounded queue, backpressure and checkpointed
// pipeline that connects adapters to the offline frame renderer.
package stream

import (
	"context"
	"errors"
	"sync"
)

// ErrQueueClosed is returned by Enqueue after Close is called.
var ErrQueueClosed = errors.New("stream: queue closed")

// Sample is the unit carried through the pipeline. MaxSize lets the pipeline
// enforce the documented memory ceiling: capacity*MaxSize + fixed buffers.
type Sample struct {
	Index   int64
	TrackID uint32
	TimeNS  int64
	Payload []byte
	Meta    map[string]string
}

// BoundedQueue is a fixed-capacity channel-backed queue. Producers block on
// Enqueue when the queue is full, which is the required backpressure point.
type BoundedQueue struct {
	ch      chan Sample
	maxSize int
	mu      sync.Mutex
	closed  bool
	high    int // observed high-water mark (in items)
}

// NewBoundedQueue creates a queue holding at most capacity samples whose
// individual payloads are bounded by maxPayloadSize bytes.
func NewBoundedQueue(capacity, maxPayloadSize int) *BoundedQueue {
	if capacity <= 0 {
		capacity = 1
	}
	if maxPayloadSize <= 0 {
		maxPayloadSize = 1
	}
	return &BoundedQueue{ch: make(chan Sample, capacity), maxSize: maxPayloadSize}
}

// Enqueue blocks while the queue is full. It returns ctx error on cancellation
// and ErrQueueClosed once the queue has been closed.
func (q *BoundedQueue) Enqueue(ctx context.Context, s Sample) error {
	if len(s.Payload) > q.maxSize {
		return errors.New("stream: sample exceeds maximum payload size")
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return ErrQueueClosed
	}
	q.mu.Unlock()
	select {
	case q.ch <- s:
		q.noteLevel()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Dequeue blocks until a sample is available. The boolean is false when the
// queue is closed and drained.
func (q *BoundedQueue) Dequeue(ctx context.Context) (Sample, bool, error) {
	select {
	case s, ok := <-q.ch:
		if !ok {
			return Sample{}, false, nil
		}
		return s, true, nil
	case <-ctx.Done():
		return Sample{}, false, ctx.Err()
	}
}

// Close marks the queue closed; buffered items remain drainable.
func (q *BoundedQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.closed {
		q.closed = true
		close(q.ch)
	}
}

// Len returns the current occupancy.
func (q *BoundedQueue) Len() int { return len(q.ch) }

// Cap returns the fixed capacity.
func (q *BoundedQueue) Cap() int { return cap(q.ch) }

// MaxPayloadSize returns the per-sample byte ceiling.
func (q *BoundedQueue) MaxPayloadSize() int { return q.maxSize }

// MemoryCeilingBytes documents the hard bound: capacity*maxPayload plus the
// fixed per-sample working buffer.
func (q *BoundedQueue) MemoryCeilingBytes(fixedBuffer int) int {
	return q.Cap()*q.maxSize + fixedBuffer
}

// HighWater returns the largest occupancy observed.
func (q *BoundedQueue) HighWater() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.high
}

func (q *BoundedQueue) noteLevel() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if n := len(q.ch); n > q.high {
		q.high = n
	}
}
