// Package rtp provides RTP sequence reordering and H.264 frame
// reassembly used by the audit pipeline.
package rtp

import "github.com/pion/rtp"

// ReorderWindow is the number of sequence slots kept for reordering.
const ReorderWindow = 64

// ReorderStats summarizes sequence level observations.
type ReorderStats struct {
	Received   int `json:"received"`    // packets accepted (excluding duplicates)
	Duplicates int `json:"duplicates"`  // retransmissions of an already seen sequence
	Reordered  int `json:"reordered"`   // packets that arrived ahead of their turn
	Gaps       int `json:"gaps"`        // missing sequence numbers, each counted once
	Late       int `json:"late"`        // packets that fell behind the reorder window
	Wraps      int `json:"wraparounds"` // 16 bit sequence wraparounds observed
}

type slot struct {
	valid bool
	seq   uint16
	pkt   *rtp.Packet
}

// Reorderer restores packet order across 16 bit sequence wraparound,
// detects duplicates and counts each sequence gap exactly once.
type Reorderer struct {
	started bool
	base    uint16 // next sequence number to emit
	slots   [ReorderWindow]slot
	emitted map[uint16]struct{} // recently emitted, for duplicate detection
	high    uint16              // highest sequence seen (in base-relative terms)
	stats   ReorderStats
}

// NewReorderer creates a Reorderer with an empty window.
func NewReorderer() *Reorderer {
	return &Reorderer{emitted: make(map[uint16]struct{})}
}

// Stats returns a copy of the current statistics.
func (r *Reorderer) Stats() ReorderStats {
	return r.stats
}

// seqDiff returns b-a in wrap-aware signed 16 bit arithmetic.
func seqDiff(a, b uint16) int16 {
	return int16(b - a)
}

// Feed inserts one packet and returns any packets that became
// emit-ready in sequence order.
func (r *Reorderer) Feed(pkt *rtp.Packet) []*rtp.Packet {
	seq := pkt.SequenceNumber
	if !r.started {
		r.started = true
		r.base = seq
		r.high = seq
	}

	diff := seqDiff(r.base, seq)
	switch {
	case diff < 0:
		// older than the emission point: retransmission or too-late packet
		if _, ok := r.emitted[seq]; ok {
			r.stats.Duplicates++
		} else {
			r.stats.Late++
		}
		return nil
	case int(diff) >= ReorderWindow:
		// window overflow: slide forward, counting skipped slots as gaps
		r.slide(int(diff) - ReorderWindow + 1)
	}

	idx := seq % ReorderWindow
	s := &r.slots[idx]
	if s.valid && s.seq == seq {
		r.stats.Duplicates++
		return nil
	}
	s.valid = true
	s.seq = seq
	s.pkt = pkt
	r.stats.Received++
	if diff > 0 {
		r.stats.Reordered++
	}
	if seqDiff(r.high, seq) > 0 {
		if seq < r.base { // wrapped past 65535
			r.stats.Wraps++
		}
		r.high = seq
	}
	return r.drain()
}

// slide advances base by n slots, counting unreceived slots as gaps.
func (r *Reorderer) slide(n int) {
	for i := 0; i < n; i++ {
		idx := r.base % ReorderWindow
		s := &r.slots[idx]
		if s.valid && s.seq == r.base {
			s.valid = false
		} else {
			r.stats.Gaps++
		}
		r.emitted[r.base] = struct{}{}
		r.base++
	}
}

// drain emits contiguous packets starting at base.
func (r *Reorderer) drain() []*rtp.Packet {
	var out []*rtp.Packet
	for {
		idx := r.base % ReorderWindow
		s := &r.slots[idx]
		if !s.valid || s.seq != r.base {
			break
		}
		out = append(out, s.pkt)
		s.valid = false
		r.emitted[r.base] = struct{}{}
		r.base++
	}
	// bound the emitted set to the last two windows
	if len(r.emitted) > 4*ReorderWindow {
		for seq := range r.emitted {
			if d := seqDiff(seq, r.base); d > int16(2*ReorderWindow) {
				delete(r.emitted, seq)
			}
		}
	}
	return out
}

// Flush terminates the stream: every still-missing sequence up to the
// highest seen is counted as a gap exactly once, and any buffered
// packets are emitted in order.
func (r *Reorderer) Flush() []*rtp.Packet {
	if !r.started {
		return nil
	}
	var out []*rtp.Packet
	for seqDiff(r.base, r.high) >= 0 {
		idx := r.base % ReorderWindow
		s := &r.slots[idx]
		if s.valid && s.seq == r.base {
			out = append(out, s.pkt)
			s.valid = false
		} else {
			r.stats.Gaps++
		}
		r.emitted[r.base] = struct{}{}
		r.base++
	}
	return out
}
