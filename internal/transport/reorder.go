package transport

import (
	"sort"

	"mediadist/internal/merr"
)

// Stats counts transport-side observations. All counters are plain sums so
// reports are stable across runs.
type Stats struct {
	Received   int
	Accepted   int
	Duplicate  int
	Reordered  int
	Late       int
	Committed  int
	Gaps       int
	BufferPeak int
	Rejected   int
	Repaired   int
}

// ReorderConfig configures one stream's reorder buffer.
type ReorderConfig struct {
	Stream int
	// Window is how many sequence numbers may be held ahead of the next
	// expected packet before missing ones are declared lost. Larger windows
	// tolerate more reordering at the cost of latency and memory.
	Window int
	// MaxSeen bounds the duplicate-detection set.
	MaxSeen int
}

// DefaultReorderConfig returns a window of 64 sequence numbers.
func DefaultReorderConfig(stream int) ReorderConfig {
	return ReorderConfig{Stream: stream, Window: 64, MaxSeen: 8192}
}

// ReorderBuffer orders packets per stream by extended 16-bit sequence number.
//
// Commit rules:
//   - A packet is emitted only once every earlier sequence number is either
//     present or has been declared lost.
//   - Emitted packets are committed; a later-arriving packet below the
//     committed watermark is reported as late and never emitted.
//   - Duplicates are counted and dropped without affecting the timeline.
//
// The buffer also reports loss spans as extended sequence intervals.
type ReorderBuffer struct {
	cfg ReorderConfig
	lim Limits

	primed  bool
	ref     int64 // extended sequence of the most recent arrival
	next    int64 // next sequence number to emit
	maxSeen int64

	held  map[int64]*Packet
	seen  map[int64]struct{}
	seenQ []int64

	stats Stats
	errs  []*merr.Error
	gaps  []SeqSpan
	// late holds sequences that arrived below the commit watermark, in
	// arrival order, so the caller can attach them to loss evidence.
	late []int64
	// newGaps holds the holes this release pass committed, in ascending
	// order. A single pass can cross several disjoint holes, so a single
	// range would conflate committed packets with loss.
	newGaps []SeqSpan
}

// SeqSpan is an extended sequence interval, inclusive at both ends.
type SeqSpan struct {
	Stream int
	First  int64
	Last   int64
}

// Count is the number of sequence numbers in the span.
func (s SeqSpan) Count() int64 {
	if s.Last < s.First {
		return 0
	}
	return s.Last - s.First + 1
}

// NewReorderBuffer validates configuration and returns an empty buffer.
func NewReorderBuffer(cfg ReorderConfig, lim Limits) (*ReorderBuffer, error) {
	lim, err := lim.Validate()
	if err != nil {
		return nil, err
	}
	if cfg.Window <= 0 {
		cfg.Window = DefaultReorderConfig(cfg.Stream).Window
	}
	if cfg.Window > lim.MaxPackets {
		return nil, merr.New(merr.StageReorder, "bad_window", merr.NoSpan(cfg.Stream), false,
			"window %d exceeds packet limit %d", cfg.Window, lim.MaxPackets)
	}
	if cfg.MaxSeen <= 0 {
		cfg.MaxSeen = DefaultReorderConfig(cfg.Stream).MaxSeen
	}
	if cfg.MaxSeen < cfg.Window {
		cfg.MaxSeen = cfg.Window * 2
	}
	return &ReorderBuffer{
		cfg:  cfg,
		lim:  lim,
		held: make(map[int64]*Packet, cfg.Window),
		seen: make(map[int64]struct{}, cfg.MaxSeen),
	}, nil
}

// extend maps a raw 16-bit sequence to int64 relative to a reference.
func extendSeq(raw uint16, ref int64, primed bool) int64 {
	const wrap = int64(1) << 16
	if !primed {
		return int64(raw)
	}
	base := ref - mod64(ref, wrap)
	cand := base + int64(raw)
	if cand-ref > wrap/2 {
		cand -= wrap
	} else if ref-cand > wrap/2 {
		cand += wrap
	}
	return cand
}

func mod64(a, b int64) int64 {
	m := a % b
	if m < 0 {
		m += b
	}
	return m
}

// Push admits one packet and returns any newly emittable packets in sequence
// order. Recoverable errors are also retained for the final report.
func (r *ReorderBuffer) Push(p *Packet) []*Packet {
	return r.push(p, false)
}

// PushRecovered admits a retransmitted packet that fills a declared hole. It
// follows the same ordering rules, but the caller is expected to have already
// told the loss recorder that this sequence was repaired.
func (r *ReorderBuffer) PushRecovered(p *Packet) []*Packet {
	return r.push(p, true)
}

func (r *ReorderBuffer) push(p *Packet, repair bool) []*Packet {
	r.stats.Received++
	ext := extendSeq(p.Seq, r.ref, r.primed)
	if !r.primed {
		r.ref = ext
		r.next = ext
		r.maxSeen = ext - 1
		r.primed = true
	}
	if ext > r.maxSeen {
		r.maxSeen = ext
	}
	if repair {
		r.stats.Repaired++
	}

	if ext < r.next {
		// Below the committed watermark: the fragment owning this sequence is
		// already final, so the packet is late evidence, not new media. This
		// check precedes duplicate detection because a repeat of an already
		// committed sequence is a late arrival, not a duplicate in flight.
		r.stats.Late++
		r.stats.Rejected++
		r.late = append(r.late, ext)
		r.markSeen(ext)
		r.noteError(merr.New(merr.StageReorder, "late_packet",
			merr.SeqSpan(r.cfg.Stream, p.Seq), true,
			"sequence %d arrived after watermark %d", p.Seq, r.next))
		return nil
	}
	if r.isSeen(ext) {
		r.stats.Duplicate++
		r.stats.Rejected++
		r.noteError(merr.New(merr.StageReorder, "duplicate_packet",
			merr.SeqSpan(r.cfg.Stream, p.Seq), true,
			"sequence %d already received (extended %d)", p.Seq, ext))
		return nil
	}
	if _, exists := r.held[ext]; exists {
		r.stats.Duplicate++
		r.stats.Rejected++
		r.markSeen(ext)
		r.noteError(merr.New(merr.StageReorder, "duplicate_packet",
			merr.SeqSpan(r.cfg.Stream, p.Seq), true,
			"sequence %d still buffered", p.Seq))
		return nil
	}
	// A retransmission is deliberately late by construction, so it is not
	// counted as a reordered arrival; only organic out-of-order packets are.
	if ext < r.maxSeen && !repair {
		r.stats.Reordered++
	}
	r.held[ext] = p
	r.markSeen(ext)
	r.stats.Accepted++
	if len(r.held) > r.stats.BufferPeak {
		r.stats.BufferPeak = len(r.held)
	}
	if len(r.held) > r.lim.MaxPackets {
		r.stats.Rejected++
		delete(r.held, ext)
		r.noteError(merr.New(merr.StageReorder, "buffer_overflow",
			merr.SeqSpan(r.cfg.Stream, p.Seq), true,
			"reorder buffer exceeded %d packets", r.lim.MaxPackets))
	}
	return r.release(false)
}

// Flush emits everything still buffered, declaring any holes as loss. Call it
// once at end of stream.
func (r *ReorderBuffer) Flush() []*Packet {
	return r.release(true)
}

// release emits held packets up to the safe horizon. Non-forced release keeps
// the most recent Window sequence numbers held back so a retransmission can
// still fill a hole before it becomes committed loss. Forced release (end of
// stream) commits everything, turning remaining holes into lost spans.
func (r *ReorderBuffer) release(force bool) []*Packet {
	if !r.primed {
		return nil
	}
	horizon := r.maxSeen
	if !force {
		horizon = r.maxSeen - int64(r.cfg.Window)
	}
	if horizon < r.next {
		return nil
	}
	var out []*Packet
	for ext := r.next; ext <= horizon; ext++ {
		if p, ok := r.held[ext]; ok {
			out = append(out, p)
			delete(r.held, ext)
			r.stats.Committed++
			continue
		}
		// Collect one contiguous hole so a burst is reported as one span.
		first := ext
		last := ext
		for last+1 <= horizon {
			if _, ok := r.held[last+1]; ok {
				break
			}
			last++
		}
		r.gaps = append(r.gaps, SeqSpan{Stream: r.cfg.Stream, First: first, Last: last})
		r.stats.Gaps++
		r.newGaps = append(r.newGaps, SeqSpan{Stream: r.cfg.Stream, First: first, Last: last})
		r.noteError(merr.New(merr.StageLoss, "sequence_gap",
			merr.Span{Stream: r.cfg.Stream, Seq: first, Offset: -1, Length: last - first + 1}, true,
			"missing %d sequence(s) %d..%d", last-first+1, first, last))
		ext = last
	}
	r.next = horizon + 1
	return out
}

// PendingHole reports whether seq is currently held back by a hole that has
// not yet been committed. A repair arriving now can still be applied.
func (r *ReorderBuffer) PendingHole(seq uint16) bool {
	ext := extendSeq(seq, r.ref, r.primed)
	return r.primed && ext >= r.next && ext <= r.maxSeen && !r.isSeen(ext)
}

// PendingGaps returns the holes inside the uncommitted tail, i.e. sequences in
// [next, maxSeen] that never arrived. These are repairable: they only become
// lost when release advances past them.
func (r *ReorderBuffer) PendingGaps() []SeqSpan {
	if !r.primed || r.maxSeen < r.next {
		return nil
	}
	var out []SeqSpan
	ext := r.next
	for ext <= r.maxSeen {
		if r.isSeen(ext) {
			ext++
			continue
		}
		first := ext
		for ext+1 <= r.maxSeen && !r.isSeen(ext+1) {
			ext++
		}
		out = append(out, SeqSpan{Stream: r.cfg.Stream, First: first, Last: ext})
		ext++
	}
	return out
}

// CommittedThrough is the highest extended sequence number already emitted.
func (r *ReorderBuffer) CommittedThrough() int64 { return r.next - 1 }

// LateArrivals returns the extended sequences that arrived after their
// fragment committed, in arrival order.
func (r *ReorderBuffer) LateArrivals() []int64 {
	return append([]int64(nil), r.late...)
}

// CommittedGaps returns the holes the most recent release pass committed, in
// ascending order, and clears them. Each hole is returned exactly once.
func (r *ReorderBuffer) CommittedGaps() []SeqSpan {
	out := r.newGaps
	r.newGaps = nil
	return out
}

// Gaps returns the loss spans recorded so far, sorted by first sequence.
func (r *ReorderBuffer) Gaps() []SeqSpan {
	out := append([]SeqSpan(nil), r.gaps...)
	sort.Slice(out, func(i, j int) bool { return out[i].First < out[j].First })
	return out
}

// Stats returns a copy of the counters.
func (r *ReorderBuffer) Stats() Stats { return r.stats }

// Errors returns a copy of recoverable errors in arrival order.
func (r *ReorderBuffer) Errors() []*merr.Error {
	return append([]*merr.Error(nil), r.errs...)
}

// Watermark is the next extended sequence number that will be emitted.
func (r *ReorderBuffer) Watermark() int64 { return r.next }

func (r *ReorderBuffer) noteError(e *merr.Error) {
	if len(r.errs) < r.lim.MaxDropped {
		r.errs = append(r.errs, e)
	}
}

func (r *ReorderBuffer) isSeen(ext int64) bool {
	_, ok := r.seen[ext]
	return ok
}

// markSeen records ext, evicting the oldest entries when the bound is reached.
// Eviction is FIFO so the set size stays deterministic.
func (r *ReorderBuffer) markSeen(ext int64) {
	if _, ok := r.seen[ext]; ok {
		return
	}
	if len(r.seen) >= r.cfg.MaxSeen {
		drop := r.seenQ[0]
		r.seenQ = r.seenQ[1:]
		delete(r.seen, drop)
	}
	r.seen[ext] = struct{}{}
	r.seenQ = append(r.seenQ, ext)
}
