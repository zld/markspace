// Package pool provides bounded-concurrency task execution. Worker
// count and queue capacity are validated against hard limits.
// Cancellation or a worker failure propagates to both ends of the
// pipeline: submissions stop, completed results are drained, and the
// pool never deadlocks.
package pool

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Hard limits for pool configuration.
const (
	MaxWorkers = 64
	MaxQueue   = 4096
)

// ErrClosed is returned by Submit after the pool stopped accepting
// tasks (Close, cancellation or worker failure).
var ErrClosed = errors.New("pool: closed")

// Result pairs a worker output with its error.
type Result[Out any] struct {
	Value Out
	Err   error
}

// Pool executes tasks with a bounded number of workers and a bounded
// task queue.
type Pool[In, Out any] struct {
	fn      func(context.Context, In) (Out, error)
	tasks   chan In
	results chan Result[Out]
	ctx     context.Context
	cancel  context.CancelFunc

	mu        sync.RWMutex // guards closed + tasks channel send/close
	closed    bool
	wg        sync.WaitGroup
	closeOnce sync.Once
	errOnce   sync.Once
	err       error
}

// New validates the configuration and starts workers.
func New[In, Out any](ctx context.Context, workers, queue int, fn func(context.Context, In) (Out, error)) (*Pool[In, Out], error) {
	if workers < 1 || workers > MaxWorkers {
		return nil, fmt.Errorf("pool: workers %d out of range [1,%d]", workers, MaxWorkers)
	}
	if queue < 1 || queue > MaxQueue {
		return nil, fmt.Errorf("pool: queue %d out of range [1,%d]", queue, MaxQueue)
	}
	pctx, cancel := context.WithCancel(ctx)
	p := &Pool[In, Out]{
		fn:      fn,
		tasks:   make(chan In, queue),
		results: make(chan Result[Out], queue+workers),
		ctx:     pctx,
		cancel:  cancel,
	}
	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go p.worker()
	}
	// Propagate cancellation/worker failure into shutdown.
	go func() {
		<-pctx.Done()
		p.Close()
	}()
	return p, nil
}

func (p *Pool[In, Out]) worker() {
	defer p.wg.Done()
	for {
		select {
		case <-p.ctx.Done():
			return
		case t, ok := <-p.tasks:
			if !ok {
				return
			}
			p.run(t)
		}
	}
}

func (p *Pool[In, Out]) run(t In) {
	out, err := p.fn(p.ctx, t)
	if err != nil {
		p.errOnce.Do(func() {
			p.err = err
			p.cancel()
		})
	}
	select {
	case p.results <- Result[Out]{Value: out, Err: err}:
	case <-p.ctx.Done():
	}
}

// Submit enqueues a task, blocking while the bounded queue is full.
// It fails fast when the pool is closed, cancelled or the caller's
// context expires.
func (p *Pool[In, Out]) Submit(ctx context.Context, in In) error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		return ErrClosed
	}
	select {
	case p.tasks <- in:
		return nil
	case <-p.ctx.Done():
		if p.err != nil {
			return p.err
		}
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Results returns the channel of completed results. It is closed once
// all workers exit; completed results remain readable after close.
func (p *Pool[In, Out]) Results() <-chan Result[Out] { return p.results }

// Err returns the first worker failure, if any.
func (p *Pool[In, Out]) Err() error { return p.err }

// Close stops accepting tasks, lets workers drain the queue, then
// closes the results channel. Safe to call more than once.
func (p *Pool[In, Out]) Close() {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		close(p.tasks)
		p.mu.Unlock()
		p.wg.Wait()
		close(p.results)
		p.cancel()
	})
}
