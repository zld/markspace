// Package stream provides the bounded, cancellable delivery stage between
// audio conversion and cache/checkpoint persistence.
//
// A Pipeline owns a fixed-capacity queue measured in both blocks and bytes.
// Producers block once either limit is reached, so a slow consumer throttles
// its upstream instead of growing memory without bound. Cancellation and
// timeout stop intake immediately and leave the last fully committed block
// position intact for the checkpoint stage.
package stream

import (
	"context"
	"sync"

	"github.com/zld/media-regress/internal/error"
)

// Item is one block in flight.
type Item struct {
	Index        int
	TrackID      uint32
	Timestamp    uint32 // RTP timestamp of the block's first sample
	Sequence     uint16 // RTP sequence number assigned on send
	Payload      []byte
	SourceFrames int64
}

// Size reports the payload size accounted against the byte limit.
func (i Item) Size() int { return len(i.Payload) }

// Config bounds the pipeline.
type Config struct {
	MaxBlocks int
	MaxBytes  int
}

// Validate checks the limits.
func (c Config) Validate() error {
	if c.MaxBlocks <= 0 {
		return merr.New(merr.StageStream, merr.KindLimit, 0, 0, "max blocks must be positive")
	}
	if c.MaxBytes <= 0 {
		return merr.New(merr.StageStream, merr.KindLimit, 0, 0, "max bytes must be positive")
	}
	return nil
}

// Stats reports observed queue occupancy.
type Stats struct {
	Enqueued        int
	Dequeued        int
	QueuePeakBlocks int
	QueuePeakBytes  int
	BlockedPushes   int
}

// Queue is the bounded block queue.
type Queue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	cfg    Config
	items  []Item
	bytes  int
	closed bool
	stats  Stats
	// lastCommitted is the highest index handed to the consumer, or -1.
	lastCommitted int
}

// NewQueue builds an empty bounded queue.
func NewQueue(cfg Config) (*Queue, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	q := &Queue{cfg: cfg, lastCommitted: -1}
	q.cond = sync.NewCond(&q.mu)
	return q, nil
}

// fits reports whether item can be admitted; the caller holds the lock.
func (q *Queue) fits(item Item) bool {
	return len(q.items) < q.cfg.MaxBlocks && q.bytes+item.Size() <= q.cfg.MaxBytes
}

// Push blocks until there is room, the context is done, or the queue closes.
// The item and its byte accounting are updated under one lock so the byte peak
// is never observed half-updated.
func (q *Queue) Push(ctx context.Context, item Item) error {
	q.mu.Lock()
	for {
		if q.closed {
			q.mu.Unlock()
			return merr.New(merr.StageStream, merr.KindCanceled, item.TrackID, int64(item.Index),
				"queue closed before block %d could be enqueued", item.Index)
		}
		if err := ctx.Err(); err != nil {
			q.mu.Unlock()
			return merr.Wrap(merr.StageStream, kindForContext(err), item.TrackID, int64(item.Index),
				err, "canceled pushing block %d", item.Index)
		}
		if q.fits(item) {
			break
		}
		q.stats.BlockedPushes++
		q.mu.Unlock()
		// Wait outside the lock so a consumer can make room, and wake on
		// cancellation even though sync.Cond cannot observe a context.
		if err := q.wait(ctx); err != nil {
			if _, ok := err.(closedError); ok {
				return merr.New(merr.StageStream, merr.KindCanceled, item.TrackID, int64(item.Index),
					"queue closed before block %d could be enqueued", item.Index)
			}
			return merr.Wrap(merr.StageStream, kindForContext(err), item.TrackID, int64(item.Index),
				err, "blocked pushing block %d", item.Index)
		}
		q.mu.Lock()
	}
	q.items = append(q.items, item)
	q.bytes += item.Size()
	q.stats.Enqueued++
	if len(q.items) > q.stats.QueuePeakBlocks {
		q.stats.QueuePeakBlocks = len(q.items)
	}
	if q.bytes > q.stats.QueuePeakBytes {
		q.stats.QueuePeakBytes = q.bytes
	}
	q.cond.Broadcast()
	q.mu.Unlock()
	return nil
}

// lockedWatcher broadcasts the cond when ctx is done. It returns a stop func.
func (q *Queue) lockedWatcher(ctx context.Context) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			q.mu.Lock()
			q.cond.Broadcast()
			q.mu.Unlock()
		case <-done:
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// wait blocks until a state change or context cancellation.
func (q *Queue) wait(ctx context.Context) error {
	stop := q.lockedWatcher(ctx)
	defer stop()
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if q.closed {
		return closedError{}
	}
	q.cond.Wait()
	return ctx.Err()
}

var ioClosed = closedError{}

type closedError struct{}

func (closedError) Error() string { return "queue closed" }

// Pop removes the next item, blocking until one exists or ctx ends. A blocked
// wait is woken by Push, Close or context cancellation.
func (q *Queue) Pop(ctx context.Context) (Item, error) {
	stop := q.lockedWatcher(ctx)
	defer stop()
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 {
		if q.closed {
			return Item{}, ErrDrained
		}
		if err := ctx.Err(); err != nil {
			return Item{}, merr.Wrap(merr.StageStream, kindForContext(err), 0,
				int64(q.lastCommitted+1), err, "canceled waiting for block")
		}
		q.cond.Wait()
	}
	it := q.items[0]
	copy(q.items, q.items[1:])
	q.items = q.items[:len(q.items)-1]
	q.bytes -= it.Size()
	q.stats.Dequeued++
	if it.Index > q.lastCommitted {
		q.lastCommitted = it.Index
	}
	q.cond.Broadcast()
	return it, nil
}

// Close stops further pushes and wakes blocked readers.
func (q *Queue) Close() {
	q.mu.Lock()
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()
}

// LastCommitted returns the highest block index handed to the consumer, or -1.
func (q *Queue) LastCommitted() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.lastCommitted
}

// StatsSnapshot returns a copy of the counters.
func (q *Queue) StatsSnapshot() Stats {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.stats
}

func kindForContext(err error) merr.Kind {
	if err == context.DeadlineExceeded {
		return merr.KindTimeout
	}
	return merr.KindCanceled
}
