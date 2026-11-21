// Package relay forwards RTP between the two loopback WebRTC hops through a
// bounded queue while preserving header fields.
package relay

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/pion/rtp"
)

// Errors reported by the forwarder.
var (
	ErrQueueFull   = errors.New("forwarder queue full")
	ErrWriteFailed = errors.New("forwarder write failed")
	ErrClosed      = errors.New("forwarder closed")
)

// Writer is the egress sink, typically a Pion TrackLocalStaticRTP.
type Writer interface {
	WriteRTP(p *rtp.Packet) error
}

// Config controls queue depth.
type Config struct {
	// QueueSize is the bounded buffer capacity. A full queue drops and counts.
	QueueSize int
}

// Stats is a snapshot of forwarder counters.
type Stats struct {
	Received    uint64
	Enqueued    uint64
	Forwarded   uint64
	Dropped     uint64
	WriteFailed uint64
	LastError   string
}

// Forwarder moves packets from Receive into a bounded queue and writes them to
// a Writer, preserving sequence number, timestamp, payload type and marker.
//
// Closing is coordinated by a mutex because a buffered channel cannot be
// closed safely while another goroutine may still send on it. Receive takes
// the read lock; Close takes the write lock, so once Close begins no Receive
// can be in flight.
type Forwarder struct {
	cfg    Config
	writer Writer

	queue  chan *rtp.Packet
	done   chan struct{}
	stop   chan struct{}
	wg     sync.WaitGroup
	once   sync.Once
	queued sync.RWMutex

	received    atomic.Uint64
	enqueued    atomic.Uint64
	forwarded   atomic.Uint64
	dropped     atomic.Uint64
	writeFailed atomic.Uint64

	errMu     sync.Mutex
	lastError error
}

// New creates a forwarder. The write loop starts when Start is called.
func New(cfg Config, writer Writer) (*Forwarder, error) {
	if writer == nil {
		return nil, fmt.Errorf("forwarder requires a writer")
	}
	if cfg.QueueSize < 1 {
		return nil, fmt.Errorf("queue size must be positive, got %d", cfg.QueueSize)
	}
	return &Forwarder{
		cfg:    cfg,
		writer: writer,
		queue:  make(chan *rtp.Packet, cfg.QueueSize),
		done:   make(chan struct{}),
		stop:   make(chan struct{}),
	}, nil
}

// Start launches the write loop. It must be called once before Close.
func (f *Forwarder) Start() {
	f.wg.Add(1)
	go f.writeLoop()
}

// Receive accepts an inbound packet without blocking. When the queue is full
// the packet is counted as dropped and ErrQueueFull is returned. Ownership of
// the packet transfers to the forwarder.
func (f *Forwarder) Receive(p *rtp.Packet) error {
	f.queued.RLock()
	defer f.queued.RUnlock()

	select {
	case <-f.done:
		return ErrClosed
	default:
	}
	if p == nil {
		return ErrClosed
	}
	f.received.Add(1)
	select {
	case f.queue <- p:
		f.enqueued.Add(1)
		return nil
	default:
		f.dropped.Add(1)
		f.setError(ErrQueueFull)
		return ErrQueueFull
	}
}

// Stats returns a counter snapshot plus the last recorded error.
func (f *Forwarder) Stats() Stats {
	s := Stats{
		Received:    f.received.Load(),
		Enqueued:    f.enqueued.Load(),
		Forwarded:   f.forwarded.Load(),
		Dropped:     f.dropped.Load(),
		WriteFailed: f.writeFailed.Load(),
	}
	f.errMu.Lock()
	if f.lastError != nil {
		s.LastError = f.lastError.Error()
	}
	f.errMu.Unlock()
	return s
}

// Close stops accepting packets, drains the queue in the write loop, and waits
// for it to finish or the context to expire. It is safe to call repeatedly.
func (f *Forwarder) Close(ctx context.Context) error {
	f.once.Do(func() {
		f.queued.Lock()
		close(f.done)
		close(f.queue)
		f.queued.Unlock()
	})
	wait := make(chan struct{})
	go func() {
		f.wg.Wait()
		close(wait)
	}()
	select {
	case <-wait:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *Forwarder) writeLoop() {
	defer f.wg.Done()
	for p := range f.queue {
		if f.write(p) != nil {
			continue
		}
	}
}

func (f *Forwarder) write(p *rtp.Packet) error {
	err := f.writer.WriteRTP(clonePacket(p))
	if err != nil {
		f.writeFailed.Add(1)
		f.setError(fmt.Errorf("%w: %v", ErrWriteFailed, err))
		return err
	}
	f.forwarded.Add(1)
	return nil
}

func (f *Forwarder) setError(err error) {
	f.errMu.Lock()
	f.lastError = err
	f.errMu.Unlock()
}

// clonePacket copies the header and payload so the writer cannot mutate the
// packet while the reader reuses its buffer.
func clonePacket(p *rtp.Packet) *rtp.Packet {
	out := *p
	out.Payload = append([]byte(nil), p.Payload...)
	return &out
}
