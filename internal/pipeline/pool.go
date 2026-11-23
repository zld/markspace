// Package pipeline provides the bounded worker pool that runs normalization
// jobs and the deterministic merger that orders recovered events.
package pipeline

import (
	"context"
	"sync"
	"time"

	"mediaarchive/internal/timeline"
)

// Hard caps keep memory bounded regardless of caller-supplied config.
const (
	MaxWorkers = 64
	MaxQueue   = 1 << 16
	// drainTimeout bounds how long Run waits for a producer to close the
	// jobs channel after the workers have stopped.
	drainTimeout = 2 * time.Second
)

// PoolConfig sizes a worker pool.
type PoolConfig struct {
	Workers int
	Queue   int
}

// DefaultPoolConfig returns a modest 4-worker / 32-slot pool.
func DefaultPoolConfig() PoolConfig { return PoolConfig{Workers: 4, Queue: 32} }

func (c PoolConfig) clamped() PoolConfig {
	if c.Workers < 1 {
		c.Workers = 1
	}
	if c.Workers > MaxWorkers {
		c.Workers = MaxWorkers
	}
	if c.Queue < 1 {
		c.Queue = 1
	}
	if c.Queue > MaxQueue {
		c.Queue = MaxQueue
	}
	return c
}

// Job is one unit of normalization work.
type Job func(ctx context.Context) error

// Pool runs jobs with a fixed worker count and bounded in-memory queue.
type Pool struct {
	cfg PoolConfig
}

// NewPool constructs a pool, clamping the configuration to the hard caps.
func NewPool(cfg PoolConfig) *Pool {
	return &Pool{cfg: cfg.clamped()}
}

// Config reports the effective (post-clamp) configuration.
func (p *Pool) Config() PoolConfig { return p.cfg }

// Run consumes jobs until the channel closes, ctx is cancelled, or a job
// returns an error. On failure or cancellation it stops dispatching, drains
// the queued jobs so producers never block, and returns the first root-cause
// error (cancellation surfaces as timeline.ErrCancelled).
func (p *Pool) Run(ctx context.Context, jobs <-chan Job) error {
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
	)
	record := func(err error) {
		once.Do(func() { firstErr = err })
		cancel()
	}

	wg.Add(p.cfg.Workers)
	for i := 0; i < p.cfg.Workers; i++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-workerCtx.Done():
					// After cancellation, keep draining queued jobs without
					// executing them, so no accepted work is lost and a
					// producer feeding the bounded queue cannot deadlock.
					return
				case job, ok := <-jobs:
					if !ok {
						return
					}
					if workerCtx.Err() != nil {
						continue
					}
					if err := job(workerCtx); err != nil {
						record(err)
						return
					}
				}
			}
		}()
	}

	// Wait for every worker to exit first, so the drain never competes with
	// healthy workers for jobs. Only then release whatever remains queued so
	// a blocked producer can unwind instead of deadlocking.
	wg.Wait()
	cancel()

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for range jobs {
		}
	}()
	select {
	case <-stopped:
	case <-time.After(drainTimeout):
	}

	if firstErr != nil {
		return firstErr
	}
	if ctx.Err() != nil {
		return timeline.NewError("pipeline", "run", -1, timeline.RecoverNone,
			ctx.Err().Error(), timeline.ErrCancelled)
	}
	return nil
}

// Feed publishes jobs into the supplied channel, honoring cancellation. It
// returns false when the context ended before every job was queued.
func Feed(ctx context.Context, queue chan<- Job, jobs []Job) bool {
	for _, j := range jobs {
		select {
		case <-ctx.Done():
			return false
		case queue <- j:
		}
	}
	return true
}
