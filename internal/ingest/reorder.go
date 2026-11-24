package ingest

import (
	"math"
	"sort"
)

// Reorderer buffers RTP events and commits them in extended-sequence order.
// Packets that arrive after their position has been committed are counted
// as late corruption and never rewrite the committed stream.
type Reorderer struct {
	window int64

	ext      SeqExtender
	buffer   map[int64]Event
	seen     map[int64]bool
	commitTo int64 // all ext seq <= commitTo are committed
	hasCmt   bool  // whether any RTP position has been committed
	ordered  []Event
	stats    Stats
}

// NewReorderer creates a Reorderer with the given reorder window (packets).
func NewReorderer(window int) *Reorderer {
	return &Reorderer{
		window: int64(window),
		buffer: make(map[int64]Event),
		seen:   make(map[int64]bool),
	}
}

// Push ingests one RTP event. RTCP events pass straight through to the
// ordered output since they carry no media sequence.
func (r *Reorderer) Push(e Event) {
	if e.Kind == KindRTCP {
		r.ordered = append(r.ordered, e)
		return
	}
	r.stats.Received++
	ext := r.ext.Extend(e.Seq())
	if r.hasCmt && ext <= r.commitTo {
		// too late: position already committed
		r.stats.Late++
		return
	}
	if r.seen[ext] {
		r.stats.Duplicates++
		return
	}
	r.seen[ext] = true
	e.Ext = ext
	r.buffer[ext] = e
	r.commit()
}

func (r *Reorderer) anyCommitted() bool { return r.hasCmt }

// commit releases buffered packets whose position is finalized: everything
// at or below maxExt-window is committed in order, recording gaps. Once a
// position is committed it is never rewritten, even if the missing packet
// arrives later.
func (r *Reorderer) commit() {
	maxExt := r.ext.Max()
	if maxExt-r.ext.Origin() < r.window {
		return
	}
	limit := maxExt - r.window
	start := r.commitTo + 1
	if !r.hasCmt {
		// First commit begins at the lowest committed-candidate position in
		// the buffer, which may lie below the first packet's extended seq
		// after reordering, so the stream origin is not a safe floor.
		start = math.MaxInt64
		for ext := range r.buffer {
			if ext < start {
				start = ext
			}
		}
		if start == math.MaxInt64 {
			return
		}
	}
	for ext := start; ext <= limit; ext++ {
		if ev, ok := r.buffer[ext]; ok {
			r.ordered = append(r.ordered, ev)
			delete(r.buffer, ext)
		} else if ext > start || r.hasCmt {
			r.stats.Lost++
			r.addGap(ext)
		}
		r.commitTo = ext
		r.hasCmt = true
	}
}

func (r *Reorderer) addGap(ext int64) {
	if n := len(r.stats.Gaps); n > 0 && r.stats.Gaps[n-1].To+1 == ext {
		r.stats.Gaps[n-1].To = ext
		return
	}
	r.stats.Gaps = append(r.stats.Gaps, Gap{From: ext, To: ext})
}

// Flush commits everything still buffered (end of stream) and returns the
// ordered events plus statistics. Unobserved trailing positions are not
// counted as loss since the stream simply ends.
func (r *Reorderer) Flush() ([]Event, Stats) {
	exts := make([]int64, 0, len(r.buffer))
	for ext := range r.buffer {
		exts = append(exts, ext)
	}
	sort.Slice(exts, func(i, j int) bool { return exts[i] < exts[j] })
	for _, ext := range exts {
		if r.hasCmt && ext <= r.commitTo {
			// position was already committed (stale buffer entry)
			delete(r.buffer, ext)
			continue
		}
		// gap between commitTo and next buffered packet is real loss
		if r.hasCmt {
			for missing := r.commitTo + 1; missing < ext; missing++ {
				r.stats.Lost++
				r.addGap(missing)
			}
		}
		r.ordered = append(r.ordered, r.buffer[ext])
		r.commitTo = ext
		r.hasCmt = true
		delete(r.buffer, ext)
	}
	return r.ordered, r.stats
}
