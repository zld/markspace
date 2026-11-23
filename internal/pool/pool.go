// Package pool provides a bounded, cancellable worker pool. Worker count and
// queue depth are configurable and hard-capped; cancellation propagates
// through the whole producer/consumer chain: on error or context cancel the
// input channel is closed, in-flight jobs observe the canceled context, and
// Wait blocks until every goroutine has exited so no worker is leaked.
package pool

import (
	"context"
	"sync"

	"mediakernel/internal/mediaerr"
)

// Job is one unit of work. Implementations must honor ctx and return a
// structured media error on failure.
type Job func(ctx context.Context) error

// Pool is a fixed-size worker pool backed by a bounded channel.
type Pool struct {
	ctx     context.Context
	done    <-chan struct{} // pool lifecycle (ctx.Done), independent of callers
	jobs    chan Job
	wg      sync.WaitGroup
	once    sync.Once
	closeMu sync.Mutex
	closed  bool

	errOnce sync.Once
	first   error
}

// New validates the bounds and starts the workers. The returned pool is
// ready for Submit; call Wait to drain and collect the first failure.
func New(ctx context.Context, workers, queue int) (*Pool, error) {
	if err := mediaerr.CheckWorkerQueue(workers, queue); err != nil {
		return nil, err
	}
	p := &Pool{ctx: ctx, done: ctx.Done(), jobs: make(chan Job, queue)}
	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go p.worker(ctx, i)
	}
	return p, nil
}

func (p *Pool) worker(ctx context.Context, id int) {
	defer p.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case job, ok := <-p.jobs:
			if !ok {
				return
			}
			if ctx.Err() != nil {
				return
			}
			if err := job(ctx); err != nil {
				p.recordError(err)
			}
		}
	}
}

// Submit enqueues a job. It blocks while the bounded queue is full (back
// pressure), returns promptly on cancellation, and fails after Close.
func (p *Pool) Submit(ctx context.Context, job Job) error {
	if err := ctx.Err(); err != nil {
		return mediaerr.New(mediaerr.CatCanceled, "ctx_submit",
			"submit after cancellation").Wrap(err)
	}
	p.closeMu.Lock()
	if p.closed {
		p.closeMu.Unlock()
		return mediaerr.New(mediaerr.CatInternal, "submit_closed",
			"submit on a closed pool")
	}
	p.closeMu.Unlock()

	select {
	case <-ctx.Done():
		return mediaerr.New(mediaerr.CatCanceled, "ctx_submit",
			"submit interrupted").Wrap(ctx.Err())
	case <-p.done:
		return mediaerr.New(mediaerr.CatCanceled, "pool_canceled",
			"submit after pool cancellation").Wrap(p.ctx.Err())
	case p.jobs <- job:
		return nil
	}
}

// Close shuts the input channel. Idempotent. After Close, workers finish the
// jobs already dequeued and exit; Wait then returns.
func (p *Pool) Close() {
	p.once.Do(func() {
		p.closeMu.Lock()
		p.closed = true
		close(p.jobs)
		p.closeMu.Unlock()
	})
}

// Wait closes the input channel if needed and blocks until every worker has
// exited, returning the first job error observed (cancellation included).
func (p *Pool) Wait() error {
	p.Close()
	p.wg.Wait()
	return p.first
}

func (p *Pool) recordError(err error) {
	p.errOnce.Do(func() { p.first = err })
}
