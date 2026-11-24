package rtp

import (
	"github.com/pion/rtp"

	"mediasync/internal/clock"
)

// ReorderStatus classifies a packet's disposition at the reorder buffer.
type ReorderStatus int

const (
	StatusBuffered ReorderStatus = iota // accepted, awaiting in-order release
	StatusDuplicate
	StatusLate          // arrived after its boundary was committed
	StatusOutOfWindow   // beyond reorder window; intervening gaps committed
	StatusQueueOverflow // bounded queue is full
)

// String renders the status for reports.
func (s ReorderStatus) String() string {
	switch s {
	case StatusBuffered:
		return "buffered"
	case StatusDuplicate:
		return "duplicate"
	case StatusLate:
		return "late"
	case StatusOutOfWindow:
		return "out_of_window"
	case StatusQueueOverflow:
		return "queue_overflow"
	default:
		return "unknown"
	}
}

// ReorderCounters aggregates the reorder decisions for the metrics report.
type ReorderCounters struct {
	PacketsIn    uint64
	Emitted      uint64
	InOrder      uint64
	Recovered    uint64 // out-of-order packets released before gap expiry
	Lost         uint64 // gaps committed once the window slid past them
	Duplicates   uint64
	Late         uint64
	OutOfWindow  uint64
	QueueOverrun uint64
}

// ReorderBuffer restores RTP sequence order over a bounded window. Once a gap
// is committed (the window has slid past it), late packets can no longer
// mutate the released boundary.
type ReorderBuffer struct {
	window   uint16
	expected uint16
	started  bool
	highest  uint16
	gapOpen  bool // at least one missing seq lies between expected and highest
	buf      map[uint16]*rtp.Packet
	delayed  map[uint16]bool // buffered while gapOpen was true
	c        ReorderCounters
}

// NewReorderBuffer builds a buffer with the given maximum reorder window.
func NewReorderBuffer(window uint16) (*ReorderBuffer, *clock.Error) {
	if int(window) > clock.MaxQueueDepth || window < 2 {
		return nil, clock.NewError(clock.ErrInvalidLength, "reorder window outside [2, MaxQueueDepth]")
	}
	return &ReorderBuffer{
		window:  window,
		buf:     make(map[uint16]*rtp.Packet),
		delayed: make(map[uint16]bool),
	}, nil
}

// Push accepts one parsed packet and returns its classification.
func (b *ReorderBuffer) Push(pkt *rtp.Packet) ReorderStatus {
	b.c.PacketsIn++
	seq := pkt.SequenceNumber
	if !b.started {
		b.started = true
		b.expected, b.highest = seq, seq
		b.buf[seq] = pkt
		return StatusBuffered
	}
	d := clock.SeqDistance(b.expected, seq)
	if d < 0 {
		b.c.Late++ // boundary already committed: immutable
		return StatusLate
	}
	if _, exists := b.buf[seq]; exists {
		b.c.Duplicates++
		return StatusDuplicate
	}
	if d > int32(b.window) {
		// Commit everything up to the new frontier, then resume there.
		b.commitRange(seq)
		b.c.OutOfWindow++
		b.buf[seq] = pkt
		b.highest = seq
		b.gapOpen = false
		return StatusOutOfWindow
	}
	if len(b.buf) >= clock.MaxQueueDepth {
		b.c.QueueOverrun++
		return StatusQueueOverflow
	}
	// Advance the buffered frontier first; gap classification then compares
	// the jump against the previous highest sequence.
	prevHighest := b.highest
	advance := clock.SeqDistance(b.highest, seq)
	if advance > 0 {
		b.highest = seq
	}
	// A jump beyond the immediate next sequence opens a gap; packets that
	// arrive behind an open gap recover it rather than delivering in order.
	if advance > 1 {
		b.gapOpen = true
	}
	if b.gapOpen && clock.SeqDistance(prevHighest, seq) <= 0 {
		b.delayed[seq] = true
	}
	b.buf[seq] = pkt
	b.expireGaps()
	if clock.SeqDistance(b.expected, b.highest) <= 0 {
		b.gapOpen = false
	}
	return StatusBuffered
}

// commitRange releases or marks lost every sequence in [expected, end).
func (b *ReorderBuffer) commitRange(end uint16) {
	for clock.SeqDistance(b.expected, end) > 0 {
		if pkt, ok := b.buf[b.expected]; ok {
			b.release(pkt)
			delete(b.buf, b.expected)
		} else {
			b.c.Lost++
		}
		b.expected++
	}
}

// expireGaps commits gaps that have fallen outside the window.
func (b *ReorderBuffer) expireGaps() {
	for clock.SeqDistance(b.expected, b.highest) > int32(b.window) {
		if pkt, ok := b.buf[b.expected]; ok {
			b.release(pkt)
			delete(b.buf, b.expected)
		} else {
			b.c.Lost++
		}
		b.expected++
	}
}

// release emits one packet and accounts whether it was delayed.
func (b *ReorderBuffer) release(pkt *rtp.Packet) {
	if b.delayed[pkt.SequenceNumber] {
		b.c.Recovered++
		delete(b.delayed, pkt.SequenceNumber)
	} else {
		b.c.InOrder++
	}
	b.c.Emitted++
}

// Drain returns all currently contiguous packets starting at the expected
// sequence, without committing gaps.
func (b *ReorderBuffer) Drain() []*rtp.Packet {
	out := make([]*rtp.Packet, 0, len(b.buf))
	for {
		pkt, ok := b.buf[b.expected]
		if !ok {
			return out
		}
		b.release(pkt)
		delete(b.buf, b.expected)
		b.expected++
		out = append(out, pkt)
	}
}

// Flush releases every buffered packet in order and commits every remaining
// hole as loss. Used at end of stream.
func (b *ReorderBuffer) Flush() []*rtp.Packet {
	out := make([]*rtp.Packet, 0, len(b.buf))
	if !b.started {
		return out
	}
	// Iterate over every sequence in [expected, highest] regardless of holes:
	// a missing seq must not terminate the scan of later buffered packets.
	span := clock.SeqDistance(b.expected, b.highest)
	for i := int32(0); i <= span; i++ {
		seq := b.expected
		if pkt, ok := b.buf[seq]; ok {
			b.release(pkt)
			delete(b.buf, seq)
			out = append(out, pkt)
		} else {
			b.c.Lost++
		}
		b.expected++
	}
	b.gapOpen = false
	b.buf = make(map[uint16]*rtp.Packet)
	b.delayed = make(map[uint16]bool)
	return out
}

// Counters returns a snapshot of reorder decisions.
func (b *ReorderBuffer) Counters() ReorderCounters { return b.c }
