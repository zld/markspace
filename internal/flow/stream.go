// Package flow provides a fixed-capacity streaming queue between pipeline
// stages. A slow consumer applies backpressure: producers block instead of
// the process buffering without bound. Cancellation and writer-side failure
// unblock every waiting producer so no goroutine leaks on shutdown.
package flow

import (
	"context"
	"errors"
	"sync"
)

// DefaultQueueCapacity is the fixed depth used when no capacity is supplied.
const DefaultQueueCapacity = 64

// Stream is a single-producer/multi-consumer-safe bounded FIFO of byte
// buffers. All memory accounting is bounded by capacity times the largest
// item currently in flight.
type Stream struct {
	items chan []byte

	mu      sync.Mutex
	closed  bool
	sendErr error
	done    chan struct{}
}

// New creates a stream with the given fixed capacity. Non-positive values
// fall back to DefaultQueueCapacity.
func New(capacity int) *Stream {
	if capacity <= 0 {
		capacity = DefaultQueueCapacity
	}
	return &Stream{
		items: make(chan []byte, capacity),
		done:  make(chan struct{}),
	}
}

// Send enqueues item, blocking while the queue is full. It returns
// ctx.Err() when the context is cancelled, or the error recorded by
// CloseWithError; a send after close always fails rather than panicking.
func (s *Stream) Send(ctx context.Context, item []byte) error {
	s.mu.Lock()
	if s.closed {
		err := s.sendErr
		s.mu.Unlock()
		if err == nil {
			err = errors.New("flow: send on closed stream")
		}
		return err
	}
	s.mu.Unlock()

	select {
	case s.items <- item:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		s.mu.Lock()
		err := s.sendErr
		s.mu.Unlock()
		if err == nil {
			err = errors.New("flow: stream closed")
		}
		return err
	}
}

// Recv blocks until an item is available. On clean drain it returns
// (nil, false) so consumers terminate with the usual comma-ok pattern.
func (s *Stream) Recv(ctx context.Context) ([]byte, bool, error) {
	select {
	case item, ok := <-s.items:
		return item, ok, nil
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}

// CloseWithError marks the stream failed and releases every producer
// blocked in Send. A nil error means a graceful close after the final item.
func (s *Stream) CloseWithError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.sendErr = err
	close(s.done)
	close(s.items)
}

// Len returns the number of queued items, useful for tests and metrics.
func (s *Stream) Len() int { return len(s.items) }
