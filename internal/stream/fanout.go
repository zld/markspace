// Package stream provides bounded multi-rendition fan-out and shared-clock
// fan-in verification.
package stream

import (
	"context"
	"sync"

	"mediapack/internal/clock"
	"mediapack/internal/fault"
)

// Frame is one source frame or boundary marker. Segment boundary frames set
// Boundary=true and carry the half-open segment interval.
type Frame struct {
	Sequence int
	Segment  int
	Start    clock.Ticks
	End      clock.Ticks
	Payload  []byte
	Boundary bool
	Corrupt  bool
}

// Queue is a bounded per-representation queue. Full queues apply backpressure
// to the broadcaster instead of dropping frames or boundaries.
type Queue struct {
	Name     string
	capacity int
	frames   chan Frame

	mu     sync.Mutex
	err    *fault.Error
	failed bool
	done   chan struct{}
	once   sync.Once

	muStats  sync.Mutex
	Consumed int
}

// NewQueue creates a bounded representation queue.
func NewQueue(name string, capacity int) *Queue {
	if capacity < 1 {
		capacity = 1
	}
	return &Queue{Name: name, capacity: capacity, frames: make(chan Frame, capacity), done: make(chan struct{})}
}

// Capacity returns the configured bound.
func (q *Queue) Capacity() int { return q.capacity }

// Enqueue blocks while the queue is full, providing backpressure.
func (q *Queue) Enqueue(ctx context.Context, f Frame) error {
	select {
	case q.frames <- f:
		return nil
	case <-q.done:
		q.mu.Lock()
		defer q.mu.Unlock()
		if q.err != nil {
			return q.err
		}
		return fault.New(fault.LayerFailed, "layer %s canceled", q.Name)
	case <-ctx.Done():
		return fault.Wrap(fault.QueueCanceled, ctx.Err(), "enqueue layer %s", q.Name)
	}
}

// Dequeue receives the next frame.
func (q *Queue) Dequeue(ctx context.Context) (Frame, error) {
	select {
	case f := <-q.frames:
		q.muStats.Lock()
		q.Consumed++
		q.muStats.Unlock()
		return f, nil
	case <-q.done:
		q.mu.Lock()
		err := q.err
		q.mu.Unlock()
		if err != nil {
			return Frame{}, err
		}
		return Frame{}, fault.New(fault.QueueCanceled, "queue %s closed", q.Name)
	case <-ctx.Done():
		return Frame{}, fault.Wrap(fault.QueueCanceled, ctx.Err(), "dequeue layer %s", q.Name)
	}
}

// Fail marks only this representation as failed and unblocks its queue.
func (q *Queue) Fail(err *fault.Error) {
	q.mu.Lock()
	q.failed = true
	q.err = err
	q.mu.Unlock()
	q.once.Do(func() { close(q.done) })
}

// Failed returns the layer failure, if any.
func (q *Queue) Failed() *fault.Error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.err
}

// Fanout broadcasts each source frame to all representation queues.
type Fanout struct {
	Queues []*Queue
}

// NewFanout creates named bounded queues in deterministic layer order.
func NewFanout(layers []string, capacity int) *Fanout {
	f := &Fanout{}
	for _, name := range layers {
		f.Queues = append(f.Queues, NewQueue(name, capacity))
	}
	return f
}

// Queue finds a layer queue.
func (f *Fanout) Queue(name string) *Queue {
	for _, q := range f.Queues {
		if q.Name == name {
			return q
		}
	}
	return nil
}

// Broadcast applies backpressure on healthy slow queues. A failed layer is
// skipped so its corruption/cancellation does not stop other representations.
func (f *Fanout) Broadcast(ctx context.Context, frame Frame) []*fault.Error {
	var errs []*fault.Error
	for _, q := range f.Queues {
		if err := q.Failed(); err != nil {
			continue
		}
		layerFrame := frame
		if err := q.Enqueue(ctx, layerFrame); err != nil {
			if fe, ok := err.(*fault.Error); ok {
				errs = append(errs, fe)
				q.Fail(fe)
				continue
			}
			errs = append(errs, fault.Wrap(fault.QueueCanceled, err, "layer %s", q.Name))
		}
	}
	return errs
}

// Run drains input and broadcasts until input closes or parent ctx cancels.
func (f *Fanout) Run(ctx context.Context, input <-chan Frame) []*fault.Error {
	var all []*fault.Error
	for {
		select {
		case <-ctx.Done():
			all = append(all, fault.Wrap(fault.QueueCanceled, ctx.Err(), "fanout canceled"))
			return all
		case frame, ok := <-input:
			if !ok {
				return all
			}
			if errs := f.Broadcast(ctx, frame); len(errs) > 0 {
				all = append(all, errs...)
			}
		}
	}
}
