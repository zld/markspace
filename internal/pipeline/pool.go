// Package pipeline runs sample processing on a bounded worker pool and
// deterministically merges per-track results back into one ordered stream.
// Cancellation flows from the input reader through every worker to the
// writer: once the context is done no new task starts, in-flight tasks
// observe the cancellation, and Close returns only after every worker has
// stopped and the output channel has drained.
package pipeline

import (
	"context"
	"errors"
	"sync"
)

// MaxWorkers and MaxQueue bound resource use regardless of input length.
const (
	MaxWorkers = 64
	MaxQueue   = 1024
)

// Task is one unit of work keyed so the merge stage can restore order.
type Task struct {
	Track uint32
	Seq   uint64
	NS    int64 // shared-base nanosecond timestamp
	Index int64 // global ingestion index, the final deterministic tiebreaker
	Data  []byte
}

// Result is a processed task. Workers must not mutate Task after returning.
type Result struct {
	Task Task
	Out  []byte
	Err  error
}

// ErrPoolClosed is returned by Submit after the pool has been shut down.
var ErrPoolClosed = errors.New("pipeline: pool closed")

// Pool is a fixed-size worker pool with a bounded input queue. Submit
// blocks when the queue is full, which applies back-pressure to the reader
// instead of buffering an unbounded amount of input.
type Pool struct {
	ctx       context.Context
	queue     chan Task
	results   chan Result
	wg        sync.WaitGroup
	collectWg sync.WaitGroup
	process   func(context.Context, Task) Result

	mu        sync.Mutex
	collected []Result // unbounded, mutex-guarded output sink

	closeOnce sync.Once
	closed    chan struct{}
	stopOnce  sync.Once
	stop      chan struct{} // closed to ask workers to exit without touching queue
}

// NewPool validates bounds and starts the workers. The process function
// receives the pool context so a cancelled reader reaches in-flight work.
func NewPool(ctx context.Context, workers, queueDepth int, process func(context.Context, Task) Result) (*Pool, error) {
	if workers <= 0 || workers > MaxWorkers {
		return nil, errors.New("pipeline: worker count out of bounds")
	}
	if queueDepth <= 0 || queueDepth > MaxQueue {
		return nil, errors.New("pipeline: queue depth out of bounds")
	}
	if process == nil {
		return nil, errors.New("pipeline: nil process function")
	}
	p := &Pool{
		ctx:     ctx,
		queue:   make(chan Task, queueDepth),
		results: make(chan Result),
		process: process,
		closed:  make(chan struct{}),
		stop:    make(chan struct{}),
	}
	// Sole internal reader of the unbuffered results channel, alive for the
	// whole pool lifetime. Because it always reads, worker sends never block
	// on capacity; the bounded input queue stays the only back-pressure.
	p.collectWg.Add(1)
	go func() {
		defer p.collectWg.Done()
		for res := range p.results {
			p.mu.Lock()
			p.collected = append(p.collected, res)
			p.mu.Unlock()
		}
	}()
	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go p.worker()
	}
	return p, nil
}

// worker pulls until the context is done, Stop is called, or the input queue
// closes. Results go through emit, which hands each one to an internal
// collector goroutine reading an unbuffered channel; that goroutine runs
// until every worker has exited, so a result send can never deadlock against
// a bounded buffer — the input queue remains the sole back-pressure point.
func (p *Pool) worker() {
	defer p.wg.Done()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-p.stop:
			return
		case task, ok := <-p.queue:
			if !ok {
				return
			}
			res := p.process(p.ctx, task)
			if !p.emit(res) {
				return
			}
		}
	}
}

// emit delivers one result, returning false when shutdown means the result
// must be abandoned.
func (p *Pool) emit(res Result) bool {
	select {
	case p.results <- res:
		return true
	case <-p.ctx.Done():
		return false
	case <-p.stop:
		return false
	}
}

// Submit enqueues a task. It blocks while the queue is full (back-pressure)
// and returns promptly with ctx.Err when cancellation wins, so a reader
// stops feeding work instead of leaking tasks into a dying pipeline.
func (p *Pool) Submit(task Task) error {
	select {
	case <-p.ctx.Done():
		return p.ctx.Err()
	case <-p.stop:
		return ErrPoolClosed
	default:
	}
	select {
	case <-p.ctx.Done():
		return p.ctx.Err()
	case <-p.stop:
		return ErrPoolClosed
	case p.queue <- task:
		return nil
	}
}

// Close shuts the pool down and returns only after every worker has exited.
// After a cancellation (or an explicit Stop path) workers leave through the
// stop/context arms, so the possibly-full input queue is simply abandoned;
// in a graceful shutdown the queue is closed first so queued tasks drain.
// The results channel is closed last, once no worker can still send.
func (p *Pool) Close() {
	p.closeOnce.Do(func() {
		if p.ctx.Err() == nil {
			close(p.queue) // graceful: finish every queued task
		} else {
			p.stopOnce.Do(func() { close(p.stop) })
		}
		p.wg.Wait()
		close(p.results)
		p.collectWg.Wait() // every sent result is now in collected
		close(p.closed)
	})
}

// Closed is closed once Close has fully torn the pool down.
func (p *Pool) Closed() <-chan struct{} { return p.closed }

// Collected returns every result captured so far (all results after Close).
// The merge stage sorts this slice into the deterministic delivery order.
func (p *Pool) Collected() []Result {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Result, len(p.collected))
	copy(out, p.collected)
	return out
}
