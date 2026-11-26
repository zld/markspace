// Package pipe provides bounded, back-pressured sample delivery.
package pipe

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// Config bounds worker concurrency and the pending chunk queue.
type Config struct {
	Workers int `json:"workers"`
	Queue   int `json:"queue"`
}

// Normalize returns safe positive bounds. Queue may be zero for hand-off.
func (c Config) Normalize() Config {
	if c.Workers <= 0 {
		c.Workers = 1
	}
	if c.Queue < 0 {
		c.Queue = 0
	}
	return c
}

// MaxInFlight is the hard bound: queued chunks plus chunks held by workers.
func (c Config) MaxInFlight() int {
	c = c.Normalize()
	return c.Workers + c.Queue
}

// Chunk is one bounded unit of work. T is usually media.Sample.
type Chunk[T any] struct {
	Index   int    `json:"index"`
	TrackID uint32 `json:"track_id,omitempty"`
	Offset  int64  `json:"offset,omitempty"`
	Data    []byte `json:"-"`
	Items   []T    `json:"-"`
}

// Stats reports delivery counts and the observed high-water mark.
type Stats struct {
	Workers    int   `json:"workers"`
	Queue      int   `json:"queue"`
	Produced   int64 `json:"produced"`
	Processed  int64 `json:"processed"`
	PeakChunks int64 `json:"peak_chunks"`
}

// Produce builds chunk index i. Returning io.EOF before count ends early.
type Produce[T any] func(ctx context.Context, index int) (Chunk[T], error)

// Handle processes one chunk.
type Handle[T any] func(ctx context.Context, chunk Chunk[T]) error

// Run streams at most count chunks through workers. A semaphore of size
// workers+queue is acquired before producing a chunk, so the observed peak
// number of unfinished chunks can never exceed workers+queue. Cancellation or
// the first handler/producer error stops production and waits for workers to
// exit.
func Run[T any](parent context.Context, cfg Config, count int, produce Produce[T], handle Handle[T]) (Stats, error) {
	cfg = cfg.Normalize()
	stats := Stats{Workers: cfg.Workers, Queue: cfg.Queue}
	if count <= 0 || produce == nil || handle == nil {
		return stats, nil
	}

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	jobs := make(chan Chunk[T], cfg.Queue)
	slots := make(chan struct{}, cfg.MaxInFlight())
	var current, peak atomic.Int64
	var produced, processed atomic.Int64

	var errMu sync.Mutex
	var firstErr error
	setErr := func(err error) {
		if err == nil {
			return
		}
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		errMu.Unlock()
		cancel()
	}

	var wg sync.WaitGroup
	for w := 0; w < cfg.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case chunk, ok := <-jobs:
					if !ok {
						return
					}
					err := handle(ctx, chunk)
					<-slots
					current.Add(-1)
					if err != nil {
						setErr(err)
						return
					}
					processed.Add(1)
				}
			}
		}()
	}

	var runErr error
produceLoop:
	for i := 0; i < count; i++ {
		select {
		case <-ctx.Done():
			runErr = ctx.Err()
			break produceLoop
		case slots <- struct{}{}:
		}

		chunk, err := produce(ctx, i)
		if err != nil {
			<-slots
			if errors.Is(err, context.Canceled) {
				runErr = err
			} else {
				runErr = fmt.Errorf("pipe: produce chunk %d: %w", i, err)
			}
			setErr(runErr)
			break produceLoop
		}

		produced.Add(1)
		cur := current.Add(1)
		for {
			p := peak.Load()
			if cur <= p || peak.CompareAndSwap(p, cur) {
				break
			}
		}

		select {
		case <-ctx.Done():
			<-slots
			current.Add(-1)
			runErr = ctx.Err()
			break produceLoop
		case jobs <- chunk:
		}
	}

	close(jobs)
	wg.Wait()

	stats.Produced = produced.Load()
	stats.Processed = processed.Load()
	stats.PeakChunks = peak.Load()

	errMu.Lock()
	handlerErr := firstErr
	errMu.Unlock()
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return stats, runErr
	}
	if handlerErr != nil {
		return stats, handlerErr
	}
	if parent != nil && parent.Err() != nil {
		return stats, parent.Err()
	}
	return stats, nil
}
