package stream

import (
	"context"
	"sync"
	"sync/atomic"
)

// Stats describes the pressure observed on a bounded queue.
type Stats struct {
	Enqueued        int   `json:"enqueued"`
	Dequeued        int   `json:"dequeued"`
	Depth           int   `json:"depth"`
	PeakDepth       int   `json:"peak_depth"`
	PeakBufferBytes int64 `json:"peak_buffer_bytes"`
}

// Queue is a capacity-bounded generic queue that records peak buffered
// bytes. Enqueue blocks while full; cancellation or Fail releases every
// blocked producer and consumer.
//
// Two concerns are locked separately: sendMu serialises channel sends
// against Close so ch can never be closed mid-send, while atomic counters
// track pressure without ever being held across a blocking operation.
type Queue[T any] struct {
	ch       chan T
	itemSize int

	sendMu  sync.Mutex
	done    chan struct{}
	failErr atomic.Value // error, set before done is closed by Fail

	enqueued  atomic.Int64
	dequeued  atomic.Int64
	depth     atomic.Int64
	peak      atomic.Int64
	peakBytes atomic.Int64
}

// NewQueue creates a queue holding at most capItems items whose accounting
// size is itemSize bytes each. Zero itemSize means items measure as empty.
func NewQueue[T any](capItems, itemSize int) *Queue[T] {
	return &Queue[T]{
		ch:       make(chan T, capItems),
		itemSize: itemSize,
		done:     make(chan struct{}),
	}
}

// Enqueue blocks while the queue is full. It returns the cancellation cause
// or the error passed to Fail instead of silently dropping data.
func (q *Queue[T]) Enqueue(ctx context.Context, v T) error {
	for {
		q.sendMu.Lock()
		select {
		case <-q.done:
			err := q.closedErr()
			q.sendMu.Unlock()
			return err
		default:
		}
		// Blocking send under sendMu: Close takes the same mutex first and
		// therefore cannot close ch while this send is in flight. Receives
		// do not take sendMu, so a full queue still drains.
		select {
		case q.ch <- v:
			q.sendMu.Unlock()
			depth := q.depth.Add(1)
			q.enqueued.Add(1)
			for {
				p := q.peak.Load()
				if depth <= p || q.peak.CompareAndSwap(p, depth) {
					break
				}
			}
			peak := depth * int64(q.itemSize)
			for {
				p := q.peakBytes.Load()
				if peak <= p || q.peakBytes.CompareAndSwap(p, peak) {
					break
				}
			}
			return nil
		case <-ctx.Done():
			q.sendMu.Unlock()
			return ctx.Err()
		case <-q.done:
			err := q.closedErr()
			q.sendMu.Unlock()
			return err
		}
	}
}

// Dequeue returns the next item; ok is false after drain, cancel or Fail.
func (q *Queue[T]) Dequeue(ctx context.Context) (v T, ok bool) {
	select {
	case v, ok = <-q.ch:
		if ok {
			q.dequeued.Add(1)
			q.depth.Add(-1)
		}
		return v, ok
	case <-ctx.Done():
		return v, false
	}
}

// Close marks the queue finished; buffered items remain readable.
func (q *Queue[T]) Close() {
	q.sendMu.Lock()
	defer q.sendMu.Unlock()
	select {
	case <-q.done:
		return
	default:
		close(q.done)
		close(q.ch)
	}
}

// Fail closes the queue with an explicit cause. Producers blocked in
// Enqueue (and future callers) receive err instead of context.Canceled.
func (q *Queue[T]) Fail(err error) {
	q.sendMu.Lock()
	defer q.sendMu.Unlock()
	select {
	case <-q.done:
		return
	default:
		if err != nil {
			q.failErr.Store(err)
		}
		close(q.done)
		close(q.ch)
	}
}

func (q *Queue[T]) closedErr() error {
	if e, ok := q.failErr.Load().(error); ok && e != nil {
		return e
	}
	return context.Canceled
}

// Stats returns a snapshot of queue pressure.
func (q *Queue[T]) Stats() Stats {
	return Stats{
		Enqueued:        int(q.enqueued.Load()),
		Dequeued:        int(q.dequeued.Load()),
		Depth:           int(q.depth.Load()),
		PeakDepth:       int(q.peak.Load()),
		PeakBufferBytes: q.peakBytes.Load(),
	}
}
