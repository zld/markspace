// Package rtp implements per-representation RTP reception: a bounded jitter
// buffer that reorders packets by sequence number with uint16 wrap handling,
// classifies late/duplicate/out-of-order arrivals, and applies a deterministic
// eviction rule when the queue fills. Each representation owns an independent
// buffer so a lossy layer can never disturb another layer's window.
package rtp

import (
	"mediasync/internal/model"
)

// Counters aggregate the arrival classifications required by the acceptance
// report. All counters are monotonic for the buffer's lifetime.
type Counters struct {
	Received   int // distinct, in-window packets accepted
	Late       int // packets arriving after their window closed
	Duplicate  int // repeated sequence numbers that were already seen
	OutOfOrder int // accepted packets whose sequence number went backwards
	Evicted    int // packets dropped because the bounded queue was full
}

// Eviction records why a packet was removed from a full queue.
type Eviction struct {
	Sequence uint16
	Reason   model.ErrorKind
}

// maxSeqDistance is the largest forward jump treated as reordering rather
// than a new generation; it also bounds the uint16 wrap arithmetic.
const maxSeqDistance = 1 << 15

// seqDiff returns the signed distance b-a in uint16 circular space.
func seqDiff(b, a uint16) int {
	d := int(b) - int(a)
	if d > maxSeqDistance {
		d -= 1 << 16
	} else if d < -maxSeqDistance {
		d += 1 << 16
	}
	return d
}

// JitterBuffer is one representation's bounded reorder buffer.
type JitterBuffer struct {
	repID      string
	capacity   int
	window     int // playout window size in sequence numbers
	packets    map[uint16][]byte
	order      []uint16 // insertion order of keys still in the map
	base       uint16   // oldest sequence number still inside the window
	haveBase   bool
	highest    uint16
	haveHigh   bool
	seen       map[uint16]bool // every sequence number ever observed
	stats      Counters
	evictions  []Eviction
}

// NewJitterBuffer creates a buffer for one representation. capacity bounds the
// number of retained packets; window bounds how far behind the highest
// sequence number a packet may be and still count as in-window.
func NewJitterBuffer(repID string, capacity, window int) *JitterBuffer {
	if capacity < 1 {
		capacity = 1
	}
	if window < 1 {
		window = 1
	}
	return &JitterBuffer{
		repID:    repID,
		capacity: capacity,
		window:   window,
		packets:  make(map[uint16][]byte),
		seen:     make(map[uint16]bool),
	}
}

// Push classifies and stores one packet. The returned error is non-nil only
// for late/duplicate/eviction events; callers decide whether to record them.
// A successfully buffered packet always returns nil.
func (b *JitterBuffer) Push(seq uint16, payload []byte) *model.MediaError {
	if b.seen[seq] {
		b.stats.Duplicate++
		return model.NewError(model.KindPacketLost, "rtp", int64(seq),
			"rep %s duplicate packet seq=%d", b.repID, seq)
	}
	b.seen[seq] = true

	if b.haveHigh && seqDiff(seq, b.highest) < -b.window {
		b.stats.Late++
		return model.NewError(model.KindPacketLost, "rtp", int64(seq),
			"rep %s late packet seq=%d, high=%d window=%d", b.repID, seq, b.highest, b.window)
	}

	if b.haveHigh && seqDiff(seq, b.highest) < 0 {
		b.stats.OutOfOrder++
	}

	if len(b.packets) >= b.capacity {
		// Deterministic rule: evict the oldest retained sequence number.
		oldest := b.order[0]
		delete(b.packets, oldest)
		b.order = b.order[1:]
		b.stats.Evicted++
		b.evictions = append(b.evictions, Eviction{Sequence: oldest, Reason: model.KindEviction})
	}

	b.packets[seq] = append([]byte(nil), payload...)
	b.order = append(b.order, seq)
	b.stats.Received++

	if !b.haveBase || seqDiff(seq, b.base) < 0 {
		b.base = seq
	}
	if !b.haveHigh || seqDiff(seq, b.highest) > 0 {
		b.highest = seq
	}
	b.haveBase, b.haveHigh = true, true
	return nil
}

// Flush returns the buffered payloads in ascending circular sequence order,
// clearing the buffer.
func (b *JitterBuffer) Flush() []OrderedPacket {
	out := make([]OrderedPacket, 0, len(b.packets))
	for seq, payload := range b.packets {
		out = append(out, OrderedPacket{Sequence: seq, Payload: payload})
	}
	sortPackets(out)
	b.packets = make(map[uint16][]byte)
	b.order = nil
	return out
}

// OrderedPacket is a sequence-numbered payload emitted in playout order.
type OrderedPacket struct {
	Sequence uint16
	Payload  []byte
}

// Stats returns a copy of the lifetime counters.
func (b *JitterBuffer) Stats() Counters { return b.stats }

// Evictions returns a copy of the recorded eviction decisions.
func (b *JitterBuffer) Evictions() []Eviction {
	out := make([]Eviction, len(b.evictions))
	copy(out, b.evictions)
	return out
}

// Highest returns the highest sequence number observed and whether any packet
// has arrived yet.
func (b *JitterBuffer) Highest() (uint16, bool) { return b.highest, b.haveHigh }

// Len is the number of currently buffered packets.
func (b *JitterBuffer) Len() int { return len(b.packets) }

// sortPackets orders packets by circular distance from the smallest sequence
// number present, which is correct inside one reorder window.
func sortPackets(pkts []OrderedPacket) {
	base := pkts[0].Sequence
	for _, p := range pkts[1:] {
		if seqDiff(p.Sequence, base) < 0 {
			base = p.Sequence
		}
	}
	// insertion sort: buffers are small and determinism matters more than speed
	for i := 1; i < len(pkts); i++ {
		for j := i; j > 0 && seqDiff(pkts[j].Sequence, pkts[j-1].Sequence) < 0; j-- {
			pkts[j], pkts[j-1] = pkts[j-1], pkts[j]
		}
	}
}
