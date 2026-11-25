// Package stream provides a bounded in-memory queue connecting pipeline
// stages. Capacity is fixed at construction so a slow consumer can never grow
// memory without limit: a full queue blocks the producer (backpressure) until
// space frees up, and context cancellation releases every blocked reader or
// writer immediately.
package stream

import (
	"context"
	"sync"

	"mediad/internal/fault"
)

// Pipe is a bounded, context-aware FIFO queue of arbitrary chunk values.
type Pipe[T any] struct {
	mu      sync.Mutex
	cond    *sync.Cond
	items   []T
	cap     int
	peak    int
	closed  bool
	abort   error
	pushes  int64
	blocked int
}

// NewPipe creates a queue holding at most capacity chunks.
func NewPipe[T any](capacity int) *Pipe[T] {
	if capacity < 1 {
		capacity = 1
	}
	p := &Pipe[T]{cap: capacity}
	p.cond = sync.NewCond(&p.mu)
	return p
}

// Push appends a chunk, blocking while the queue is full. Cancellation or a
// peer-side CloseWithError aborts the call with a fault error.
func (p *Pipe[T]) Push(ctx context.Context, v T) error {
	p.mu.Lock()
	p.blocked++
	for len(p.items) >= p.cap && p.abort == nil && !p.closed {
		if err := p.wait(ctx); err != nil {
			p.blocked--
			p.mu.Unlock()
			return err
		}
	}
	p.blocked--
	if p.abort != nil {
		p.mu.Unlock()
		return p.abort
	}
	if p.closed {
		p.mu.Unlock()
		return fault.New(fault.CatCanceled, "stream.push", true,
			"push on closed pipe")
	}
	p.items = append(p.items, v)
	p.pushes++
	if len(p.items) > p.peak {
		p.peak = len(p.items)
	}
	p.cond.Broadcast()
	p.mu.Unlock()
	return nil
}

// Pop removes the oldest chunk, blocking while the queue is empty. It returns
// io.EOF once the pipe is drained and closed.
func (p *Pipe[T]) Pop(ctx context.Context) (T, error) {
	var zero T
	p.mu.Lock()
	for len(p.items) == 0 && !p.closed && p.abort == nil {
		if err := p.wait(ctx); err != nil {
			p.mu.Unlock()
			return zero, err
		}
	}
	if len(p.items) > 0 {
		v := p.items[0]
		p.items = p.items[1:]
		p.cond.Broadcast()
		p.mu.Unlock()
		return v, nil
	}
	if p.abort != nil {
		p.mu.Unlock()
		return zero, p.abort
	}
	p.mu.Unlock()
	return zero, errEOF
}

// wait suspends the caller until Broadcast or context completion. The caller
// must hold p.mu; we temporarily release it via a watcher goroutine.
func (p *Pipe[T]) wait(ctx context.Context) error {
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
	p.cond.Wait()
	close(done)
	if ctx.Err() != nil {
		return fault.Wrap(fault.CatCanceled, "stream.wait", -1, -1, true, ctx.Err())
	}
	return nil
}

// Close signals normal completion. Chunks already queued remain readable;
// once drained, Pop returns io.EOF.
func (p *Pipe[T]) Close() {
	p.mu.Lock()
	p.closed = true
	p.cond.Broadcast()
	p.mu.Unlock()
}

// CloseWithError aborts every blocked and future operation. It is used for
// internal failures so downstream stages do not hang forever.
func (p *Pipe[T]) CloseWithError(err error) {
	p.mu.Lock()
	if p.abort == nil {
		if fe, ok := fault.As(err); ok {
			p.abort = fe
		} else {
			p.abort = fault.Wrap(fault.CatWrite, "stream.abort", -1, -1, true, err)
		}
	}
	p.cond.Broadcast()
	p.mu.Unlock()
}

// Stats is a point-in-time snapshot of queue counters.
type Stats struct {
	Queued        int
	PeakQueued    int
	TotalPushes   int64
	BlockedWaiters int
}

// Snapshot returns the current occupancy and historical peak.
func (p *Pipe[T]) Snapshot() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Stats{
		Queued:         len(p.items),
		PeakQueued:     p.peak,
		TotalPushes:    p.pushes,
		BlockedWaiters: p.blocked,
	}
}
