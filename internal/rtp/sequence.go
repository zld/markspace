// Package rtp implements sequence-number continuity checking and
// frame reassembly for RTP packets. Both transport adapters feed the
// same types, so UDP and TCP-interleaved sessions are validated with
// identical logic.
package rtp

import "fmt"

// seqMod is the RTP sequence-number modulus (RFC 3550, 16-bit field).
const seqMod = 1 << 16

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

// SequenceChecker tracks RTP sequence numbers per SSRC with proper
// modulo-65536 arithmetic, including the 65535 -> 0 wraparound.
//
// The first packet on each SSRC establishes the baseline and never
// produces a gap. Every later packet is compared against the expected
// successor of the highest contiguous sequence seen; missing numbers
// are reported as a gap and the running count is updated. Reordered
// (late) packets that fall inside an already-seen range are accepted
// without creating new gaps.
type SequenceChecker struct {
	initialized map[uint32]bool
	// maxContig is the highest sequence number whose full run from
	// the baseline has been observed (modulo arithmetic via head index).
	head     map[uint32]uint16
	gaps     []Gap
	gapCount int
}

// NewSequenceChecker creates an empty checker.
func NewSequenceChecker() *SequenceChecker {
	return &SequenceChecker{
		initialized: map[uint32]bool{},
		head:        map[uint32]uint16{},
	}
}

// delta returns b-a modulo 65536 as an unsigned 16-bit value, so the
// 65535 -> 0 wraparound yields delta 1 instead of a huge negative gap.
func delta(a, b uint16) uint16 {
	return b - a
}

// Observe feeds one (SSRC, sequence number) pair into the checker.
// It returns the gap recorded for this packet, or nil if the packet
// was the expected successor, a baseline packet or a late reorder.
func (c *SequenceChecker) Observe(ssrc uint32, seq uint16) *Gap {
	if !c.initialized[ssrc] {
		c.initialized[ssrc] = true
		c.head[ssrc] = seq
		return nil
	}

	head := c.head[ssrc]
	d := delta(head, seq)

	switch {
	case d == 0:
		// Duplicate of the current head: nothing to report.
		return nil
	case d < seqMod/2:
		// Forward progression. d==1 is the expected successor.
		if d == 1 {
			c.head[ssrc] = seq
			return nil
		}
		g := Gap{
			SSRC:         ssrc,
			FirstMissing: head + 1,
			MissingCount: int(d) - 1,
			AfterPrev:    head,
			BeforeNext:   seq,
		}
		c.gaps = append(c.gaps, g)
		c.gapCount += g.MissingCount
		c.head[ssrc] = seq
		return &g
	default:
		// Backward jump (or a forward jump of more than half the
		// space, treated as late/reordered data). The contiguous
		// head is not moved and no new gap is charged: either the
		// missing range was already counted when the head advanced,
		// or the packet arrived out of order.
		return nil
	}
}

// GapCount returns the total number of missing sequence numbers across
// all SSRCs and gaps.
func (c *SequenceChecker) GapCount() int { return c.gapCount }

// Gaps returns a copy of every recorded gap.
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
