package stream

import (
	"context"
	"sync"

	"mediapipeline/internal/fault"
)

// Processor handles one dequeued item. A non-nil error stops the pipeline;
// the error is classified by the implementation (e.g. truncated box).
type Processor func(ctx context.Context, item Item) error

// Pipeline runs one producer goroutine feeding a bounded queue consumed by a
// fixed worker pool. It records the queue occupancy peak for back-pressure
// verification and guarantees the queue never exceeds its capacity.
type Pipeline struct {
	queue     *BoundedQueue
	workers   int
	processor Processor

	mu      sync.Mutex
	peak    int
	stopped bool
}

// Stats summarizes a finished run.
type Stats struct {
	// Processed is the number of items handled without error.
	Processed int
	// QueuePeak is the maximum simultaneous queue occupancy.
	QueuePeak int
	// Capacity is the configured bound.
	Capacity int
}

// NewPipeline validates parameters and constructs a pipeline.
func NewPipeline(capacity, workers int, processor Processor) (*Pipeline, error) {
	if workers < 1 {
		return nil, errInvalidWorkers()
	}
	q, err := NewBoundedQueue(capacity)
	if err != nil {
		return nil, err
	}
	if processor == nil {
		return nil, errNilProcessor()
	}
	return &Pipeline{queue: q, workers: workers, processor: processor}, nil
}

// Run executes produce to completion and drains it through the worker pool.
// produce should enqueue every item and then return; the queue is closed
// automatically. If produce or any processor fails, Run cancels outstanding
// work and returns the first error without advancing any caller checkpoint.
func (p *Pipeline) Run(ctx context.Context, produce func(context.Context, *BoundedQueue) error) (Stats, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var producerErr error
	var procErr error
	var wg sync.WaitGroup
	var countMu sync.Mutex
	processed := 0

	// Workers.
	wg.Add(p.workers)
	for i := 0; i < p.workers; i++ {
		go func() {
			defer wg.Done()
			for {
				item, ok, err := p.queue.Dequeue(runCtx)
				if err != nil {
					// A canceled context after a processor failure is an
					// expected side effect, not the root cause.
					p.recordCancelation(&procErr, err)
					return
				}
				if !ok {
					return
				}
				if err := p.processor(runCtx, item); err != nil {
					p.recordError(&procErr, err)
					cancel()
					return
				}
				countMu.Lock()
				processed++
				countMu.Unlock()
			}
		}()
	}

	// Producer runs inline so back-pressure is observable to the caller.
	producerErr = produce(runCtx, p.queue)
	if producerErr == nil {
		p.queue.Close()
	} else {
		cancel()
		p.queue.Close()
	}
	wg.Wait()

	// A substantive failure always wins over the cancelation it triggered:
	// producers blocked on a full queue and workers blocked on dequeue both
	// observe ctx cancel after a processor error.
	if procErr != nil {
		return Stats{}, procErr
	}
	if producerErr != nil {
		return Stats{}, producerErr
	}
	return Stats{
		Processed: processed,
		QueuePeak: p.queue.Peak(),
		Capacity:  p.queue.capacity,
	}, nil
}

func (p *Pipeline) recordError(target *error, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if *target == nil {
		*target = err
	}
}

// recordCancelation stores a cancelation error only when no substantive
// failure has been recorded yet, and never replaces one.
func (p *Pipeline) recordCancelation(target *error, err error) {
	if !fault.Is(err, fault.CodeCanceled) {
		p.recordError(target, err)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	// Never let a wake-up cancelation mask the processor/producer failure
	// that triggered the cancel in the first place.
	if *target == nil {
		*target = err
	}
}
