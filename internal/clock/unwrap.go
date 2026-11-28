package clock

import "errors"

// ErrAmbiguousOrder is returned when a jump of exactly half the modulus
// makes the before/after relation undecidable; callers must fail the track.
var ErrAmbiguousOrder = errors.New("clock: ambiguous ordering (half-modulus jump)")

// SeqUnwrapper expands 16-bit RTP sequence numbers into a monotonic int64
// space, tolerating wrap-around and moderate reordering.
type SeqUnwrapper struct {
	init   bool
	maxSeq uint16
	cycles int64
}

// Unwrap maps seq into the expanded sequence space.
func (u *SeqUnwrapper) Unwrap(seq uint16) (int64, error) {
	if !u.init {
		u.init = true
		u.maxSeq = seq
		return int64(seq), nil
	}
	diff := seq - u.maxSeq // modular distance
	switch {
	case diff == 0x8000:
		return 0, ErrAmbiguousOrder
	case diff < 0x8000: // ahead of maxSeq, possibly across the wrap
		if seq < u.maxSeq {
			u.cycles++
		}
		u.maxSeq = seq
		return u.cycles<<16 | int64(seq), nil
	default: // behind maxSeq (late / reordered packet)
		if seq > u.maxSeq { // behind across the boundary: previous cycle
			return (u.cycles-1)<<16 | int64(seq), nil
		}
		return u.cycles<<16 | int64(seq), nil
	}
}

// TSUnwrapper expands 32-bit RTP timestamps into a monotonic int64 space.
type TSUnwrapper struct {
	init   bool
	maxTS  uint32
	cycles int64
}

// Unwrap maps ts into the expanded timestamp space.
func (u *TSUnwrapper) Unwrap(ts uint32) (int64, error) {
	if !u.init {
		u.init = true
		u.maxTS = ts
		return int64(ts), nil
	}
	diff := ts - u.maxTS
	switch {
	case diff == 0x80000000:
		return 0, ErrAmbiguousOrder
	case diff < 0x80000000:
		if ts < u.maxTS {
			u.cycles++
		}
		u.maxTS = ts
		return u.cycles<<32 | int64(ts), nil
	default:
		if ts > u.maxTS {
			return (u.cycles-1)<<32 | int64(ts), nil
		}
		return u.cycles<<32 | int64(ts), nil
	}
}
