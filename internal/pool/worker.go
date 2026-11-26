package pool

import (
	"context"
	"sync"
)

const (
	MaxWorkers    = 4
	QueueCapacity = 8
)

// Config is the bounded execution policy. The zero value uses the acceptance
// limits: four workers and an eight-item queue.
type Config struct {
	Workers int
	Queue   int
}

func (c Config) normalized() (int, int) {
	workers := c.Workers
	if workers <= 0 {
		workers = MaxWorkers
	}
	if workers > MaxWorkers {
		workers = MaxWorkers
	}
	queue := c.Queue
	if queue <= 0 {
		queue = QueueCapacity
	}
	if queue > QueueCapacity {
		queue = QueueCapacity
	}
	return workers, queue
}

type envelope[J any] struct {
	index int
	job   J
}

type Result[R any] struct {
	Index int
	Value R
	Err   error
}

// Pool is a fixed-size, bounded-queue worker pool. Cancellation is propagated
// through the supplied context to both producer and handlers.
type Pool[J any, R any] struct {
	workers int
	queue   int
	handle  func(context.Context, J) (R, error)
}

func New[J any, R any](cfg Config, handle func(context.Context, J) (R, error)) *Pool[J, R] {
	workers, queue := cfg.normalized()
	return &Pool[J, R]{workers: workers, queue: queue, handle: handle}
}

func (p *Pool[J, R]) WorkerCount() int { return p.workers }
func (p *Pool[J, R]) QueueSize() int   { return p.queue }

// Run executes all jobs and returns values in input order, independent of
// completion order. The first handler error cancels remaining work.
func (p *Pool[J, R]) Run(parent context.Context, jobs []J) ([]R, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	jobCh := make(chan envelope[J], p.queue)
	resultCh := make(chan Result[R], len(jobs))
	var wg sync.WaitGroup
	for w := 0; w < p.workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for env := range jobCh {
				if ctx.Err() != nil {
					resultCh <- Result[R]{Index: env.index, Err: ctx.Err()}
					continue
				}
				value, err := p.handle(ctx, env.job)
				resultCh <- Result[R]{Index: env.index, Value: value, Err: err}
				if err != nil {
					cancel()
				}
			}
		}()
	}

	go func() {
		defer close(jobCh)
		for i, job := range jobs {
			select {
			case jobCh <- envelope[J]{index: i, job: job}:
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	values := make([]R, len(jobs))
	var firstErr error
	for result := range resultCh {
		if result.Err != nil {
			if firstErr == nil {
				firstErr = result.Err
				cancel()
			}
			continue
		}
		values[result.Index] = result.Value
	}
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil && parent.Err() != nil {
		return nil, parent.Err()
	}
	return values, nil
}
