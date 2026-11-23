package transport

import (
	"sort"

	"github.com/zld/media-check/media"
)

// GapState describes the lifecycle of one missing-sequence range.
type GapState string

const (
	// GapPending means the range is still inside the reorder window.
	GapPending GapState = "pending"
	// GapRecovered means a late packet filled the range before it was lost.
	GapRecovered GapState = "recovered"
	// GapLost means the range left the window without being filled.
	GapLost GapState = "lost"
	// GapLate means a packet arrived after its range was already declared lost.
	GapLate GapState = "late_after_loss"
)

// Gap is one contiguous run of missing extended sequence numbers. Start and End
// are inclusive.
type Gap struct {
	Start uint64   `json:"start"`
	End   uint64   `json:"end"`
	State GapState `json:"state"`
}

// Len returns the number of missing sequences in the gap.
func (g Gap) Len() int { return int(g.End-g.Start) + 1 }

// Reorderer is a bounded reorder buffer. Packets are released in ascending
// extended sequence order; once the buffer exceeds its window the head of the
// missing range is declared lost and successors are released.
type Reorderer struct {
	window   uint64
	next     uint64
	started  bool
	hold     map[uint64]Packet
	maxHeld  uint64
	gaps     []Gap
	finished bool

	Stats Stats
}

// NewReorderer builds a reorderer with the given window in packets.
func NewReorderer(window int) (*Reorderer, error) {
	if window < 1 || window > 1<<20 {
		return nil, media.NewError(media.ClassInvalidArgument, "reorder", 0, "window %d outside 1..1048576", window)
	}
	return &Reorderer{window: uint64(window), hold: make(map[uint64]Packet)}, nil
}

// Push accepts a packet by extended sequence number and returns every packet
// that became releasable, in ascending order. Duplicate or too-old packets are
// reported via stale=true and never released.
func (r *Reorderer) Push(ext uint64, p Packet) (out []Packet, stale bool) {
	r.Stats.Received++
	if !r.started {
		r.started = true
		r.next = ext
	}
	if ext < r.next {
		r.Stats.LateArrived++
		r.markLate(ext)
		return nil, true
	}
	if _, dup := r.hold[ext]; dup {
		return nil, true
	}
	if ext < r.maxHeld {
		// arrived behind the high-water mark but inside the window: this is a
		// genuine reorder event, released later in sequence order.
		r.Stats.Reordered++
	}
	p.ExtSeq = ext
	r.hold[ext] = p
	if ext > r.maxHeld {
		r.maxHeld = ext
	}
	out = append(out, r.collect()...)
	out = append(out, r.expire()...)
	r.Stats.Dispatched += len(out)
	return out, false
}

// collect releases the contiguous run starting at next.
func (r *Reorderer) collect() []Packet {
	var out []Packet
	for {
		p, ok := r.hold[r.next]
		if !ok {
			return out
		}
		delete(r.hold, r.next)
		r.recoverAt(r.next)
		out = append(out, p)
		r.next++
	}
}

// expire declares the head of the buffer lost while it lags the newest packet
// by at least the window size, then releases anything that became contiguous.
func (r *Reorderer) expire() []Packet {
	var out []Packet
	for r.maxHeld >= r.next && r.maxHeld-r.next >= r.window {
		if _, ok := r.hold[r.next]; ok {
			break
		}
		r.recordLoss(r.next)
		r.Stats.Lost++
		r.next++
		out = append(out, r.collect()...)
	}
	return out
}

// recordLoss appends seq to the trailing lost gap, extending it if adjacent.
func (r *Reorderer) recordLoss(seq uint64) {
	if n := len(r.gaps); n > 0 {
		g := &r.gaps[n-1]
		if g.State == GapLost && g.End+1 == seq {
			g.End = seq
			return
		}
	}
	r.gaps = append(r.gaps, Gap{Start: seq, End: seq, State: GapLost})
}

// recoverAt marks a gap recovered when a packet at seq arrives in order. Only
// gaps that were previously lost can transition; a gap that was never declared
// is simply not recorded.
func (r *Reorderer) recoverAt(seq uint64) {
	for i := range r.gaps {
		g := &r.gaps[i]
		if seq >= g.Start && seq <= g.End && g.State != GapRecovered {
			g.State = GapRecovered
			return
		}
	}
}

// markLate flags a packet that arrived after its sequence was declared lost.
func (r *Reorderer) markLate(ext uint64) {
	for i := range r.gaps {
		g := &r.gaps[i]
		if ext >= g.Start && ext <= g.End {
			g.State = GapLate
			return
		}
	}
}

// Finish closes the stream: any still-missing head sequences are declared lost,
// all held packets are released in ascending order, and the finalized gaps are
// returned sorted by start.
func (r *Reorderer) Finish() ([]Packet, []Gap) {
	if r.finished {
		return nil, r.Gaps()
	}
	r.finished = true
	var out []Packet
	if r.started && r.maxHeld >= r.next {
		limit := r.maxHeld
		for r.next <= limit {
			if p, ok := r.hold[r.next]; ok {
				delete(r.hold, r.next)
				r.recoverAt(r.next)
				out = append(out, p)
				r.Stats.Dispatched++
				r.next++
				continue
			}
			r.recordLoss(r.next)
			r.Stats.Lost++
			r.next++
		}
	}
	for i := range r.gaps {
		if r.gaps[i].State == GapPending {
			r.gaps[i].State = GapLost
		}
	}
	return out, r.Gaps()
}

// Gaps returns a sorted copy of the gap records.
func (r *Reorderer) Gaps() []Gap {
	out := append([]Gap(nil), r.gaps...)
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// LostRanges returns gaps that ended up lost or were filled late.
func (r *Reorderer) LostRanges() []Gap {
	var out []Gap
	for _, g := range r.Gaps() {
		if g.State == GapLost || g.State == GapLate {
			out = append(out, g)
		}
	}
	return out
}

// Pending returns the number of buffered packets not yet released.
func (r *Reorderer) Pending() int { return len(r.hold) }

// Next returns the next expected extended sequence number.
func (r *Reorderer) Next() uint64 { return r.next }

// Recorder is the receive-side driver: parse, extend, dedup, reorder and record
// gaps for one RTP stream. It is driven from a single goroutine.
type Recorder struct {
	ext   *SeqExtender
	dedup *DedupFilter
	reord *Reorderer
}

// NewRecorder wires the stages together for one SSRC.
func NewRecorder(ssrc uint32, window, dedupWindow int) (*Recorder, error) {
	re, err := NewReorderer(window)
	if err != nil {
		return nil, err
	}
	dd, err := NewDedupFilter(dedupWindow)
	if err != nil {
		return nil, err
	}
	return &Recorder{ext: NewSeqExtender(ssrc), dedup: dd, reord: re}, nil
}

// Consume parses one datagram and returns the packets it released, in order.
func (r *Recorder) Consume(datagram []byte, arrival int64) ([]Packet, error) {
	p, err := Parse(datagram, arrival)
	if err != nil {
		return nil, err
	}
	ext, err := r.ext.Extend(p)
	if err != nil {
		return nil, err
	}
	if r.dedup.Seen(ext) {
		r.reord.Stats.Duplicates++
		return nil, nil
	}
	released, _ := r.reord.Push(ext, p)
	return released, nil
}

// Finish flushes the reorder buffer at end of input.
func (r *Recorder) Finish() ([]Packet, []Gap) { return r.reord.Finish() }

// Stats returns the merged counter view. Duplicates come from the dedup stage,
// everything else from the reorder stage.
func (r *Recorder) Stats() Stats {
	s := r.reord.Stats
	s.Duplicates = r.dedup.Dropped()
	return s
}

// Gaps returns the finalized gap record list.
func (r *Recorder) Gaps() []Gap { return r.reord.Gaps() }
