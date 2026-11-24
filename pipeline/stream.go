// Package pipeline provides bounded, memory-accounted channels and the local
// verification orchestration built on top of the media kernel packages.
package pipeline

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"example.com/media-kernel/media"
)

// Config configures a bounded stream.
type Config struct {
	// QueueCapacity is the maximum number of queued envelopes.
	QueueCapacity int `json:"queue_capacity"`
	// MaxMemoryBytes caps total acquired, un-released envelope payload bytes.
	MaxMemoryBytes int64 `json:"max_memory_bytes"`
}

// Gate is a mutex/condition-based byte memory budget.
type Gate struct {
	mu    sync.Mutex
	cond  *sync.Cond
	limit int64
	used  int64
	peak  int64
}

// NewGate creates a memory gate with limitBytes.
func NewGate(limitBytes int64) *Gate {
	g := &Gate{limit: limitBytes}
	g.cond = sync.NewCond(&g.mu)
	return g
}

// Acquire reserves n bytes, blocking until budget is available. A single item
// larger than the limit is rejected rather than deadlocking the pipeline.
func (g *Gate) Acquire(ctx context.Context, n int64) error {
	if n < 0 {
		return media.NewError("pipeline", media.CodeInvalidArgument, "negative memory reservation", nil)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if n > g.limit {
		return media.NewError("pipeline", media.CodeMemoryLimit,
			fmt.Sprintf("item size %d exceeds max memory %d", n, g.limit), nil)
	}
	stop := context.AfterFunc(ctx, g.cond.Broadcast)
	defer stop()
	for g.used+n > g.limit {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		g.cond.Wait()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	g.used += n
	if g.used > g.peak {
		g.peak = g.used
	}
	return nil
}

// Release returns n bytes to the budget.
func (g *Gate) Release(n int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.used -= n
	if g.used < 0 {
		g.used = 0
	}
	g.cond.Broadcast()
}

// Stats returns current, peak and limit tracked bytes.
func (g *Gate) Stats() (used, peak, limit int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.used, g.peak, g.limit
}

// Envelope is a queued value with its memory reservation.
type Envelope[T any] struct {
	Value  T
	size   int64
	stream *Stream[T]
	once   sync.Once
}

// Done releases the envelope's memory reservation exactly once.
func (e *Envelope[T]) Done() {
	e.once.Do(func() { e.stream.gate.Release(e.size) })
}

// Stream is a bounded generic queue with backpressure and byte accounting.
type Stream[T any] struct {
	ch   chan *Envelope[T]
	gate *Gate
	cfg  Config
}

// NewStream constructs a bounded stream.
func NewStream[T any](cfg Config) *Stream[T] {
	if cfg.QueueCapacity <= 0 {
		cfg.QueueCapacity = 16
	}
	if cfg.MaxMemoryBytes <= 0 {
		cfg.MaxMemoryBytes = 8 << 20
	}
	return &Stream[T]{
		ch:   make(chan *Envelope[T], cfg.QueueCapacity),
		gate: NewGate(cfg.MaxMemoryBytes),
		cfg:  cfg,
	}
}

// Push reserves size bytes and enqueues value. It blocks when the bounded
// channel is full or the byte budget is exhausted, thereby applying
// backpressure to the upstream producer.
func (s *Stream[T]) Push(ctx context.Context, value T, size int64) error {
	if err := s.gate.Acquire(ctx, size); err != nil {
		return err
	}
	env := &Envelope[T]{Value: value, size: size, stream: s}
	select {
	case s.ch <- env:
		return nil
	case <-ctx.Done():
		env.Done()
		return ctx.Err()
	}
}

// Items returns the receive-only bounded channel.
func (s *Stream[T]) Items() <-chan *Envelope[T] { return s.ch }

// Close closes the queue after all producers have finished pushing.
func (s *Stream[T]) Close() { close(s.ch) }

// Stats returns memory accounting statistics.
func (s *Stream[T]) Stats() (used, peak, limit int64) { return s.gate.Stats() }

// ParseSize parses sizes such as 8MiB, 64KiB, 1MB and plain bytes.
func ParseSize(v string) (int64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, media.NewError("pipeline", media.CodeInvalidArgument, "empty size", nil)
	}
	units := []struct {
		suffix string
		mult   int64
	}{
		{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
		{"GB", 1_000_000_000}, {"MB", 1_000_000}, {"KB", 1000},
		{"B", 1},
	}
	for _, u := range units {
		if strings.HasSuffix(v, u.suffix) {
			n, err := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(v, u.suffix)), 10, 64)
			if err != nil || n < 0 {
				return 0, media.NewError("pipeline", media.CodeInvalidArgument, "invalid size "+v, err)
			}
			return n * u.mult, nil
		}
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, media.NewError("pipeline", media.CodeInvalidArgument, "invalid byte size "+v, err)
	}
	return n, nil
}
