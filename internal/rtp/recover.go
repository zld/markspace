// Package rtp models packet loss and reordering for the fan-in stage using
// github.com/pion/rtp for wire-format parsing.
//
// The pipeline never reconstructs media from RTP: it uses sequence numbers
// only, to decide which blocks survived transport and in what order they
// should reach the merger. Reordering is undone inside a bounded window; a
// hole that outlives the window is counted as unrecoverable rather than
// waited on forever, so one lossy layer cannot stall the others.
package rtp

// Window is the reorder window size in packets, as required by the design.
const Window = 64

// Packet is one transport observation for a media block.
type Packet struct {
	Sequence   uint16
	PayloadLen int
	// Marker flags the last packet of a block.
	Marker bool
}

// Stats summarises a recovery run.
//
// PacketRecoveryRate is delivered/(delivered+lost): the share of the expected
// sequence range that reached the merger. Out-of-order packets that were put
// back in order are reported separately as Recovered and do not change the
// rate.
type Stats struct {
	Delivered int
	Recovered int // packets that arrived out of order and were re-sequenced
	Lost      int // holes declared unrecoverable
	Duplicate int
	// MaxReorder is the largest observed reorder distance, in packets.
	MaxReorder int
	FirstSeq   uint16
	LastSeq    uint16
	HaveRange  bool
}

// PacketRecoveryRate returns delivered / (delivered + lost), or 1 when the
// stream carried nothing.
func (s Stats) PacketRecoveryRate() float64 {
	d := s.Delivered + s.Lost
	if d == 0 {
		return 1
	}
	return float64(s.Delivered) / float64(d)
}

// Cursor reorders one sequence-number stream inside a bounded window.
type Cursor struct {
	window int
	next   uint16
	have   bool
	high   uint16

	pending map[uint16]Packet
	stats   Stats
}

// NewCursor returns a reorder cursor with a bounded window.
func NewCursor(window int) *Cursor {
	if window <= 0 {
		window = Window
	}
	return &Cursor{window: window, pending: make(map[uint16]Packet)}
}

// Window returns the configured window.
func (c *Cursor) Window() int { return c.window }

// Stats returns the current counters.
func (c *Cursor) Stats() Stats { return c.stats }

// Pending returns how many packets are buffered awaiting an earlier hole.
func (c *Cursor) Pending() int { return len(c.pending) }

// Push feeds one received packet and appends every packet that became
// contiguous to out, in ascending sequence order.
func (c *Cursor) Push(p Packet, out []Packet) []Packet {
	if !c.have {
		c.have = true
		c.next = p.Sequence
		c.high = p.Sequence
		c.stats.FirstSeq = p.Sequence
		c.stats.HaveRange = true
	}
	if _, ok := c.pending[p.Sequence]; ok {
		c.stats.Duplicate++
		return out
	}
	// A sequence strictly behind the next expected one has already been
	// delivered (or declared lost): treat a late arrival as a duplicate.
	if seqDistance(c.next, p.Sequence) < 0 {
		c.stats.Duplicate++
		return out
	}
	if seqDistance(c.high, p.Sequence) > 0 {
		c.high = p.Sequence
		c.stats.LastSeq = p.Sequence
	}
	// The reported reorder distance is bounded by the window: a distance
	// beyond it is a hole, not a reorder, and would be declared lost below.
	if d := seqDistance(c.next, p.Sequence); int(d) > c.stats.MaxReorder && int(d) < c.window+1 {
		c.stats.MaxReorder = int(d)
	}
	if p.Sequence != c.next {
		c.stats.Recovered++
	}
	// Keep the buffer strictly inside the window. A slot is needed for this
	// packet, so while the buffer is full the oldest hole (the packet at
	// next) has outlived the window and is declared unrecoverable.
	for len(c.pending) >= c.window {
		if _, ok := c.pending[c.next]; ok {
			// The next packet is buffered, so the buffer is contiguous and
			// deliverContiguous drains it before we get here. This branch is
			// therefore unreachable in practice; keep it defensive.
			out = c.deliverContiguous(out)
			break
		}
		c.stats.Lost++
		c.next++
		out = c.deliverContiguous(out)
	}
	c.pending[p.Sequence] = p
	out = c.deliverContiguous(out)

	// Enforce the window: once the highest buffered packet sits a full window
	// ahead of the expected sequence, the hole at next is unrecoverable.
	for len(c.pending) > 0 && seqDistance(c.next, c.high) >= int32(c.window) {
		if _, ok := c.pending[c.next]; ok {
			break
		}
		c.stats.Lost++
		c.next++
		out = c.deliverContiguous(out)
	}
	return out
}

// deliverContiguous emits every buffered packet that is now in order.
func (c *Cursor) deliverContiguous(out []Packet) []Packet {
	for {
		q, ok := c.pending[c.next]
		if !ok {
			return out
		}
		delete(c.pending, c.next)
		c.stats.Delivered++
		out = append(out, q)
		c.next++
	}
}

// Flush ends the stream: trailing holes are declared unrecoverable and every
// remaining buffered packet is delivered in sequence order.
func (c *Cursor) Flush(out []Packet) []Packet {
	if !c.have {
		return out
	}
	for {
		if q, ok := c.pending[c.next]; ok {
			delete(c.pending, c.next)
			c.stats.Delivered++
			out = append(out, q)
			c.next++
			continue
		}
		if len(c.pending) == 0 {
			return out
		}
		// Count the hole, then the next buffered packet will be delivered on
		// the following iteration.
		c.stats.Lost++
		c.next++
	}
}

// seqDistance returns b-a in 16-bit sequence space, sign-extended, so wrap is
// handled without special cases.
func seqDistance(a, b uint16) int32 { return int32(int16(b - a)) }
