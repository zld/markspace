// Package rtp receives RTP packets over an unreliable, reordered and
// burst-loss network, removes duplicates, restores sequence order with
// 16-bit wrap handling and records missing ranges as explicit gaps
// instead of inventing replacement samples.
package rtp

// Outcome classifies a single Push call.
type Outcome int

const (
	// OutBuffered means the packet was accepted (immediately delivered
	// or held inside the reorder window).
	OutBuffered Outcome = iota
	// OutDuplicate means this extended sequence number was already seen.
	OutDuplicate
	// OutLateGap means the packet arrived beyond the reorder window;
	// its sequence number has already been recorded as a gap.
	OutLateGap
)

// Delivered is one in-order payload with its extended sequence number.
type Delivered struct {
	Seq     uint64
	Payload []byte
}

// Gap is an inclusive range of extended sequence numbers never received.
type Gap struct {
	StartSeq uint64
	EndSeq   uint64
}

// Stats summarizes window behavior for the acceptance report.
type Stats struct {
	Duplicates  int
	Reordered   int
	Gaps        int
	LateDropped int
	Delivered   int
}

// Window is a bounded reorder buffer keyed by extended (unwrapped) RTP
// sequence numbers. Both internal maps stay bounded by the window width:
// entries at or below the finalized boundary are pruned immediately.
type Window struct {
	width     uint64
	seen      map[uint64]bool
	pending   map[uint64][]byte
	firstSeq  uint64
	highest   uint64
	next      uint64
	finalized uint64 // every seq <= finalized was delivered or gapped
	started   bool
	gaps      []Gap
	finalGaps int // number of gaps finalized before Flush
	stats     Stats
}

// NewWindow creates a reorder window allowing packets up to width slots
// out of order before a missing slot is finalized as a gap.
func NewWindow(width uint16) *Window {
	return &Window{
		width:   uint64(width),
		seen:    make(map[uint64]bool),
		pending: make(map[uint64][]byte),
	}
}

// Push feeds one sequence number and its payload. It returns the
// classification and the packets that can now be delivered in order
// (possibly empty). Delivered payloads are owned by the caller.
func (w *Window) Push(seq uint16, payload []byte) (Outcome, []Delivered) {
	if !w.started {
		w.started = true
		ext := uint64(seq)
		w.firstSeq, w.highest, w.next, w.finalized = ext, ext, ext, ext-1
		w.seen[ext] = true
		w.pending[ext] = payload
		return OutBuffered, w.drain()
	}
	ext := unwrap(seq, w.highest)
	if w.seen[ext] {
		w.stats.Duplicates++
		return OutDuplicate, nil
	}
	if ext < w.highest && w.highest-ext > w.width {
		// Beyond the low edge of the window: the slot was already
		// delivered or recorded as a gap. Never insert data here.
		w.stats.LateDropped++
		return OutLateGap, nil
	}
	w.seen[ext] = true
	w.retractGap(ext) // late in-window packet fills a finalized gap
	if ext < w.highest {
		// A higher sequence number had already arrived: this packet
		// came in out of order and was restored by the window.
		w.stats.Reordered++
	}
	w.pending[ext] = payload
	if ext > w.highest {
		w.highest = ext
	}
	w.finalizeHoles()
	return OutBuffered, w.drain()
}

// Flush ends the stream: every still-missing slot below the highest
// sequence number becomes a gap, then buffered packets are delivered.
func (w *Window) Flush() (delivered []Delivered, gaps []Gap) {
	if !w.started {
		return nil, nil
	}
	// Only scan above the finalized edge: every slot at or below it is
	// already delivered or recorded as a gap, regardless of pruning.
	start := w.finalized + 1
	if start < w.firstSeq {
		start = w.firstSeq
	}
	for s := start; s <= w.highest; s++ {
		if !w.seen[s] {
			w.ensureGap(s, s)
		}
	}
	w.finalized = w.highest
	w.finalGaps = len(w.gaps)
	return w.drain(), append([]Gap(nil), w.gaps...)
}

// FirstSeq returns the extended sequence number that anchors the
// timeline (sample position zero).
func (w *Window) FirstSeq() uint64 { return w.firstSeq }

// Gaps returns a copy of all finalized gap ranges.
func (w *Window) Gaps() []Gap { return append([]Gap(nil), w.gaps...) }

// FinalizedGaps returns only gaps confirmed before Flush, i.e. ranges
// that can no longer be filled by an in-window late packet.
func (w *Window) FinalizedGaps() []Gap {
	return append([]Gap(nil), w.gaps[:w.finalGaps]...)
}

// Stats returns a copy of the running counters.
func (w *Window) Stats() Stats { return w.stats }

// drain emits every contiguous pending packet starting at w.next,
// skipping over sequence slots already recorded as gaps (those slots
// never contain a packet and must not stall later delivery).
func (w *Window) drain() []Delivered {
	var out []Delivered
	for {
		if _, isGap := w.gapAt(w.next); isGap {
			w.next++
			continue
		}
		payload, ok := w.pending[w.next]
		if !ok {
			w.pruneSeen()
			return out
		}
		delete(w.pending, w.next)
		w.stats.Delivered++
		out = append(out, Delivered{Seq: w.next, Payload: payload})
		w.next++
	}
}

// gapAt reports whether seq is inside a finalized gap range.
func (w *Window) gapAt(seq uint64) (Gap, bool) {
	for _, g := range w.gaps {
		if seq >= g.StartSeq && seq <= g.EndSeq {
			return g, true
		}
	}
	return Gap{}, false
}

// finalizeHoles marks unseen slots that can no longer be filled because
// they fell beyond the low edge of the window.
func (w *Window) finalizeHoles() {
	upto := int64(w.highest) - int64(w.width) - 1
	for s := int64(w.finalized) + 1; s <= upto; s++ {
		us := uint64(s)
		if !w.seen[us] {
			w.ensureGap(us, us)
		}
		w.finalized = us
	}
}

// pruneSeen drops bookkeeping for sequence slots already more than one
// window width behind the delivery edge. The subtraction is written in
// addition form to avoid unsigned underflow near the stream start.
func (w *Window) pruneSeen() {
	for s := range w.seen {
		if s < w.next && w.next-s > w.width+1 {
			delete(w.seen, s)
		}
	}
}

// ensureGap records [start,end], merging with the previous range when
// the ranges touch, so adjacent missing slots form one gap.
func (w *Window) ensureGap(start, end uint64) {
	if n := len(w.gaps); n > 0 && w.gaps[n-1].EndSeq+1 == start {
		w.gaps[n-1].EndSeq = end
	} else {
		w.gaps = append(w.gaps, Gap{StartSeq: start, EndSeq: end})
	}
	w.finalGaps = len(w.gaps)
	w.stats.Gaps = len(w.gaps)
}

// retractGap removes a single slot from the finalized gap list because a
// packet that was still inside the window has just filled it.
func (w *Window) retractGap(seq uint64) {
	for i, g := range w.gaps {
		if seq < g.StartSeq || seq > g.EndSeq {
			continue
		}
		switch {
		case g.StartSeq == g.EndSeq:
			w.gaps = append(w.gaps[:i], w.gaps[i+1:]...)
		case seq == g.StartSeq:
			w.gaps[i].StartSeq++
		case seq == g.EndSeq:
			w.gaps[i].EndSeq--
		default:
			// Split into two ranges.
			right := Gap{StartSeq: seq + 1, EndSeq: g.EndSeq}
			w.gaps[i].EndSeq = seq - 1
			w.gaps = append(w.gaps[:i+1], append([]Gap{right}, w.gaps[i+1:]...)...)
		}
		w.finalGaps = len(w.gaps)
		w.stats.Gaps = len(w.gaps)
		return
	}
}

// unwrap extends a 16-bit sequence number against the highest extended
// value seen, choosing the candidate nearest the expected next value.
func unwrap(seq uint16, highest uint64) uint64 {
	base := highest + 1
	candidate := (base &^ 0xFFFF) | uint64(seq)
	d := int64(candidate) - int64(base)
	switch {
	case d > 32767:
		candidate -= 1 << 16
	case d <= -32768:
		candidate += 1 << 16
	}
	return candidate
}
