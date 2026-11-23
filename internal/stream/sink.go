package stream

import (
	"context"
	"io"
	"time"

	"mediqc/internal/fault"
)

// Sink consumes chunks in order.
type Sink interface {
	Write(c Chunk) error
}

// SlowSink simulates a downstream consumer with per-chunk latency.
type SlowSink struct {
	Delay   time.Duration
	W       io.Writer // optional byte drain, may be nil
	Written int64
	Chunks  int
}

// Write sleeps for the configured delay, then records the chunk.
func (s *SlowSink) Write(c Chunk) error {
	if s.Delay > 0 {
		time.Sleep(s.Delay)
	}
	if s.W != nil {
		if _, err := s.W.Write(c.Payload); err != nil {
			return fault.New(fault.CodeSinkClosed, 0, -1,
				"sink write failed: "+err.Error(), "resume from the checkpoint")
		}
	}
	s.Written += int64(len(c.Payload))
	s.Chunks++
	return nil
}

// Drain pops chunks until the pipeline is closed and empty, writing each
// to sink. A sink failure closes the queue so producers stop promptly.
func Drain(ctx context.Context, p *Pipeline, sink Sink) error {
	for {
		c, ok := p.Pop(ctx)
		if !ok {
			if ctx.Err() != nil {
				return fault.New(fault.CodeCancelled, 0, -1,
					"drain cancelled", "checkpoint on disk holds confirmed progress")
			}
			return nil
		}
		if err := sink.Write(c); err != nil {
			p.Close(err)
			return err
		}
	}
}
