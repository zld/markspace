// Package rtp contains the ingest-side RTP reordering and H.264 access-unit
// reassembly logic.
package rtp

import (
	"sort"
	"sync"

	"github.com/pion/rtp"
)

const (
	// MaxReorderBuffer is the maximum number of out-of-order packets kept
	// while waiting for a missing sequence number.
	MaxReorderBuffer = 64
	seqModulo        = 65536
)

// ReorderStats collects reordering counters.
type ReorderStats struct {
	// Received is the total number of packets offered to the buffer.
	Received uint64
	// Duplicates is the number of packets whose sequence number was already
	// observed.
	Duplicates uint64
	// LateOutOfWindow is the number of unseen packets that arrived after
	// their sequence number had already been passed or flushed.
	LateOutOfWindow uint64
	// Gaps is the number of distinct missing sequence numbers observed.
	// Any given sequence number is counted at most once.
	Gaps uint64
	// Flushed is the number of packets released by an explicit Flush.
	Flushed uint64
}

// ReorderBuffer sorts RTP packets by sequence number modulo 65536.
//
// It recognises sequence-number wraparound, duplicate packets, late packets
// falling outside the 64-packet window and holes (gaps). A missing sequence
// number contributes to ReorderStats.Gaps at most once.
type ReorderBuffer struct {
	mu sync.Mutex

	initialized bool
	expected    uint16

	// slots holds out-of-order packets, indexed by sequence number & 63.
	slots [MaxReorderBuffer]*rtp.Packet
	// seen is an exact bitmap of every sequence number already observed,
	// which makes duplicate detection independent of slot reuse.
	seen [seqModulo / 8]byte
	// gapCounted records holes already added to the gap counter.
	gapCounted map[uint16]struct{}

	stats ReorderStats
}

// NewReorderBuffer creates an empty reorder buffer.
func NewReorderBuffer() *ReorderBuffer {
	return &ReorderBuffer{gapCounted: map[uint16]struct{}{}}
}

// Push ingests one packet and returns the packets that can now be delivered
// in sequence order. The returned slice is reused on the next call and must
// not be retained.
func (b *ReorderBuffer) Push(pkt *rtp.Packet) []*rtp.Packet {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.stats.Received++
	seq := pkt.SequenceNumber

	if !b.initialized {
		b.initialized = true
		b.expected = seq + 1
		b.markSeen(seq)
		return []*rtp.Packet{pkt}
	}

	if b.wasSeen(seq) {
		b.stats.Duplicates++
		return nil
	}

	rel := int16(seq - b.expected)

	switch {
	case rel < 0:
		// unseen, but the sequence number is already behind the in-order
		// cursor: it arrived too late to be placed in the window.
		b.stats.LateOutOfWindow++
		b.markSeen(seq)
		return nil

	case rel >= MaxReorderBuffer:
		// jump beyond the window: account for the holes once, release the
		// buffered packets, then resynchronise on the new packet.
		b.countGaps(b.expected, seq)
		out := b.drainLocked()
		b.expected = seq + 1
		b.markSeen(seq)
		return append(out, pkt)

	case rel > 0:
		// forward jump within the window: remember the holes and park the
		// packet until the missing predecessors arrive.
		b.countGaps(b.expected, seq)
		b.slots[seq&(MaxReorderBuffer-1)] = pkt
		b.markSeen(seq)
		return nil
	}

	// rel == 0: the expected in-order packet. Deliver it and any
	// consecutively buffered successors.
	b.markSeen(seq)
	b.expected = seq + 1
	out := []*rtp.Packet{pkt}
	out = b.deliverChainLocked(out)
	return out
}

// Flush releases every packet still held in the buffer in sequence order and
// counts any remaining holes as gaps. It models a timeout that gives up
// waiting for the missing packets.
func (b *ReorderBuffer) Flush() []*rtp.Packet {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.initialized {
		return nil
	}
	return b.drainLocked()
}

// Stats returns a copy of the current counters.
func (b *ReorderBuffer) Stats() ReorderStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stats
}

func (b *ReorderBuffer) deliverChainLocked(out []*rtp.Packet) []*rtp.Packet {
	for {
		idx := b.expected & (MaxReorderBuffer - 1)
		pkt := b.slots[idx]
		if pkt == nil || pkt.SequenceNumber != b.expected {
			return out
		}
		b.slots[idx] = nil
		delete(b.gapCounted, b.expected)
		out = append(out, pkt)
		b.expected++
	}
}

// drainLocked emits all buffered packets in sequence order, accounting for
// the holes between them, and advances the cursor past the last one.
func (b *ReorderBuffer) drainLocked() []*rtp.Packet {
	var queued []*rtp.Packet
	for i, pkt := range b.slots {
		if pkt != nil {
			queued = append(queued, pkt)
			b.slots[i] = nil
		}
	}
	if len(queued) == 0 {
		return nil
	}

	// sort by signed 16-bit distance from the cursor so wraparound works
	sort.Slice(queued, func(i, j int) bool {
		return int16(queued[i].SequenceNumber-b.expected) <
			int16(queued[j].SequenceNumber-b.expected)
	})

	out := make([]*rtp.Packet, 0, len(queued))
	for _, pkt := range queued {
		b.countGaps(b.expected, pkt.SequenceNumber)
		delete(b.gapCounted, pkt.SequenceNumber)
		out = append(out, pkt)
		b.expected = pkt.SequenceNumber + 1
		b.stats.Flushed++
	}
	return out
}

// countGaps records every still-missing sequence number in [from, to) once.
// markSeen removes a sequence from this set (and refunds its counter) when
// the late packet finally arrives, so only holes that survive a flush count.
func (b *ReorderBuffer) countGaps(from, to uint16) {
	for seq := from; seq != to; seq++ {
		if _, ok := b.gapCounted[seq]; !ok && !b.wasSeen(seq) {
			b.gapCounted[seq] = struct{}{}
			b.stats.Gaps++
		}
	}
}

func (b *ReorderBuffer) markSeen(seq uint16) {
	if _, ok := b.gapCounted[seq]; ok {
		delete(b.gapCounted, seq)
		b.stats.Gaps--
	}
	b.seen[seq/8] |= 1 << (seq % 8)
}

func (b *ReorderBuffer) wasSeen(seq uint16) bool {
	return b.seen[seq/8]&(1<<(seq%8)) != 0
}
