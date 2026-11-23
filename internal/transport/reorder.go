package transport

import (
	"sort"

	"mediaroute/internal/clock"
)

// Gap describes a sequence window the reorder window could not repair,
// together with the RTP timestamp span of the packets around it so later
// stages can quarantine the affected time range.
type Gap struct {
	SSRC       uint32
	FromSeq    uint16 // first missing sequence number
	ToSeq      uint16 // last missing sequence number (inclusive)
	Count      int
	StartTS    uint32 // timestamp of the packet before the gap
	EndTS      uint32 // timestamp of the packet after the gap
	Recovered  bool   // true if a late packet eventually filled it
}

// Stats accumulates per-SSRC stream restoration counters.
type Stats struct {
	SSRC       uint32
	Received   int // distinct accepted packets (duplicates excluded)
	Duplicates int // idempotent duplicate deliveries
	Reordered  int // packets that arrived out of order but inside the window
	Lost       int // sequence numbers never recovered
	Gaps       []Gap
}

// Reorderer restores one SSRC stream. Memory is bounded by Window: packets
// arriving further than Window ahead of the expected sequence number are
// refused (the caller simulates this as a burst gap) rather than buffered.
type Reorderer struct {
	SSRC    uint32
	Window  int
	started bool
	next    int64 // unwrapped expected sequence
	lastTS  uint32
	buf     map[uint16]*Packet
	stats   Stats
	gap     *Gap // currently open (not yet closed) gap
}

// NewReorderer builds a reorderer with a finite reorder window.
func NewReorderer(ssrc uint32, window int) *Reorderer {
	if window < 1 {
		window = 1
	}
	return &Reorderer{SSRC: ssrc, Window: window, buf: map[uint16]*Packet{}}
}

// Push accepts one packet. It returns the packets that can now be released in
// sequence order (zero or more), and whether this exact packet was a
// duplicate (handled idempotently).
func (r *Reorderer) Push(p *Packet) (out []*Packet, duplicate bool, err error) {
	if p.SSRC != r.SSRC {
		return nil, false, clock.E(clock.PhaseReorder, p.SSRC, int(p.Seq), true,
			"SSRC mismatch on reorderer", nil)
	}
	if !r.started {
		r.started = true
		r.next = int64(p.Seq)
		r.stats.SSRC = r.SSRC
	}
	dist := int64(int16(p.Seq - uint16(r.next))) // signed modular distance
	switch {
	case dist < 0:
		// Already released, or already held: duplicate delivery.
		r.stats.Duplicates++
		return nil, true, nil
	case dist > int64(r.Window):
		// Outside the finite window: record a burst gap and skip ahead so
		// the stream stays bounded instead of buffering without limit.
		r.openGap(p, dist)
		r.stats.Lost += int(dist)
		r.next += dist
	}
	if _, held := r.buf[p.Seq]; held {
		r.stats.Duplicates++
		return nil, true, nil
	}
	if dist > 0 && r.gap == nil {
		// Arrived ahead of expectation but inside the window: reordered.
		r.stats.Reordered++
	}
	r.buf[p.Seq] = p
	r.stats.Received++
	return r.flush(p), false, nil
}

func (r *Reorderer) openGap(after *Packet, dist int64) {
	from := uint16(r.next)
	to := uint16(r.next + dist - 1)
	g := Gap{SSRC: r.SSRC, FromSeq: from, ToSeq: to, Count: int(dist), EndTS: after.Timestamp}
	// Preserve the timestamp of the packet immediately preceding the gap;
	// on consecutive gaps keep the original span start.
	if r.gap != nil {
		g.StartTS = r.gap.StartTS
	} else {
		g.StartTS = r.lastTS
	}
	r.gap = &g
	r.stats.Gaps = append(r.stats.Gaps, g)
}

// flush releases every contiguous packet starting at r.next.
func (r *Reorderer) flush(after *Packet) []*Packet {
	var out []*Packet
	for {
		p, ok := r.buf[uint16(r.next)]
		if !ok {
			return out
		}
		delete(r.buf, uint16(r.next))
		if r.gap != nil {
			r.gap.Recovered = false
			r.gap.EndTS = p.Timestamp
			r.gap = nil
		}
		out = append(out, p)
		r.lastTS = p.Timestamp
		r.next++
	}
}

// Drain ends the stream: any remaining buffered packets are released in
// sequence order and an unfinished gap is kept in stats with its preserved
// time span. The reorderer must not be reused afterwards.
func (r *Reorderer) Drain() []*Packet {
	seqs := make([]uint16, 0, len(r.buf))
	for s := range r.buf {
		seqs = append(seqs, s)
	}
	sort.Slice(seqs, func(i, j int) bool {
		return int64(int16(seqs[i]-uint16(r.next))) < int64(int16(seqs[j]-uint16(r.next)))
	})
	out := make([]*Packet, 0, len(seqs))
	for _, s := range seqs {
		out = append(out, r.buf[s])
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
	window int
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

// Stream returns the reorderer for an SSRC, or nil.
func (d *Demux) Stream(ssrc uint32) *Reorderer { return d.streams[ssrc] }

// SSRCs lists the observed stream identifiers in first-seen order would
// require extra state; callers generally know their track SSRCs.
func (d *Demux) SSRCs() []uint32 {
	out := make([]uint32, 0, len(d.streams))
	for s := range d.streams {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
