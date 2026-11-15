package jitter

import (
	"time"

	"jbbench/internal/protocol"
)

// DropPolicy selects behavior when the reorder window cannot hold a frame.
type DropPolicy int

const (
	// DropRejectNew refuses the incoming frame and counts an overflow drop.
	DropRejectNew DropPolicy = iota
	// DropEvictOld removes the oldest buffered frame to make room.
	DropEvictOld
)

// WindowStats holds independent counters for every discard reason.
type WindowStats struct {
	Accepted       uint64
	DuplicateDrops uint64
	LateDrops      uint64 // frame at or behind the play cursor
	OverflowDrops  uint64 // frame outside window range, or window full
	Reordered      uint64 // accepted packets that arrived out of order
}

// Frame is a decoded packet paired with its arrival time on the receive clock.
type Frame struct {
	Pkt     protocol.Packet
	Arrival time.Duration
}

// ReorderWindow keeps a bounded, keyed set of frames indexed by frame number.
type ReorderWindow struct {
	capacity int
	policy   DropPolicy
	cursor   uint32 // next frame number to be played
	started  bool
	frames   map[uint32]Frame
	high     uint32 // highest frame number ever accepted
	stats    WindowStats
}

// NewReorderWindow builds a window holding capacityFrames future frames.
func NewReorderWindow(capacityFrames int, policy DropPolicy) *ReorderWindow {
	if capacityFrames < 1 {
		capacityFrames = 1
	}
	return &ReorderWindow{
		capacity: capacityFrames,
		policy:   policy,
		frames:   make(map[uint32]Frame),
	}
}

// Push admits a frame. The returned reason is "" on acceptance, otherwise one
// of "duplicate", "late" or "overflow". Bad packets must never reach Push.
func (w *ReorderWindow) Push(f Frame) (accepted bool, reason string) {
	no := f.Pkt.FrameNo
	if !w.started {
		w.started = true
		w.cursor = no
		w.high = no
		w.frames[no] = f
		w.stats.Accepted++
		return true, ""
	}
	if no < w.cursor {
		w.stats.LateDrops++
		return false, "late"
	}
	if _, exists := w.frames[no]; exists {
		w.stats.DuplicateDrops++
		return false, "duplicate"
	}
	if no-w.cursor >= uint32(w.capacity) {
		if w.policy == DropRejectNew {
			w.stats.OverflowDrops++
			return false, "overflow"
		}
		w.evictOldest()
		w.stats.OverflowDrops++
	} else if no != w.cursor {
		w.stats.Reordered++
	}
	w.frames[no] = f
	if no > w.high {
		w.high = no
	}
	w.stats.Accepted++
	return true, ""
}

func (w *ReorderWindow) evictOldest() {
	oldest := w.high + 1
	for k := range w.frames {
		if k < oldest {
			oldest = k
		}
	}
	delete(w.frames, oldest)
}

// Pop removes and returns the frame at the cursor. It returns false when the
// cursor frame is missing (a gap the caller must conceal).
func (w *ReorderWindow) Pop() (Frame, bool) {
	if !w.started {
		return Frame{}, false
	}
	f, ok := w.frames[w.cursor]
	if !ok {
		return Frame{}, false
	}
	delete(w.frames, w.cursor)
	w.cursor++
	return f, true
}

// Skip advances the cursor over one missing frame (concealed by the caller).
func (w *ReorderWindow) Skip() {
	if w.started {
		w.cursor++
	}
}

// Has reports whether frame no is buffered.
func (w *ReorderWindow) Has(no uint32) bool {
	_, ok := w.frames[no]
	return ok
}

// FirstMissing returns the first missing frame at or after the cursor and
// whether any later frame has already been seen (confirming a true gap versus
// frames simply not yet arrived).
func (w *ReorderWindow) FirstMissing() (uint32, bool) {
	if !w.started || w.Has(w.cursor) {
		return 0, false
	}
	return w.cursor, w.high > w.cursor
}

// PendingGaps counts missing frames between the cursor and the highest seen.
func (w *ReorderWindow) PendingGaps() int {
	if !w.started {
		return 0
	}
	missing := 0
	for no := w.cursor; no < w.high; no++ {
		if !w.Has(no) {
			missing++
		}
	}
	return missing
}

// Len is the number of buffered frames.
func (w *ReorderWindow) Len() int { return len(w.frames) }

// Cursor is the next frame number to be played.
func (w *ReorderWindow) Cursor() uint32 { return w.cursor }

// High is the highest accepted frame number.
func (w *ReorderWindow) High() uint32 { return w.high }

// Stats returns a copy of the window counters.
func (w *ReorderWindow) Stats() WindowStats { return w.stats }
