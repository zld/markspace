// Package stream provides bounded pipeline queues, backpressure and resume.
package stream

import (
	"context"
	"errors"
	"sync"

	"example.com/archiveverify/internal/model"
)

// Item is one unit of pipeline work. Size is its accounted cache footprint.
type Item struct {
	ID      int
	Size    int64
	Payload any
}

// Options bounds queue length and accounted cache memory.
type Options struct {
	Queue       int
	MemoryBytes int64
	Workers     int
}

// Stats reports observed resource use and backpressure.
type Stats struct {
	Processed          int   `json:"processed"`
	PeakMemoryBytes    int64 `json:"peak_memory_bytes"`
	BackpressureEvents int64 `json:"backpressure_events"`
}

// Pipe is a bounded producer/worker pipeline.
type Pipe struct {
	opts Options
}

// NewPipe constructs a pipe with safe defaults.
func NewPipe(opts Options) *Pipe {
	if opts.Queue <= 0 {
		opts.Queue = 1
	}
	if opts.MemoryBytes <= 0 {
		opts.MemoryBytes = 8 << 20
	}
	if opts.Workers <= 0 {
		opts.Workers = 1
	}
	return &Pipe{opts: opts}
}

type runState struct {
	mu        sync.Mutex
	cond      *sync.Cond
	used      int64
	peak      int64
	backpress int64
	processed int
}

func newRunState() *runState {
	s := &runState{}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// Run feeds items through a bounded queue and one or more workers. When the
// queue or memory budget is full the producer blocks, providing backpressure.
// Cancellation uses one cause for producer and workers.
func (p *Pipe) Run(parent context.Context, items []Item, handle func(context.Context, Item) error) (Stats, error) {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	stopWatch := context.AfterFunc(parent, func() {
		cause := context.Cause(parent)
		if errors.Is(cause, context.Canceled) {
			cause = errors.Join(model.ErrCanceled, context.Canceled)
		}
		cancel(cause)
	})
	defer stopWatch()

	jobs := make(chan Item, p.opts.Queue)
	state := newRunState()
	var once sync.Once
	var firstErr error
	fail := func(err error) {
		if err == nil {
			return
		}
		once.Do(func() { firstErr = err })
		cancel(err)
	}

	var wg sync.WaitGroup
	for w := 0; w < p.opts.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					fail(context.Cause(ctx))
					return
				case item, ok := <-jobs:
					if !ok {
						return
					}
					if err := handle(ctx, item); err != nil {
						fail(err)
						return
					}
					state.mu.Lock()
					state.processed++
					state.used -= item.Size
					if state.used < 0 {
						state.used = 0
					}
					state.cond.Broadcast()
					state.mu.Unlock()
				}
			}
		}()
	}

	producerErr := p.produce(ctx, jobs, state, items)
	if producerErr != nil {
		fail(producerErr)
	}
	close(jobs)
	wg.Wait()

	st := Stats{
		Processed:          state.processed,
		PeakMemoryBytes:    state.peak,
		BackpressureEvents: state.backpress,
	}
	if firstErr != nil {
		return st, context.Cause(ctx)
	}
	if err := context.Cause(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return st, err
	}
	return st, nil
}

func (p *Pipe) produce(ctx context.Context, jobs chan<- Item, st *runState, items []Item) error {
	stop := context.AfterFunc(ctx, func() {
		st.mu.Lock()
		st.cond.Broadcast()
		st.mu.Unlock()
	})
	defer stop()

	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return context.Cause(ctx)
		}
		if item.Size > p.opts.MemoryBytes {
			me := &model.MediaError{Code: model.CodeLimit, Err: errors.Join(model.ErrResourceLimit,
				errors.New("item exceeds memory budget"))}
			return me
		}

		st.mu.Lock()
		waited := false
		for st.used+item.Size > p.opts.MemoryBytes {
			waited = true
			st.cond.Wait()
			if err := context.Cause(ctx); err != nil {
				st.mu.Unlock()
				return err
			}
		}
		st.used += item.Size
		if st.used > st.peak {
			st.peak = st.used
		}
		if waited {
			st.backpress++
		}
		st.mu.Unlock()

		select {
		case jobs <- item:
		default:
			st.mu.Lock()
			st.backpress++
			st.mu.Unlock()
			select {
			case jobs <- item:
			case <-ctx.Done():
				st.mu.Lock()
				st.used -= item.Size
				if st.used < 0 {
					st.used = 0
				}
				st.cond.Broadcast()
				st.mu.Unlock()
				return context.Cause(ctx)
			}
		}
	}
	return nil
}
