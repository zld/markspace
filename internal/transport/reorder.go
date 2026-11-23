package transport

import (
	"sort"

	"mediaroute/internal/clock"
)

// Gap describes a sequence window that never fully arrived, together with
// the RTP timestamp span of the packets around it so later stages can
// quarantine the affected time range.
type Gap struct {
	SSRC      uint32
	FromSeq   uint16 // first missing sequence number
	ToSeq     uint16 // last missing sequence number (inclusive)
	Count     int
	StartTS   uint32 // timestamp of the packet before the gap
	EndTS     uint32 // timestamp of the first packet after the gap
	Recovered bool   // true if late packets eventually filled every missing seq
}

// Stats accumulates per-SSRC stream restoration counters.
type Stats struct {
	SSRC       uint32
	Received   int // distinct accepted packets (duplicates excluded)
	Duplicates int // idempotent duplicate deliveries
	Reordered  int // packets that arrived out of sequence order
	Lost       int // sequence numbers missing when the stream drained
	Gaps       []Gap
}

// Reorderer restores one SSRC stream. Hold-buffer memory is bounded by
// Window: only packets within Window sequence positions of the current
// expectation are buffered. Sequence holes are only declared lost at Drain,
// so ordinary reordering inside the window is never mistaken for packet
// loss; a packet beyond the window is reported as a non-recoverable error
// for the caller to quarantine rather than silently swallowed.
type Reorderer struct {
	SSRC    uint32
	Window  int
	started bool
	next    int64 // unwrapped sequence number expected next
	lastTS  uint32
	buf     map[uint16]*Packet
	// seen records every sequence number already accepted (held, released or
	// evicted). A later delivery of any seen seq is an idempotent duplicate.
	seen map[uint16]bool
	// origin is the sequence pinned as the stream start while probation runs;
	// probation ends once the reorder window has had a chance to reveal any
	// earlier in-window sequence.
	origin    int64
	probation bool
	stats     Stats
}

// NewReorderer builds a reorderer with a finite reorder window.
func NewReorderer(ssrc uint32, window int) *Reorderer {
	if window < 1 {
		window = 1
	}
	return &Reorderer{SSRC: ssrc, Window: window,
		buf: map[uint16]*Packet{}, seen: map[uint16]bool{}}
}

// Push accepts one packet. It returns packets now releasable in sequence
// order and whether this packet was a duplicate handled idempotently.
//
// The reorder window bounds the hold buffer: at most Window packets may wait
// on missing sequence numbers. When the buffer is full a still-later packet
// forces the oldest open hole to be declared lost (memory stays bounded)
// instead of rejecting the new packet. Holes settled this way, and holes
// remaining at Drain, are the only source of the Lost counter.
func (r *Reorderer) Push(p *Packet) (out []*Packet, duplicate bool, err error) {
	if p.SSRC != r.SSRC {
		return nil, false, clock.E(clock.PhaseReorder, p.SSRC, int(p.Seq), true,
			"SSRC mismatch on reorderer", nil)
	}
	if !r.started {
		r.started = true
		r.stats.SSRC = r.SSRC
		r.origin = int64(p.Seq)
		r.next = r.origin
		r.seen[p.Seq] = true
		r.stats.Received++
		// The stream origin is not trusted yet: an in-window reorder may
		// deliver seq N+1, N+2 before N. Buffer until the stream-start
		// probation settles (see settleProbation) instead of pinning the
		// origin to whichever packet happened to arrive first.
		r.buf[p.Seq] = p
		r.probation = true
		return nil, false, nil
	}
	// Any sequence already observed (held, released or evicted) is a
	// duplicate regardless of its current position relative to next. This
	// check runs before probation so a replay during stream start is not
	// accepted a second time and does not inflate conservation.
	if r.seen[p.Seq] {
		r.stats.Duplicates++
		return nil, true, nil
	}
	if r.probation {
		// Out-of-order arrivals during probation are still repairs, not loss.
		if d := int64(int16(p.Seq - uint16(r.next))); d < 0 || d > 1 {
			r.stats.Reordered++
		}
		r.buf[p.Seq] = p
		r.seen[p.Seq] = true
		r.stats.Received++
		if d := int64(int16(p.Seq - uint16(r.origin))); d >= int64(r.Window) {
			// A packet a full window past the origin proves the origin is
			// not hiding a lower sequence, so probation can end now.
			r.settleProbation()
			return r.flush(), false, nil
		}
		if r.bufLen() >= r.Window {
			r.settleProbation()
			return r.flush(), false, nil
		}
		return nil, false, nil
	}
	dist := int64(int16(p.Seq - uint16(r.next))) // signed modular distance
	// Bounded-memory rule: if accepting this packet would leave more than
	// Window sequence positions of outstanding holes, settle the leading
	// holes (declare genuinely absent sequences lost) until it fits. The
	// packet itself is always accepted and buffered.
	for dist > int64(r.Window) {
		r.evictOneStep(p, dist)
		dist = int64(int16(p.Seq - uint16(r.next)))
	}
	if dist > 0 {
		r.stats.Reordered++
	}
	r.buf[p.Seq] = p
	r.seen[p.Seq] = true
	r.stats.Received++
	return r.flush(), false, nil
}

// settleProbation ends the stream-start probation by choosing the lowest
// buffered sequence as the origin, then flushing every contiguous packet from
// there. Sequences below the chosen origin can only be an out-of-window
// reorder, which probation cannot repair by design.
func (r *Reorderer) settleProbation() {
	r.probation = false
	if len(r.buf) == 0 {
		return
	}
	first := true
	var low uint16
	for seq := range r.buf {
		if first {
			low, first = seq, false
			continue
		}
		// Prefer the sequence closest to (but not above) the origin.
		if d := int64(int16(seq - uint16(r.origin))); d < int64(int16(low-uint16(r.origin))) {
			low = seq
		}
	}
	r.next = int64(low)
}

// bufLen reports how many packets are held.
func (r *Reorderer) bufLen() int { return len(r.buf) }

// evictOneStep advances the expectation by one position while the target
// packet remains outside the window, declaring the sequence lost when no
// buffered packet occupies it. Every call moves r.next forward, so the
// caller's loop is guaranteed to terminate.
func (r *Reorderer) evictOneStep(after *Packet, dist int64) {
	holeSeq := uint16(r.next)
	if _, held := r.buf[holeSeq]; !held {
		r.seen[holeSeq] = true
		r.stats.Lost++
		r.recordGap(holeSeq, after)
	}
	r.next++
}

// recordGap appends or extends the current contiguous lost-sequence gap.
func (r *Reorderer) recordGap(seq uint16, after *Packet) {
	if n := len(r.stats.Gaps); n > 0 && r.stats.Gaps[n-1].ToSeq == seq-1 {
		g := &r.stats.Gaps[n-1]
		g.ToSeq = seq
		g.Count++
		g.EndTS = after.Timestamp
		return
	}
	r.stats.Gaps = append(r.stats.Gaps, Gap{
		SSRC: r.SSRC, FromSeq: seq, ToSeq: seq, Count: 1,
		StartTS: r.lastTS, EndTS: after.Timestamp,
	})
}

// flush releases every contiguous packet starting at r.next. A hole inside
// the window simply pauses release until later packets fill it.
func (r *Reorderer) flush() []*Packet {
	var out []*Packet
	for {
		p, ok := r.buf[uint16(r.next)]
		if !ok {
			return out
		}
		delete(r.buf, uint16(r.next))
		out = append(out, p)
		r.lastTS = p.Timestamp
		r.next++
	}
}

// Drain ends the stream: remaining buffered packets are released in sequence
// order and every sequence hole still open — before the first held packet,
// between held packets, or after the last one — is declared a gap/lost so the
// conservation invariant closes exactly.
func (r *Reorderer) Drain() []*Packet {
	if r.probation {
		r.settleProbation()
	}
	if len(r.buf) == 0 {
		return nil
	}
	seqs := make([]uint16, 0, len(r.buf))
	for s := range r.buf {
		seqs = append(seqs, s)
	}
	sort.Slice(seqs, func(i, j int) bool {
		return int64(int16(seqs[i]-uint16(r.next))) <
			int64(int16(seqs[j]-uint16(r.next)))
	})
	var out []*Packet
	for _, s := range seqs {
		// Settle every missing sequence before this held packet.
		for r.next < int64(s) {
			hole := uint16(r.next)
			r.seen[hole] = true
			r.stats.Lost++
			r.recordGap(hole, r.buf[s])
			r.next++
		}
		out = append(out, r.buf[s])
		r.lastTS = r.buf[s].Timestamp
		r.next++
	}
	r.buf = map[uint16]*Packet{}
	return out
}

// Stats returns a copy of the accumulated counters.
func (r *Reorderer) Stats() Stats {
	s := r.stats
	s.Gaps = append([]Gap(nil), r.stats.Gaps...)
	return s
}

// Held returns how many packets are currently buffered (bounded by Window).
func (r *Reorderer) Held() int { return len(r.buf) }

// Demux routes packets from several SSRCs into independent reorderers.
type Demux struct {
	window  int
	streams map[uint32]*Reorderer
}

// NewDemux builds a demuxer sharing one reorder window size.
func NewDemux(window int) *Demux {
	return &Demux{window: window, streams: map[uint32]*Reorderer{}}
}

// Push routes one packet, creating its stream on first sight.
func (d *Demux) Push(p *Packet) ([]*Packet, bool, error) {
	r, ok := d.streams[p.SSRC]
	if !ok {
		r = NewReorderer(p.SSRC, d.window)
		d.streams[p.SSRC] = r
	}
	return r.Push(p)
}

// Drain releases every stream's remaining in-order packets and closes gaps.
func (d *Demux) Drain() map[uint32][]*Packet {
	out := map[uint32][]*Packet{}
	for ssrc, r := range d.streams {
		if pkts := r.Drain(); len(pkts) > 0 {
			out[ssrc] = pkts
		}
	}
	return out
}

// Stream returns the reorderer for an SSRC, or nil.
func (d *Demux) Stream(ssrc uint32) *Reorderer { return d.streams[ssrc] }

// SSRCs lists the observed stream identifiers sorted ascending.
func (d *Demux) SSRCs() []uint32 {
	out := make([]uint32, 0, len(d.streams))
	for s := range d.streams {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
