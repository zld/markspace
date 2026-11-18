package buffer

import (
	"fmt"

	"jitterlab/internal/packet"
)

const (
	// MaxWindowSize bounds how far ahead of the playout point a packet may
	// arrive. It also caps window memory: MaxWindowSize frames of payload.
	MaxWindowSize = 4096

	seqMask = 1<<40 - 1
	seqMod  = 1 << 40
	seqHalf = 1 << 39
)

// Status classifies the result of offering a packet to the window.
type Status int

const (
	// Accepted means the packet was stored for playout.
	Accepted Status = iota
	// Duplicate means the sequence was already stored or recently played.
	Duplicate
	// Expired means the sequence is outside the reorder window: either it
	// was given up on long ago or it is too far ahead.
	Expired
)

func (s Status) String() string {
	switch s {
	case Accepted:
		return "accepted"
	case Duplicate:
		return "duplicate"
	case Expired:
		return "expired"
	default:
		return "unknown"
	}
}

// PushResult reports how a packet was treated by the window.
type PushResult struct {
	Status Status
	// Reordered is true when the packet arrived after a higher sequence
	// number had already been seen. It is only set for accepted packets.
	Reordered bool
}

// Window reorders packets by extended 40-bit sequence number, removes
// duplicates and refuses packets that fall outside the configured span.
//
// The window is not safe for concurrent use; the controller serialises
// access from the receive and playout paths.
type Window struct {
	size int

	init    bool
	started bool   // a sequence has been played out or skipped
	next    uint64 // next sequence to play out
	highest uint64 // highest extended sequence seen

	pending   map[uint64]packet.Frame
	delivered map[uint64]struct{}
	order     []uint64 // delivered sequences, oldest first, for pruning
}

// NewWindow creates a window spanning size sequence numbers ahead of the
// playout point. A size of zero is invalid.
func NewWindow(size int) (*Window, error) {
	if size <= 0 {
		return nil, fmt.Errorf("buffer: window size %d must be positive", size)
	}
	if size > MaxWindowSize {
		return nil, fmt.Errorf("buffer: window size %d exceeds limit %d", size, MaxWindowSize)
	}
	return &Window{
		size:      size,
		pending:   make(map[uint64]packet.Frame, size),
		delivered: make(map[uint64]struct{}, size),
	}, nil
}

// Push offers a frame to the window. Duplicate and expired packets are not
// stored. A late packet can never displace a frame that was already played
// because next only moves forward.
func (w *Window) Push(f packet.Frame) PushResult {
	seq := w.extend(packet.MaskSeq(f.Seq))
	first := !w.init
	if first {
		w.init = true
		w.next = seq
		w.highest = seq
	}
	reordered := !first && seq < w.highest
	if seq > w.highest {
		w.highest = seq
	}

	if seq < w.next {
		if _, ok := w.delivered[seq]; ok {
			return PushResult{Status: Duplicate}
		}
		// Before anything has been played out the horizon may still move
		// backwards to pick up a packet that arrived ahead of its peers.
		if !w.started && w.next-seq <= uint64(w.size) {
			w.next = seq
		} else {
			return PushResult{Status: Expired}
		}
	}
	if seq >= w.next+uint64(w.size) {
		return PushResult{Status: Expired}
	}
	if _, ok := w.pending[seq]; ok {
		return PushResult{Status: Duplicate}
	}

	f.Seq = seq & seqMask
	w.pending[seq] = f
	return PushResult{Status: Accepted, Reordered: reordered}
}

// extend maps a 40-bit wire sequence onto the continuous extended timeline
// anchored at the highest sequence seen so far, unwrapping at 2^40.
func (w *Window) extend(seq uint64) uint64 {
	if !w.init {
		return seq
	}
	base := (w.highest &^ seqMask) | seq
	if base < w.highest && w.highest-base > seqHalf {
		return base + seqMod
	}
	return base
}

// Pop removes and returns the next in-order frame. It reports false when
// the next sequence is missing.
func (w *Window) Pop() (packet.Frame, bool) {
	f, ok := w.pending[w.next]
	if !ok {
		return packet.Frame{}, false
	}
	delete(w.pending, w.next)
	w.delivered[w.next] = struct{}{}
	w.order = append(w.order, w.next)
	w.next++
	w.started = true
	w.prune()
	return f, true
}

// SkipNext abandons the next sequence without delivering it. The caller
// normally records a concealed frame in its place.
func (w *Window) SkipNext() {
	if !w.init {
		return
	}
	delete(w.pending, w.next)
	w.next++
	w.started = true
	w.prune()
}

// SkipTo abandons every sequence below target (target must be >= the
// current next) and returns how many buffered frames were discarded. It is
// used to shed latency when the buffer grows past its upper bound.
func (w *Window) SkipTo(target uint64) int {
	if !w.init || target <= w.next {
		return 0
	}
	dropped := 0
	for seq := range w.pending {
		if seq < target {
			delete(w.pending, seq)
			dropped++
		}
	}
	w.next = target
	w.started = true
	if w.highest < target {
		w.highest = target
	}
	w.prune()
	return dropped
}

// HasNext reports whether the next sequence is buffered.
func (w *Window) HasNext() bool {
	if !w.init {
		return false
	}
	_, ok := w.pending[w.next]
	return ok
}

// Next returns the next sequence to be played out.
func (w *Window) Next() uint64 {
	if !w.init {
		return 0
	}
	return w.next
}

// Initialized reports whether at least one packet has been accepted.
func (w *Window) Initialized() bool { return w.init }

// Len returns the number of buffered frames.
func (w *Window) Len() int { return len(w.pending) }

// Highest returns the highest extended sequence seen, if any.
func (w *Window) Highest() (uint64, bool) { return w.highest, w.init }

// prune drops delivery history that can no longer be referenced by a
// duplicate arriving within the window span.
func (w *Window) prune() {
	if uint64(w.size) > w.next {
		return
	}
	limit := w.next - uint64(w.size)
	for len(w.order) > 0 && w.order[0] < limit {
		delete(w.delivered, w.order[0])
		w.order = w.order[1:]
	}
}
