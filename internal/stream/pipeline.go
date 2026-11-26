package stream

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"mediaprep/internal/model"
)

const BlockSize = 64 * 1024

type Options struct {
	QueueLength       int
	BufferBudgetBytes int64
	SlowConsumer      time.Duration
}

type Block struct {
	Seq          int    `json:"seq"`
	SourceOffset int64  `json:"source_offset"`
	Data         []byte `json:"-"`
}

type Metrics struct {
	BlockSize          int   `json:"block_size"`
	QueueCapacity      int   `json:"queue_capacity"`
	QueuePeak          int64 `json:"queue_peak"`
	BufferBudgetBytes  int64 `json:"buffer_budget_bytes"`
	BufferPeakBytes    int64 `json:"buffer_peak_bytes"`
	BufferActualBytes  int64 `json:"buffer_actual_bytes"`
	BlocksProduced     int64 `json:"blocks_produced"`
	BlocksConsumed     int64 `json:"blocks_consumed"`
	BlocksDropped      int64 `json:"blocks_dropped"`
	TasksStarted       int64 `json:"tasks_started"`
	TasksFinished      int64 `json:"tasks_finished"`
	ResidualTasks      int64 `json:"residual_tasks"`
	BackpressureEvents int64 `json:"backpressure_events"`
}

type Sink func(context.Context, Block) error

type Pipeline struct {
	opts      Options
	blockSize int
	sem       chan struct{}

	queueDepth atomic.Int64
	queuePeak  atomic.Int64
	bufferHeld atomic.Int64
	bufferPeak atomic.Int64

	produced  atomic.Int64
	consumed  atomic.Int64
	dropped   atomic.Int64
	started   atomic.Int64
	finished  atomic.Int64
	backpress atomic.Int64
}

func New(opts Options) (*Pipeline, error) {
	if opts.QueueLength <= 0 {
		opts.QueueLength = 2
	}
	if opts.BufferBudgetBytes <= 0 {
		opts.BufferBudgetBytes = 4 * BlockSize
	}
	if opts.BufferBudgetBytes < BlockSize {
		return nil, model.NewError(model.CodeInvalidMetadata, model.PhaseStream, "", -1,
			"buffer budget %d is smaller than 64 KiB block", opts.BufferBudgetBytes)
	}
	slots := int(opts.BufferBudgetBytes / BlockSize)
	return &Pipeline{
		opts:      opts,
		blockSize: BlockSize,
		sem:       make(chan struct{}, slots),
	}, nil
}

// Process reads fixed-size blocks, applies both queue and byte-budget bounds,
// and invokes sink serially. Cancellation unblocks budget acquisition, queue
// writes, and a context-aware slow sink. Queued blocks are discarded and
// released after cancellation so no task remains running.
func (p *Pipeline) Process(ctx context.Context, r io.Reader, sink Sink) (Metrics, error) {
	if sink == nil {
		sink = func(context.Context, Block) error { return nil }
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	ch := make(chan Block, p.opts.QueueLength)
	var (
		wg       sync.WaitGroup
		sinkMu   sync.Mutex
		sinkErrs []error
	)
	addSinkErr := func(err error) {
		if err == nil {
			return
		}
		sinkMu.Lock()
		sinkErrs = append(sinkErrs, err)
		sinkMu.Unlock()
		cancel()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for b := range ch {
			// Receipt removes the block from queue occupancy even though its
			// byte-budget slot remains held until processing finishes.
			p.changeQueueDepth(-1)
			if ctx.Err() != nil {
				p.dropped.Add(1)
				p.releaseBuffer()
				continue
			}
			p.started.Add(1)
			err := p.slowSink(ctx, b, sink)
			p.finished.Add(1)
			p.consumed.Add(1)
			p.releaseBuffer()
			if err != nil {
				addSinkErr(err)
				// Keep draining until the producer observes cancellation and
				// closes the channel; this releases every queued slot.
				continue
			}
		}
	}()

	producerErr := p.produce(ctx, r, ch)
	close(ch)
	wg.Wait()

	sinkMu.Lock()
	var sinkErr error
	if len(sinkErrs) > 0 {
		sinkErr = errors.Join(sinkErrs...)
	}
	sinkMu.Unlock()

	m := p.Snapshot()
	if sinkErr != nil {
		return m, sinkErr
	}
	if producerErr != nil && !errors.Is(producerErr, context.Canceled) {
		return m, producerErr
	}
	// A canceled pipeline is expected by the cancel verification case.
	return m, producerErr
}

func (p *Pipeline) produce(ctx context.Context, r io.Reader, ch chan<- Block) error {
	seq := 0
	var offset int64
	for {
		if err := p.acquireBuffer(ctx); err != nil {
			return err
		}
		buf := make([]byte, p.blockSize)
		n, err := io.ReadFull(r, buf)
		if err == io.EOF {
			p.releaseBuffer()
			return nil
		}
		if err != nil && err != io.ErrUnexpectedEOF {
			p.releaseBuffer()
			return model.NewError(model.CodeTruncated, model.PhaseStream, "", offset, "read block: %v", err)
		}
		buf = buf[:n]
		b := Block{Seq: seq, SourceOffset: offset, Data: buf}
		seq++
		p.produced.Add(1)

		if len(ch) == cap(ch) {
			p.backpress.Add(1)
		}
		select {
		case ch <- b:
			p.changeQueueDepth(1)
			offset += int64(n)
		case <-ctx.Done():
			p.releaseBuffer()
			return ctx.Err()
		}
		if n < p.blockSize {
			return nil
		}
	}
}

func (p *Pipeline) slowSink(ctx context.Context, b Block, sink Sink) error {
	if p.opts.SlowConsumer > 0 {
		t := time.NewTimer(p.opts.SlowConsumer)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
	}
	return sink(ctx, b)
}

func (p *Pipeline) acquireBuffer(ctx context.Context) error {
	select {
	case p.sem <- struct{}{}:
		p.bufferHeld.Add(int64(p.blockSize))
		held := p.bufferHeld.Load()
		for {
			peak := p.bufferPeak.Load()
			if held <= peak || p.bufferPeak.CompareAndSwap(peak, held) {
				break
			}
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Pipeline) releaseBuffer() {
	// Receive one slot and account one fixed block reservation.
	<-p.sem
	p.bufferHeld.Add(-int64(p.blockSize))
}

func (p *Pipeline) changeQueueDepth(delta int64) {
	d := p.queueDepth.Add(delta)
	if delta > 0 {
		for {
			peak := p.queuePeak.Load()
			if d <= peak || p.queuePeak.CompareAndSwap(peak, d) {
				return
			}
		}
	}
}

func (p *Pipeline) Snapshot() Metrics {
	started := p.started.Load()
	finished := p.finished.Load()
	return Metrics{
		BlockSize:          p.blockSize,
		QueueCapacity:      p.opts.QueueLength,
		QueuePeak:          p.queuePeak.Load(),
		BufferBudgetBytes:  p.opts.BufferBudgetBytes,
		BufferPeakBytes:    p.bufferPeak.Load(),
		BufferActualBytes:  p.bufferHeld.Load(),
		BlocksProduced:     p.produced.Load(),
		BlocksConsumed:     p.consumed.Load(),
		BlocksDropped:      p.dropped.Load(),
		TasksStarted:       started,
		TasksFinished:      finished,
		ResidualTasks:      started - finished,
		BackpressureEvents: p.backpress.Load(),
	}
}
