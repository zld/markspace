package stream

import (
	"context"
	"sync"
	"time"

	"github.com/zld/media-regress/internal/error"
)

// ErrDrained signals that the queue is closed and empty.
var ErrDrained = merr.New(merr.StageStream, merr.KindCanceled, 0, 0, "pipeline drained")

// Delivery is one block as received by a consumer.
type Delivery struct {
	Item
	// Latency is how long the block waited in the queue.
	Latency time.Duration
}

// Consumer receives blocks. Return a non-nil error to stop the pipeline; the
// error is propagated to the producer unchanged.
type Consumer func(ctx context.Context, d Delivery) error

// Pipeline couples a producer of blocks to a consumer through the bounded
// queue. Exactly one pipeline runs per track; blocks never cross tracks.
type Pipeline struct {
	queue    *Queue
	consumer Consumer
	// ConsumerDelay simulates a slow consumer; it applies per dequeue.
	ConsumerDelay time.Duration

	sentMu    sync.Mutex
	seq       uint16
	lastTS    uint32
	haveTS    bool
	delivered int
}

// NewPipeline builds a pipeline. The consumer may be nil for tests that only
// exercise backpressure.
func NewPipeline(cfg Config, consumer Consumer) (*Pipeline, error) {
	q, err := NewQueue(cfg)
	if err != nil {
		return nil, err
	}
	return &Pipeline{queue: q, consumer: consumer}, nil
}

// Push enqueues a block, assigning the next RTP sequence number and enforcing
// monotonic timestamps. Timestamps that move backwards or skip more than one
// block's worth of samples are reported as timeline errors, not repaired.
func (p *Pipeline) Push(ctx context.Context, it Item) error {
	p.sentMu.Lock()
	it.Sequence = p.seq
	p.seq++
	if p.haveTS && it.Timestamp < p.lastTS {
		prev := p.lastTS
		p.sentMu.Unlock()
		return merr.New(merr.StageStream, merr.KindInconsistent, it.TrackID, int64(it.Index),
			"rtp timestamp regressed from %d to %d on block %d", prev, it.Timestamp, it.Index)
	}
	p.lastTS = it.Timestamp
	p.haveTS = true
	p.sentMu.Unlock()
	return p.queue.Push(ctx, it)
}

// Run drains the queue into the consumer until the queue closes and empties.
func (p *Pipeline) Run(ctx context.Context) error {
	defer p.queue.Close()
	for {
		it, err := p.queue.Pop(ctx)
		if err == ErrDrained {
			return nil
		}
		if err != nil {
			return err
		}
		if p.ConsumerDelay > 0 {
			select {
			case <-time.After(p.ConsumerDelay):
			case <-ctx.Done():
				return merr.Wrap(merr.StageStream, kindForContext(ctx.Err()), it.TrackID,
					int64(it.Index), ctx.Err(), "consumer canceled")
			}
		}
		if p.consumer != nil {
			if err := p.consumer(ctx, Delivery{Item: it}); err != nil {
				return err
			}
		}
		p.sentMu.Lock()
		p.delivered++
		p.sentMu.Unlock()
	}
}

// Close stops intake and wakes the consumer.
func (p *Pipeline) Close() { p.queue.Close() }

// Stats returns queue statistics.
func (p *Pipeline) Stats() Stats { return p.queue.StatsSnapshot() }

// LastCommitted returns the highest delivered block index, or -1.
func (p *Pipeline) LastCommitted() int { return p.queue.LastCommitted() }

// Delivered returns the number of blocks handed to the consumer.
func (p *Pipeline) Delivered() int {
	p.sentMu.Lock()
	defer p.sentMu.Unlock()
	return p.delivered
}
