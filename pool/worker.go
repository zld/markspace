// Package pool provides a bounded worker pool. Concurrency and queue depth
// are both hard limits: producers block on a full queue, every shutdown path
// drains workers, and cancellation flows from the producer's context through
// every worker and the merge-side result channel.
package pool

import (
	"context"
	"errors"
	"sync"
)

// ErrQueueFull is returned by TrySubmit when the bounded queue is saturated.
var ErrQueueFull = errors.New("worker queue is full")

// ErrPoolClosed is returned by Submit/TrySubmit after the pool has shut down.
var ErrPoolClosed = errors.New("worker pool is closed")

// Task is one unit of work. The sequence is assigned by the producer and
// preserved on the Result so the merge stage can sort deterministically.
type Task struct {
	Sequence int
	Payload  any
}

// Result is one worker output, paired with the originating sequence.
type Result struct {
	Sequence int
	Value    any
	Err      error
}

// Handler processes one task. Returning an error marks that item failed but
// does not stop the pool; only Shutdown or context cancellation does.
type Handler func(ctx context.Context, t Task) (any, error)

// Pool is a fixed-size pool with a bounded buffered job channel.
type Pool struct {
	jobs    chan Task
	results chan Result
	done    chan struct{} // closed by Shutdown so blocked sends can release
	handler Handler
	wg      sync.WaitGroup
	mu      sync.Mutex
	closed  bool
}

// New starts workers goroutines with a queue holding at most queueDepth
// buffered tasks. The result channel capacity matches the maximum number of
// in-flight tasks (queued plus one per worker) so a result send can never
// block on a consumer that has stopped reading.
func New(ctx context.Context, workers, queueDepth int, handler Handler) *Pool {
	if workers < 1 {
		workers = 1
	}
	if queueDepth < 0 {
		queueDepth = 0
	}
	p := &Pool{
		jobs:    make(chan Task, queueDepth),
		results: make(chan Result, queueDepth+workers),
		done:    make(chan struct{}),
		handler: handler,
	}
	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go p.run(ctx, i)
	}
	return p
}

// run is the worker loop. While the context is alive it processes jobs; once
// the context is canceled it keeps draining the buffered queue until Shutdown
// closes the jobs channel, which guarantees close(jobs) never panics and no
// goroutine outlives Shutdown.
func (p *Pool) run(ctx context.Context, id int) {
	defer p.wg.Done()
	for {
		select {
		case <-ctx.Done():
			// Drain anything already queued, then wait for shutdown.
			for {
				select {
				case t, ok := <-p.jobs:
					if !ok {
						return
					}
					p.dispatch(ctx, t)
				default:
					select {
					case t, ok := <-p.jobs:
						if !ok {
							return
						}
						p.dispatch(ctx, t)
					case <-p.done:
						return
					}
				}
			}
		case t, ok := <-p.jobs:
			if !ok {
				return
			}
			p.dispatch(ctx, t)
		}
	}
}

// dispatch runs one handler and delivers its result. A handler blocked on
// the shared context is released by cancellation; a result nobody reads is
// released when Shutdown closes done.
func (p *Pool) dispatch(ctx context.Context, t Task) {
	val, err := p.handler(ctx, t)
	select {
	case p.results <- Result{Sequence: t.Sequence, Value: val, Err: err}:
	case <-ctx.Done():
	case <-p.done:
	}
}

// Submit blocks until the task is queued, the context is canceled or the pool
// has been closed. It propagates the context error rather than dropping work.
func (p *Pool) Submit(ctx context.Context, t Task) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return ErrPoolClosed
	}
	p.mu.Unlock()
	select {
	case p.jobs <- t:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return ErrPoolClosed
	}
}

// TrySubmit enqueues without blocking, returning ErrQueueFull when saturated
// and ErrPoolClosed after shutdown.
func (p *Pool) TrySubmit(t Task) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return ErrPoolClosed
	}
	p.mu.Unlock()
	select {
	case p.jobs <- t:
		return nil
	default:
		return ErrQueueFull
	}
}

// Results exposes the output channel consumed by the merge stage.
func (p *Pool) Results() <-chan Result { return p.results }

// Shutdown stops accepting tasks, closes the jobs channel and waits for every
// worker to exit, then releases blocked sends and closes the result channel.
// It is safe to call once; duplicate calls return ErrPoolClosed.
func (p *Pool) Shutdown() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return ErrPoolClosed
	}
	p.closed = true
	p.mu.Unlock()

	close(p.jobs)
	p.wg.Wait()
	close(p.done)
	close(p.results)
	return nil
}
