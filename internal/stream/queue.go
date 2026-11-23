// Package stream implements the bounded multi-bitrate fan-out: one parsed
// input timeline is fanned out to representation layers, each backed by a
// fixed-capacity queue. Slow consumers are isolated by a configurable policy
// (block or drop non-key data), dropped packets are counted, and a damaged
// layer can independently recover from its most recent segment boundary.
// Every queue has a hard memory bound that never grows with input length.
package stream

import (
	"errors"
	"sync"
)

// Overflow policy applied when a layer queue is full.
const (
	// PolicyBlock applies backpressure: Enqueue blocks until space frees.
	PolicyBlock = "block"
	// PolicyDrop discards non-key packets when full; key packets still block.
	PolicyDrop = "drop"
)

// Envelope is one unit carried through the pipeline. Key packets (keyframes /
// segment-start markers) are never silently dropped under PolicyDrop.
type Envelope struct {
	// Seq is the RTP sequence number of the source packet.
	Seq uint16
	// TS is the unwrapped, master-clock timestamp in integer ticks.
	TS int64
	// Key marks a keyframe / segment-boundary packet.
	Key bool
	// Layer is the representation index this envelope belongs to.
	Layer int
	// Bytes is the synthetic payload size used for bitrate statistics.
	Bytes int
}

// Queue is a fixed-capacity, concurrency-safe FIFO of Envelope values. Its
// memory use is bounded by Cap() regardless of how many packets are offered;
// under PolicyDrop overflow is counted rather than buffered.
type Queue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	slots  []Envelope
	head   int
	size   int
	cap    int
	policy string
	closed bool
	// Dropped counts packets discarded under PolicyDrop.
	Dropped int64
	// BlockedNano is accumulated wait time under backpressure (integer ns).
	BlockedNano int64
}

// NewQueue creates a queue of the given fixed capacity and overflow policy.
func NewQueue(capacity int, policy string) (*Queue, error) {
	if capacity <= 0 {
		return nil, errors.New("stream: queue capacity must be positive")
	}
	if policy != PolicyBlock && policy != PolicyDrop {
		return nil, errors.New("stream: unknown overflow policy " + policy)
	}
	q := &Queue{slots: make([]Envelope, capacity), cap: capacity, policy: policy}
	q.cond = sync.NewCond(&q.mu)
	return q, nil
}

// Cap returns the fixed capacity (the hard in-memory packet bound).
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.size
}

// Cap returns the fixed capacity.
func (q *Queue) Cap() int { return q.cap }

// Enqueue adds a packet. Under PolicyBlock (or for a key packet under
// PolicyDrop) it blocks while the queue is full. Under PolicyDrop a full
// queue with a non-key packet records a drop and returns immediately.
func (q *Queue) Enqueue(e Envelope) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.policy == PolicyDrop && !e.Key {
		if q.closed {
			return ErrQueueClosed
		}
		if q.size == q.cap {
			q.Dropped++
			return nil // bounded: overflow counted, not buffered
		}
	} else {
		for q.size == q.cap && !q.closed {
			q.cond.Wait()
		}
		if q.closed {
			return ErrQueueClosed
		}
	}
	q.slots[(q.head+q.size)%q.cap] = e
	q.size++
	q.cond.Broadcast()
	return nil
}

// Dequeue removes and returns the oldest packet, blocking until one is
// available or the queue is closed and drained.
func (q *Queue) Dequeue() (Envelope, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.size == 0 && !q.closed {
		q.cond.Wait()
	}
	if q.size == 0 {
		return Envelope{}, false
	}
	e := q.slots[q.head]
	q.head = (q.head + 1) % q.cap
	q.size--
	q.cond.Broadcast()
	return e, true
}

// TryDequeue returns the oldest packet without blocking.
func (q *Queue) TryDequeue() (Envelope, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.size == 0 {
		return Envelope{}, false
	}
	e := q.slots[q.head]
	q.head = (q.head + 1) % q.cap
	q.size--
	q.cond.Broadcast()
	return e, true
}

// Close releases waiting producers/consumers; buffered packets remain
// drainable via Dequeue until empty.
func (q *Queue) Close() {
	q.mu.Lock()
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()
}

// Stats is a point-in-time queue snapshot.
type Stats struct {
	Layer   int
	Len     int
	Cap     int
	Dropped int64
	Policy  string
}

// Snapshot returns the current queue statistics.
func (q *Queue) Snapshot(layer int) Stats {
	q.mu.Lock()
	defer q.mu.Unlock()
	return Stats{Layer: layer, Len: q.size, Cap: q.cap, Dropped: q.Dropped, Policy: q.policy}
}

// ErrQueueClosed is returned by Enqueue after Close.
var ErrQueueClosed = errors.New("stream: queue closed")
