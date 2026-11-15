package jitter

// Drop reasons returned by Window.Insert; stable strings for reporting.
const (
	DropDuplicate = "duplicate"
	DropTooOld    = "too_old"     // behind the receive window
	DropLate      = "late"        // at or behind the playout cursor
	DropFull      = "window_full" // window at capacity
)

// Less compares sequence numbers with unsigned 16-bit wraparound.
func Less(a, b uint16) bool { return int16(a-b) < 0 }

// Window is a bounded out-of-order reassembly buffer keyed by sequence
// number. Duplicates never overwrite a buffered frame.
type Window struct {
	size      int
	frames    map[uint16][]int16
	head      uint16 // highest seq inserted
	hasHead   bool
	played    uint16 // next seq to be played
	hasPlayed bool
}

// NewWindow creates a window holding at most size frames.
func NewWindow(size int) *Window {
	if size < 4 {
		size = 4
	}
	return &Window{size: size, frames: make(map[uint16][]int16)}
}

// SetPlayed marks the playout cursor; packets behind it are "late".
func (w *Window) SetPlayed(next uint16) { w.played, w.hasPlayed = next, true }

// Insert buffers a frame, or returns a drop reason without storing it.
func (w *Window) Insert(seq uint16, samples []int16) string {
	if w.hasPlayed && Less(seq, w.played) {
		return DropLate
	}
	if _, dup := w.frames[seq]; dup {
		return DropDuplicate
	}
	if w.hasHead && Less(seq, w.head) && w.head-seq >= uint16(w.size) {
		return DropTooOld
	}
	if len(w.frames) >= w.size {
		return DropFull // deterministic: drop the incoming packet
	}
	w.frames[seq] = samples
	if !w.hasHead || Less(w.head, seq) {
		w.head = seq
	}
	w.hasHead = true
	return ""
}

// Pop removes and returns the frame for seq, if buffered.
func (w *Window) Pop(seq uint16) ([]int16, bool) {
	s, ok := w.frames[seq]
	if ok {
		delete(w.frames, seq)
	}
	return s, ok
}

// Depth returns the number of buffered frames at or ahead of cursor.
func (w *Window) Depth(cursor uint16) int {
	n := 0
	for seq := range w.frames {
		if !Less(seq, cursor) {
			n++
		}
	}
	return n
}
