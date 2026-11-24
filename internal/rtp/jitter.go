// Package rtp implements the jitter buffer and loss-recovery classification
// for incoming RTP packet streams. Packets are reordered by sequence number;
// duplicates are dropped, packets inside the late window are reinserted, and
// gaps that fall outside the window are declared lost and recorded.
package rtp

import (
	"github.com/pion/rtp"
)

// Stats counts packet-level events observed by the jitter buffer.
type Stats struct {
	Received   uint64 // packets pushed into the buffer
	Emitted    uint64 // packets released in sequence order
	Reordered  uint64 // packets that arrived out of order but inside the window
	Duplicates uint64 // packets seen more than once
	Late       uint64 // packets that arrived after their seq was declared lost
	Lost       uint64 // sequences declared lost (window exceeded)
}

// Buffer is a sequence-number ordered jitter buffer with a fixed reorder
// window (in packets). Memory is bounded by the window size.
type Buffer struct {
	window  int
	next    uint16 // next sequence number to emit
	started bool
	pending map[uint16]*rtp.Packet
	// recent records recently resolved sequences: true = emitted,
	// false = declared lost. Entries further than window behind next are
	// evicted, keeping the map bounded.
	recent map[uint16]bool
	stats  Stats
	lost   []uint16
}

// NewBuffer creates a jitter buffer holding at most windowPackets packets.
func NewBuffer(windowPackets int) *Buffer {
	if windowPackets < 1 {
		windowPackets = 1
	}
	return &Buffer{
		window:  windowPackets,
		pending: make(map[uint16]*rtp.Packet),
		recent:  make(map[uint16]bool),
	}
}

// Stats returns a copy of the current counters.
func (b *Buffer) Stats() Stats { return b.stats }

// Lost returns the sequences declared lost so far, in declaration order.
func (b *Buffer) Lost() []uint16 {
	out := make([]uint16, len(b.lost))
	copy(out, b.lost)
	return out
}

// Push ingests one packet in arrival order and returns the packets that
// became emit-ready in sequence order (possibly empty).
func (b *Buffer) Push(p *rtp.Packet) []*rtp.Packet {
	b.stats.Received++
	seq := p.SequenceNumber
	if !b.started {
		b.started = true
		b.next = seq
	}
	if seqLess(seq, b.next) {
		// Already resolved: duplicate (was emitted) or late (was lost).
		if emitted, ok := b.recent[seq]; ok && emitted {
			b.stats.Duplicates++
		} else if ok {
			b.stats.Late++
		} else {
			// Evicted from history; treat conservatively as duplicate.
			b.stats.Duplicates++
		}
		return nil
	}
	if _, dup := b.pending[seq]; dup {
		b.stats.Duplicates++
		return nil
	}
	if !seqEqual(seq, b.next) {
		b.stats.Reordered++
	}
	b.pending[seq] = p
	b.evict()
	return b.drain(false)
}

// Flush declares every remaining gap lost and returns the packets still
// pending, in sequence order.
func (b *Buffer) Flush() []*rtp.Packet {
	return b.drain(true)
}

// drain emits contiguous packets from next. When flush is false it only
// declares a gap lost once pending exceeds the window; when true it
// resolves everything.
func (b *Buffer) drain(flush bool) []*rtp.Packet {
	var out []*rtp.Packet
	for {
		if p, ok := b.pending[b.next]; ok {
			delete(b.pending, b.next)
			b.recent[b.next] = true
			b.stats.Emitted++
			out = append(out, p)
			b.next++
			continue
		}
		if !flush && len(b.pending) <= b.window {
			break
		}
		if len(b.pending) == 0 {
			break
		}
		// Gap exceeds the window (or flushing): declare next lost.
		b.recent[b.next] = false
		b.lost = append(b.lost, b.next)
		b.stats.Lost++
		b.next++
	}
	b.evict()
	return out
}

// evict drops history entries further than window behind next.
func (b *Buffer) evict() {
	for s := range b.recent {
		if seqLess(s, b.next-uint16(b.window)) {
			delete(b.recent, s)
		}
	}
}

// seqLess reports whether a comes before b in 16-bit sequence arithmetic.
func seqLess(a, b uint16) bool { return int16(a-b) < 0 }

func seqEqual(a, b uint16) bool { return a == b }
