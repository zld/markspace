package pipeline

import (
	"context"
	"sync"

	"github.com/example/tsrepair/internal/model"
)

// Item is the unit passed between stages. Seq orders items deterministically
// and Boundary marks a point from which recovery can safely resume.
type Item struct {
	Seq      int64
	Offset   int64
	Boundary bool
	Payload  any
}

// StageFunc processes one item. Returning an error fails the pipeline unless
// the error is recoverable and the runner was started in recovery mode, in
// which case the item is dropped and processing continues at the next item.
type StageFunc func(ctx context.Context, in Item) (Item, error)

// Stage binds a name and bounded queue to processing logic.
type Stage struct {
	Name string
	Run  StageFunc
}

// Stats reports pipeline behavior after Run returns.
type Stats struct {
	Processed     int
	Dropped       int
	Recovered     int
	MaxQueueDepth int
	StageErrors   []*model.StreamError
}

// Runner executes a sequence of stages connected by bounded queues. Each
// queue has exactly one writer pump, which closes the queue on exit; closure
// therefore cascades downstream and cancellation/failure unblocks every
// stage without leaking goroutines.
type Runner struct {
	queues    []*Queue
	stages    []Stage
	recover   bool
	onDropped func(Item)
	mu        sync.Mutex
	stats     Stats
	lastGood  int64 // highest confirmed boundary Seq
}

// NewRunner builds a runner. queueCapacity bounds every inter-stage queue.
func NewRunner(queueCapacity int, recover bool, stages ...Stage) *Runner {
	r := &Runner{stages: stages, recover: recover, lastGood: -1}
	for range stages {
		r.queues = append(r.queues, NewQueue(queueCapacity))
	}
	return r
}

// SetOnDropped registers a callback invoked for every item dropped in
// recovery mode, letting callers release resources reserved upstream.
func (r *Runner) SetOnDropped(fn func(Item)) { r.onDropped = fn }

// Run feeds items from source into the first stage and waits for completion.
// A recoverable stage fault drops the offending item and resumes at the next
// one; a fatal fault or internal error cancels every stage and is returned.
func (r *Runner) Run(ctx context.Context, source <-chan Item) (*Stats, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var fatalErr *model.StreamError
	var fatalMu sync.Mutex
	setFatal := func(err *model.StreamError) {
		fatalMu.Lock()
		if fatalErr == nil {
			fatalErr = err
		}
		fatalMu.Unlock()
		cancel()
	}
	// Source pump: sole writer of queues[0].
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer r.queues[0].Close()
		for {
			select {
			case <-ctx.Done():
				return
			case item, ok := <-source:
				if !ok {
					return
				}
				if !r.queues[0].Put(ctx, item) {
					return
				}
			}
		}
	}()
	// One pump per stage: sole writer of the next queue, closes it on exit.
	for idx, stage := range r.stages {
		wg.Add(1)
		go func(idx int, st Stage) {
			defer wg.Done()
			if idx < len(r.stages)-1 {
				defer r.queues[idx+1].Close()
			}
			for {
				raw, ok := r.queues[idx].Get(ctx)
				if !ok {
					return
				}
				out, err := st.Run(ctx, raw.(Item))
				if err != nil {
					se := model.AsStreamError(err)
					if se == nil {
						se = model.New(model.StagePipeline, model.SeverityInternal,
							"non-classified stage error").Wrap(err)
					}
					r.mu.Lock()
					r.stats.StageErrors = append(r.stats.StageErrors, se)
					r.mu.Unlock()
					if r.recover && se.Severity == model.SeverityRecoverable {
						r.markDropped()
						if r.onDropped != nil {
							r.onDropped(raw.(Item))
						}
						continue
					}
					setFatal(se)
					return
				}
				if idx == len(r.stages)-1 {
					r.confirm(out)
					continue
				}
				if !r.queues[idx+1].Put(ctx, out) {
					return
				}
			}
		}(idx, stage)
	}
	wg.Wait()
	for _, q := range r.queues {
		if d := q.MaxDepth(); d > r.stats.MaxQueueDepth {
			r.stats.MaxQueueDepth = d
		}
	}
	r.mu.Lock()
	stats := r.stats
	r.mu.Unlock()
	if fatalErr != nil {
		return &stats, fatalErr
	}
	return &stats, nil
}

// LastGoodBoundary returns the highest Seq confirmed at a boundary, enabling
// callers to resume without replaying confirmed items.
func (r *Runner) LastGoodBoundary() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastGood
}

func (r *Runner) confirm(item Item) {
	r.mu.Lock()
	r.stats.Processed++
	if item.Boundary && item.Seq > r.lastGood {
		r.lastGood = item.Seq
	}
	r.mu.Unlock()
}

func (r *Runner) markDropped() {
	r.mu.Lock()
	r.stats.Dropped++
	r.stats.Recovered++
	r.mu.Unlock()
}
