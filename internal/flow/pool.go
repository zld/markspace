// Package flow provides a bounded work pipeline with explicit backpressure
// and cancellation that propagates from consumer to workers to producer.
package flow

import (
	"context"
	"sync"
)

// Job is a unit of work identified by a contiguous sequence number.
type Job struct {
	Seq  int
	Data any
}

// Result is a finished job. Err is non-nil when the job failed; such results
// are delivered so the merger can decide whether the failure is fatal.
type Result struct {
	Seq  int
	Data any
	Err  error
}

// Config bounds the pipeline.
type Config struct {
	// Workers is the number of concurrent job processors; must be >= 1.
	Workers int
	// Queue is the capacity of the producer handoff channel and of the
	// result channel. Small queues make backpressure observable.
	Queue int
}

// Func processes a single job. It must honour ctx.
type Func func(ctx context.Context, job Job) (any, error)

// Stats records observable pipeline behaviour.
type Stats struct {
	// QueuePeak is the high-water mark of buffered, not-yet-started jobs.
	QueuePeak int
	// Submitted counts jobs handed to workers; Completed and Failed count
	// results consumed by the consumer.
	Submitted int
	Completed int
	Failed    int
}

// Pipeline runs jobs through a fixed pool.
type Pipeline struct {
	cfg Config
	mu  sync.Mutex
	st  Stats
}

// New validates cfg and returns a pipeline.
func New(cfg Config) (*Pipeline, error) {
	if cfg.Workers < 1 {
		return nil, errConfig("workers must be >= 1")
	}
	if cfg.Queue < 1 {
		return nil, errConfig("queue must be >= 1")
	}
	return &Pipeline{cfg: cfg}, nil
}

// Run starts the producer, workers and consumer loop.
//
// produce is called on its own goroutine; it must stop sending when ctx is
// done and return. work is called concurrently by cfg.Workers goroutines.
// consume is called in completion order on the calling goroutine; returning
// an error cancels the pipeline, and the returned error is that error.
//
// Backpressure: the producer blocks on a channel of capacity cfg.Queue when
// workers are busy, and workers block delivering results when the consumer is
// slow. Cancelling ctx, or the consumer returning an error, is observed by
// both the workers and the producer.
func (p *Pipeline) Run(ctx context.Context, produce func(context.Context, chan<- Job) error, work Func, consume func(Result) error) (Stats, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan Job, p.cfg.Queue)
	results := make(chan Result, p.cfg.Queue)

	var wg sync.WaitGroup
	var produceErr error

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(jobs)
		if err := produce(ctx, jobs); err != nil && ctx.Err() == nil {
			produceErr = err
			cancel()
		}
	}()

	var workers sync.WaitGroup
	for i := 0; i < p.cfg.Workers; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range jobs {
				// The handoff buffer capacity is the configured queue size,
				// so its occupancy alone is the reported peak. Counting the
				// job already in hand would double-count a worker's slot.
				p.observeQueue(len(jobs))
				if ctx.Err() != nil {
					// Keep draining so a producer blocked on send can make
					// progress and observe cancellation.
					continue
				}
				p.noteSubmitted()
				val, err := work(ctx, job)
				select {
				case results <- Result{Seq: job.Seq, Data: val, Err: err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		workers.Wait()
		close(results)
	}()

	var consumeErr error
	for res := range results {
		if consumeErr != nil {
			continue
		}
		p.noteConsumed(res.Err != nil)
		if err := consume(res); err != nil {
			consumeErr = err
			cancel()
		}
	}
	wg.Wait()

	if consumeErr != nil {
		return p.Stats(), consumeErr
	}
	if produceErr != nil {
		return p.Stats(), produceErr
	}
	if err := ctx.Err(); err != nil {
		return p.Stats(), err
	}
	return p.Stats(), nil
}

func (p *Pipeline) observeQueue(n int) {
	p.mu.Lock()
	if n > p.st.QueuePeak {
		p.st.QueuePeak = n
	}
	p.mu.Unlock()
}

func (p *Pipeline) noteSubmitted() {
	p.mu.Lock()
	p.st.Submitted++
	p.mu.Unlock()
}

func (p *Pipeline) noteConsumed(failed bool) {
	p.mu.Lock()
	if failed {
		p.st.Failed++
	} else {
		p.st.Completed++
	}
	p.mu.Unlock()
}

// Stats returns a snapshot.
func (p *Pipeline) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st
}
