package stream

import (
	"context"
	"sync"
)

// Envelope is one bounded unit of work. Size must be the wire/heap bytes
// charged against the queue budget.
type Envelope struct {
	Sequence     int64
	ByteSize     int64
	Kind         string
	Payload      any
	SourceOffset int64
}

// Pipe is a byte-bounded handoff between producer and consumer. It never drops
// an accepted envelope: Submit applies backpressure until budget is released.
type Pipe struct {
	budget *Budget
	ch     chan Envelope

	mu       sync.Mutex
	produced int64
	consumed int64
	acked    int64
}

func NewPipe(maxBytes int64) *Pipe {
	if maxBytes <= 0 {
		maxBytes = 1
	}
	return &Pipe{
		budget: NewBudget(maxBytes),
		// Rendezvous handoff; byte budget is the real buffer bound.
		ch: make(chan Envelope),
	}
}

// Submit reserves byte budget and then hands the envelope to the consumer.
// While blocked, reserved bytes remain accounted so Peak/Used stay bounded.
func (p *Pipe) Submit(ctx context.Context, e Envelope) error {
	if e.ByteSize <= 0 {
		e.ByteSize = int64(estimateSize(e.Payload))
	}
	if err := p.budget.Acquire(ctx, e.ByteSize); err != nil {
		return err
	}
	p.mu.Lock()
	p.produced++
	p.mu.Unlock()

	select {
	case p.ch <- e:
		return nil
	case <-ctx.Done():
		p.budget.Release(e.ByteSize)
		return ctx.Err()
	}
}

// Receive waits for the next envelope. The caller must call Ack after the
// envelope and its output/checkpoint side effects are durable.
func (p *Pipe) Receive(ctx context.Context) (Envelope, error) {
	select {
	case e := <-p.ch:
		p.mu.Lock()
		p.consumed++
		p.mu.Unlock()
		return e, nil
	case <-ctx.Done():
		return Envelope{}, ctx.Err()
	}
}

func (p *Pipe) Ack(e Envelope) {
	p.budget.Release(e.ByteSize)
	p.mu.Lock()
	p.acked++
	p.mu.Unlock()
}

func (p *Pipe) Budget() *Budget  { return p.budget }
func (p *Pipe) PeakBytes() int64 { return p.budget.Peak() }
func (p *Pipe) UsedBytes() int64 { return p.budget.Used() }

type PipeStats struct {
	MaxBytes  int64 `json:"max_bytes"`
	PeakBytes int64 `json:"peak_bytes"`
	UsedBytes int64 `json:"used_bytes"`
	Produced  int64 `json:"produced"`
	Consumed  int64 `json:"consumed"`
	Acked     int64 `json:"acked"`
	WaitCount int64 `json:"backpressure_waits"`
}

func (p *Pipe) Stats() PipeStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return PipeStats{
		MaxBytes: p.budget.Max(), PeakBytes: p.budget.Peak(), UsedBytes: p.budget.Used(),
		Produced: p.produced, Consumed: p.consumed, Acked: p.acked,
		WaitCount: p.budget.WaitCount(),
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
