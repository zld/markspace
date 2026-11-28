// Package stream provides a bounded, cancelable pipeline with backpressure.
//
// Every queue between stages holds at most the configured number of items, so a
// slow downstream stage makes the upstream stage block instead of growing
// memory. The first error or cancellation travels through the whole pipeline:
// every stage stops, its input and output channels are closed, and Wait returns
// the original cause.
package stream

import (
	"context"
	"fmt"
	"sync"

	"mediarc/model"
)

const streamName = "stream"

// Item is the unit of work flowing through the pipeline. Data is owned by the
// receiver once the item is accepted.
type Item struct {
	// Seq is a monotonically increasing source position.
	Seq uint64
	// Kind labels the item for downstream stages.
	Kind string
	// Data is the payload.
	Data []byte
}

// StageFunc consumes one input channel and produces into one output channel.
// It must return when in is closed, when the context is done, or on its own
// error; it must not close out, which the pipeline owns.
type StageFunc func(ctx context.Context, in <-chan Item, out chan<- Item) error

// Config bounds the pipeline.
type Config struct {
	// BufferSize is the capacity of every inter-stage queue. Must be positive.
	BufferSize int
	// Name is used in errors.
	Name string
}

func (c Config) validate() (Config, error) {
	if c.BufferSize <= 0 {
		return Config{}, model.New(model.CodeInvalidArgument, streamName,
			fmt.Sprintf("buffer size must be positive, got %d", c.BufferSize))
	}
	if c.Name == "" {
		c.Name = "pipeline"
	}
	return c, nil
}

// Pipeline is a chain of stages with bounded queues between them.
type Pipeline struct {
	cfg    Config
	ctx    context.Context
	cancel context.CancelFunc

	input chan Item
	out   chan Item

	// sendMu serialises producers with CloseInput so that a send can never
	// race with closing the input channel. Producers hold the read lock;
	// CloseInput and Cancel take the write lock.
	sendMu    sync.RWMutex
	closed    bool
	closeOnce sync.Once
	errOnce   sync.Once
	err       error

	wg sync.WaitGroup
}

// NewPipeline validates cfg and wires stages between a caller-fed input queue
// and an output queue. With no stages the pipeline is a pass-through.
func NewPipeline(parent context.Context, cfg Config, stages ...StageFunc) (*Pipeline, error) {
	validated, err := cfg.validate()
	if err != nil {
		return nil, err
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	p := &Pipeline{
		cfg:    validated,
		ctx:    ctx,
		cancel: cancel,
		input:  make(chan Item, validated.BufferSize),
	}
	// Chain the channels: input -> stage0 -> stage1 -> ... -> out.
	in := p.input
	for _, stage := range stages {
		out := make(chan Item, validated.BufferSize)
		p.wg.Add(1)
		go p.runStage(stage, in, out)
		in = out
	}
	p.out = in
	return p, nil
}

// Cap reports the per-queue capacity.
func (p *Pipeline) Cap() int { return p.cfg.BufferSize }

// Output is the final stage's channel. It is closed once every stage returns.
func (p *Pipeline) Output() <-chan Item { return p.out }

// Err returns the first error that stopped the pipeline, or nil while it is
// still running cleanly.
func (p *Pipeline) Err() error {
	return p.err
}

// Buffered reports how many items are queued in the input and output channels.
func (p *Pipeline) Buffered() (input, output int) {
	return len(p.input), len(p.out)
}
