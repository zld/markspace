package clock

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
)

// EventRecord is the minimal replayable fact about one accepted event.
type EventRecord struct {
	Time   int64    // event time in ticks
	ExtSeq int64    // extended sequence number (tie-breaker)
	Hash   [32]byte // content hash of the event payload
}

// Window is a fixed tick span of the timeline with a deterministic
// content hash over its events.
type Window struct {
	ID     int64
	Start  int64 // inclusive
	End    int64 // exclusive
	Hash   [32]byte
	Closed bool
	Late   int // late events replayed into this window after closure

	events []EventRecord
}

// Replayer segments the timeline into fixed windows, closes them by
// watermark, and deterministically recomputes a window's hash when late
// data arrives after closure. Recomputing a window never touches any
// other window, so unaffected segment hashes are stable.
type Replayer struct {
	size    int64
	retain  int
	windows map[int64]*Window

	// LateDropped counts late events whose window was already evicted.
	LateDropped int
}

// NewReplayer creates a Replayer with the given window size in ticks.
// retain bounds how many recent windows (open or closed) are kept for
// late recomputation; older windows are evicted.
func NewReplayer(windowSize int64, retain int) *Replayer {
	if windowSize <= 0 {
		windowSize = 1
	}
	if retain <= 0 {
		retain = 1
	}
	return &Replayer{size: windowSize, retain: retain, windows: make(map[int64]*Window)}
}

func (r *Replayer) windowFor(t int64) *Window {
	id := t / r.size
	if t < 0 && t%r.size != 0 {
		id-- // floor division for negative times
	}
	w, ok := r.windows[id]
	if !ok {
		w = &Window{ID: id, Start: id * r.size, End: (id + 1) * r.size}
		r.windows[id] = w
		r.evict()
	}
	return w
}

// evict drops the oldest windows beyond the retention bound.
func (r *Replayer) evict() {
	for len(r.windows) > r.retain {
		oldest := int64(0)
		first := true
		for id := range r.windows {
			if first || id < oldest {
				oldest, first = id, true
			}
		}
		delete(r.windows, oldest)
	}
}

// hashWindow computes the deterministic content hash: events are sorted
// by (Time, ExtSeq, Hash) so the result is independent of arrival order.
func hashWindow(events []EventRecord) [32]byte {
	sorted := make([]EventRecord, len(events))
	copy(sorted, events)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Time != b.Time {
			return a.Time < b.Time
		}
		if a.ExtSeq != b.ExtSeq {
			return a.ExtSeq < b.ExtSeq
		}
		return string(a.Hash[:]) < string(b.Hash[:])
	})
	h := sha256.New()
	var buf [16]byte
	for _, e := range sorted {
		binary.BigEndian.PutUint64(buf[0:8], uint64(e.Time))
		binary.BigEndian.PutUint64(buf[8:16], uint64(e.ExtSeq))
		h.Write(buf[:])
		h.Write(e.Hash[:])
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

// Add inserts an on-time event, then closes every window whose End is
// at or before the watermark. It returns the newly closed windows in ID
// order; their hashes are final unless a late event reopens them.
func (r *Replayer) Add(rec EventRecord, watermark int64) []Window {
	w := r.windowFor(rec.Time)
	w.events = append(w.events, rec)
	w.Hash = hashWindow(w.events)

	var closed []Window
	for id, win := range r.windows {
		if !win.Closed && win.End <= watermark {
			win.Closed = true
			closed = append(closed, *win)
			_ = id
		}
	}
	sort.Slice(closed, func(i, j int) bool { return closed[i].ID < closed[j].ID })
	return closed
}

// AddLate inserts an event into an already-closed (retained) window and
// deterministically recomputes its hash. ok is false when the window
// was already evicted; the event is then counted and dropped.
func (r *Replayer) AddLate(rec EventRecord) (w *Window, ok bool) {
	id := rec.Time / r.size
	if rec.Time < 0 && rec.Time%r.size != 0 {
		id--
	}
	win, ok := r.windows[id]
	if !ok {
		r.LateDropped++
		return nil, false
	}
	win.events = append(win.events, rec)
	win.Late++
	win.Hash = hashWindow(win.events)
	return win, true
}

// Flush closes and returns all remaining windows in ID order.
func (r *Replayer) Flush() []Window {
	var out []Window
	for _, w := range r.windows {
		if !w.Closed {
			w.Closed = true
		}
		out = append(out, *w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
