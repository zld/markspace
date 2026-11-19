package jitter

import "jitterlab/internal/protocol"

// SeqLess reports whether a precedes b in 32-bit modular sequence space.
func SeqLess(a, b uint32) bool { return int32(a-b) < 0 }

// InsertResult classifies the outcome of a Window.Insert.
type InsertResult int

const (
	Inserted  InsertResult = iota // accepted into the window
	Duplicate                     // seq already buffered or already played
	Late                          // seq retired before arrival (concealed or too old)
	Full                          // window at capacity, packet discarded
)

const (
	// retiredTrack bounds how many retired seqs we remember to split
	// re-arrivals into duplicates (already played) vs. late (concealed).
	retiredTrack = 256
	// maxForward resets the window when a seq jumps implausibly far ahead,
	// keeping memory bounded on garbage input.
	maxForward = 2048
)

// Window is a bounded per-stream reorder buffer keyed by sequence number.
// Memory is capped by max packets plus the retired-seq ring.
type Window struct {
	max      int
	started  bool
	base     uint32 // next seq expected for playout
	pkts     map[uint32]protocol.Packet
	retired  map[uint32]bool // seq -> true if played, false if skipped
	retireQ  []uint32
	Resets   int // times a forward jump forced a window reset
}

func NewWindow(max int) *Window {
	if max < 1 {
		max = 1
	}
	return &Window{
		max:     max,
		pkts:    make(map[uint32]protocol.Packet),
		retired: make(map[uint32]bool),
	}
}

func (w *Window) Started() bool { return w.started }
func (w *Window) Base() uint32  { return w.base }
func (w *Window) Len() int      { return len(w.pkts) }

// Has reports whether seq is currently buffered.
func (w *Window) Has(seq uint32) bool {
	_, ok := w.pkts[seq]
	return ok
}

// Insert adds p to the window, classifying duplicates and late arrivals.
func (w *Window) Insert(p protocol.Packet) InsertResult {
	if !w.started {
		w.started = true
		w.base = p.Seq
		w.pkts[p.Seq] = p
		return Inserted
	}
	if SeqLess(p.Seq, w.base) {
		if played, ok := w.retired[p.Seq]; ok && played {
			return Duplicate
		}
		return Late
	}
	if _, ok := w.pkts[p.Seq]; ok {
		return Duplicate
	}
	if dist := p.Seq - w.base; dist > maxForward {
		// Implausible forward jump: drop buffered state and resync.
		w.pkts = map[uint32]protocol.Packet{p.Seq: p}
		w.base = p.Seq
		w.Resets++
		return Inserted
	}
	if len(w.pkts) >= w.max {
		return Full
	}
	w.pkts[p.Seq] = p
	return Inserted
}

// Pop removes and returns the packet at the playout base, if present.
func (w *Window) Pop() (protocol.Packet, bool) {
	p, ok := w.pkts[w.base]
	if !ok {
		return protocol.Packet{}, false
	}
	delete(w.pkts, w.base)
	w.retire(w.base, true)
	w.base++
	return p, true
}

// Skip retires the playout base as missing (concealed) and advances.
func (w *Window) Skip() {
	if !w.started {
		return
	}
	w.retire(w.base, false)
	w.base++
}

func (w *Window) retire(seq uint32, played bool) {
	w.retired[seq] = played
	w.retireQ = append(w.retireQ, seq)
	if len(w.retireQ) > retiredTrack {
		delete(w.retired, w.retireQ[0])
		w.retireQ = w.retireQ[1:]
	}
}
