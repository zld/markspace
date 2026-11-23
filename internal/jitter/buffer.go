// Package jitter implements a deterministic reorder buffer for RTP audio.
//
// Packets are ordered by extended sequence number. A contiguous run below the
// reorder watermark is committed exactly once and can never be rewritten by a
// late arrival. Duplicates and packets that arrive after commit are dropped
// with an explicit reason, as are packets that exceed the memory window. Every
// drop is recorded as a fault so the provenance manifest can explain it.
package jitter

import (
	"fmt"
	"sort"

	"mediarun/internal/fault"
	rtpparse "mediarun/internal/rtp"
)

// DropReason explains why a packet did not enter the committed stream.
type DropReason string

const (
	ReasonNone      DropReason = ""
	ReasonDuplicate DropReason = "duplicate"
	ReasonLate      DropReason = "late_after_commit"
	ReasonOverflow  DropReason = "window_overflow"
	ReasonConfig    DropReason = "config"
)

// Config bounds the buffer. Window is the reorder depth in sequence numbers;
// MaxPackets caps resident packets independently of Window.
type Config struct {
	Window     uint64
	MaxPackets int
}

// Result reports the outcome of one Push.
type Result struct {
	Extended  uint64
	Reason    DropReason
	Committed []*rtpparse.Packet // packets released by this push, in order
}

// Stats are cumulative counters for manifests.
type Stats struct {
	Received   int
	Accepted   int
	Duplicates int
	Late       int
	Overflow   int
	Committed  int
	outOfOrder int
}

// OutOfOrder returns how many accepted packets arrived below the highest
// sequence seen so far.
func (s Stats) OutOfOrder() int { return s.outOfOrder }

// Buffer is a single-track jitter window.
type Buffer struct {
	cfg     Config
	seq     rtpparse.SequenceWindow
	pending map[uint64]*rtpparse.Packet
	highest uint64
	floor   uint64
	sawAny  bool
	stats   Stats
	faults  fault.List
}

// New validates config and returns an empty buffer.
func New(cfg Config) (*Buffer, error) {
	if cfg.Window == 0 {
		return nil, fault.New(fault.StageJitter, fault.KindConfig, "jitter window must be positive")
	}
	if cfg.MaxPackets <= 0 {
		return nil, fault.New(fault.StageJitter, fault.KindConfig, "max packets must be positive")
	}
	return &Buffer{cfg: cfg, pending: make(map[uint64]*rtpparse.Packet)}, nil
}

// Push inserts a packet. Committed packets released by this call are returned
// inside Result in ascending sequence order. A packet that is dropped leaves
// the committed stream untouched: late data can never alter emitted frames.
func (b *Buffer) Push(pkt *rtpparse.Packet) Result {
	b.stats.Received++
	if pkt == nil {
		b.drop(fault.KindConfig, ReasonConfig, 0, -1, "nil packet")
		return Result{Reason: ReasonConfig}
	}
	ext := b.seq.Extend(pkt.Sequence)
	pkt.ExtSeq = ext
	res := Result{Extended: ext}

	if b.sawAny && ext < b.floor {
		b.stats.Late++
		b.drop(fault.KindLate, ReasonLate, ext, pkt.Offset,
			"sequence %d already committed (floor %d)", ext, b.floor)
		res.Reason = ReasonLate
		return res
	}
	if _, ok := b.pending[ext]; ok {
		b.stats.Duplicates++
		b.drop(fault.KindDuplicate, ReasonDuplicate, ext, pkt.Offset,
			"sequence %d already buffered", ext)
		res.Reason = ReasonDuplicate
		return res
	}
	if len(b.pending) >= b.cfg.MaxPackets {
		b.stats.Overflow++
		b.drop(fault.KindWindow, ReasonOverflow, ext, pkt.Offset,
			"resident %d reached cap %d", len(b.pending), b.cfg.MaxPackets)
		res.Reason = ReasonOverflow
		return res
	}
	if !b.sawAny {
		b.sawAny = true
		b.highest = ext
		b.floor = ext
	}
	if ext > b.highest {
		b.highest = ext
	} else if ext < b.highest {
		b.stats.outOfOrder++
	}
	b.pending[ext] = pkt
	b.stats.Accepted++

	// Commit everything older than the reorder watermark. When the watermark
	// advances past a missing sequence, the gap stays open and is reported by
	// the recovery stage instead of being mistaken for a late packet.
	if b.highest >= b.cfg.Window && b.highest-b.cfg.Window >= b.floor {
		res.Committed = b.commitThrough(b.highest - b.cfg.Window)
	}
	return res
}

// Flush commits all buffered packets in sequence order. It is called once the
// input side signals end of stream.
func (b *Buffer) Flush() []*rtpparse.Packet {
	if len(b.pending) == 0 {
		return nil
	}
	keys := b.sortedKeys()
	out := make([]*rtpparse.Packet, 0, len(keys))
	for _, k := range keys {
		out = append(out, b.pending[k])
		delete(b.pending, k)
		b.stats.Committed++
	}
	if len(keys) > 0 {
		b.floor = keys[len(keys)-1] + 1
	}
	return out
}

// Stats returns a snapshot of the cumulative counters.
func (b *Buffer) Stats() Stats { return b.stats }

// Faults returns a copy of the drop records.
func (b *Buffer) Faults() fault.List { return append(fault.List(nil), b.faults...) }

// Pending returns how many packets are currently resident.
func (b *Buffer) Pending() int { return len(b.pending) }

// Floor returns the lowest sequence that has not yet been committed.
func (b *Buffer) Floor() uint64 { return b.floor }

// ContiguousRun returns the longest gap-free run starting at floor among
// pending packets. Recovery uses it to distinguish a small hole from a long
// outage.
func (b *Buffer) ContiguousRun() int {
	n := 0
	for seq := b.floor; ; seq++ {
		if _, ok := b.pending[seq]; !ok {
			return n
		}
		n++
	}
}

// Missing returns the sequence gaps inside [floor, highest] as half-open
// ranges. Holes that are still inside the reorder window are included because
// the caller decides whether to wait or recover.
func (b *Buffer) Missing() [][2]uint64 {
	if !b.sawAny || b.highest < b.floor {
		return nil
	}
	var out [][2]uint64
	start := uint64(0)
	inGap := false
	for seq := b.floor; seq <= b.highest; seq++ {
		_, ok := b.pending[seq]
		if !ok && !inGap {
			start, inGap = seq, true
		}
		if ok && inGap {
			out = append(out, [2]uint64{start, seq})
			inGap = false
		}
	}
	if inGap {
		out = append(out, [2]uint64{start, b.highest + 1})
	}
	return out
}

// commitThrough releases every buffered packet with sequence <= limit and
// advances the floor past them.
func (b *Buffer) commitThrough(limit uint64) []*rtpparse.Packet {
	var out []*rtpparse.Packet
	for {
		if b.floor > limit {
			return out
		}
		pkt, ok := b.pending[b.floor]
		if !ok {
			return out
		}
		delete(b.pending, b.floor)
		b.stats.Committed++
		out = append(out, pkt)
		b.floor++
	}
}

func (b *Buffer) sortedKeys() []uint64 {
	keys := make([]uint64, 0, len(b.pending))
	for k := range b.pending {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

func (b *Buffer) drop(kind fault.Kind, reason DropReason, ext uint64, offset int64, format string, args ...any) {
	detail := fmt.Sprintf("%s: %s", reason, fmt.Sprintf(format, args...))
	e := fault.New(fault.StageJitter, kind, "%s", detail)
	e.At(offset, -1)
	if offset >= 0 {
		e.Recoverable = true
	}
	b.faults = append(b.faults, e)
	// Keep the fault list bounded: an adversarial stream of duplicates must
	// not grow memory with input length.
	const maxRecorded = 1024
	if len(b.faults) > maxRecorded {
		b.faults = b.faults[len(b.faults)-maxRecorded:]
	}
}

// DiscardGap advances the commit floor past a hole whose first sequence is
// exactly the current floor, then releases any buffered packets that follow
// contiguously. Recovery calls it once it has decided a hole is either filled
// or unrecoverable, so the buffer can never deadlock on a permanently lost
// packet. It refuses to cross packets that are still resident.
func (b *Buffer) DiscardGap(end uint64) ([]*rtpparse.Packet, error) {
	if end <= b.floor {
		return nil, fmt.Errorf("gap end %d must exceed floor %d", end, b.floor)
	}
	for seq := b.floor; seq < end; seq++ {
		if _, ok := b.pending[seq]; ok {
			return nil, fmt.Errorf("sequence %d is resident, not a gap", seq)
		}
	}
	b.floor = end
	return b.commitThrough(b.highest), nil
}
