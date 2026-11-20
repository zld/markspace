package jitter

// Frame is one queued PCM frame inside the controller.
type Frame struct {
	Seq       uint16
	Timestamp uint32
	PCM       []byte
}

// InsertResult classifies what the window did with an incoming packet.
type InsertResult int

const (
	Inserted InsertResult = iota
	Duplicate
	TooLate   // behind the playout cursor; dropped
	Overflow  // window full; dropped per policy
)

// seqDiff returns a-b in 16-bit wraparound arithmetic, as a signed value.
func seqDiff(a, b uint16) int16 { return int16(a - b) }

// Window is a reorder buffer over 16-bit sequence numbers. It holds at
// most Cap frames, keyed by absolute sequence number, and answers
// "have you seen seq" across wraparound.
type Window struct {
	cap   int
	items map[uint16]Frame
	// next is the next sequence number the playout side wants. Valid
	// only after started.
	next    uint16
	started bool
}

func NewWindow(capacity int) *Window {
	if capacity < 1 {
		capacity = 1
	}
	return &Window{cap: capacity, items: make(map[uint16]Frame)}
}

// Cap returns the window capacity in frames.
func (w *Window) Cap() int { return w.cap }

// Len returns the number of queued frames.
func (w *Window) Len() int { return len(w.items) }

// Insert places f into the window, classifying duplicates, late
// packets and overflow. The first packet ever seen anchors the
// playout cursor at its sequence number.
func (w *Window) Insert(f Frame) InsertResult {
	if !w.started {
		w.started = true
		w.next = f.Seq
	}
	// Behind (or at, if already consumed) the playout cursor?
	if w.started && seqDiff(f.Seq, w.next) < 0 {
		if _, ok := w.items[f.Seq]; ok {
			return Duplicate
		}
		return TooLate
	}
	if _, ok := w.items[f.Seq]; ok {
		return Duplicate
	}
	// Too far ahead of the cursor to ever buffer?
	if int(seqDiff(f.Seq, w.next)) >= w.cap {
		return Overflow
	}
	if len(w.items) >= w.cap {
		return Overflow
	}
	w.items[f.Seq] = f
	return Inserted
}

// PopNext returns the frame for the current playout cursor and
// advances it, or reports a miss (caller must conceal).
func (w *Window) PopNext() (Frame, bool) {
	if !w.started {
		return Frame{}, false
	}
	f, ok := w.items[w.next]
	if ok {
		delete(w.items, w.next)
	}
	w.next++
	return f, ok
}

// PeekNext returns the sequence number the playout side wants next.
func (w *Window) PeekNext() (uint16, bool) { return w.next, w.started }

// EvictBeyond drops queued frames farther than ahead frames past the
// cursor (used when the target waterline shrinks). Returns the number
// evicted.
func (w *Window) EvictBeyond(ahead int) int {
	if !w.started {
		return 0
	}
	evicted := 0
	for seq := range w.items {
		if int(seqDiff(seq, w.next)) >= ahead {
			delete(w.items, seq)
			evicted++
		}
	}
	return evicted
}
