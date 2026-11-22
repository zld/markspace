package stream

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// ErrConsumerStalled is reported by SlowConsumer hooks; it is not fatal.
var ErrConsumerStalled = errors.New("stream: consumer stalled")

// Consumer receives chunks from a fan-out. It must return promptly or block;
// blocking pauses only this consumer (and the producer once its queue fills).
type Consumer interface {
	// Name identifies the representation layer.
	Name() string
	// Offer receives the next chunk. Returning an error detaches the consumer.
	Offer(ctx context.Context, ch Chunk) error
	// Close is called exactly once after the consumer stops.
	Close() error
}

// Fanout copies one chunk stream to several consumers with per-consumer
// bounded queues. A consumer whose queue is full pauses the producer; it never
// causes unbounded buffering and never drops data for other consumers.
type Fanout struct {
	consumers []*consumerState
	queue     int
	src       *Reader

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	closed   bool
	produced int64
	peak     atomic.Int64
	stalls   atomic.Int64
}

type consumerState struct {
	sink   Consumer
	queue  chan Chunk
	done   chan struct{}
	once   sync.Once
	errMu  sync.Mutex
	err    error
	queued atomic.Int64
	peak   atomic.Int64
	count  atomic.Int64
}

// NewFanout starts one bounded queue per consumer.
func NewFanout(ctx context.Context, src *Reader, queue int, consumers ...Consumer) (*Fanout, error) {
	if src == nil {
		return nil, errors.New("stream: nil reader")
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
	f := &Fanout{queue: queue, src: src, ctx: ctx, cancel: cancel}
	seen := map[string]bool{}
	for i, c := range consumers {
		if c == nil {
			cancel()
			return nil, fmt.Errorf("stream: nil consumer at %d", i)
		}
		if seen[c.Name()] {
			cancel()
			return nil, fmt.Errorf("stream: duplicate consumer %q", c.Name())
		}
		seen[c.Name()] = true
		st := &consumerState{sink: c, queue: make(chan Chunk, queue), done: make(chan struct{})}
		f.consumers = append(f.consumers, st)
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
	defer f.shutdown()
	for {
		ch, err := f.src.Next(f.ctx)
		if err != nil {
			f.recordErr(err)
			return
		}
		f.mu.Lock()
		f.produced++
		f.mu.Unlock()
		for _, st := range f.consumers {
			select {
			case <-st.done:
				continue
			default:
			}
			select {
			case st.queue <- ch:
				depth := int(st.queued.Add(1))
				notePeak(&st.peak, int64(depth))
				notePeak(&f.peak, int64(depth))
			case <-st.done:
			case <-f.ctx.Done():
				f.recordErr(f.ctx.Err())
				return
			}
		}
	}
}

func (f *Fanout) consume(st *consumerState) {
	defer f.wg.Done()
	defer st.once.Do(func() { close(st.done) })
	defer func() { _ = st.sink.Close() }()
	for {
		select {
		case <-f.ctx.Done():
			return
		case ch, ok := <-st.queue:
			if !ok {
				return
			}
			st.queued.Add(-1)
			st.count.Add(1)
			if err := st.sink.Offer(f.ctx, ch); err != nil {
				if !errors.Is(err, ErrConsumerStalled) {
					st.errMu.Lock()
					st.err = err
					st.errMu.Unlock()
				}
				f.stalls.Add(1)
				return
			}
		}
	}
}

func (f *Fanout) shutdown() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	f.mu.Unlock()
	for _, st := range f.consumers {
		st.once.Do(func() { close(st.done) })
	}
	_ = f.src.Close()
}

func (f *Fanout) recordErr(err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, ErrClosed) {
		return
	}
}

// Wait blocks until every goroutine exits. All goroutines exit after either
// cancellation, source exhaustion, or Close.
func (f *Fanout) Wait() {
	f.wg.Wait()
}

// Close cancels the fan-out and waits for all goroutines. It is safe to call
// more than once and from any goroutine.
func (f *Fanout) Close() error {
	f.cancel()
	f.shutdown()
	f.Wait()
	var errs []error
	for _, st := range f.consumers {
		st.errMu.Lock()
		if st.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", st.sink.Name(), st.err))
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

// Stats reports deterministic per-consumer flow accounting.
func (f *Fanout) Stats() (int64, []ConsumerStats) {
	f.mu.Lock()
	produced := f.produced
	f.mu.Unlock()
	out := make([]ConsumerStats, 0, len(f.consumers))
	for _, st := range f.consumers {
		st.errMu.Lock()
		failed := st.err != nil
		st.errMu.Unlock()
		out = append(out, ConsumerStats{Name: st.sink.Name(), Delivered: st.count.Load(), PeakQueued: st.peak.Load(), Failed: failed})
	}
	return produced, out
}

// QueueDepth is the configured per-consumer queue bound.
func (f *Fanout) QueueDepth() int { return f.queue }

// PeakQueued is the largest number of chunks buffered by any single consumer.
func (f *Fanout) PeakQueued() int64 { return f.peak.Load() }

// Bound returns the maximum number of chunks the fan-out may hold at once.
func (f *Fanout) Bound() int64 { return int64(len(f.consumers)) * int64(f.queue) }

func notePeak(counter *atomic.Int64, v int64) {
	for {
		cur := counter.Load()
		if v <= cur || counter.CompareAndSwap(cur, v) {
			return
		}
	}
}
