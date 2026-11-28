// Package pipeline runs validation jobs on a bounded worker pool and merges
// their results back in input order. Both concurrency and queue depth are
// configured with hard caps, and a canceled context drains in-flight work
// without ever publishing a success-marked result.
package pipeline

import (
	"context"
	"sync"
	"time"

	"mediasync/internal/fault"
)

// Hard configuration caps. Callers cannot raise the pool past these.
const (
	MaxWorkers = 32
	MaxQueue   = 4096
)

// Processor executes one job. Implementations must return promptly when ctx
// is canceled and must treat inputs as untrusted.
type Processor func(ctx context.Context, job Job) (any, error)

// Job is one unit of work carrying its original input index.
type Job struct {
	Index int
	Input string
}

// Result is what a job returns. Index is echoed for deterministic merging.
type Result struct {
	Index    int
	Input    string
	Output   any
	Latency  int64 // nanoseconds spent in the worker
	Err      error
	Canceled bool
}

// Pool is a fixed-size bounded worker pool with a bounded input queue. An
// internal sink goroutine drains every delivered result so workers can never
// block on a full result channel, which makes Close deadlock-free even when
// the caller is not reading concurrently.
type Pool struct {
	workers   int
	jobs      chan Job
	results   chan Result
	wg        sync.WaitGroup
	mu        sync.Mutex
	collected []Result
	sinkDone  chan struct{}
	processor Processor
}

// NewPool clamps workers/queue into [1, Max*] and starts the workers plus a
// result sink. Call Submit for each job, then Close.
func NewPool(ctx context.Context, workers, queue int, processor Processor) *Pool {
	if workers < 1 {
		workers = 1
	}
	if workers > MaxWorkers {
		workers = MaxWorkers
	}
	if queue < 1 {
		queue = 1
	}
	if queue > MaxQueue {
		queue = MaxQueue
	}
	if processor == nil {
		processor = func(context.Context, Job) (any, error) { return nil, nil }
	}
	p := &Pool{
		workers:   workers,
		jobs:      make(chan Job, queue),
		results:   make(chan Result),
		sinkDone:  make(chan struct{}),
		processor: processor,
	}
	// The sink guarantees every worker always has a reader.
	go func() {
		defer close(p.sinkDone)
		for r := range p.results {
			p.mu.Lock()
			p.collected = append(p.collected, r)
			p.mu.Unlock()
		}
	}()
	for range workers {
		p.wg.Add(1)
		go p.worker(ctx)
	}
	return p
}

// Workers reports the (possibly clamped) worker count.
func (p *Pool) Workers() int { return p.workers }

func (p *Pool) worker(ctx context.Context) {
	defer p.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case job, ok := <-p.jobs:
			if !ok {
				return
			}
			r := p.run(ctx, job)
			// A canceled context means nobody will consume further output;
			// exit rather than block. Such results are never successes.
			select {
			case p.results <- r:
			case <-ctx.Done():
				return
			}
		}
	}
}

// Submit enqueues a job. After cancellation it returns a fault instead of
// accepting more work, so no new task can enter a draining pool. The context
// is checked first (not via a random select) so cancellation always wins over
// an available queue slot.
func (p *Pool) Submit(ctx context.Context, job Job) error {
	if err := ctx.Err(); err != nil {
		return fault.New(fault.ErrCanceled, "", -1, "submit after cancellation")
	}
	select {
	case p.jobs <- job:
		return nil
	default:
	}
	select {
	case <-ctx.Done():
		return fault.New(fault.ErrCanceled, "", -1, "submit after cancellation")
	case p.jobs <- job:
		return nil
	}
}

// Close stops accepting jobs, waits for in-flight workers to exit and then
// finalizes the collected result set. It always returns.
func (p *Pool) Close() {
	close(p.jobs)
	p.wg.Wait()
	close(p.results)
	<-p.sinkDone
}

// Collected returns every result delivered before Close, in arrival order.
func (p *Pool) Collected() []Result {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Result, len(p.collected))
	copy(out, p.collected)
	return out
}

// run executes one job, converting a post-cancellation completion into an
// explicit canceled result so no success can be published after cancel.
func (p *Pool) run(ctx context.Context, job Job) Result {
	res := Result{Index: job.Index, Input: job.Input}
	if err := ctx.Err(); err != nil {
		res.Canceled = true
		res.Err = fault.New(fault.ErrCanceled, "", -1, "job canceled before start")
		return res
	}
	start := time.Now()
	out, err := p.processor(ctx, job)
	res.Latency = time.Since(start).Nanoseconds()
	if err != nil {
		if ctx.Err() != nil {
			res.Canceled = true
		}
		res.Err = err
		return res
	}
	if ctx.Err() != nil {
		res.Canceled = true
		res.Err = fault.New(fault.ErrCanceled, "", -1, "job completed after cancellation")
		return res
	}
	res.Output = out
	return res
}
