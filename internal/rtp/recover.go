// Package rtp recovers ordered packet streams from 16-bit-sequence RTP
// flows: it reorders within a bounded window, counts duplicates, recovers
// retransmitted packets that arrive inside the window, and marks losses
// beyond the window as unrecoverable.
package rtp

import (
	"github.com/pion/rtp"

	"mediapipe/internal/clock"
)

// Stats summarizes a Recoverer's lifetime counters.
type Stats struct {
	Received   int // packets pushed
	Emitted    int // packets emitted in order
	Recovered  int // retransmitted packets recovered inside the window
	Lost       int // packets declared lost (window exceeded or flushed)
	Duplicates int // duplicate pushes
}

// RecoveryRate is recovered / (recovered + lost); 1 when nothing was lost.
func (s Stats) RecoveryRate() float64 {
	den := s.Recovered + s.Lost
	if den == 0 {
		return 1
	}
	return float64(s.Recovered) / float64(den)
}

// Recoverer reorders one RTP stream with a bounded recovery window.
type Recoverer struct {
	Window  uint16
	next    uint16
	started bool
	buf     map[uint16]*rtp.Packet
	nacked  map[uint16]bool // missing seqs still inside the window
	stats   Stats
}

// NewRecoverer creates a Recoverer with the given window size in packets.
func NewRecoverer(window uint16) *Recoverer {
	return &Recoverer{
		Window: window,
		buf:    map[uint16]*rtp.Packet{},
		nacked: map[uint16]bool{},
	}
}

// Stats returns the current counters.
func (r *Recoverer) Stats() Stats { return r.stats }

// Push feeds one received packet and returns any packets that are now
// emit-ready in sequence order.
func (r *Recoverer) Push(p *rtp.Packet) []*rtp.Packet {
	r.stats.Received++
	seq := p.SequenceNumber
	if !r.started {
		r.started = true
		r.next = seq
	}
	delta := clock.Seq16Delta(seq, r.next)
	switch {
	case delta == 0:
		if r.nacked[seq] {
			delete(r.nacked, seq)
			r.stats.Recovered++
		}
		return r.emit(p)
	case delta > 0:
		if int(delta) > int(r.Window) {
			// Gap beyond the recovery window: everything up to seq is lost.
			return r.jumpLost(p)
		}
		if _, dup := r.buf[seq]; dup {
			r.stats.Duplicates++
			return nil
		}
		// Open NACK entries for the hole, then buffer the packet.
		for s := r.next; clock.Seq16Less(s, seq); s++ {
			if _, ok := r.buf[s]; !ok {
				r.nacked[s] = true
			}
		}
		r.buf[seq] = p
		return r.enforceWindow()
	default: // delta < 0: late or duplicate
		if r.nacked[seq] {
			delete(r.nacked, seq)
			r.stats.Recovered++
			r.buf[seq] = p
			return r.drain()
		}
		r.stats.Duplicates++
		return nil
	}
}

// emit outputs p, advances next, and drains consecutive buffered packets.
func (r *Recoverer) emit(p *rtp.Packet) []*rtp.Packet {
	out := []*rtp.Packet{p}
	r.stats.Emitted++
	r.next = p.SequenceNumber + 1
	return append(out, r.drain()...)
}

// drain emits buffered packets that are now contiguous with next.
func (r *Recoverer) drain() []*rtp.Packet {
	var out []*rtp.Packet
	for {
		p, ok := r.buf[r.next]
		if !ok {
			return out
		}
		delete(r.buf, r.next)
		delete(r.nacked, r.next)
		out = append(out, p)
		r.stats.Emitted++
		r.next++
	}
}

// enforceWindow declares losses when the buffered span exceeds the window.
func (r *Recoverer) enforceWindow() []*rtp.Packet {
	var out []*rtp.Packet
	for len(r.buf) > 0 {
		hiDelta := int32(-1)
		for s := range r.buf {
			if d := clock.Seq16Delta(s, r.next); d > hiDelta {
				hiDelta = d
			}
		}
		if hiDelta < int32(r.Window) {
			break
		}
		if p, ok := r.buf[r.next]; ok {
			delete(r.buf, r.next)
			delete(r.nacked, r.next)
			out = append(out, p)
			r.stats.Emitted++
		} else {
			r.declareLost()
		}
		r.next++
	}
	return append(out, r.drain()...)
}

// jumpLost handles a gap larger than the window in a single packet.
func (r *Recoverer) jumpLost(p *rtp.Packet) []*rtp.Packet {
	var out []*rtp.Packet
	for clock.Seq16Less(r.next, p.SequenceNumber) {
		if bp, ok := r.buf[r.next]; ok {
			delete(r.buf, r.next)
			delete(r.nacked, r.next)
			out = append(out, bp)
			r.stats.Emitted++
		} else {
			r.declareLost()
		}
		r.next++
	}
	return append(out, r.emit(p)...)
}

// Flush ends the stream: pending holes become losses and the buffer drains.
func (r *Recoverer) Flush() []*rtp.Packet {
	var out []*rtp.Packet
	for len(r.buf) > 0 || len(r.nacked) > 0 {
		if p, ok := r.buf[r.next]; ok {
			delete(r.buf, r.next)
			delete(r.nacked, r.next)
			out = append(out, p)
			r.stats.Emitted++
		} else {
			r.declareLost()
		}
		r.next++
	}
	return out
}

func (r *Recoverer) declareLost() {
	delete(r.nacked, r.next)
	r.stats.Lost++
}
