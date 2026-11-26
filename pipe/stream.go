package pipe

import (
	"context"
	"errors"
	"math"
	"sort"
	"sync"
	"time"

	"archivecheck/internal"
)

// Record is one bounded-pipeline work item.
type Record struct {
	Cursor    int64
	Kind      string
	Source    string
	Payload   []byte
	SizeBytes int64
}

// Size returns the accounted byte footprint of a record.
func (r Record) Size() int64 {
	if r.SizeBytes > 0 {
		return r.SizeBytes
	}
	if len(r.Payload) > 0 {
		return int64(len(r.Payload))
	}
	return 1
}

// Config controls queue depth and memory.
type Config struct {
	Queue       int
	MemoryBytes int64
	Budget      *Budget
}

// Handler processes one record. Returning an error aborts the stream and keeps
// the last successfully confirmed cursor.
type Handler func(context.Context, Record) error

// Budget is a cancellable byte semaphore with peak accounting.
type Budget struct {
	maxBytes int64
	mu       sync.Mutex
	wake     chan struct{}
	inUse    int64
	peak     int64
}

func NewBudget(maxBytes int64) *Budget {
	if maxBytes <= 0 {
		maxBytes = math.MaxInt64
	}
	return &Budget{maxBytes: maxBytes, wake: make(chan struct{})}
}

func (b *Budget) Acquire(ctx context.Context, n int64) error {
	if n <= 0 {
		n = 1
	}
	if n > b.maxBytes {
		return internal.NewFault("pipe.memory", "", 0, "record size %d exceeds budget %d", n, b.maxBytes)
	}
	for {
		b.mu.Lock()
		if b.inUse+n <= b.maxBytes {
			b.inUse += n
			if b.inUse > b.peak {
				b.peak = b.inUse
			}
			b.mu.Unlock()
			return nil
		}
		wake := b.wake
		b.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (b *Budget) Release(n int64) {
	if n <= 0 {
		return
	}
	b.mu.Lock()
	b.inUse -= n
	if b.inUse < 0 {
		b.inUse = 0
	}
	close(b.wake)
	b.wake = make(chan struct{})
	b.mu.Unlock()
}

func (b *Budget) PeakBytes() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.peak
}

func (b *Budget) InUseBytes() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.inUse
}

type envelope struct {
	rec       Record
	size      int64
	startedAt time.Time
}

// Stream is a fixed-capacity, backpressured record stream.
type Stream struct {
	cfg     Config
	handler Handler
	budget  *Budget
	jobs    chan envelope

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	closeOnce sync.Once
	closeErr  error

	mu         sync.Mutex
	lastCursor int64
	hasCursor  bool
	latencies  []time.Duration
	firstErr   error
}

func NewStream(cfg Config, handler Handler) *Stream {
	if cfg.Queue <= 0 {
		cfg.Queue = 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	budget := cfg.Budget
	if budget == nil {
		budget = NewBudget(cfg.MemoryBytes)
	}
	return &Stream{
		cfg:     cfg,
		handler: handler,
		budget:  budget,
		jobs:    make(chan envelope, cfg.Queue),
		ctx:     ctx,
		cancel:  cancel,
	}
}

// Start launches the single consumer, preserving ordered cursor confirmation.
func (s *Stream) Start() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for env := range s.jobs {
			size := env.size
			if s.ctx.Err() != nil {
				s.budget.Release(size)
				continue
			}
			err := s.handler(s.ctx, env.rec)
			s.budget.Release(size)
			s.recordLatency(time.Since(env.startedAt))
			if err != nil {
				s.mu.Lock()
				if s.firstErr == nil {
					s.firstErr = err
				}
				s.mu.Unlock()
				s.cancel()
				return
			}
			s.confirm(env.rec.Cursor)
		}
	}()
}

// Send applies memory and fixed-queue backpressure. On cancellation the
// reservation is released and the confirmed cursor is left unchanged.
func (s *Stream) Send(ctx context.Context, rec Record) error {
	size := rec.Size()
	if err := s.budget.Acquire(ctx, size); err != nil {
		return err
	}
	env := envelope{rec: rec, size: size, startedAt: time.Now()}
	select {
	case s.jobs <- env:
		return nil
	case <-ctx.Done():
		s.budget.Release(size)
		return ctx.Err()
	case <-s.ctx.Done():
		s.budget.Release(size)
		if err := s.Err(); err != nil {
			return err
		}
		return s.ctx.Err()
	}
}

// Close closes the queue and waits for the consumer.
func (s *Stream) Close() error {
	s.closeOnce.Do(func() { close(s.jobs) })
	s.wg.Wait()
	s.cancel()
	return s.Err()
}

// Cancel aborts processing without closing from the producer side.
func (s *Stream) Cancel() {
	s.cancel()
}

func (s *Stream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.firstErr
}

func (s *Stream) confirm(cursor int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasCursor || cursor > s.lastCursor {
		s.lastCursor = cursor
		s.hasCursor = true
	}
}

// LastCursor returns the highest confirmed cursor and whether one exists.
func (s *Stream) LastCursor() (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastCursor, s.hasCursor
}

func (s *Stream) recordLatency(d time.Duration) {
	s.mu.Lock()
	s.latencies = append(s.latencies, d)
	s.mu.Unlock()
}

// P95Latency returns nearest-rank P95 latency in milliseconds.
func (s *Stream) P95Latency() time.Duration {
	s.mu.Lock()
	xs := append([]time.Duration(nil), s.latencies...)
	s.mu.Unlock()
	if len(xs) == 0 {
		return 0
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	idx := int(math.Ceil(0.95*float64(len(xs)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(xs) {
		idx = len(xs) - 1
	}
	return xs[idx]
}

func (s *Stream) PeakInFlightBytes() int64 { return s.budget.PeakBytes() }

// Process is a small blocking helper used by tests and replay.
func Process(ctx context.Context, cfg Config, records []Record, h Handler) (int64, int64, time.Duration, error) {
	st := NewStream(cfg, h)
	st.Start()
	for _, r := range records {
		if err := st.Send(ctx, r); err != nil {
			_ = st.Close()
			cursor, _ := st.LastCursor()
			return cursor, st.PeakInFlightBytes(), st.P95Latency(), err
		}
	}
	err := st.Close()
	cursor, _ := st.LastCursor()
	return cursor, st.PeakInFlightBytes(), st.P95Latency(), err
}

// IsHalt reports whether err is a deliberate interruption marker.
func IsHalt(err error) bool { return errors.Is(err, ErrHalt) }

var ErrHalt = errors.New("processing halted for simulated interruption")
