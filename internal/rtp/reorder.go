// Package rtp reorders RTP packets inside a sequence-number window and
// accounts for duplicates, recovered reordering, losses and late arrivals.
// It never fabricates media data: only packets observed on the wire leave
// the buffer.
package rtp

import (
	"github.com/pion/rtp"
)

// Stats summarizes the lifetime behaviour of a ReorderBuffer.
type Stats struct {
	Expected   uint32 // sequence slots visited from the first packet to the frontier
	Received   uint32 // unique packets accepted
	Duplicates int    // packets whose sequence number was already seen
	Recovered  int    // out-of-order packets successfully delivered in order
	Lost       int    // gaps declared lost once the window moved past them
	Late       int    // packets arriving after their slot was declared lost
}

// ReorderBuffer holds out-of-order packets until sequence continuity allows
// delivery, or until the window forces the missing slots to be declared lost.
type ReorderBuffer struct {
	window  uint16
	buf     map[uint16]*rtp.Packet
	reorder map[uint16]bool // slots whose packet arrived out of order
	seen    map[uint16]bool
	next    uint16 // next sequence number expected for in-order delivery
	maxSeq  uint16 // highest sequence number observed
	started bool
	stats   Stats
}

// NewReorderBuffer creates a buffer. windowSize is the maximum reorder depth
// in packets; slots older than the window cannot be recovered.
func NewReorderBuffer(windowSize uint16) *ReorderBuffer {
	if windowSize == 0 {
		windowSize = 1
	}
	return &ReorderBuffer{
		window:  windowSize,
		buf:     map[uint16]*rtp.Packet{},
		reorder: map[uint16]bool{},
		seen:    map[uint16]bool{},
	}
}

// seqDistance returns the forward modular distance from a to b over uint16.
func seqDistance(a, b uint16) uint16 { return b - a }

// Feed accepts one parsed packet. It returns the packets that can now be
// delivered in sequence order (possibly empty) and whether the input was a
// duplicate. No stand-in packets are ever emitted for missing sequence slots.
func (b *ReorderBuffer) Feed(pkt *rtp.Packet) (ready []*rtp.Packet, duplicate bool) {
	seq := pkt.SequenceNumber
	if !b.started {
		b.started = true
		b.next, b.maxSeq = seq, seq
		b.seen[seq] = true
		b.buf[seq] = pkt
		b.stats.Received++
		b.stats.Expected++
		return b.drain(), false
	}
	if b.isBehind(seq) {
		if b.seen[seq] {
			b.stats.Duplicates++
			return nil, true
		}
		b.stats.Late++
		return nil, false
	}
	if b.seen[seq] {
		b.stats.Duplicates++
		return nil, true
	}
	if seq != b.next {
		b.reorder[seq] = true
	}
	b.seen[seq] = true
	b.buf[seq] = pkt
	b.stats.Received++
	if d := seqDistance(b.maxSeq, seq); d > 0 && d < 0x8000 {
		b.maxSeq = seq
	}
	b.advanceWindow()
	return b.drain(), false
}

// isBehind reports whether seq belongs to a slot the delivery frontier has
// already passed (behind by less than half the 16-bit space).
func (b *ReorderBuffer) isBehind(seq uint16) bool {
	if seq == b.next {
		return false
	}
	d := seqDistance(seq, b.next)
	return d > 0 && d < 0x8000
}

// advanceWindow declares slots lost once the observed frontier is at least
// windowSize ahead of the delivery frontier.
func (b *ReorderBuffer) advanceWindow() {
	for seqDistance(b.next, b.maxSeq) >= b.window {
		if _, ok := b.buf[b.next]; !ok {
			b.stats.Lost++
		}
		b.passNext()
	}
}

// drain emits every contiguous buffered packet starting at next. Packets that
// arrived out of order count as recovered at the moment of in-order delivery.
func (b *ReorderBuffer) drain() []*rtp.Packet {
	var out []*rtp.Packet
	for {
		pkt, ok := b.buf[b.next]
		if !ok {
			break
		}
		delete(b.buf, b.next)
		if b.reorder[b.next] {
			b.stats.Recovered++
			delete(b.reorder, b.next)
		}
		out = append(out, pkt)
		b.passNext()
	}
	return out
}

func (b *ReorderBuffer) passNext() {
	b.next++
	b.stats.Expected++
}

// Flush delivers every remaining buffered packet in sequence order; any
// missing slots between them are declared lost. Call it at end of stream.
func (b *ReorderBuffer) Flush() []*rtp.Packet {
	if !b.started {
		return nil
	}
	var out []*rtp.Packet
	for len(b.buf) > 0 {
		if pkt, ok := b.buf[b.next]; ok {
			delete(b.buf, b.next)
			if b.reorder[b.next] {
				b.stats.Recovered++
				delete(b.reorder, b.next)
			}
			out = append(out, pkt)
		} else {
			b.stats.Lost++
		}
		b.passNext()
	}
	return out
}

// Stats returns a copy of the running counters.
func (b *ReorderBuffer) Stats() Stats { return b.stats }

// LossRecoveryRate is recovered / (recovered + lost). It is 1.0 when no
// disorder event occurred at all.
func (b *ReorderBuffer) LossRecoveryRate() float64 {
	denom := b.stats.Recovered + b.stats.Lost
	if denom == 0 {
		return 1
	}
	return float64(b.stats.Recovered) / float64(denom)
}
