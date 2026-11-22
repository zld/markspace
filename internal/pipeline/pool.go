// Package pipeline wires bounded production, processing and merging stages
// with context cancellation propagated end to end.
package pipeline

import (
	"context"
	"sync"

	"mediaarchive/internal/diag"
)

// Pool is a fixed-capacity worker pool. Work queues are bounded, so a slow
// consumer applies backpressure instead of growing memory without bound.
type Pool struct {
	Workers int
	Queue   int
}

// Job is one unit of work. Offset identifies the job's position in the input
// so cancellation and errors stay attributable.
type Job struct {
	Index  int64
	Offset int64
	Kind   string
}

// Handler processes one job. Returning an error stops the pool after in-flight
// jobs finish, and the error is reported with the job's index.
type Handler func(ctx context.Context, job Job) error

// Result summarizes a pool run.
type Result struct {
	Processed int64
	Skipped   int64
	Err       error
}

// Run consumes jobs from produce (until produce returns false or errors) and
// dispatches them to a bounded number of workers. Cancellation is honored at
// three points: production, job acquisition and job processing.
func (p Pool) Run(ctx context.Context, produce func(ctx context.Context) (Job, bool, error), handle Handler) Result {
	workers := p.Workers
	if workers <= 0 {
		workers = 1
	}
	queue := p.Queue
	if queue <= 0 {
		queue = 1
	}
	jobs := make(chan Job, queue)
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		firstErr  error
		processed int64
	)
	setErr := func(err error) {
		if err == nil {
			return
		}
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case job, ok := <-jobs:
					if !ok {
						return
					}
					if err := ctx.Err(); err != nil {
						setErr(err)
						return
					}
					if err := handle(ctx, job); err != nil {
						setErr(err)
						cancel()
						return
					}
					mu.Lock()
					processed++
					mu.Unlock()
				}
			}
		}()
	}

	// Producer runs on the calling goroutine; it stops when the queue is full
	// and the context is canceled.
produceLoop:
	for {
		if err := ctx.Err(); err != nil {
			setErr(err)
			break
		}
		job, ok, err := produce(ctx)
		if err != nil {
			setErr(err)
			cancel()
			break
		}
		if !ok {
			break
		}
		select {
		case jobs <- job:
		case <-ctx.Done():
			setErr(ctx.Err())
			break produceLoop
		}
	}
	close(jobs)
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	return Result{Processed: processed, Err: firstErr}
}

// ErrCanceled wraps a cancellation as a structured fault with an offset so the
// CLI can report where processing stopped.
func ErrCanceled(offset int64) error {
	return diag.New(diag.CodeCanceled, offset, "processing canceled")
}
