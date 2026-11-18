package rtp

import (
	"time"
)

// ReorderStats accumulates reordering counters for one run.
type ReorderStats struct {
	// Received is the number of packets accepted into the reorderer.
	Received int
	// Duplicates counts packets whose 16-bit sequence is already buffered or
	// emitted; they never reach the output.
	Duplicates int
	// LatePackets counts packets older than the emit frontier that arrive
	// after the gap already advanced. They are dropped immediately and can
	// never re-enter an output frame.
	LatePackets int
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
//
// The emit frontier lives in a 64-bit extended space so it never underflows
// when the 16-bit counter wraps; buffered packets are keyed by the same
// extended number modulo 2^32, which never collides inside a bounded window.
type Reorderer struct {
	window   int
	timeout  time.Duration
	init     bool
	expected uint64 // extended sequence number of the next packet to emit
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

// extend maps a 16-bit sequence onto the 64-bit extended numbering space
// anchored on the next expected sequence. A packet within half of the 16-bit
// space ahead of the frontier is treated as future traffic (including a wrap),
// anything farther behind as a genuinely late, older packet.
func (r *Reorderer) extend(seq uint16) uint64 {
	front := uint16(r.expected)
	delta := int32(seq) - int32(front) // -65535..65535, no truncation
	if delta >= 0x8000 {
		delta -= 0x10000 // seq is actually one cycle older
	}
	return r.expected + uint64(int64(delta))
}

// Push inserts a packet and returns the packets that became ready, in order.
// Duplicates and late packets are counted and dropped.
func (r *Reorderer) Push(pkt *Packet, now time.Time) []*Packet {
	if !r.init {
		// Anchor the first observed packet low in the extended space so a
		// later uint16 wrap increments rather than underflows the frontier.
		r.init = true
		r.expected = uint64(pkt.Header.SequenceNumber)
	}
	ext := r.extend(pkt.Header.SequenceNumber)
	if ext < r.expected {
		// Behind the emit frontier. A packet one step behind is the immediate
		// retransmission of the last emitted packet (a duplicate); anything
		// farther behind is late traffic for an already flushed gap.
		if r.expected-ext == 1 {
			r.stats.Duplicates++
		} else {
			r.stats.LatePackets++
		}
		return nil
	}

	key := uint32(ext)
	if _, exists := r.entries[key]; exists {
		r.stats.Duplicates++
		return nil
	}

	// Far-ahead packet: it cannot fit the bounded window. Flush everything
	// below it first, registering the missing sequences as gaps.
	if int(ext-r.expected) >= r.window {
		out := r.advanceTo(ext)
		r.entries[key] = buffered{pkt: pkt, at: now}
		r.stats.Received++
		return append(out, r.drain()...)
	}

	r.entries[key] = buffered{pkt: pkt, at: now}
	r.stats.Received++
	return r.drain()
}

// drain emits every contiguous packet starting at expected.
func (r *Reorderer) drain() []*Packet {
	var out []*Packet
	for {
		e, ok := r.entries[uint32(r.expected)]
		if !ok {
			return out
		}
		delete(r.entries, uint32(r.expected))
		out = append(out, e.pkt)
		r.expected++
	}
}

// advanceTo moves the emit frontier up to target, counting missing sequences.
// Buffered packets below target are returned; missing ones become gaps.
func (r *Reorderer) advanceTo(target uint64) []*Packet {
	var out []*Packet
	inGap := false
	for r.expected < target {
		key := uint32(r.expected)
		if e, ok := r.entries[key]; ok {
			delete(r.entries, key)
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
	if _, ok := r.entries[uint32(r.expected)]; ok {
		return nil // head present; normal draining will make progress
	}
	var oldest time.Time
	oldestKey := uint32(0)
	found := false
	for key, e := range r.entries {
		if !found || e.at.Before(oldest) {
			oldest, oldestKey, found = e.at, key, true
		}
	}
	if !found || now.Sub(oldest) < r.timeout {
		return nil
	}
	r.stats.TimeoutFlushes++
	// Recover the buffered packet's extended sequence from its key.
	target := r.expected + uint64(int64(int32(oldestKey-uint32(r.expected))))
	out := r.advanceTo(target)
	return append(out, r.drain()...)
}

// Flush ends a stream: every hole is abandoned. The frontier advances to one
// past the furthest buffered packet, all remaining missing sequences are
// registered as gaps, and buffered packets are returned in order.
func (r *Reorderer) Flush(now time.Time) []*Packet {
	if !r.init || len(r.entries) == 0 {
		return nil
	}
	maxKey := uint32(0)
	found := false
	for key := range r.entries {
		if !found || delta(key, uint32(r.expected)) > delta(maxKey, uint32(r.expected)) {
			maxKey, found = key, true
		}
	}
	if !found {
		return nil
	}
	target := r.expected + uint64(int64(int32(maxKey-uint32(r.expected)))) + 1
	return r.advanceTo(target)
}

// delta returns a signed 32-bit distance between two sequence keys.
func delta(a, b uint32) int32 { return int32(a - b) }
