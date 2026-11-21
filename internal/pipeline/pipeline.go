// Package pipeline runs the per-tier build stages concurrently, applying
// back-pressure through bounded queues and propagating cancellation so every
// goroutine and open file is cleaned up on error or interrupt.
package pipeline

import (
	"context"
	"errors"
	"sync"
)

// ErrQueueClosed is returned by Enqueue after the queue is shut down.
var ErrQueueClosed = errors.New("pipeline: queue closed")

// BoundedQueue is a capacity-limited FIFO. Send blocks while the queue is
// full, providing natural back-pressure, and honours context cancellation.
// The data channel is never closed; shutdown is signalled separately so a
// concurrent Enqueue can never panic on a closed channel.
type BoundedQueue[T any] struct {
	ch     chan T
	closed chan struct{}
	once   sync.Once
}

// NewBoundedQueue creates a queue holding at most capacity items.
func NewBoundedQueue[T any](capacity int) *BoundedQueue[T] {
	if capacity < 1 {
		capacity = 1
	}
	return &BoundedQueue[T]{
		ch:     make(chan T, capacity),
		closed: make(chan struct{}),
	}
}

// Enqueue blocks until space is available, the context is cancelled or the
// queue is closed. It returns the context error or ErrQueueClosed.
func (q *BoundedQueue[T]) Enqueue(ctx context.Context, v T) error {
	select {
	case <-q.closed:
		return ErrQueueClosed
	case <-ctx.Done():
		return ctx.Err()
	case q.ch <- v:
		return nil
	}
}

// Dequeue blocks until an item is available. After Close it drains any
// buffered items, then returns ok=false; context cancellation also unblocks.
func (q *BoundedQueue[T]) Dequeue(ctx context.Context) (T, bool, error) {
	var zero T
	// First, consume anything already buffered without blocking.
	select {
	case v := <-q.ch:
		return v, true, nil
	default:
	}
	select {
	case v := <-q.ch:
		return v, true, nil
	case <-ctx.Done():
		return zero, false, ctx.Err()
	case <-q.closed:
		// Closed: drain remaining items, then report end-of-queue.
		select {
		case v := <-q.ch:
			return v, true, nil
		default:
			return zero, false, nil
		}
	}
}

// Close signals shutdown. Buffered items remain readable until drained.
func (q *BoundedQueue[T]) Close() {
	q.once.Do(func() { close(q.closed) })
}

// Len returns the current number of queued items.
func (q *BoundedQueue[T]) Len() int { return len(q.ch) }

// TierJob is one rendition's unit of work.
type TierJob struct {
	Name string
	Data []byte
}

// Worker processes a single tier job and returns a stage-specific result.
type Worker func(ctx context.Context, job TierJob) error

// Run executes every job across a fixed worker pool of at most concurrency
// goroutines. The first error cancels the remaining work; Run waits for all
// goroutines to finish so no resources leak.
func Run(ctx context.Context, jobs []TierJob, concurrency int, worker Worker) error {
	if concurrency < 1 {
		concurrency = 1
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobsCh := make(chan TierJob, len(jobs))
	for _, job := range jobs {
		jobsCh <- job
	}
	close(jobsCh)

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case job, ok := <-jobsCh:
					if !ok {
						return
					}
					if err := worker(ctx, job); err != nil {
						mu.Lock()
						if firstErr == nil {
							firstErr = err
						}
						mu.Unlock()
						cancel()
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}
