package stream

import (
	"context"
	"sync"
)

// Envelope is one bounded unit of work. Size is the wire/heap bytes charged
// against the queue budget.
type Envelope struct {
	Sequence     int64
	ByteSize     int64
	Kind         string
	Payload      any
	SourceOffset int64
}

// Pipe is a dynamically queued, byte-bounded handoff. Accepted envelopes are
// never dropped: once the byte budget is full, Submit blocks and applies
// backpressure to the producer.
type Pipe struct {
	budget *Budget

	mu    sync.Mutex
	cond  *sync.Cond
	queue []Envelope

	produced int64
	consumed int64
	acked    int64
}

func NewPipe(maxBytes int64) *Pipe {
	if maxBytes <= 0 {
		maxBytes = 1
	}
	p := &Pipe{budget: NewBudget(maxBytes)}
	p.cond = sync.NewCond(&p.mu)
	return p
}

// Submit reserves byte budget and appends the envelope. While blocked in
// Acquire, queue occupancy is already at the configured ceiling.
func (p *Pipe) Submit(ctx context.Context, e Envelope) error {
	if e.ByteSize <= 0 {
		e.ByteSize = int64(estimateSize(e.Payload))
	}
	if err := p.budget.Acquire(ctx, e.ByteSize); err != nil {
		return err
	}

	p.mu.Lock()
	p.queue = append(p.queue, e)
	p.produced++
	p.cond.Signal()
	p.mu.Unlock()
	return nil
}

// Receive waits for the next envelope. The caller must call Ack after the
// envelope and its output/checkpoint side effects are durable.
func (p *Pipe) Receive(ctx context.Context) (Envelope, error) {
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			p.mu.Lock()
			p.cond.Broadcast()
			p.mu.Unlock()
		case <-stop:
		}
	}()
	defer close(stop)

	p.mu.Lock()
	defer p.mu.Unlock()
	for len(p.queue) == 0 {
		if err := ctx.Err(); err != nil {
			return Envelope{}, err
		}
		p.cond.Wait()
	}
	e := p.queue[0]
	p.queue[0] = Envelope{} // avoid retaining Payload references
	p.queue = p.queue[1:]
	p.consumed++
	return e, nil
}

func (p *Pipe) Ack(e Envelope) {
	p.budget.Release(e.ByteSize)
	p.mu.Lock()
	p.acked++
	p.cond.Broadcast()
	p.mu.Unlock()
}

func (p *Pipe) Budget() *Budget { return p.budget }

func (p *Pipe) PeakBytes() int64 { return p.budget.Peak() }
func (p *Pipe) UsedBytes() int64 { return p.budget.Used() }

func (p *Pipe) QueuedPackets() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.queue)
}

type PipeStats struct {
	MaxBytes      int64 `json:"max_bytes"`
	PeakBytes     int64 `json:"peak_bytes"`
	UsedBytes     int64 `json:"used_bytes"`
	Produced      int64 `json:"produced"`
	Consumed      int64 `json:"consumed"`
	Acked         int64 `json:"acked"`
	WaitCount     int64 `json:"backpressure_waits"`
	QueuedPackets int   `json:"queued_packets"`
}

func (p *Pipe) Stats() PipeStats {
	p.mu.Lock()
	queued := len(p.queue)
	p.mu.Unlock()
	return PipeStats{
		MaxBytes: p.budget.Max(), PeakBytes: p.budget.Peak(), UsedBytes: p.budget.Used(),
		Produced: p.produced, Consumed: p.consumed, Acked: p.acked,
		WaitCount: p.budget.WaitCount(), QueuedPackets: queued,
	}
}

func estimateSize(v any) int {
	if v == nil {
		return 1
	}
	type sizer interface{ Size() int }
	if s, ok := v.(sizer); ok {
		if n := s.Size(); n > 0 {
			return n
		}
	}
	return 1
}
