// Package stream provides the bounded byte queue connecting the streaming
// kernel to the remuxer. The queue enforces a hard memory ceiling: when the
// consumer is slow, producers block (or, with TryPush, fail fast with a
// backpressure error). Cancellation unblocks every goroutine.
package stream

import (
	"context"
	"errors"
	"sync/atomic"
)

// ErrBackpressure is returned by TryPush when the queue is full.
var ErrBackpressure = errors.New("stream: backpressure (queue full)")

// ErrClosed is returned when operating on a closed queue.
var ErrClosed = errors.New("stream: queue closed")

// tokenUnit is the byte granularity of the capacity semaphore.
const tokenUnit = 1024

// Chunk is one unit of streamed media data.
type Chunk struct {
	Data     []byte
	TrackID  int
	IsSample bool
}

type item struct {
	c     Chunk
	units int
}

// Queue is a bounded multi-producer single-consumer byte queue.
type Queue struct {
	chunks chan item
	tokens chan struct{}
	done   chan struct{}
	closed atomic.Bool
	cur    atomic.Int64
	peak   atomic.Int64
}

// New creates a queue holding at most maxBytes of buffered payload.
func New(maxBytes int64) *Queue {
	units := int(maxBytes+tokenUnit-1) / tokenUnit
	if units < 1 {
		units = 1
	}
	return &Queue{
		chunks: make(chan item, 64),
		tokens: make(chan struct{}, units),
		done:   make(chan struct{}),
	}
}

func unitsFor(n int) int {
	u := (n + tokenUnit - 1) / tokenUnit
	if u < 1 {
		return 1
	}
	return u
}

// Push blocks until the chunk fits within the byte budget, the queue is
// closed, or ctx is cancelled.
func (q *Queue) Push(ctx context.Context, c Chunk) error {
	return q.push(ctx, c, true)
}

// TryPush fails fast with ErrBackpressure when the chunk does not fit.
func (q *Queue) TryPush(ctx context.Context, c Chunk) error {
	return q.push(ctx, c, false)
}

func (q *Queue) push(ctx context.Context, c Chunk, blocking bool) error {
	if q.closed.Load() {
		return ErrClosed
	}
	units := unitsFor(len(c.Data))
	acquired := 0
	acquire := func() error {
		if blocking {
			select {
			case q.tokens <- struct{}{}:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			case <-q.done:
				return ErrClosed
			}
		}
		select {
		case q.tokens <- struct{}{}:
			return nil
		default:
			return ErrBackpressure
		}
	}
	release := func(n int) {
		for i := 0; i < n; i++ {
			<-q.tokens
		}
		q.cur.Add(int64(-n * tokenUnit))
	}
	for acquired < units {
		if err := acquire(); err != nil {
			release(acquired)
			return err
		}
		acquired++
	}
	cur := q.cur.Add(int64(units * tokenUnit))
	for {
		old := q.peak.Load()
		if cur <= old || q.peak.CompareAndSwap(old, cur) {
			break
		}
	}
	it := item{c: c, units: units}
	if blocking {
		select {
		case q.chunks <- it:
			return nil
		case <-ctx.Done():
			release(units)
			return ctx.Err()
		case <-q.done:
			release(units)
			return ErrClosed
		}
	}
	select {
	case q.chunks <- it:
		return nil
	default:
		release(units)
		return ErrBackpressure
	}
}

// Pop returns the next chunk, blocking until one is available, the queue
// is closed and drained, or ctx is cancelled.
func (q *Queue) Pop(ctx context.Context) (Chunk, error) {
	select {
	case it := <-q.chunks:
		for i := 0; i < it.units; i++ {
			<-q.tokens
		}
		q.cur.Add(int64(-it.units * tokenUnit))
		return it.c, nil
	case <-ctx.Done():
		return Chunk{}, ctx.Err()
	case <-q.done:
		// Drain whatever remains after close.
		select {
		case it := <-q.chunks:
			for i := 0; i < it.units; i++ {
				<-q.tokens
			}
			q.cur.Add(int64(-it.units * tokenUnit))
			return it.c, nil
		default:
			return Chunk{}, ErrClosed
		}
	}
}

// Close shuts the queue down; blocked producers and consumers wake up.
func (q *Queue) Close() {
	if q.closed.CompareAndSwap(false, true) {
		close(q.done)
	}
}

// Peak returns the maximum buffered bytes observed so far.
func (q *Queue) Peak() int64 { return q.peak.Load() }

// Buffered returns the currently buffered bytes (approximate, unit-rounded).
func (q *Queue) Buffered() int64 { return q.cur.Load() }
