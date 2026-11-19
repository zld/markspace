package rtp

import "errors"

// ErrInvalidWindow is returned when the reorder window is out of range.
var ErrInvalidWindow = errors.New("rtp: reorder window must be between 1 and 4096")

// maxWindow bounds memory used by the reorder buffer.
const maxWindow = 4096

// Stats are the counters maintained while reordering packets.
type Stats struct {
	Received   int // packets handed to Push, including duplicates
	Duplicates int // exact re-delivery of a sequence number
	Late       int // packets arriving after their slot was skipped
	Gaps       int // number of skipped sequence numbers
	Flushed    int // window-exhaustion / end-of-session advances
	Emitted    int // packets released in sequence order
}

// Reorderer restores RTP packet order within a bounded sequence
// window. Missing packets never block the pipeline: when the window
// fills, the gap is recorded and the expected sequence advances.
// Sequence arithmetic is modular, so 16-bit wraparound is handled
// transparently.
type Reorderer struct {
	window   int
	buf      map[uint16]*Packet
	expected uint16
	started  bool
	// emitted tracks sequence numbers already released, within a
	// bounded history, so a re-sent packet is a duplicate while a
	// packet filling a previously skipped gap is merely late.
	emitted     map[uint16]struct{}
	emittedFIFO []uint16
	stats       Stats
}

// NewReorderer creates a Reorderer with the given window size.
func NewReorderer(window int) (*Reorderer, error) {
	if window < 1 || window > maxWindow {
		return nil, ErrInvalidWindow
	}
	return &Reorderer{
		window:      window,
		buf:         make(map[uint16]*Packet, window),
		emitted:     make(map[uint16]struct{}, window),
		emittedFIFO: make([]uint16, 0, window),
	}, nil
}

// recordEmitted marks a released sequence number and evicts the oldest
// entry once the bounded history fills, keeping memory proportional to
// the configured window.
func (r *Reorderer) recordEmitted(seq uint16) {
	if _, ok := r.emitted[seq]; ok {
		return
	}
	r.emitted[seq] = struct{}{}
	r.emittedFIFO = append(r.emittedFIFO, seq)
	if len(r.emittedFIFO) > r.window {
		oldest := r.emittedFIFO[0]
		r.emittedFIFO = r.emittedFIFO[1:]
		delete(r.emitted, oldest)
	}
}

// wasEmitted reports whether seq was released and is still inside the
// bounded emitted-history window.
func (r *Reorderer) wasEmitted(seq uint16) bool {
	_, ok := r.emitted[seq]
	return ok
}

// seqDistance returns (a - b) interpreted as a signed 16-bit value.
// Positive means a is later in the stream than b.
func seqDistance(a, b uint16) int {
	d := int(int16(a - b))
	return d
}

// Push inserts one packet and returns every packet that can now be
// released in sequence order. A duplicate packet yields no output and
// increments the duplicate counter; an over-window jump records the
// gap and advances the expected sequence. Push never returns nil for
// a well-formed call, but the slice may be empty while packets are
// still being reordered.
func (r *Reorderer) Push(p *Packet) []*Packet {
	r.stats.Received++
	seq := p.SequenceNumber

	// First packet seeds the expected sequence.
	if !r.started {
		r.started = true
		r.expected = seq
	}

	d := seqDistance(seq, r.expected)

	switch {
	case d < 0:
		switch {
		case r.buf[seq] != nil:
			// An identical copy is still queued: true duplicate.
			r.stats.Duplicates++
		case r.wasEmitted(seq):
			// The sequence was delivered once already: re-delivery.
			r.stats.Duplicates++
		default:
			// It was skipped as a gap and never received: a late
			// fill that must not be re-emitted.
			r.stats.Late++
		}
		return nil

	case d == 0:
		if _, exists := r.buf[seq]; exists {
			r.stats.Duplicates++
			return nil
		}
		r.buf[seq] = p

	case d > 0:
		if _, exists := r.buf[seq]; exists {
			r.stats.Duplicates++
			return nil
		}
		// Packet lies beyond the window: do not wait for the
		// missing sequence numbers, record the gap and jump.
		if d > r.window {
			r.stats.Gaps += d
			r.stats.Flushed++
			r.expected = seq
		}
		r.buf[seq] = p
	}

	out := make([]*Packet, 0, len(r.buf))
	for {
		pkt, ok := r.buf[r.expected]
		if !ok {
			break
		}
		delete(r.buf, r.expected)
		out = append(out, pkt)
		r.recordEmitted(r.expected)
		r.expected++
		r.stats.Emitted++
	}

	// Window exhausted while still holding a hole: advance past the
	// hole, counting every skipped sequence number as a gap.
	if len(r.buf) >= r.window {
		r.advanceWindow(&out)
	}
	return out
}

// advanceWindow moves the expected sequence to the earliest buffered
// packet when the window is full, recording the skipped gap.
func (r *Reorderer) advanceWindow(out *[]*Packet) {
	next := r.earliestBuffered()
	if next == r.expected {
		return
	}
	gap := seqDistance(next, r.expected)
	if gap > 0 {
		r.stats.Gaps += gap
	}
	r.stats.Flushed++
	r.expected = next
	for {
		pkt, ok := r.buf[r.expected]
		if !ok {
			break
		}
		delete(r.buf, r.expected)
		*out = append(*out, pkt)
		r.recordEmitted(r.expected)
		r.expected++
		r.stats.Emitted++
	}
}

// earliestBuffered scans the window for the sequence closest to the
// expected one. The window is bounded, so this stays O(window).
func (r *Reorderer) earliestBuffered() uint16 {
	best := r.expected
	bestDist := int(1 << 15)
	for seq := range r.buf {
		d := seqDistance(seq, r.expected)
		if d >= 0 && d < bestDist {
			best = seq
			bestDist = d
		}
	}
	return best
}

// Flush releases everything currently buffered in sequence order,
// treating any remaining hole as a gap. It is called at session end
// so buffered data is never silently dropped.
func (r *Reorderer) Flush() []*Packet {
	if !r.started || len(r.buf) == 0 {
		return nil
	}
	out := make([]*Packet, 0, len(r.buf))
	for len(r.buf) > 0 {
		next := r.earliestBuffered()
		if gap := seqDistance(next, r.expected); gap > 0 {
			r.stats.Gaps += gap
			r.stats.Flushed++
		}
		r.expected = next
		pkt := r.buf[r.expected]
		delete(r.buf, r.expected)
		out = append(out, pkt)
		r.recordEmitted(r.expected)
		r.expected++
		r.stats.Emitted++
	}
	return out
}

// Stats returns a copy of the running counters.
func (r *Reorderer) Stats() Stats { return r.stats }

// Pending reports how many packets are buffered behind a hole.
func (r *Reorderer) Pending() int { return len(r.buf) }
