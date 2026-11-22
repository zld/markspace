// Package stream runs the execution stage: packets are located purely
// through the digest-bound index, read in bounded chunks, and handed to
// a consumer through a bounded queue. Slow consumers create measurable
// backpressure; cancellation releases all resources and leaves no
// half-finished output that could be mistaken for a usable result.
package stream

import (
	"context"
	"sync/atomic"
	"time"
)

// Packet is one unit of media flowing through the queue. Data is
// filled by the producer from the indexed offsets; its size is bounded
// by the block size limit in the ebml package.
type Packet struct {
	Track    uint64
	Ns       int64 // repaired unified-clock timestamp
	Keyframe bool
	Offset   int64 // input byte offset (for traceability)
	// ReadOff/ReadLen locate the payload in the input when Data is nil.
	ReadOff  int64
	ReadLen  int64
	Data     []byte
	Enqueued time.Time
}

// Queue is a bounded, closeable packet queue tracking peak occupancy.
type Queue struct {
	ch        chan Packet
	closed    atomic.Bool
	curBytes  atomic.Int64
	peakLen   atomic.Int64
	peakBytes atomic.Int64
}

// NewQueue creates a queue holding at most cap packets.
func NewQueue(capacity int) *Queue {
	if capacity < 1 {
		capacity = 1
	}
	return &Queue{ch: make(chan Packet, capacity)}
}

// Push blocks until there is room, the queue is closed, or ctx is
// cancelled. It returns false when the push did not happen.
func (q *Queue) Push(ctx context.Context, p Packet) bool {
	if q.closed.Load() {
		return false
	}
	p.Enqueued = time.Now()
	select {
	case q.ch <- p:
	case <-ctx.Done():
		return false
	}
	n := int64(len(q.ch))
	for {
		peak := q.peakLen.Load()
		if n <= peak || q.peakLen.CompareAndSwap(peak, n) {
			break
		}
	}
	b := q.curBytes.Add(int64(len(p.Data)))
	for {
		peak := q.peakBytes.Load()
		if b <= peak || q.peakBytes.CompareAndSwap(peak, b) {
			break
		}
	}
	return true
}

// Pop returns the next packet, or ok=false when the queue is closed and
// drained or ctx is cancelled.
func (q *Queue) Pop(ctx context.Context) (p Packet, ok bool) {
	select {
	case p, ok = <-q.ch:
		if ok {
			q.curBytes.Add(-int64(len(p.Data)))
		}
		return p, ok
	case <-ctx.Done():
		return Packet{}, false
	}
}

// Close signals no more pushes; remaining packets can still be popped.
func (q *Queue) Close() {
	if q.closed.CompareAndSwap(false, true) {
		close(q.ch)
	}
}

// Stats returns the peak queue length and peak buffered payload bytes.
func (q *Queue) Stats() (peakLen int, peakBytes int64) {
	return int(q.peakLen.Load()), q.peakBytes.Load()
}
