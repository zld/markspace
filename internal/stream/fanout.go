package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// ErrConsumerStalled is a consumer-side signal that it stopped consuming; it
// detaches the consumer without treating the run as failed.
var ErrConsumerStalled = errors.New("stream: consumer stalled")

// Consumer receives the chunks routed to exactly one representation layer.
type Consumer interface {
	// Name identifies the representation layer and is the routing key.
	Name() string
	// Offer receives one chunk. It must respect ctx so cancellation always
	// unblocks it.
	Offer(ctx context.Context, ch Chunk) error
	// Close is called exactly once after the consumer stops.
	Close() error
}

// Item is one routed chunk. Layer selects the destination consumer.
type Item struct {
	Layer string
	Chunk Chunk
}

// Feed produces routed chunks in a deterministic order. It returns io.EOF when
// the stream is exhausted.
type Feed interface {
	Next(ctx context.Context) (Item, error)
}

// FeedFunc adapts a function to Feed.
type FeedFunc func(ctx context.Context) (Item, error)

// Next implements Feed.
func (f FeedFunc) Next(ctx context.Context) (Item, error) { return f(ctx) }

// Fanout routes one feed to per-layer bounded queues. When a consumer's queue
// is full the producer blocks: nothing is dropped and no layer is starved of
// its already-produced data. Cancellation or Close stops every goroutine.
type Fanout struct {
	consumers map[string]*consumerState
	order     []string
	queue     int
	feed      Feed

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	produced atomic.Int64
	peak     atomic.Int64
}

type consumerState struct {
	sink  Consumer
	queue chan Item
	done  chan struct{}
	once  sync.Once
	errMu sync.Mutex
	err   error
	count atomic.Int64
	peak  atomic.Int64
}

// NewFanout starts one bounded queue per consumer.
func NewFanout(ctx context.Context, feed Feed, queue int, consumers ...Consumer) (*Fanout, error) {
	if feed == nil {
		return nil, errors.New("stream: nil feed")
	}
	if queue <= 0 {
		return nil, errors.New("stream: fan-out queue must be positive")
	}
	if len(consumers) == 0 {
		return nil, errors.New("stream: no consumers")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	f := &Fanout{queue: queue, feed: feed, ctx: ctx, cancel: cancel, consumers: map[string]*consumerState{}}
	for i, c := range consumers {
		if c == nil {
			cancel()
			return nil, fmt.Errorf("stream: nil consumer at %d", i)
		}
		if _, dup := f.consumers[c.Name()]; dup {
			cancel()
			return nil, fmt.Errorf("stream: duplicate consumer %q", c.Name())
		}
		f.consumers[c.Name()] = &consumerState{sink: c, queue: make(chan Item, queue), done: make(chan struct{})}
		f.order = append(f.order, c.Name())
	}
	for _, st := range f.consumers {
		f.wg.Add(1)
		go f.consume(st)
	}
	f.wg.Add(1)
	go f.produce()
	return f, nil
}

func (f *Fanout) produce() {
	defer f.wg.Done()
	defer f.closeQueues()
	for {
		item, err := f.feed.Next(f.ctx)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
				f.setErr(err)
			}
			return
		}
		st, ok := f.consumers[item.Layer]
		if !ok {
			f.setErr(fmt.Errorf("stream: feed routed unknown layer %q", item.Layer))
			return
		}
		select {
		case st.queue <- item:
			f.produced.Add(1)
			notePeak(&st.peak, int64(len(st.queue)))
			notePeak(&f.peak, int64(len(st.queue)))
		case <-st.done:
			// This layer detached; its already-queued items are dropped but
			// every other layer keeps receiving the producer's output.
		case <-f.ctx.Done():
			return
		}
	}
}

// closeQueues closes every consumer queue exactly once. It is only called by
// the producer goroutine, the sole sender, so no send-on-closed race exists.
func (f *Fanout) closeQueues() {
	for _, name := range f.order {
		close(f.consumers[name].queue)
	}
}

func (f *Fanout) consume(st *consumerState) {
	defer f.wg.Done()
	defer func() {
		st.once.Do(func() { _ = st.sink.Close() })
		close(st.done)
	}()
	for {
		select {
		case <-f.ctx.Done():
			return
		case item, ok := <-st.queue:
			if !ok {
				return
			}
			st.count.Add(1)
			if err := st.sink.Offer(f.ctx, item.Chunk); err != nil {
				if !errors.Is(err, ErrConsumerStalled) {
					st.errMu.Lock()
					st.err = err
					st.errMu.Unlock()
				}
				return
			}
		}
	}
}

func (f *Fanout) setErr(err error) {
	for _, st := range f.consumers {
		st.errMu.Lock()
		if st.err == nil {
			st.err = err
		}
		st.errMu.Unlock()
	}
}

// Wait blocks until every goroutine exits.
func (f *Fanout) Wait() { f.wg.Wait() }

// Close cancels, waits and reports consumer failures.
func (f *Fanout) Close() error {
	f.cancel()
	f.Wait()
	var errs []error
	for _, name := range f.order {
		st := f.consumers[name]
		st.errMu.Lock()
		if st.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, st.err))
		}
		st.errMu.Unlock()
	}
	return errors.Join(errs...)
}

// ConsumerStats reports per-layer flow accounting.
type ConsumerStats struct {
	Name       string
	Delivered  int64
	PeakQueued int64
	Failed     bool
}

// Stats reports per-consumer accounting in deterministic layer order.
func (f *Fanout) Stats() (int64, []ConsumerStats) {
	out := make([]ConsumerStats, 0, len(f.order))
	for _, name := range f.order {
		st := f.consumers[name]
		st.errMu.Lock()
		failed := st.err != nil
		st.errMu.Unlock()
		out = append(out, ConsumerStats{Name: name, Delivered: st.count.Load(), PeakQueued: st.peak.Load(), Failed: failed})
	}
	return f.produced.Load(), out
}

// QueueDepth is the configured per-consumer queue bound.
func (f *Fanout) QueueDepth() int { return f.queue }

// PeakQueued is the largest number of chunks buffered by any one consumer.
func (f *Fanout) PeakQueued() int64 { return f.peak.Load() }

// Bound is the maximum number of chunks the fan-out may buffer at once.
func (f *Fanout) Bound() int64 { return int64(len(f.consumers)) * int64(f.queue) }

// Cancel requests shutdown without waiting.
func (f *Fanout) Cancel() { f.cancel() }

func notePeak(counter *atomic.Int64, v int64) {
	for {
		cur := counter.Load()
		if v <= cur || counter.CompareAndSwap(cur, v) {
			return
		}
	}
}
