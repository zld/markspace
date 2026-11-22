// Package pipeline provides a bounded worker pool with cancellation
// propagation and a deterministic merger that orders results by manifest
// sequence, independent of completion order.
package pipeline

import (
	"context"
	"fmt"
	"sync"
)

// Job is one unit of work. A nil error means success; the first non-nil
// error cancels the whole pool.
type Job func(ctx context.Context) error

// Pool is a bounded-concurrency worker pool. The job queue has fixed
// capacity: Submit blocks when full (backpressure) and unblocks with an
// error when the pool is cancelled.
type Pool struct {
	ctx    context.Context
	cancel context.CancelFunc
	jobs   chan Job

	pending sync.WaitGroup // submitted but not finished or discarded
	workers sync.WaitGroup

	errOnce sync.Once
	err     error
}

// NewPool starts workers goroutines with a queue of queueCap jobs.
func NewPool(ctx context.Context, workers, queueCap int) (*Pool, error) {
	if workers < 1 || workers > 8 {
		return nil, fmt.Errorf("workers must be 1..8, got %d", workers)
	}
	if queueCap < 1 {
		return nil, fmt.Errorf("queue capacity must be >= 1")
	}
	c, cancel := context.WithCancel(ctx)
	p := &Pool{ctx: c, cancel: cancel, jobs: make(chan Job, queueCap)}
	for i := 0; i < workers; i++ {
		p.workers.Add(1)
		go p.loop()
	}
	return p, nil
}

func (p *Pool) loop() {
	defer p.workers.Done()
	for {
		select {
		case <-p.ctx.Done():
			p.drain()
			return
		case j := <-p.jobs:
			if err := j(p.ctx); err != nil {
				p.fail(err)
			}
			p.pending.Done()
		}
	}
}

// drain discards queued jobs without executing them so Wait can return.
func (p *Pool) drain() {
	for {
		select {
		case <-p.jobs:
			p.pending.Done()
		default:
			return
		}
	}
}

// Submit enqueues a job, blocking while the queue is full. It returns a
// non-nil error if the pool is cancelled before the job is accepted.
func (p *Pool) Submit(j Job) error {
	select {
	case <-p.ctx.Done():
		return p.Err()
	default:
	}
	p.pending.Add(1)
	select {
	case p.jobs <- j:
		return nil
	case <-p.ctx.Done():
		p.pending.Done()
		return p.Err()
	}
}

func (p *Pool) fail(err error) {
	p.errOnce.Do(func() {
		p.err = err
		p.cancel()
	})
}

// Err returns the first job error, or the context error if cancelled
// externally before any job failed.
func (p *Pool) Err() error {
	if p.err != nil {
		return p.err
	}
	return p.ctx.Err()
}

// Wait blocks until every submitted job has finished or been discarded,
// then shuts the workers down. It returns the first job error, if any.
func (p *Pool) Wait() error {
	p.pending.Wait()
	p.cancel()
	p.workers.Wait()
	return p.err
}
