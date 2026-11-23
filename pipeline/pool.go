// Package pipeline implements the bounded worker pool, deterministic merge,
// and the production/consumption run loop with cancellation and resume.
package pipeline

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zld/media-check/media"
)

// Status enumerates the terminal state of one task.
type Status string

const (
	StatusOK        Status = "ok"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Job is one unit of segment work. It deliberately carries only references so
// the queue stays small regardless of segment size.
type Job struct {
	Index      int           `json:"index"`
	TrackID    uint32        `json:"track_id"`
	Segment    media.Segment `json:"segment"`
	Table      *media.SampleTable
	SourcePath string
}

// Result is the outcome of one job.
type Result struct {
	Index        int           `json:"index"`
	TrackID      uint32        `json:"track_id"`
	Segment      media.Segment `json:"segment"`
	Status       Status        `json:"status"`
	SampleCount  uint32        `json:"sample_count"`
	FirstSample  uint32        `json:"first_sample"`
	DTSStart     uint64        `json:"dts_start"`
	Duration     uint64        `json:"duration"`
	Bytes        int64         `json:"bytes"`
	SHA256       string        `json:"sha256"`
	ArtifactPath string        `json:"artifact_path,omitempty"`
	ArtifactSHA  string        `json:"artifact_sha256,omitempty"`
	Attempts     int           `json:"attempts"`
	Resumed      bool          `json:"resumed"`
	Elapsed      time.Duration `json:"-"`
	Err          error         `json:"-"`
	ErrClass     media.Class   `json:"error_class,omitempty"`
	ErrText      string        `json:"error,omitempty"`
}

// Handler runs one job; it must observe ctx and return promptly on cancel.
type Handler func(ctx context.Context, j Job) Result

// PoolStats reports pool accounting. Submitted always equals
// Completed+Failed+Cancelled after Close.
type PoolStats struct {
	Submitted int `json:"submitted"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`
	Workers   int `json:"workers"`
	QueueCap  int `json:"queue_capacity"`
	PeakQueue int `json:"peak_queue"`
}

// ErrPoolClosed is returned by Submit after Close.
var ErrPoolClosed = errors.New("pipeline: pool closed")

// Pool is a bounded worker pool with backpressure on Submit. Every accepted
// job yields exactly one Result, including jobs abandoned on cancellation.
//
// Close delivers the results of abandoned jobs itself, so the results channel
// must be drained concurrently while Close runs.
type Pool struct {
	ctx     context.Context
	cancel  context.CancelFunc
	jobs    chan Job
	results chan Result
	h       Handler
	workers int
	queue   int

	sendMu sync.Mutex // serialises Submit against Close's drain
	wg     sync.WaitGroup

	mu     sync.Mutex
	closed bool

	submitted int64
	completed int64
	failed    int64
	cancelled int64
	peakQueue int64
}

// NewPool starts workers immediately. queue is the channel capacity; when full,
// Submit blocks, which propagates backpressure to the producer.
func NewPool(parent context.Context, workers, queue int, h Handler) (*Pool, error) {
	if workers < 1 || workers > 4096 {
		return nil, media.NewError(media.ClassInvalidArgument, "pool", 0, "workers %d outside 1..4096", workers)
	}
	if queue < 1 || queue > 1<<20 {
		return nil, media.NewError(media.ClassInvalidArgument, "pool", 0, "queue %d outside 1..1048576", queue)
	}
	if h == nil {
		return nil, media.NewError(media.ClassInvalidArgument, "pool", 0, "nil handler")
	}
	ctx, cancel := context.WithCancel(parent)
	p := &Pool{
		ctx: ctx, cancel: cancel,
		jobs:    make(chan Job, queue),
		results: make(chan Result, workers+queue+1),
		h:       h, workers: workers, queue: queue,
	}
	p.wg.Add(workers)
	for i := 0; i < workers; i++ {
		go p.worker()
	}
	return p, nil
}

// worker drains the job queue until it is closed. Cancellation is decided per
// job: a context that is already done turns the job into a cancelled result
// without invoking the handler, so every accepted job still yields exactly one
// result and Close never has to synthesize results concurrently.
func (p *Pool) worker() {
	defer p.wg.Done()
	for j := range p.jobs {
		if p.ctx.Err() != nil {
			p.emit(ctxCancelled(j))
			continue
		}
		p.run(j)
	}
}

func ctxCancelled(j Job) Result {
	return Result{
		Index: j.Index, TrackID: j.TrackID, Segment: j.Segment,
		Status: StatusCancelled, Err: context.Canceled,
		ErrClass: media.ClassCancelled, ErrText: "cancelled before execution",
	}
}

func (p *Pool) run(j Job) {
	start := time.Now()
	res := p.h(p.ctx, j)
	res.Elapsed = time.Since(start)
	if res.Status == "" {
		res.Status = StatusOK
	}
	// A handler that returned success after the run was cancelled must not be
	// recorded as verified content.
	if p.ctx.Err() != nil && res.Status == StatusOK {
		res.Status = StatusCancelled
		res.Err = p.ctx.Err()
	}
	p.emit(res)
}

// emit accounts for and publishes one result.
func (p *Pool) emit(res Result) {
	if res.Err != nil {
		if res.ErrClass == "" {
			res.ErrClass = media.ClassOf(res.Err)
		}
		res.ErrText = res.Err.Error()
	}
	switch res.Status {
	case StatusOK:
		atomic.AddInt64(&p.completed, 1)
	case StatusFailed:
		atomic.AddInt64(&p.failed, 1)
	default:
		atomic.AddInt64(&p.cancelled, 1)
	}
	p.results <- res
}

// Submit hands a job to the pool. While the queue is full it blocks, which is
// the backpressure signal to the producer. It returns the context error if the
// run is being cancelled and ErrPoolClosed after Close.
func (p *Pool) Submit(j Job) error {
	p.sendMu.Lock()
	defer p.sendMu.Unlock()
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return ErrPoolClosed
	}
	select {
	case <-p.ctx.Done():
		return p.ctx.Err()
	case p.jobs <- j:
		atomic.AddInt64(&p.submitted, 1)
		if n := int64(len(p.jobs)); n > atomic.LoadInt64(&p.peakQueue) {
			atomic.StoreInt64(&p.peakQueue, n)
		}
		return nil
	}
}

// Results returns the result stream. It is closed after Close.
func (p *Pool) Results() <-chan Result { return p.results }

// Cancel signals every worker and blocked Submit to stop.
func (p *Pool) Cancel() { p.cancel() }

// Close stops intake, waits for workers to drain the queue, and closes the
// result channel. It is idempotent.
//
// Close does not cancel the context: on the success path every queued job still
// runs. Cancellation is an explicit Cancel (or a cancelled parent); queued work
// then yields cancelled results rather than being dropped.
func (p *Pool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()

	// Taking sendMu waits out any in-flight Submit, so closing jobs cannot race
	// with a send. A Submit blocked on a full queue returns once workers make
	// room or the context is cancelled; cancellation also unblocks it.
	p.sendMu.Lock()
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	close(p.jobs)
	p.sendMu.Unlock()

	// Workers drain every queued job (running it, or recording it cancelled)
	// before exiting, so no result has to be synthesized here.
	p.wg.Wait()
	close(p.results)
}

// Stats returns a consistent snapshot of the counters.
func (p *Pool) Stats() PoolStats {
	return PoolStats{
		Submitted: int(atomic.LoadInt64(&p.submitted)),
		Completed: int(atomic.LoadInt64(&p.completed)),
		Failed:    int(atomic.LoadInt64(&p.failed)),
		Cancelled: int(atomic.LoadInt64(&p.cancelled)),
		Workers:   p.workers,
		QueueCap:  p.queue,
		PeakQueue: int(atomic.LoadInt64(&p.peakQueue)),
	}
}
