// Package pipeline wires the ingest, timeline and index subsystems together
// with bounded channels. When a downstream stage is slower the bounded queue
// blocks the producer; when the context is cancelled or a downstream stage
// fails, reading stops and a locatable error is returned.
package pipeline

import (
	"context"
	"fmt"
	"sync"

	"mediaroute/internal/model"
)

// Config bounds the pipeline. QueueCapacity is the number of fragments that
// may be in flight, and MaxBoxBytes bounds one retained fragment buffer. Peak
// retained media memory is therefore at most
// QueueCapacity * 2 * MaxBoxBytes (one moof and one mdat per in-flight
// fragment) plus the caller's input buffer.
type Config struct {
	QueueCapacity int
	MaxBoxBytes   int
}

// DefaultConfig returns the demo defaults.
func DefaultConfig() Config {
	return Config{QueueCapacity: 2, MaxBoxBytes: 8 << 20}
}

// Hard limits keep a malformed flag from allocating unbounded memory.
const (
	maxQueueCapacity = 1024
	maxBoxBytesLimit = 64 << 20
)

// Normalize fills zero fields with defaults and rejects out-of-range values.
func (c Config) Normalize() (Config, error) {
	d := DefaultConfig()
	if c.QueueCapacity <= 0 {
		c.QueueCapacity = d.QueueCapacity
	}
	if c.MaxBoxBytes <= 0 {
		c.MaxBoxBytes = d.MaxBoxBytes
	}
	if c.QueueCapacity > maxQueueCapacity {
		return c, model.NewTaskError(model.CategoryInvalidConfig, "pipeline",
			fmt.Sprintf("queue capacity %d exceeds %d", c.QueueCapacity, maxQueueCapacity))
	}
	if c.MaxBoxBytes > maxBoxBytesLimit {
		return c, model.NewTaskError(model.CategoryInvalidConfig, "pipeline",
			fmt.Sprintf("max box bytes %d exceeds %d", c.MaxBoxBytes, maxBoxBytesLimit))
	}
	if c.MaxBoxBytes < 1024 {
		return c, model.NewTaskError(model.CategoryInvalidConfig, "pipeline",
			fmt.Sprintf("max box bytes %d is below the 1 KiB minimum", c.MaxBoxBytes))
	}
	return c, nil
}

// MemoryBudget returns the upper bound, in bytes, of media buffers the
// pipeline may retain at once.
func (c Config) MemoryBudget() int64 {
	return int64(c.QueueCapacity) * 2 * int64(c.MaxBoxBytes)
}

// Stage is one processing step. Receive may block; returning an error stops
// every upstream producer. Sends must respect ctx cancellation.
type Stage struct {
	Name string
	Run  func(ctx context.Context, in <-chan model.Fragment, out chan<- model.Fragment) error
}

// stageError carries which stage failed so Run can attribute the error.
type stageError struct {
	stage string
	err   error
}

// Run executes stages in order over the supplied fragments and returns the
// fragments emitted by the last stage, in order. The first error, from any
// stage or the feeder, is returned as a TaskError after cancelling all work
// and draining every channel.
func Run(ctx context.Context, cfg Config, frags []model.Fragment, stages ...Stage) ([]model.Fragment, error) {
	cfg, err := cfg.Normalize()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	feed := make(chan model.Fragment, cfg.QueueCapacity)
	queues := make([]chan model.Fragment, len(stages))
	for i := range queues {
		queues[i] = make(chan model.Fragment, cfg.QueueCapacity)
	}
	var (
		wg    sync.WaitGroup
		errMu sync.Mutex
		first *stageError
	)
	record := func(se stageError) {
		errMu.Lock()
		if first == nil {
			cp := se
			first = &cp
		}
		errMu.Unlock()
	}

	// Feeder: the first backpressure point. A full feed blocks here.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(feed)
		for i := range frags {
			select {
			case feed <- frags[i]:
			case <-ctx.Done():
				record(stageError{"source", model.NewTaskError(model.CategoryCanceled, "pipeline",
					"context cancelled while feeding fragments")})
				return
			}
		}
	}()

	in := feed
	for i, st := range stages {
		// Capture per-iteration copies: the closures must not observe a later
		// reassignment of in, or a stage would read and write one channel.
		stageIn := in
		stageOut := queues[i]
		stage := st
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(stageOut)
			if err := stage.Run(ctx, stageIn, stageOut); err != nil {
				record(stageError{stage.Name, err})
				cancel()
			}
		}()
		in = stageOut
	}

	collected := make([]model.Fragment, 0, len(frags))
	collectDone := make(chan struct{})
	go func() {
		defer close(collectDone)
		for frag := range in {
			collected = append(collected, frag)
		}
	}()

	wg.Wait()
	<-collectDone
	if first != nil {
		if te, ok := model.AsTaskError(first.err); ok {
			return nil, te
		}
		return nil, model.WrapTaskError(model.CategoryDownstream, first.stage,
			"stage failed", first.err)
	}
	return collected, nil
}
