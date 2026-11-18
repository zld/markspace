package rtp

import (
	"time"
)

// ReorderStats accumulates reordering counters for one run.
type ReorderStats struct {
	// Received is the number of in-order-or-future packets accepted.
	Received int
	// Duplicates counts packets whose sequence was already emitted or
	// superseded (late duplicates included); they never reach the output.
	Duplicates int
	// MissingPackets counts individual sequence numbers that were skipped.
	MissingPackets int
	// GapEvents counts contiguous runs of missing sequences.
	GapEvents int
	// TimeoutFlushes counts flushes forced by the reorder timeout.
	TimeoutFlushes int
}

type buffered struct {
	pkt *Packet
	at  time.Time
}

// Reorderer restores RTP packet order using a bounded window over 16-bit
// sequence numbers with full uint16 wraparound support.
type Reorderer struct {
	window   int
	timeout  time.Duration
	init     bool
	expected uint32 // extended sequence number of the next packet to emit
	entries  map[uint32]buffered
	stats    ReorderStats
}

// NewReorderer builds a Reorderer. window and timeout are validated by config.
func NewReorderer(window int, timeout time.Duration) *Reorderer {
	return &Reorderer{
		window:  window,
		timeout: timeout,
		entries: make(map[uint32]buffered),
	}
}

// Stats returns a copy of the current counters.
func (r *Reorderer) Stats() ReorderStats { return r.stats }

// Reset drops all buffered state so a later run cannot see stale packets.
func (r *Reorderer) Reset() {
	r.init = false
	r.expected = 0
	r.entries = make(map[uint32]buffered)
	r.stats = ReorderStats{}
}

// extend maps a 16-bit sequence onto the extended 32-bit numbering space
// anchored on the next expected sequence.
func (r *Reorderer) extend(seq uint16) uint32 {
	delta := int16(seq - uint16(r.expected))
	return r.expected + uint32(int32(delta))
}

// Push inserts a packet and returns the packets that became ready, in order.
// Duplicates and late packets are counted and dropped.
func (r *Reorderer) Push(pkt *Packet, now time.Time) []*Packet {
	ext := r.extend(pkt.Header.SequenceNumber)
	if !r.init {
		r.init = true
		r.expected = ext
	} else if ext < r.expected {
		// Already emitted, or older than an overflow-advanced window: the
		// packet must never re-enter an output frame.
		r.stats.Duplicates++
		return nil
	}

	if _, exists := r.entries[ext]; exists {
		r.stats.Duplicates++
		return nil
	}

	// Far-ahead packet: it cannot fit the bounded window. Flush everything
	// below it first, registering the missing sequences as gaps.
	if r.init && int(ext-r.expected) >= r.window {
		out := r.advanceTo(ext, now)
		r.entries[ext] = buffered{pkt: pkt, at: now}
		r.stats.Received++
		return append(out, r.drain()...)
	}

	r.entries[ext] = buffered{pkt: pkt, at: now}
	r.stats.Received++
	return r.drain()
}

// drain emits every contiguous packet starting at expected.
func (r *Reorderer) drain() []*Packet {
	var out []*Packet
	for {
		e, ok := r.entries[r.expected]
		if !ok {
			return out
		}
		delete(r.entries, r.expected)
		out = append(out, e.pkt)
		r.expected++
	}
}

// advanceTo moves the emit frontier up to target, counting missing sequences.
// Buffered packets below target are returned; missing ones become gaps.
func (r *Reorderer) advanceTo(target uint32, now time.Time) []*Packet {
	var out []*Packet
	inGap := false
	for r.expected < target {
		if e, ok := r.entries[r.expected]; ok {
			delete(r.entries, r.expected)
			out = append(out, e.pkt)
			inGap = false
		} else {
			r.stats.MissingPackets++
			if !inGap {
				r.stats.GapEvents++
				inGap = true
			}
		}
		r.expected++
	}
	return out
}

// Tick applies the reorder timeout: when the head packet is missing and the
// oldest buffered packet has waited longer than the timeout, the frontier is
// advanced past the hole so waiting packets are not held indefinitely.
func (r *Reorderer) Tick(now time.Time) []*Packet {
	if !r.init || len(r.entries) == 0 {
		return nil
	}
	if _, ok := r.entries[r.expected]; ok {
		return nil // head present; normal draining will make progress
	}
	oldest := now
	oldestSeq := uint32(0)
	found := false
	for seq, e := range r.entries {
		if !found || e.at.Before(oldest) {
			oldest, oldestSeq, found = e.at, seq, true
		}
	}
	if !found || now.Sub(oldest) < r.timeout {
		return nil
	}
	r.stats.TimeoutFlushes++
	out := r.advanceTo(oldestSeq, now)
	return append(out, r.drain()...)
}
