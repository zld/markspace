// Package worker runs analysis jobs on a bounded worker pool with a fixed
// queue capacity. Cancellation flows from the caller's context through the
// producer to every consumer: after cancellation no new job is accepted and
// the pool returns completed work together with an explicit cancel verdict.
package worker

import (
	"context"
	"sync"
	"sync/atomic"

	"tracecheck/internal/model"
)

// Job is one unit of analysis identified for the final report.
type Job struct {
	ID    int
	Label string
	// Run does the actual work. It receives the pool context so long
	// running jobs observe cancellation directly.
	Run func(ctx context.Context) error
}

// Result is the outcome of one accepted job.
type Result struct {
	JobID    int
	Label    string
	Err      error
	Findings model.Findings
}

// Stats summarizes pool behavior for the cancellation report.
type Stats struct {
	Submitted   int  `json:"submitted"`
	Accepted    int  `json:"accepted"`
	Rejected    int  `json:"rejected"`
	Completed   int  `json:"completed"`
	NotReceived int  `json:"not_received"`
	MaxQueue    int  `json:"max_queue_depth"`
	Canceled    bool `json:"canceled"`
}

// Pool is a fixed-size worker pool with a bounded work queue.
type Pool struct {
	jobs    chan Job
	results chan Result
	wg      sync.WaitGroup
	collect sync.WaitGroup
	ctx     context.Context

	submitted atomic.Int64
	accepted  atomic.Int64
	rejected  atomic.Int64
	completed atomic.Int64
	canceled  atomic.Bool
	closed    atomic.Bool

	queueMu  sync.Mutex
	maxQueue int

	collectedMu sync.Mutex
	collected   []Result
}

// New creates a pool with workers goroutines and a queue holding at most
// queueDepth jobs. Zero values fall back to one worker and depth one.
func New(ctx context.Context, workers, queueDepth int) *Pool {
	if workers < 1 {
		workers = 1
	}
	if queueDepth < 1 {
		queueDepth = 1
	}
	p := &Pool{
		jobs:    make(chan Job, queueDepth),
		results: make(chan Result, workers),
		ctx:     ctx,
	}
	// A dedicated collector drains results continuously so workers can
	// never block on a full result channel while Close waits for them.
	p.collect.Add(1)
	go func() {
		defer p.collect.Done()
		for r := range p.results {
			p.collectedMu.Lock()
			p.collected = append(p.collected, r)
			p.collectedMu.Unlock()
		}
	}()
	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go p.consume()
	}
	context.AfterFunc(ctx, func() { p.canceled.Store(true) })
	return p
}

func (p *Pool) consume() {
	defer p.wg.Done()
	for {
		select {
		case <-p.ctx.Done():
			return
		case job, ok := <-p.jobs:
			if !ok {
				return
			}
			p.noteDepth()
			res := Result{JobID: job.ID, Label: job.Label}
			if job.Run != nil {
				res.Err = job.Run(p.ctx)
			}
			p.completed.Add(1)
			p.results <- res
		}
	}
}

// noteDepth samples the current buffered queue length under the queue lock.
func (p *Pool) noteDepth() {
	depth := len(p.jobs)
	p.queueMu.Lock()
	if depth > p.maxQueue {
		p.maxQueue = depth
	}
	p.queueMu.Unlock()
}

// QueueDepth returns the current number of buffered jobs.
func (p *Pool) QueueDepth() int {
	p.queueMu.Lock()
	defer p.queueMu.Unlock()
	return len(p.jobs)
}

// MaxQueueDepth returns the highest buffered depth observed so far.
func (p *Pool) MaxQueueDepth() int {
	p.queueMu.Lock()
	defer p.queueMu.Unlock()
	return p.maxQueue
}

// Submit hands one job to the pool. After the context is canceled, or after
// Close, the job is rejected and counted as not received; Submit never
// blocks on a full queue once cancellation has started.
func (p *Pool) Submit(job Job) (accepted bool) {
	p.submitted.Add(1)
	if p.closed.Load() || p.ctx.Err() != nil {
		p.rejected.Add(1)
		return false
	}
	defer func() {
		// Close may race a blocked send; a closed-channel send means the
		// job could no longer be received, so count it as rejected.
		if recover() != nil {
			p.rejected.Add(1)
			accepted = false
		}
	}()
	select {
	case <-p.ctx.Done():
		p.rejected.Add(1)
		return false
	case p.jobs <- job:
		p.accepted.Add(1)
		p.noteDepth()
		return true
	}
}

// Close stops accepting work, waits for in-flight jobs to finish or observe
// cancellation, and returns every completed result with pool statistics.
// Jobs still queued when cancellation arrives are reported as not received.
func (p *Pool) Close() (results []Result, stats Stats) {
	p.closed.Store(true)
	close(p.jobs)
	p.wg.Wait()
	close(p.results)
	p.collect.Wait()
	p.collectedMu.Lock()
	results = p.collected
	p.collectedMu.Unlock()
	stats.Submitted = int(p.submitted.Load())
	stats.Accepted = int(p.accepted.Load())
	stats.Rejected = int(p.rejected.Load())
	stats.Completed = int(p.completed.Load())
	stats.NotReceived = stats.Submitted - stats.Completed
	stats.MaxQueue = p.MaxQueueDepth()
	stats.Canceled = p.canceled.Load() || p.ctx.Err() != nil
	return results, stats
}
