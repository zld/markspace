// Package pipeline runs audio segments through a bounded worker pool and
// merges their results deterministically. Queue depth and goroutine count are
// capped; cancellation propagates from the producer through every worker and
// the merger, and a canceled run never publishes half-finished segments.
package pipeline

import (
	"context"
	"sync"

	"rtpregress/internal/audio"
	"rtpregress/internal/clock"
)

// Default resource caps keep a trace run local and bounded.
const (
	MaxWorkers   = 16
	MaxQueueJobs = 256
)

// Job is one unit of audio work tagged with its ordering coordinates.
type Job struct {
	Segment audio.Segment
	// Order coordinates drive the deterministic merge key.
	PID       uint16
	SegNumber int
}

// PoolResult is the envelope carrying a job plus its converter output.
type PoolResult struct {
	Job    Job
	Result *audio.Result
	Err    error
}

// Processor converts one audio segment. *audio.Converter satisfies it; tests
// substitute a deterministic fake to assert worker-count independence.
type Processor interface {
	Process(ctx context.Context, seg audio.Segment) (*audio.Result, error)
}

// Pool executes jobs with a fixed, bounded number of worker goroutines.
type Pool struct {
	workers   int
	queueSize int
	conv      Processor
}

// NewPool validates worker/queue bounds and binds a processor.
func NewPool(workers, queueSize int, conv Processor) (*Pool, error) {
	if workers < 1 || workers > MaxWorkers {
		return nil, clock.New(clock.CodeLimitExceeded, "pipeline", -1, 0, 0,
			"workers %d outside [1,%d]", workers, MaxWorkers)
	}
	if queueSize < 1 || queueSize > MaxQueueJobs {
		return nil, clock.New(clock.CodeLimitExceeded, "pipeline", -1, 0, 0,
			"queue size %d outside [1,%d]", queueSize, MaxQueueJobs)
	}
	return &Pool{workers: workers, queueSize: queueSize, conv: conv}, nil
}

// Run feeds jobs from the producer into workers and calls handle for every
// successfully completed result in completion order. When ctx is canceled,
// in-flight ffmpeg processes are killed and Run returns without invoking
// handle for any unfinished job. The producer must close jobs when done.
func (p *Pool) Run(ctx context.Context, jobs <-chan Job,
	handle func(PoolResult)) error {
	var (
		wg      sync.WaitGroup
		results = make(chan PoolResult, p.queueSize)
	)
	wg.Add(p.workers)
	for i := 0; i < p.workers; i++ {
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
					res, err := p.conv.Process(ctx, job.Segment)
					select {
					case results <- PoolResult{Job: job, Result: res, Err: err}:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}
	// Close results only after every worker has stopped, so a clean run can
	// never leave completed results stranded in the buffered channel.
	go func() {
		wg.Wait()
		close(results)
	}()
	// Drain every buffered result until the channel closes. Cancellation
	// returns immediately and never publishes in-flight (half-finished) work.
	for {
		select {
		case <-ctx.Done():
			wg.Wait() // let killed ffmpeg children reap before returning
			return clock.New(clock.CodeCanceled, "pipeline", -1, 0, 0,
				"pool canceled: %v", ctx.Err())
		case r, ok := <-results:
			if !ok {
				return nil
			}
			// a converter failure is surfaced but does not kill siblings;
			// the caller decides strict vs isolate handling.
			handle(r)
		}
	}
}
