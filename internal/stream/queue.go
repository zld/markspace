// Package stream provides the bounded processing pipeline and backpressure queue.
package stream

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// ErrConsumer is returned when the consumer permanently rejects a block.
var ErrConsumer = errors.New("consumer failed")

// Block is one bounded unit of analysis/render work.
type Block struct {
	Index     int
	Key       string
	Data      []byte
	StagePath string
}

// Queue is a fixed-capacity block queue. Capacity two is the release default.
type Queue struct {
	ch     chan Block
	cap    int
	mu     sync.Mutex
	peak   int
	closed bool
}

func NewQueue(maxBlocks int) *Queue {
	if maxBlocks < 1 {
		maxBlocks = 2
	}
	return &Queue{ch: make(chan Block, maxBlocks), cap: maxBlocks}
}

func (q *Queue) Cap() int { return q.cap }

// Push applies backpressure when maxBlocks are already cached.
func (q *Queue) Push(ctx context.Context, b Block) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case q.ch <- b:
		q.mu.Lock()
		if n := len(q.ch); n > q.peak {
			q.peak = n
		}
		q.mu.Unlock()
		return nil
	}
}

// Pop waits for a block, cancellation, or queue closure.
func (q *Queue) Pop(ctx context.Context) (Block, bool, error) {
	select {
	case <-ctx.Done():
		return Block{}, false, ctx.Err()
	case b, ok := <-q.ch:
		if !ok {
			return Block{}, false, nil
		}
		return b, true, nil
	}
}

func (q *Queue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.closed {
		q.closed = true
		close(q.ch)
	}
}

// Peak returns the maximum observed cached block count.
func (q *Queue) Peak() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.peak
}

// Staging tracks unpublished temporary files for deterministic cleanup.
type Staging struct {
	Dir   string
	mu    sync.Mutex
	alive map[string]struct{}
}

// NewStaging creates a clean staging directory under parent.
func NewStaging(parent, prefix string) (*Staging, error) {
	if parent == "" {
		parent = os.TempDir()
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(parent, prefix)
	if err != nil {
		return nil, err
	}
	return &Staging{Dir: dir, alive: map[string]struct{}{}}, nil
}

// CreateFile creates a tracked temporary file in the staging directory.
func (s *Staging) CreateFile(name string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(filepath.Join(s.Dir, name)), 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(s.Dir, name)
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.alive[path] = struct{}{}
	s.mu.Unlock()
	return f, nil
}

// MarkPublished removes a path from the unpublished set after atomic commit.
func (s *Staging) MarkPublished(path string) {
	s.mu.Lock()
	delete(s.alive, path)
	s.mu.Unlock()
}

// Abort removes the entire staging tree and always returns the first error only.
func (s *Staging) Abort() error {
	if s == nil || s.Dir == "" {
		return nil
	}
	err := os.RemoveAll(s.Dir)
	s.mu.Lock()
	s.alive = map[string]struct{}{}
	s.mu.Unlock()
	return err
}
