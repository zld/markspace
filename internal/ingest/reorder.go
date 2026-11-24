package ingest

import "sort"

// Reorderer buffers RTP events and commits them in extended-sequence order.
// Packets that arrive after their position has been committed are counted
// as late corruption and never rewrite the committed stream.
type Reorderer struct {
	window uint64

	init     bool
	cycles   uint64 // wrap offset for sequence numbers
	maxExt   uint64 // highest ext seq seen
	buffer   map[uint64]Event
	seen     map[uint64]bool
	commitTo uint64 // all ext seq <= commitTo are committed
	hasCmt   bool   // whether any RTP position has been committed
	ordered  []Event
	stats    Stats
}

// NewReorderer creates a Reorderer with the given reorder window (packets).
func NewReorderer(window int) *Reorderer {
	return &Reorderer{
		window: uint64(window),
		buffer: make(map[uint64]Event),
		seen:   make(map[uint64]bool),
	}
}

// extendSeq maps a 16-bit sequence number to the extended domain, tracking
// wrap based on the highest sequence observed so far.
func (r *Reorderer) extendSeq(seq uint16) uint64 {
	const half = uint64(1) << 15
	if !r.init {
		r.init = true
		r.maxExt = uint64(seq)
		return uint64(seq)
	}
	cand := uint64(seq) + r.cycles
	// position candidate within +-half of maxExt
	for cand+half < r.maxExt {
		r.cycles += 1 << 16
		cand += 1 << 16
	}
	for cand > r.maxExt+half {
		cand -= 1 << 16
	}
	if cand > r.maxExt {
		r.maxExt = cand
	}
	return cand
}

// Push ingests one RTP event. RTCP events pass straight through to the
// ordered output since they carry no media sequence.
func (r *Reorderer) Push(e Event) {
	if e.Kind == KindRTCP {
		r.ordered = append(r.ordered, e)
		return
	}
	r.stats.Received++
	ext := r.extendSeq(e.Seq())
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
	r.buffer[ext] = e
	r.commit()
}

func (r *Reorderer) anyCommitted() bool { return r.hasCmt }

// commit releases buffered packets whose position is finalized: everything
// at or below maxExt-window is committed in order, recording gaps.
func (r *Reorderer) commit() {
	if r.maxExt <= r.window {
		return
	}
	limit := r.maxExt - r.window
	start := r.commitTo + 1
	if r.commitTo == 0 && !r.anyCommitted() {
		// first commit begins at the lowest buffered seq
		start = ^uint64(0)
		for ext := range r.buffer {
			if ext < start {
				start = ext
			}
		}
		if start == ^uint64(0) {
			return
		}
	}
	for ext := start; ext <= limit; ext++ {
		if ev, ok := r.buffer[ext]; ok {
			r.ordered = append(r.ordered, ev)
			delete(r.buffer, ext)
		} else if r.anyCommitted() || ext > start {
			r.stats.Lost++
			r.addGap(ext)
		}
		r.commitTo = ext
		r.hasCmt = true
	}
}


func (r *Reorderer) addGap(ext uint64) {
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
	exts := make([]uint64, 0, len(r.buffer))
	for ext := range r.buffer {
		exts = append(exts, ext)
	}
	sort.Slice(exts, func(i, j int) bool { return exts[i] < exts[j] })
	for _, ext := range exts {
		// gap between commitTo and next buffered packet is real loss
		if r.anyCommitted() {
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
