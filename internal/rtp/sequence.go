package rtp

import "fmt"

// seqMod is the RTP sequence-number modulus (RFC 3550, 16-bit field).
const seqMod = 1 << 16

// reorderWindow bounds how far ahead of the contiguous head a packet
// may arrive while still being treated as a reordered (late) packet.
// It is larger than any realistic reorder distance and far smaller
// than half the 16-bit space, so genuinely large losses are charged
// promptly instead of being buffered forever.
const reorderWindow = 256

// Gap records a detected sequence-number gap.
type Gap struct {
	// SSRC the gap was observed on.
	SSRC uint32
	// First missing sequence number.
	FirstMissing uint16
	// Count of consecutively missing sequence numbers.
	MissingCount int
	// Last sequence number received before the gap.
	AfterPrev uint16
	// First sequence number received after the gap.
	BeforeNext uint16
}

// seqState holds the per-SSRC continuity bookkeeping.
type seqState struct {
	initialized bool
	// head is the highest sequence number whose entire run from the
	// baseline up to itself has been observed (modulo-65536 math).
	head uint16
	// received holds sequence numbers strictly beyond head that have
	// already arrived (early/reordered packets), keyed by absolute seq.
	received map[uint16]struct{}
	// frontier is the highest buffered sequence beyond head. It lets a
	// late packet (arriving after the frontier) be recognized as a fill
	// rather than an obsolete duplicate.
	frontier uint16
	hasFront bool
}

// SequenceChecker tracks RTP sequence numbers per SSRC with proper
// modulo-65536 arithmetic, including the 65535 -> 0 wraparound.
//
// The first packet on each SSRC establishes the baseline and never
// produces a gap. A later packet is one of: the expected successor
// (advances the head, possibly through buffered packets), an early
// packet (buffered until the head catches up), a late duplicate
// (older than the head, ignored) or a large forward jump (the skipped
// range is charged as a gap immediately). When the stream ends,
// Finalize charges buffered packets whose predecessors never arrived.
// Gap statistics thus depend solely on the set of observed sequence
// numbers, never on packet arrival ordering or scheduling.
type SequenceChecker struct {
	states   map[uint32]*seqState
	gaps     []Gap
	gapCount int
}

// NewSequenceChecker creates an empty checker.
func NewSequenceChecker() *SequenceChecker {
	return &SequenceChecker{states: map[uint32]*seqState{}}
}

// delta returns b-a modulo 65536 as an unsigned 16-bit value, so the
// 65535 -> 0 wraparound yields delta 1 instead of a huge negative gap.
func delta(a, b uint16) uint16 {
	return b - a
}

// Observe feeds one (SSRC, sequence number) pair into the checker.
// It returns a gap only when a large out-of-window jump is detected;
// ordinary losses surface through Finalize once proven unfillable.
func (c *SequenceChecker) Observe(ssrc uint32, seq uint16) *Gap {
	st, ok := c.states[ssrc]
	if !ok {
		c.states[ssrc] = &seqState{
			initialized: true,
			head:        seq,
			received:    map[uint16]struct{}{},
		}
		return nil
	}

	d := delta(st.head, seq)
	switch {
	case d == 0:
		return nil // exact duplicate of the head
	case d >= seqMod/2:
		// Older than the head in circular terms. It is either a true
		// late duplicate (already behind the head) or a fill arriving
		// after the buffered frontier advanced past it.
		if st.hasFront {
			offToFront := delta(seq, st.frontier)
			if offToFront < seqMod/2 {
				// seq lies between head and frontier: buffer it so
				// Finalize sees the hole as filled.
				st.received[seq] = struct{}{}
			}
		}
		return nil
	case d > reorderWindow:
		// Large jump: the skipped range cannot be filled by reordering.
		g := c.recordGap(ssrc, st.head, seq, int(d)-1)
		st.head = seq
		st.received = map[uint16]struct{}{}
		st.hasFront = false
		return g
	case d > 1:
		// Early packet within the window: buffer by absolute sequence.
		st.received[seq] = struct{}{}
		if !st.hasFront || delta(st.frontier, seq) < seqMod/2 {
			st.frontier = seq
			st.hasFront = true
		}
		return nil
	}

	// d == 1: expected successor. Walk the head across any buffered
	// consecutive run; holes inside the run are left for Finalize.
	st.head = seq
	for {
		next := st.head + 1
		if _, ok := st.received[next]; !ok {
			return nil
		}
		delete(st.received, next)
		st.head = next
	}
}

// recordGap appends a gap of missingCount missing sequences ending
// immediately before beforeNext, given the last contiguous sequence.
func (c *SequenceChecker) recordGap(ssrc uint32, prevHead, beforeNext uint16,
	missingCount int,
) *Gap {
	g := Gap{
		SSRC:         ssrc,
		FirstMissing: prevHead + 1,
		MissingCount: missingCount,
		AfterPrev:    prevHead,
		BeforeNext:   beforeNext,
	}
	c.gaps = append(c.gaps, g)
	c.gapCount += missingCount
	return &g
}

// Finalize charges holes that remain open at end of stream: a buffered
// packet whose predecessors never arrived means those predecessors are
// permanently missing. Call after the last packet has been observed.
// It is safe to call more than once; subsequent calls report nothing.
func (c *SequenceChecker) Finalize() []Gap {
	var out []Gap

	// Deterministic SSRC iteration.
	ssrcs := make([]uint32, 0, len(c.states))
	for ssrc := range c.states {
		ssrcs = append(ssrcs, ssrc)
	}
	for i := 1; i < len(ssrcs); i++ {
		for j := i; j > 0 && ssrcs[j-1] > ssrcs[j]; j-- {
			ssrcs[j-1], ssrcs[j] = ssrcs[j], ssrcs[j-1]
		}
	}

	for _, ssrc := range ssrcs {
		st := c.states[ssrc]
		if len(st.received) == 0 {
			continue
		}
		// Smallest buffered sequence marks the first unfillable hole.
		var minSeq uint16
		first := true
		for seq := range st.received {
			if first || delta(st.head, seq) < delta(st.head, minSeq) {
				minSeq, first = seq, false
			}
		}
		missing := int(delta(st.head, minSeq)) - 1
		if missing <= 0 {
			continue
		}
		g := c.recordGap(ssrc, st.head, minSeq, missing)
		out = append(out, *g)
		// The hole is reported; drop the buffer so a repeat Finalize
		// cannot double-count.
		st.received = map[uint16]struct{}{}
	}
	return out
}

// EndStream declares that the stream on ssrc was supposed to end at
// expectedLastSeq. If the highest delivered sequence is behind it, the
// missing tail is charged as a gap. This covers transports (notably
// UDP with an internal reorder buffer) that withhold packets behind an
// unfillable end-of-stream gap: the scenario knows the true tail even
// though the checker never observed it. Calling it is optional; when
// the observed head already reaches expectedLastSeq it is a no-op.
func (c *SequenceChecker) EndStream(ssrc uint32, expectedLastSeq uint16) *Gap {
	st, ok := c.states[ssrc]
	if !ok {
		return nil
	}
	// Account for any early packets already buffered.
	frontier := st.head
	for seq := range st.received {
		if delta(frontier, seq) < seqMod/2 && delta(st.head, seq) > 0 {
			frontier = seq
		}
	}
	d := delta(frontier, expectedLastSeq)
	if d == 0 || d >= seqMod/2 {
		return nil // observed the tail, or expectation is in the past
	}
	missing := int(d)
	g := c.recordGap(ssrc, frontier, expectedLastSeq+1, missing)
	// Mark the expectation consumed so Finalize/EndStream cannot
	// double-count the same tail.
	st.received = map[uint16]struct{}{}
	return g
}

// GapCount returns the total number of missing sequence numbers across
// all SSRCs and gaps.
func (c *SequenceChecker) GapCount() int { return c.gapCount }

// Gaps returns a copy of every recorded gap, ordered by detection.
func (c *SequenceChecker) Gaps() []Gap {
	out := make([]Gap, len(c.gaps))
	copy(out, c.gaps)
	return out
}

// String renders the gap for deterministic textual reports.
func (g Gap) String() string {
	return fmt.Sprintf("ssrc=%d gap after=%d before=%d first_missing=%d missing=%d",
		g.SSRC, g.AfterPrev, g.BeforeNext, g.FirstMissing, g.MissingCount)
}
