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

// Stream is a bounded FIFO. Its item type is generic so pipelines can queue
// parsed packets directly without dropping metadata into raw bytes.
//
// Shutdown never closes the items channel: a blocked sender and a close are
// an inherent data race. Instead the separate done channel signals
// termination, which every blocked Send/Recv selects on, while the mutex
// guards the closed flag and the recorded error.
type Stream[T any] struct {
	items chan T

	mu      sync.RWMutex
	closed  bool
	sendErr error
	done    chan struct{}
}

// New creates a stream with the given fixed capacity. Non-positive values
// fall back to DefaultQueueCapacity.
func New[T any](capacity int) *Stream[T] {
	if capacity <= 0 {
		capacity = DefaultQueueCapacity
	}
	return &Stream[T]{
		items: make(chan T, capacity),
		done:  make(chan struct{}),
	}
}

// Send enqueues item, blocking while the queue is full. It returns
// ctx.Err() when the context is cancelled, or the error recorded by
// CloseWithError; a send after close always fails rather than panicking.
func (s *Stream[T]) Send(ctx context.Context, item T) error {
	s.mu.RLock()
	if s.closed {
		err := s.sendErr
		s.mu.RUnlock()
		if err == nil {
			err = errors.New("flow: send on closed stream")
		}
		return err
	}
	s.mu.RUnlock()

	select {
	case s.items <- item:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return s.closeError()
	}
}

// SendUntilClose enqueues item but also returns when stop fires, letting a
// producer abandon a full queue once its consumer has terminated early
// (for example at an interruption point).
func (s *Stream[T]) SendUntilClose(ctx context.Context, item T, stop <-chan struct{}) error {
	s.mu.RLock()
	if s.closed {
		err := s.sendErr
		s.mu.RUnlock()
		if err == nil {
			err = errors.New("flow: send on closed stream")
		}
		return err
	}
	s.mu.RUnlock()
	select {
	case s.items <- item:
		return nil
	case <-stop:
		return ErrStopped
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return s.closeError()
	}
}

// Recv blocks until an item is available. After CloseWithError it drains any
// buffered items, then returns (zero, false) so consumers terminate with the
// usual comma-ok pattern.
func (s *Stream[T]) Recv(ctx context.Context) (T, bool, error) {
	select {
	case item, ok := <-s.items:
		if !ok {
			var zero T
			return zero, false, nil
		}
		return item, true, nil
	case <-ctx.Done():
		var zero T
		return zero, false, ctx.Err()
	case <-s.done:
		// Termination signaled: opportunistically drain anything already
		// buffered, otherwise report end-of-stream.
		select {
		case item := <-s.items:
			return item, true, nil
		default:
			var zero T
			return zero, false, nil
		}
	}
}

// CloseWithError marks the stream failed and releases every producer or
// consumer blocked in Send/Recv. A nil error means a graceful close after
// the final buffered item.
func (s *Stream[T]) CloseWithError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.sendErr = err
	close(s.done)
}

// closeError returns the error recorded at shutdown under the read lock.
func (s *Stream[T]) closeError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.sendErr == nil {
		return errors.New("flow: stream closed")
	}
	return s.sendErr
}

// Len returns the number of buffered items, useful for tests and metrics.
func (s *Stream[T]) Len() int { return len(s.items) }

// ErrStopped signals that a producer quit because its consumer stopped.
var ErrStopped = errors.New("flow: producer stopped by consumer shutdown")
