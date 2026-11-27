// Package pipeline connects processing stages with bounded, blocking
// channels. When a downstream stage is slow, a full queue makes the upstream
// stage wait, and every blocking send or receive also honors cancellation.
// Frame-count and total-byte limits stop the current task with a structured
// limit fault instead of growing memory without bound.
package pipeline

import (
	"context"

	"example.com/continuity/internal/fault"
	"example.com/continuity/internal/frame"
)

// Queue is a bounded, cancellation-aware channel of frame records.
type Queue struct {
	ch chan *frame.Record
}

// NewQueue creates a queue with the given capacity (must be positive).
func NewQueue(capacity int) *Queue {
	if capacity <= 0 {
		capacity = 1
	}
	return &Queue{ch: make(chan *frame.Record, capacity)}
}

// Put blocks until there is room, the queue is closed, or ctx is canceled.
func (q *Queue) Put(ctx context.Context, rec *frame.Record) error {
	select {
	case q.ch <- rec:
		return nil
	case <-ctx.Done():
		return fault.New(fault.StagePipeline, "enqueue", ctx.Err())
	}
}

// Get blocks until a record is available, the channel drains and closes, or
// ctx is canceled.
func (q *Queue) Get(ctx context.Context) (*frame.Record, error) {
	select {
	case rec, ok := <-q.ch:
		if !ok {
			return nil, errQueueClosed
		}
		return rec, nil
	case <-ctx.Done():
		return nil, fault.New(fault.StagePipeline, "dequeue", ctx.Err())
	}
}

// Close signals end of input.
func (q *Queue) Close() { close(q.ch) }

// Len reports the current queue depth (for diagnostics and tests).
func (q *Queue) Len() int { return len(q.ch) }

// Cap reports the queue capacity.
func (q *Queue) Cap() int { return cap(q.ch) }

type closedErr struct{}

func (closedErr) Error() string { return "pipeline: queue closed" }

var errQueueClosed = closedErr{}

// IsClosed reports whether err is the clean end-of-queue signal.
func IsClosed(err error) bool { return err == errQueueClosed }

// Counters enforce the configured frame and byte budgets.
type Counters struct {
	MaxFrames     int
	MaxTotalBytes int64
	frames        int
	bytes         int64
}

// Add accounts for one accepted frame, enforcing both budgets.
func (c *Counters) Add(rec *frame.Record) error {
	if c.MaxFrames > 0 && c.frames >= c.MaxFrames {
		return fault.New(fault.StagePipeline, "frame-limit", fault.ErrLimitExceeded).
			AtFrame(int64(rec.Seq)).AtOffset(rec.Offset)
	}
	next := c.bytes + int64(len(rec.Payload))
	if c.MaxTotalBytes > 0 && next > c.MaxTotalBytes {
		return fault.New(fault.StagePipeline, "byte-limit", fault.ErrLimitExceeded).
			AtFrame(int64(rec.Seq)).AtOffset(rec.Offset)
	}
	c.frames++
	c.bytes = next
	return nil
}

// Frames reports the number of accounted frames.
func (c *Counters) Frames() int { return c.frames }

// Bytes reports the number of accounted payload bytes.
func (c *Counters) Bytes() int64 { return c.bytes }

// Stage is one processing step connected by queues.
type Stage func(ctx context.Context, in, out *Queue) error

// Run wires stages together with a queue between every adjacent pair and
// runs them concurrently. The input queue is fed by the caller via feed.
// Cancellation of ctx stops every stage; the first stage error is returned.
func Run(ctx context.Context, capacity int, feed func(ctx context.Context, in *Queue) error,
	stages ...Stage) error {
	if len(stages) == 0 {
		return fault.New(fault.StagePipeline, "run", errNoStages)
	}
	queues := make([]*Queue, len(stages)+1)
	for i := range queues {
		queues[i] = NewQueue(capacity)
	}
	errCh := make(chan error, len(stages)+1)

	go func() {
		err := feed(ctx, queues[0])
		queues[0].Close()
		errCh <- err
	}()
	for i, stage := range stages {
		i, stage := i, stage
		go func() {
			err := stage(ctx, queues[i], queues[i+1])
			queues[i+1].Close()
			errCh <- err
		}()
	}
	// Collect one result per goroutine; return the first real failure.
	var firstErr error
	for i := 0; i < len(stages)+1; i++ {
		if err := <-errCh; err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

type noStagesErr struct{}

func (noStagesErr) Error() string { return "pipeline: no stages configured" }

var errNoStages = noStagesErr{}
