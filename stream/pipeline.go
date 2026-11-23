package stream

import (
	"context"
	"errors"
	"sync"
)

// Checkpoint is the last fully completed frame position. Recovery resumes at
// NextIndex so completed frames are never submitted twice.
type Checkpoint struct {
	NextIndex int64 `json:"next_index"`
	TimeNS    int64 `json:"time_ns"`
	Completed int64 `json:"completed"`
}

// CheckpointStore persists checkpoints durably enough for process restart.
type CheckpointStore interface {
	Load() (Checkpoint, bool, error)
	Save(cp Checkpoint) error
}

// MemoryStore is the in-process store used by tests.
type MemoryStore struct {
	mu sync.Mutex
	cp Checkpoint
	ok bool
}

func (m *MemoryStore) Load() (Checkpoint, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cp, m.ok, nil
}

func (m *MemoryStore) Save(cp Checkpoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cp, m.ok = cp, true
	return nil
}

// Consumer processes one sample; returning an error fails the pipeline.
type Consumer func(ctx context.Context, s Sample) error

// Pipeline drives a bounded queue with checkpoint-aware replay.
type Pipeline struct {
	queue    *BoundedQueue
	store    CheckpointStore
	slowHook func() // test hook: invoked when Enqueue blocks (backpressure)
	mu       sync.Mutex
	blocked  bool
	canceled bool
}

// NewPipeline wires a queue and checkpoint store.
func NewPipeline(q *BoundedQueue, store CheckpointStore) *Pipeline {
	if store == nil {
		store = &MemoryStore{}
	}
	return &Pipeline{queue: q, store: store}
}

// SetBlockedHook registers a callback fired once each time Enqueue blocks.
func (p *Pipeline) SetBlockedHook(fn func()) { p.slowHook = fn }

// ResumePoint returns the checkpoint to restart from.
func (p *Pipeline) ResumePoint() (Checkpoint, bool, error) { return p.store.Load() }

// Run starts the producer goroutine and consumes until the queue is closed
// and drained, ctx is canceled, or the consumer returns an error. On cancel it
// flushes the last complete checkpoint and reports ctx.Canceled.
func (p *Pipeline) Run(ctx context.Context, producer func(ctx context.Context, q *BoundedQueue) error, consume Consumer) (Checkpoint, error) {
	cp, _, err := p.store.Load()
	if err != nil {
		return cp, err
	}
	prodErr := make(chan error, 1)
	go func() {
		prodErr <- producer(ctx, p.queue)
	}()
	for {
		s, ok, err := p.queue.Dequeue(ctx)
		if err != nil {
			p.markCanceled()
			return cp, err
		}
		if !ok {
			break // closed and drained
		}
		// Replay guard: samples below the checkpoint are already complete.
		if s.Index < cp.NextIndex {
			continue
		}
		if err := consume(ctx, s); err != nil {
			return cp, err
		}
		cp = Checkpoint{NextIndex: s.Index + 1, TimeNS: s.TimeNS, Completed: s.Index + 1}
		if err := p.store.Save(cp); err != nil {
			return cp, err
		}
	}
	if err := <-prodErr; err != nil && !errors.Is(err, context.Canceled) {
		return cp, err
	}
	return cp, nil
}

// Produce enqueues with backpressure instrumentation for slow consumers.
func (p *Pipeline) Produce(ctx context.Context, s Sample) error {
	if p.queue.Len() == p.queue.Cap() {
		p.mu.Lock()
		if !p.blocked {
			p.blocked = true
			if p.slowHook != nil {
				p.slowHook()
			}
		}
		p.mu.Unlock()
	}
	err := p.queue.Enqueue(ctx, s)
	if err == nil {
		p.mu.Lock()
		p.blocked = false
		p.mu.Unlock()
	}
	return err
}

func (p *Pipeline) markCanceled() {
	p.mu.Lock()
	p.canceled = true
	p.mu.Unlock()
}

// Canceled reports whether Run exited via context cancellation.
func (p *Pipeline) Canceled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.canceled
}
