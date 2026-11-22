package stream

import (
	"context"
	"sync"

	"medpipe/timeline"
)

// Processor consumes one block and returns the block's output-grid interval.
// Implementations live in the CLI adapter and wrap audio.Pipeline.
type Processor func(ctx context.Context, b QueuedBlock) (timeline.Interval, error)

// Checkpointer persists per-track processing positions. Implementations must
// be safe for concurrent use across worker goroutines.
type Checkpointer interface {
	Load(trackID string) (timeline.Checkpoint, bool)
	Commit(cp timeline.Checkpoint) error
	All() []timeline.Checkpoint
}

// Runner binds a bounded queue to producer and consumer goroutines with
// coordinated cancellation and checkpointed recovery.
type Runner struct {
	queue  *BoundedQueue
	store  Checkpointer
	proc   Processor
	workers int

	wgProd sync.WaitGroup
	wgWork sync.WaitGroup

	mu       sync.Mutex
	firstErr *timeline.Error
	cancel   context.CancelFunc
}

// NewRunner validates the wiring before any goroutine starts.
func NewRunner(q *BoundedQueue, store Checkpointer, proc Processor, workers int) (*Runner, error) {
	if q == nil || proc == nil {
		return nil, timeline.NewError(timeline.ErrCodeIllegalBlockLength, timeline.StageRunner, "",
			"runner requires a queue and a processor")
	}
	if workers <= 0 {
		workers = 1
	}
	return &Runner{queue: q, store: store, proc: proc, workers: workers}, nil
}

// StartProducers launches the given producer functions. Each producer pushes
// into the bounded queue and therefore applies backpressure automatically.
func (r *Runner) StartProducers(ctx context.Context, producers ...func(context.Context) error) {
	ctx, r.cancel = context.WithCancel(ctx)
	for _, p := range producers {
		r.wgProd.Add(1)
		go func(fn func(context.Context) error) {
			defer r.wgProd.Done()
			if err := fn(ctx); err != nil {
				r.fail(err)
				r.cancel()
			}
		}(p)
	}
	go func() {
		r.wgProd.Wait()
		r.queue.Close() // consumers finish once every producer is done
	}()
}

// StartConsumers launches the worker pool. Every successfully processed block
// commits a checkpoint before the next block is taken, so a failure keeps all
// earlier checkpoints durable and a rerun can resume.
func (r *Runner) StartConsumers(ctx context.Context) {
	if r.cancel == nil {
		ctx, r.cancel = context.WithCancel(ctx)
	}
	for i := 0; i < r.workers; i++ {
		r.wgWork.Add(1)
		go func() {
			defer r.wgWork.Done()
			for {
				b, ok, err := r.queue.Pop(ctx)
				if err != nil {
					r.fail(err)
					return
				}
				if !ok {
					return // queue closed and drained
				}
				iv, err := r.proc(ctx, b)
				if err != nil {
					pe, ok := timeline.AsError(err)
					if !ok || pe.TrackID == "" {
						err = timeline.NewError(peCode(err), peStage(err), b.TrackID, "%v", err)
					}
					r.fail(err)
					r.cancel()
					return
				}
				if r.store != nil {
					cp := timeline.Checkpoint{
						TrackID: b.TrackID, BlockIndex: b.Sequence,
						Position: iv.End,
					}
					if err := r.store.Commit(cp); err != nil {
						r.fail(err)
						r.cancel()
						return
					}
				}
			}
		}()
	}
}

// Wait blocks until every goroutine has exited and returns the first failure.
// Cancellation propagates to all producers and consumers via the context.
func (r *Runner) Wait() error {
	r.wgProd.Wait()
	r.wgWork.Wait()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.firstErr != nil {
		return r.firstErr
	}
	return nil
}

// Cancel signals every goroutine to stop.
func (r *Runner) Cancel() {
	if r.cancel != nil {
		r.cancel()
	}
}

func (r *Runner) fail(err error) {
	pe, ok := timeline.AsError(err)
	if !ok {
		pe = timeline.NewError(timeline.ErrCodeCheckpoint, timeline.StageRunner, "", "%v", err)
	}
	r.mu.Lock()
	if r.firstErr == nil {
		r.firstErr = pe
	}
	r.mu.Unlock()
}

func peCode(err error) timeline.ErrorCode {
	if pe, ok := timeline.AsError(err); ok {
		return pe.Code
	}
	return timeline.ErrCodeCheckpoint
}

func peStage(err error) string {
	if pe, ok := timeline.AsError(err); ok {
		return pe.Stage
	}
	return timeline.StageRunner
}

// QueueStats exposes the bounded-memory counters for manifest/CLI output.
func (r *Runner) QueueStats() Stats { return r.queue.Stats() }

// ResumePosition returns the last checkpoint of a track; zero when none.
func (r *Runner) ResumePosition(trackID string) (timeline.Checkpoint, bool) {
	if r.store == nil {
		return timeline.Checkpoint{}, false
	}
	return r.store.Load(trackID)
}
