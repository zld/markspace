package rtp

import (
	"github.com/pion/rtp"

	"mediasync/internal/clock"
)

// ReorderStatus classifies a packet's disposition at the reorder buffer.
type ReorderStatus int

const (
	StatusBuffered ReorderStatus = iota // accepted into the bounded buffer
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

// ReorderBuffer restores RTP sequence order over a bounded window. Push only
// buffers packets and commits loss counters; packets are always handed to the
// caller through Drain/Flush. Once a gap is committed, a late packet can never
// mutate the released boundary.
type ReorderBuffer struct {
	window   uint16
	expected uint16 // next sequence not yet released or committed lost
	started  bool
	highest  uint16 // highest sequence ever accepted
	gapOpen  bool
	buf      map[uint16]*rtp.Packet
	delayed  map[uint16]bool // buffered while a sequence gap was open
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

// Push accepts one parsed packet and returns its classification. It never
// releases a packet to the caller; use Drain or Flush for that.
func (b *ReorderBuffer) Push(pkt *rtp.Packet) ReorderStatus {
	b.c.PacketsIn++
	seq := pkt.SequenceNumber
	if !b.started {
		b.started = true
		b.expected, b.highest = seq, seq
		b.buf[seq] = pkt
		return StatusBuffered
	}
	// A sequence at or before the release frontier is immutable history.
	if clock.SeqDistance(b.expected, seq) < 0 {
		b.c.Late++
		return StatusLate
	}
	if _, exists := b.buf[seq]; exists {
		b.c.Duplicates++
		return StatusDuplicate
	}
	// A leap beyond highest+window commits every intervening sequence.
	if clock.SeqDistance(b.highest, seq) > int32(b.window) {
		b.commitGaps(seq)
		b.c.OutOfWindow++
		b.gapOpen = false
		b.highest = seq
		b.buf[seq] = pkt
		return StatusOutOfWindow
	}
	if len(b.buf) >= clock.MaxQueueDepth {
		b.c.QueueOverrun++
		return StatusQueueOverflow
	}
	prevHighest := b.highest
	advance := clock.SeqDistance(b.highest, seq)
	if advance > 1 {
		b.gapOpen = true
	}
	if b.gapOpen && clock.SeqDistance(prevHighest, seq) <= 0 {
		b.delayed[seq] = true
	}
	if advance > 0 {
		b.highest = seq
	}
	b.buf[seq] = pkt
	b.slideWindow()
	if clock.SeqDistance(b.expected, b.highest) <= 0 && !b.hasHole() {
		b.gapOpen = false
	}
	return StatusBuffered
}

// hasHole reports whether a missing sequence lies in [expected, highest].
func (b *ReorderBuffer) hasHole() bool {
	for s := b.expected; clock.SeqDistance(s, b.highest) > 0; s++ {
		if _, ok := b.buf[s]; !ok {
			return true
		}
	}
	return false
}

// commitGaps marks every missing sequence in [expected, end) as lost and
// advances expected, leaving any buffered packets in place for Drain.
func (b *ReorderBuffer) commitGaps(end uint16) {
	for clock.SeqDistance(b.expected, end) > 0 {
		if _, ok := b.buf[b.expected]; !ok {
			b.c.Lost++
		}
		b.expected++
	}
}

// slideWindow commits holes that have aged out of the reorder window, but
// never discards buffered packets: they remain drainable by the caller.
func (b *ReorderBuffer) slideWindow() {
	for b.hasHole() && clock.SeqDistance(b.expected, b.highest) > int32(b.window) {
		if _, ok := b.buf[b.expected]; !ok {
			b.c.Lost++
		}
		b.expected++
	}
}

// release accounts and emits one buffered packet.
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
// sequence and advances the frontier. Committed holes are skipped.
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
