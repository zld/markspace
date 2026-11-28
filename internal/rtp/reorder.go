// Package rtp reorders RTP packets by sequence number with a bounded reorder
// window. Duplicates are dropped, in-window disorder is restored to order, and
// packets that never arrive once the window is exceeded become explicit gap
// errors instead of silent holes.
package rtp

import (
	"github.com/pion/rtp"

	"medcheck/internal/model"
)

// Gap describes one sequence number that was skipped past the reorder window.
type Gap struct {
	Track  uint32
	Source uint32
	Seq    uint16
}

// Result is what one Push releases: now-orderable packets and any finalized
// gaps, plus counters for packets that were discarded as duplicates.
type Result struct {
	Packets    []*rtp.Packet
	Gaps       []Gap
	Duplicates int
}

// Reorderer is a per (track, source) bounded sequence reorder buffer.
type Reorderer struct {
	track    uint32
	source   uint32
	window   uint16
	init     bool
	expected uint16 // next sequence number that must be emitted
	maxSeq   uint16 // furthest sequence ever accepted
	buf      map[uint16]*rtp.Packet
	seen     map[uint16]struct{}
}

// NewReorderer builds a reorderer. window is the maximum forward distance
// (in sequence numbers) held open for late packets.
func NewReorderer(track, source uint32, window uint16) *Reorderer {
	return &Reorderer{
		track:  track,
		source: source,
		window: window,
		buf:    make(map[uint16]*rtp.Packet),
		seen:   make(map[uint16]struct{}),
	}
}

// forward returns the forward cyclic distance from a to b in [0,65535].
func forward(a, b uint16) uint16 { return b - a }

// Push inserts one parsed packet and returns everything that can now be
// released in sequence order.
func (r *Reorderer) Push(pkt *rtp.Packet) Result {
	var res Result
	if pkt == nil {
		return res
	}
	seq := pkt.SequenceNumber
	if !r.init {
		r.init = true
		r.expected, r.maxSeq = seq, seq
	} else {
		if _, ok := r.seen[seq]; ok {
			res.Duplicates++
			return res
		}
		d := forward(r.expected, seq)
		switch {
		case d == 0:
			// expected slot, handled below
		case d > r.window:
			if d > 1<<15 {
				// Behind the water mark and never seen: late drop, no gap.
				res.Duplicates++
				return res
			}
			// Forward jump beyond the window finalizes every missing seq.
			for s := r.expected; s != seq; s++ {
				res.Gaps = append(res.Gaps, Gap{r.track, r.source, s})
			}
			r.expected = seq
		}
		if forward(r.maxSeq, seq) < 1<<15 {
			r.maxSeq = seq
		}
	}
	r.seen[seq] = struct{}{}
	r.buf[seq] = pkt
	res.Packets = r.drain()
	return res
}

func (r *Reorderer) drain() []*rtp.Packet {
	var out []*rtp.Packet
	for {
		pkt, ok := r.buf[r.expected]
		if !ok {
			return out
		}
		out = append(out, pkt)
		delete(r.buf, r.expected)
		r.expected++
	}
}

// Flush ends the stream: remaining holes up to the furthest seen packet are
// reported as gaps and any buffered packets are emitted in sequence order.
func (r *Reorderer) Flush() Result {
	var res Result
	if !r.init {
		return res
	}
	for s := r.expected; s != r.maxSeq+1; s++ {
		if pkt, ok := r.buf[s]; ok {
			res.Packets = append(res.Packets, pkt)
			delete(r.buf, s)
		} else {
			res.Gaps = append(res.Gaps, Gap{r.track, r.source, s})
		}
	}
	r.expected = r.maxSeq + 1
	return res
}

// GapError converts a Gap into the shared error model. off is the arrival
// position; unknown byte offsets use -1.
func (g Gap) GapError(arrival int64) *model.CodeError {
	return &model.CodeError{
		Kind:     model.KindGap,
		Track:    g.Track,
		Source:   g.Source,
		Seq:      uint32(g.Seq),
		Offset:   model.RawOffset{Start: arrival, End: arrival},
		FirstPos: arrival,
		Message:  "missing rtp sequence outside reorder window",
	}
}
