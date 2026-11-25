package stream

import (
	"context"
	"sync"
	"time"

	"mediapack/model"
)

// DefaultMaxUnitBytes bounds a single queued unit. Together with capacity this
// gives callers a fixed upper memory bound for queued media.
const DefaultMaxUnitBytes = 4 * 1024 * 1024

// BoundedQueue is a fixed-capacity queue of media units. Its storage cannot
// grow with the length of an input stream: producers block or receive a
// recoverable timeout/backpressure error when all slots are occupied.
type BoundedQueue struct {
	capacity     int
	maxUnitBytes int

	mu        sync.Mutex
	notFull   *sync.Cond
	notEmpty  *sync.Cond
	items     []*model.Unit
	closed    bool
	peak      int
	enqueued  uint64
	timeouts  uint64
	oversized uint64
}

// NewBoundedQueue creates a queue with capacity slots.
func NewBoundedQueue(capacity, maxUnitBytes int) (*BoundedQueue, error) {
	if capacity <= 0 {
		return nil, model.NewError("", "", -1, model.CategoryInvalidArgument, "queue capacity must be positive", nil)
	}
	if maxUnitBytes <= 0 {
		maxUnitBytes = DefaultMaxUnitBytes
	}
	q := &BoundedQueue{capacity: capacity, maxUnitBytes: maxUnitBytes}
	q.notFull = sync.NewCond(&q.mu)
	q.notEmpty = sync.NewCond(&q.mu)
	return q, nil
}

// Cap returns the fixed slot capacity.
func (q *BoundedQueue) Cap() int { return q.capacity }

// Len returns the current occupancy.
func (q *BoundedQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// Peak returns the largest observed occupancy.
func (q *BoundedQueue) Peak() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.peak
}

// QueueStats is the reportable queue state.
type QueueStats struct {
	Capacity         int    `json:"capacity"`
	PeakOccupancy    int    `json:"peak_occupancy"`
	CurrentOccupancy int    `json:"current_occupancy"`
	Enqueued         uint64 `json:"enqueued"`
	Timeouts         uint64 `json:"backpressure_timeouts"`
	Oversized        uint64 `json:"oversized_rejected"`
}

// Stats returns cumulative queue counters.
func (q *BoundedQueue) Stats() QueueStats {
	q.mu.Lock()
	defer q.mu.Unlock()
	return QueueStats{
		Capacity:         q.capacity,
		PeakOccupancy:    q.peak,
		CurrentOccupancy: len(q.items),
		Enqueued:         q.enqueued,
		Timeouts:         q.timeouts,
		Oversized:        q.oversized,
	}
}

func (q *BoundedQueue) checkSize(u *model.Unit) error {
	if u == nil {
		return model.NewError("", "", -1, model.CategoryInvalidArgument, "nil media unit", nil)
	}
	size := u.Size
	if size == 0 {
		size = len(u.Payload)
	}
	if size > q.maxUnitBytes {
		q.mu.Lock()
		q.oversized++
		q.mu.Unlock()
		return model.NewError(u.Track, u.ID, u.SourceOffset, model.CategoryUnitTooLarge,
			"media unit exceeds bounded queue unit size", nil).RecoverableWarn()
	}
	return nil
}

// Put blocks until a slot is available or ctx is done. Context deadline/cancel
// is converted to a recoverable backpressure error.
func (q *BoundedQueue) Put(ctx context.Context, u *model.Unit) error {
	if err := q.checkSize(u); err != nil {
		return err
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	stopWake := context.AfterFunc(ctx, q.notFull.Broadcast)
	defer stopWake()

	for len(q.items) == q.capacity && !q.closed && ctx.Err() == nil {
		q.notFull.Wait()
	}
	if q.closed {
		return model.NewError(u.Track, u.ID, u.SourceOffset, model.CategoryInvalidArgument, "queue closed", nil)
	}
	if err := ctx.Err(); err != nil {
		q.timeouts++
		return model.NewError(u.Track, u.ID, u.SourceOffset, model.CategoryQueueBackpressure,
			"producer timed out or was canceled while queue was full", err).RecoverableWarn()
	}

	// Timestamp after the backpressure wait so latency measures queue dwell.
	u.EnqueuedAt = time.Now()
	q.items = append(q.items, u)
	q.enqueued++
	if len(q.items) > q.peak {
		q.peak = len(q.items)
	}
	q.notEmpty.Signal()
	return nil
}

// PutTimeout applies a bounded blocking timeout.
func (q *BoundedQueue) PutTimeout(u *model.Unit, wait time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	return q.Put(ctx, u)
}

// Get receives the next unit. It returns nil after Close and exhaustion.
func (q *BoundedQueue) Get(ctx context.Context) (*model.Unit, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	stopWake := context.AfterFunc(ctx, q.notEmpty.Broadcast)
	defer stopWake()

	for len(q.items) == 0 && !q.closed && ctx.Err() == nil {
		q.notEmpty.Wait()
	}
	if len(q.items) == 0 {
		if ctx.Err() != nil {
			return nil, model.NewError("", "", -1, model.CategoryQueueBackpressure,
				"consumer wait canceled", ctx.Err()).RecoverableWarn()
		}
		return nil, nil
	}
	u := q.items[0]
	q.items[0] = nil
	q.items = q.items[1:]
	q.notFull.Signal()
	return u, nil
}

// Close marks the queue closed and wakes all waiters.
func (q *BoundedQueue) Close() {
	q.mu.Lock()
	q.closed = true
	q.notFull.Broadcast()
	q.notEmpty.Broadcast()
	q.mu.Unlock()
}
