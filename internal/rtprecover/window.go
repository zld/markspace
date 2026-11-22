// Package rtprecover turns a lossy, reordered, possibly duplicated RTP packet
// stream into an in-order stream with explicit gap markers.
//
// Recovery operates on sequence-number space with uint16 wraparound handled via
// modular distance, so a wrap from 65535 to 0 is never mistaken for a new
// stream or for massive loss. Timeline coordinates are attached to packets by
// the caller using the unified timeline; this package never invents media time.
package rtprecover

import (
	"github.com/pion/rtp"

	"github.com/archive/mediakernel/internal/timeline"
)

// MaxWindow caps the reorder buffer regardless of caller configuration.
const MaxWindow = 4096

// Stats counts every disposition the window can produce. Counters are
// monotonic for the life of the Window.
type Stats struct {
	Received  int // total packets handed to Push
	Emitted   int // packets returned in order
	Duplicate int // retransmissions / duplicates suppressed
	Late      int // packets arriving outside the window, discarded
	Flushed   int // packets drained by Flush at end of stream
}

// OrderedPacket is one packet placed on the output sequence.
type OrderedPacket struct {
	Packet    *rtp.Packet
	TimelineUS int64 // unified-axis timestamp assigned by the caller
}

// Window is a bounded reorder buffer. The zero value is not usable; create one
// with NewWindow.
type Window struct {
	capacity int
	stats    Stats
	// base is the next sequence number expected on the output stream.
	base      uint16
	haveBase  bool
	buf       map[uint16]OrderedPacket
	// firstSeen tracks the arrival-time ordering used when flushing the tail.
	arrival []uint16
}

// NewWindow creates a reorder window holding at most capacity out-of-order
// packets. capacity above MaxWindow is rejected.
func NewWindow(capacity int) (*Window, error) {
	if capacity <= 0 {
		return nil, timeline.NewError(timeline.KindInvalidInput, "reorder window capacity must be positive")
	}
	if capacity > MaxWindow {
		return nil, timeline.NewError(timeline.KindLimit, "reorder window %d exceeds limit %d", capacity, MaxWindow)
	}
	return &Window{capacity: capacity, buf: make(map[uint16]OrderedPacket, capacity)}, nil
}

// SeqDistance returns the signed forward distance from a to b in uint16
// modular space. A positive result means b is ahead of a (so 0 is ahead of
// 65535 by 1); negative means b trails a.
func SeqDistance(a, b uint16) int {
	d := int(uint16(b - a))
	if d > 32767 {
		d -= 65536
	}
	return d
}

// Push accepts one packet and returns any packets that became releasable in
// strict sequence order. A duplicate (same sequence number already seen or
// buffered) is counted and suppressed; a packet trailing the output base by
// more than the window capacity is a late discard.
func (w *Window) Push(p *rtp.Packet, timelineUS int64) ([]OrderedPacket, error) {
	if p == nil {
		return nil, timeline.NewError(timeline.KindInvalidInput, "nil rtp packet")
	}
	w.stats.Received++
	seq := p.SequenceNumber

	if !w.haveBase {
		// The first packet ever seen anchors the output stream; nothing before
		// it is known, so no gap is fabricated for the "past".
		w.base = seq
		w.haveBase = true
		w.buf[seq] = OrderedPacket{Packet: p, TimelineUS: timelineUS}
		w.arrival = append(w.arrival, seq)
		return w.drain(), nil
	}

	dist := SeqDistance(w.base, seq)
	switch {
	case dist < 0:
		// Trailing the output base. A retransmission within the window span is
		// a duplicate whether it is still buffered or already released; only
		// packets older than the window count as unrecoverably late.
		if -dist <= w.capacity {
			w.stats.Duplicate++
			return nil, nil
		}
		w.stats.Late++
		return nil, nil
	case dist == 0:
		// base itself: buffered means duplicate delivery; an unfilled base
		// position means this packet closes the current hole and must be
		// stored so the contiguous drain can release it.
		if _, known := w.buf[seq]; known {
			w.stats.Duplicate++
			return nil, nil
		}
		w.buf[seq] = OrderedPacket{Packet: p, TimelineUS: timelineUS}
		w.arrival = append(w.arrival, seq)
		return w.drain(), nil
	default:
		if _, known := w.buf[seq]; known {
			w.stats.Duplicate++
			return nil, nil
		}
		if dist > w.capacity {
			// A persistent hole blocks the base and this packet lies beyond the
			// window. A real jitter buffer slides over the hole: jump base to the
			// oldest buffered sequence, releasing everything contiguous up to it.
			// The skipped sequences stay missing and become a gap in the loss
			// layer rather than poisoning every later packet as "late".
			advanced := w.advancePastHole()
			if advanced {
				// Recompute distance after the slide; the packet may now fit.
				dist = SeqDistance(w.base, seq)
			}
			if dist > w.capacity || dist <= 0 {
				w.stats.Late++
				return nil, nil
			}
		}
		w.buf[seq] = OrderedPacket{Packet: p, TimelineUS: timelineUS}
		w.arrival = append(w.arrival, seq)
		return w.drain(), nil
	}
}

// advancePastHole moves base to the smallest buffered sequence ahead of the
// current base, releasing nothing but abandoning the unrecoverable hole. It
// returns false when no forward packet is buffered.
func (w *Window) advancePastHole() bool {
	var jump uint16
	best := 1 << 30
	for seq := range w.buf {
		d := SeqDistance(w.base, seq)
		if d > 0 && d < best {
			best, jump = d, seq
		}
	}
	if best == 1<<30 {
		return false
	}
	w.base = jump
	return true
}

// drain emits the longest contiguous run starting at base, then advances base.
func (w *Window) drain() []OrderedPacket {
	var out []OrderedPacket
	for {
		op, ok := w.buf[w.base]
		if !ok {
			break
		}
		out = append(out, op)
		delete(w.buf, w.base)
		w.base++ // modular increment via uint16 wrap
		w.stats.Emitted++
	}
	if len(out) > 0 {
		// Rebuild the arrival list, dropping released entries. Allocate a fresh
		// backing array: reusing the old one would alias keys we still need.
		kept := make([]uint16, 0, len(w.arrival))
		for _, s := range w.arrival {
			if _, ok := w.buf[s]; ok {
				kept = append(kept, s)
			}
		}
		w.arrival = kept
	}
	return out
}

// Flush releases everything still buffered, in sequence order (gaps force
// partial ordering: contiguous runs are emitted in order). It is called once at
// end of stream so no received packet is silently retained.
func (w *Window) Flush() []OrderedPacket {
	var out []OrderedPacket
	for len(w.buf) > 0 {
		// Emit contiguous run from base; when blocked by a hole, skip base to
		// the oldest buffered sequence and continue.
		if op, ok := w.buf[w.base]; ok {
			out = append(out, op)
			delete(w.buf, w.base)
			w.base++
			w.stats.Flushed++
			continue
		}
		// Hole at base: jump to the smallest forward distance buffered.
		var jump uint16
		best := 1 << 30
		for seq := range w.buf {
			d := SeqDistance(w.base, seq)
			if d > 0 && d < best {
				best, jump = d, seq
			}
		}
		w.base = jump
	}
	w.arrival = nil
	return out
}

// Base returns the next sequence number the output stream expects.
func (w *Window) Base() (uint16, bool) { return w.base, w.haveBase }

// Buffered returns how many packets are currently held.
func (w *Window) Buffered() int { return len(w.buf) }

// Stats returns a copy of the running counters.
func (w *Window) Stats() Stats { return w.stats }
