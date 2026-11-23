// Package stream moves chunks through a bounded, byte-limited queue.
// A slow consumer triggers backpressure instead of dropping data; on
// cancellation or write failure the queue closes and the checkpoint
// left on disk stays resumable.
package stream

import (
	"context"
	"sync"

	"mediqc/internal/fault"
)

// Chunk is one unit of work on the wire.
type Chunk struct {
	Seq     int
	Payload []byte
}

// Pipeline is a blocking queue bounded by buffered bytes.
type Pipeline struct {
	mu        sync.Mutex
	cond      *sync.Cond
	queue     []Chunk
	curBytes  int64
	maxBytes  int64
	PeakBytes int64 // high-water mark of buffered bytes
	closed    bool
	err       error
}

// New builds a pipeline that buffers at most maxBytes.
func New(maxBytes int64) *Pipeline {
	p := &Pipeline{maxBytes: maxBytes}
	p.cond = sync.NewCond(&p.mu)
	return p
}

// Push enqueues c, blocking while the buffer is full (backpressure).
// It never drops data: it returns only after c is queued, the pipeline
// closes, or ctx is cancelled.
func (p *Pipeline) Push(ctx context.Context, c Chunk) error {
	size := int64(len(c.Payload))
	if size > p.maxBytes {
		return fault.New(fault.CodeFrameOverflow, 0, -1,
			"chunk larger than the buffer limit", "raise --max-buffer-bytes")
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			p.mu.Lock()
			p.cond.Broadcast()
			p.mu.Unlock()
		case <-done:
		}
	}()
	defer close(done)
	p.mu.Lock()
	defer p.mu.Unlock()
	for !p.closed && p.curBytes+size > p.maxBytes && ctx.Err() == nil {
		p.cond.Wait()
	}
	if ctx.Err() != nil {
		return fault.New(fault.CodeCancelled, 0, -1,
			"push cancelled", "checkpoint on disk holds confirmed progress")
	}
	if p.closed {
		return fault.New(fault.CodeSinkClosed, 0, -1,
			"pipeline closed: "+p.errText(), "resume from the checkpoint")
	}
	p.queue = append(p.queue, c)
	p.curBytes += size
	if p.curBytes > p.PeakBytes {
		p.PeakBytes = p.curBytes
	}
	p.cond.Broadcast()
	return nil
}

// Pop dequeues the next chunk, blocking while the pipeline is empty.
// The second return value is false once the pipeline is closed and drained.
func (p *Pipeline) Pop(ctx context.Context) (Chunk, bool) {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			p.mu.Lock()
			p.cond.Broadcast()
			p.mu.Unlock()
		case <-done:
		}
	}()
	defer close(done)
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.queue) == 0 && !p.closed && ctx.Err() == nil {
		p.cond.Wait()
	}
	if len(p.queue) == 0 {
		return Chunk{}, false
	}
	c := p.queue[0]
	p.queue = p.queue[1:]
	p.curBytes -= int64(len(c.Payload))
	p.cond.Broadcast()
	return c, true
}

// Close shuts the pipeline down; pending consumers drain what is queued,
// later pushes fail with E_SINK_CLOSED.
func (p *Pipeline) Close(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		p.err = err
		p.cond.Broadcast()
	}
}

func (p *Pipeline) errText() string {
	if p.err == nil {
		return "no error"
	}
	return p.err.Error()
}
