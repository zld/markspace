// Package rtp restores RTP packet order from capture records: extended
// sequence numbers, 16-bit wrap handling, duplicate suppression and gap
// detection inside a bounded reorder window.
package rtp

import "github.com/pion/rtp"

// Observation is the result of feeding one captured packet to the
// SequenceTracker. The tracker never fabricates audio for a missing number;
// gaps are reported separately once the reorder window passes them.
type Observation struct {
	Sequence  uint16
	Extended  uint32
	Delta     int16
	Duplicate bool
	Reordered bool
	Wrapped   bool
	First     bool
}

// SequenceTracker maintains the extended sequence number state of one RTP
// stream, following RFC 3550 appendix A.1 extended sequence handling. The
// seen-set holds extended (32-bit) numbers so the same 16-bit value in two
// different wrap cycles is never confused, and is pruned to a bounded
// reorder window so long traces stay memory bounded.
type SequenceTracker struct {
	initialized  bool
	base         uint32
	maxSeq       uint16
	cycles       uint32
	expected     uint32
	seen         map[uint32]struct{}
	window       uint16
	wrapObserved bool
}

// NewSequenceTracker creates a tracker. window is the maximum reorder
// distance; state older than window sequence numbers is discarded.
// A zero or oversized window defaults to 32767, half of the 16-bit space.
func NewSequenceTracker(window uint16) *SequenceTracker {
	if window == 0 || window > 32767 {
		window = 32767
	}
	return &SequenceTracker{
		seen:   make(map[uint32]struct{}),
		window: window,
	}
}

// Push records one sequence number and returns its classification. The same
// extended number pushed more than once is flagged Duplicate; a 16-bit
// value seen again after a wrap belongs to a new cycle and is not a
// duplicate.
func (s *SequenceTracker) Push(seq uint16) Observation {
	obs := Observation{Sequence: seq}
	if !s.initialized {
		s.initialized = true
		s.base = uint32(seq)
		s.maxSeq = seq
		s.expected = uint32(seq) + 1
		obs.First = true
		obs.Extended = s.extend(seq)
		s.seen[obs.Extended] = struct{}{}
		return obs
	}

	obs.Delta = int16(seq - s.maxSeq)
	w := int16(s.window)
	extResolved := false
	switch {
	case obs.Delta == 0:
		// Current maximum seen again: a duplicate within this cycle.
		obs.Duplicate = true
	case obs.Delta < 0 && s.isWrapTo(seq):
		// Forward 16-bit wrap into a new cycle (e.g. 65535 -> 2).
		obs.Wrapped = true
		s.wrapObserved = true
		s.cycles += 1 << 16
		s.maxSeq = seq
		s.expected = s.extend(seq) + 1
	case obs.Delta < 0:
		// Late reordered packet. The window is below half the 16-bit
		// space, so a late packet can only belong to the previous cycle,
		// which is the current one until the first wrap happens.
		obs.Reordered = true
		if s.cycles == 0 {
			obs.Extended = uint32(seq)
		} else {
			obs.Extended = (s.cycles - (1 << 16)) | uint32(seq)
		}
		extResolved = true
	case obs.Delta < w && seq < s.maxSeq:
		// Wrap boundary reached with a positive signed delta
		// (65535 -> 0 yields delta +1).
		obs.Wrapped = true
		s.wrapObserved = true
		s.cycles += 1 << 16
		s.maxSeq = seq
		s.expected = s.extend(seq) + 1
	default:
		// In-window forward progress, or a large forward jump. Jumps do
		// not count as wraps; the skipped numbers stay candidates for
		// late arrival until the reorder buffer proves them missing.
		s.maxSeq = seq
		s.expected = s.extend(seq) + 1
	}
	if !extResolved {
		obs.Extended = s.extend(seq)
	}

	if !obs.Duplicate {
		if _, ok := s.seen[obs.Extended]; ok {
			obs.Duplicate = true
			obs.Reordered = false
		} else {
			s.seen[obs.Extended] = struct{}{}
			s.prune()
		}
	}
	return obs
}

// extend applies the current cycle count to a 16-bit sequence number.
func (s *SequenceTracker) extend(seq uint16) uint32 {
	return s.cycles | uint32(seq)
}

// isWrapTo decides whether seq arriving behind maxSeq starts a new cycle.
// The current maximum sits near the end of the 16-bit space and seq sits
// near its beginning, so the forward wrap distance (maxSeq -> 65535 ->
// seq) is smaller than the backward reorder distance.
func (s *SequenceTracker) isWrapTo(seq uint16) bool {
	const half uint16 = 1 << 15
	return s.maxSeq > half && seq < half
}

// prune drops seen entries further than window behind the current maximum.
func (s *SequenceTracker) prune() {
	if len(s.seen) < int(s.window) {
		return
	}
	floor := s.extend(s.maxSeq) - uint32(s.window)
	for ext := range s.seen {
		// ext > floor handles wrap arithmetic correctly in uint32.
		if ext <= floor {
			delete(s.seen, ext)
		}
	}
}

// Expected returns the next extended sequence number in order.
func (s *SequenceTracker) Expected() uint32 { return s.expected }

// MaxExtended returns the extended value of the highest sequence seen.
func (s *SequenceTracker) MaxExtended() uint32 {
	if !s.initialized {
		return 0
	}
	return s.extend(s.maxSeq)
}

// Base returns the extended sequence number of the first packet.
func (s *SequenceTracker) Base() uint32 { return s.base }

// Seen reports whether the extended sequence has already been recorded.
func (s *SequenceTracker) Seen(ext uint32) bool {
	_, ok := s.seen[ext]
	return ok
}

// WrapObserved reports whether at least one 16-bit wrap has happened.
func (s *SequenceTracker) WrapObserved() bool { return s.wrapObserved }

// Window returns the configured reorder window.
func (s *SequenceTracker) Window() uint16 { return s.window }

// HeaderSequence is a convenience that reads the sequence number from a
// parsed pion RTP header without pulling the full packet into this package.
func HeaderSequence(h *rtp.Header) uint16 { return h.SequenceNumber }
