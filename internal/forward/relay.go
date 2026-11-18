package forward

import (
	"context"
	"sync"
	"sync/atomic"
)

// OverflowMode controls queue-full behaviour.
type OverflowMode string

const (
	// OverflowBlock makes Enqueue wait until space is available or Close.
	OverflowBlock OverflowMode = "block"
	// OverflowDrop discards the new packet and increments dropped stats.
	OverflowDrop OverflowMode = "drop"
)

// RelayStats is the atomic counter set exported for the final report.
type RelayStats struct {
	Enqueued   int64 `json:"enqueued"`
	Forwarded  int64 `json:"forwarded"`
	Dropped    int64 `json:"dropped"`
	Duplicates int64 `json:"duplicates"`
	Reordered  int64 `json:"reordered"`
	Malformed  int64 `json:"malformed"`
}

// queuedPacket keeps the untouched wire bytes; the relay never rewrites them.
type queuedPacket struct {
	raw []byte
}

// Relay is a bounded single-producer/single-consumer RTP queue. It never
// mutates packet media fields: raw bytes travel unchanged to the sink.
type Relay struct {
	queue    chan queuedPacket
	mode     OverflowMode
	stats    RelayStats
	closed   chan struct{}
	doneOnce sync.Once
	wg       sync.WaitGroup

	orderMu sync.Mutex
	seen    map[uint16]struct{} // sequence numbers already observed
	maxSeq  uint16
	hasMax  bool
}

// NewRelay builds a relay of the given capacity (must be > 0).
func NewRelay(capacity int, mode OverflowMode) *Relay {
	if capacity <= 0 {
		capacity = 1
	}
	r := &Relay{
		queue:  make(chan queuedPacket, capacity),
		mode:   mode,
		closed: make(chan struct{}),
		seen:   make(map[uint16]struct{}),
	}
	return r
}

// Enqueue validates and queues one packet. Returns false when the packet is
// rejected (malformed or dropped on a full queue). Block mode waits for room
// unless ctx is cancelled; shutdown always unblocks callers.
func (r *Relay) Enqueue(ctx context.Context, raw []byte) (bool, error) {
	pkt, err := Parse(raw)
	if err != nil {
		atomic.AddInt64(&r.stats.Malformed, 1)
		return false, err
	}
	entry := queuedPacket{raw: raw}
	// Classify against the set of sequence numbers already observed and
	// the highest one: a repeat is a duplicate, a first-time late arrival
	// is a reorder, both handled with 16-bit wraparound.
	r.orderMu.Lock()
	if _, dup := r.seen[pkt.SequenceNumber]; dup {
		atomic.AddInt64(&r.stats.Duplicates, 1)
	} else if r.hasMax && SeqDelta(r.maxSeq, pkt.SequenceNumber) < 0 {
		atomic.AddInt64(&r.stats.Reordered, 1)
	}
	r.seen[pkt.SequenceNumber] = struct{}{}
	if !r.hasMax || SeqDelta(r.maxSeq, pkt.SequenceNumber) > 0 {
		r.maxSeq = pkt.SequenceNumber
		r.hasMax = true
	}
	r.orderMu.Unlock()
	atomic.AddInt64(&r.stats.Enqueued, 1)

	if r.mode == OverflowDrop {
		select {
		case r.queue <- entry:
			return true, nil
		default:
			atomic.AddInt64(&r.stats.Dropped, 1)
			return false, nil
		}
	}
	select {
	case r.queue <- entry:
		return true, nil
	case <-r.closed:
		atomic.AddInt64(&r.stats.Dropped, 1)
		return false, nil
	case <-ctx.Done():
		atomic.AddInt64(&r.stats.Dropped, 1)
		return false, ctx.Err()
	}
}

// Run drains the queue into sink until Close or ctx cancellation. Any
// packets still queued at shutdown are forwarded once without blocking;
// anything that cannot be drained counts as unforwarded via Stats.
func (r *Relay) Run(ctx context.Context, sink func(raw []byte)) {
	r.wg.Add(1)
	defer r.wg.Done()
	for {
		select {
		case entry := <-r.queue:
			sink(entry.raw)
			atomic.AddInt64(&r.stats.Forwarded, 1)
		case <-ctx.Done():
			r.drainRemaining(sink)
			return
		case <-r.closed:
			r.drainRemaining(sink)
			return
		}
	}
}

// drainRemaining forwards anything left in the queue without blocking.
func (r *Relay) drainRemaining(sink func(raw []byte)) {
	for {
		select {
		case entry := <-r.queue:
			sink(entry.raw)
			atomic.AddInt64(&r.stats.Forwarded, 1)
		default:
			return
		}
	}
}

// Close stops accepting packets. It never blocks on the consumer.
func (r *Relay) Close() {
	r.doneOnce.Do(func() { close(r.closed) })
}

// Wait blocks until Run has returned, so shutdown can prove goroutine exit.
func (r *Relay) Wait() { r.wg.Wait() }

// Pending reports how many packets are queued but not yet forwarded.
func (r *Relay) Pending() int { return len(r.queue) }

// Stats returns a consistent snapshot of all relay counters.
func (r *Relay) Stats() RelayStats {
	return RelayStats{
		Enqueued:   atomic.LoadInt64(&r.stats.Enqueued),
		Forwarded:  atomic.LoadInt64(&r.stats.Forwarded),
		Dropped:    atomic.LoadInt64(&r.stats.Dropped),
		Duplicates: atomic.LoadInt64(&r.stats.Duplicates),
		Reordered:  atomic.LoadInt64(&r.stats.Reordered),
		Malformed:  atomic.LoadInt64(&r.stats.Malformed),
	}
}
