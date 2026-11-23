package clock

import (
	"errors"
	"fmt"
)

// RolloverMod is the modulus of 32-bit RTP timestamps (RFC 3550).
const RolloverMod int64 = 1 << 32

// MaxSeqGap is the largest RTP sequence-number gap (in packets) inside
// which a timestamp backward jump can still be attributed to wrap-around
// rather than to an out-of-order or ambiguous stream.
const MaxSeqGap uint16 = 4096

// AmbiguityWindow is the timestamp distance (in clock ticks) below which a
// backward jump cannot be told apart from reordering without more evidence.
const AmbiguityWindow int64 = 1 << 16

// ErrAmbiguousRollover is returned when a timestamp backward jump could be
// either wrap-around or packet reordering and the sequence numbers do not
// decide between them. The raw input offset is attached for provenance.
var ErrAmbiguousRollover = errors.New("clock: ambiguous RTP timestamp rollover")

// AmbiguityError carries the unresolved jump together with the byte offset
// of the offending packet inside its input stream.
type AmbiguityError struct {
	Offset       int64
	PrevSeq      uint16
	Seq          uint16
	PrevRaw      uint32
	Raw          uint32
	SeqGap       uint16
	BackDistance int64
}

func (e *AmbiguityError) Error() string {
	return fmt.Sprintf(
		"%s: offset=%d seq %d->%d (gap=%d) ts %d->%d back=%d",
		ErrAmbiguousRollover, e.Offset, e.PrevSeq, e.Seq, e.SeqGap,
		e.PrevRaw, e.Raw, e.BackDistance,
	)
}

// Unwrap lets errors.Is(err, ErrAmbiguousRollover) work.
func (e *AmbiguityError) Unwrap() error { return ErrAmbiguousRollover }

// seqDelta returns the forward distance from a to b in 16-bit sequence space.
func seqDelta(a, b uint16) uint16 { return b - a }

// Unwrapper extends 32-bit RTP timestamps to monotonically non-decreasing
// 64-bit values. It is stateful per SSRC/track and must be fed packets in
// capture (file) order, not in reordered sequence order.
type Unwrapper struct {
	initialized bool
	base        int64 // extended timestamp of the first accepted packet
	lastExt     int64 // last extended timestamp
	lastSeq     uint16
	lastRaw     uint32
	rollovers   int64 // number of accepted forward wrap-arounds
}

// NewUnwrapper creates an empty unwrapper.
func NewUnwrapper() *Unwrapper { return &Unwrapper{} }

// Rollovers reports how many wrap-arounds have been accepted so far.
func (u *Unwrapper) Rollovers() int64 { return u.rollovers }

// Base returns the extended timestamp of the first packet, or 0.
func (u *Unwrapper) Base() int64 { return u.base }

// Push feeds one (sequence number, raw timestamp) pair observed at byte
// offset. Duplicate sequence numbers repeat the previously extended value.
// A backward raw timestamp is unwrapped only when the sequence evidence is
// conclusive; otherwise an *AmbiguityError carrying offset is returned and
// the unwrapper state is left untouched so the caller can record the fault.
func (u *Unwrapper) Push(seq uint16, raw uint32, offset int64) (int64, error) {
	if !u.initialized {
		u.initialized = true
		u.base = int64(raw)
		u.lastExt = int64(raw)
		u.lastSeq = seq
		u.lastRaw = raw
		return u.lastExt, nil
	}

	gap := seqDelta(u.lastSeq, seq)

	// Same sequence number: retransmission / duplicate packet.
	if gap == 0 {
		return u.lastExt, nil
	}

	// Packet older in sequence space: reordering. Timestamps must be
	// reconstructed relative to the last rollover boundary without
	// advancing state.
	if gap > 1<<15 {
		back := uint16(0) - gap // distance backwards in seq space
		ext := u.reconstructOrdered(raw, int64(back))
		return ext, nil
	}

	// Forward in sequence space.
	if raw >= u.lastRaw {
		// Normal forward progress (same wrap cycle).
		ext := u.cycleBase() + int64(raw)
		u.advance(seq, raw, ext, false)
		return ext, nil
	}

	// Raw timestamp went backwards while sequence advanced. A small
	// backward move (within AmbiguityWindow) is indistinguishable from
	// jitter or reordering and must be reported as ambiguous. A large
	// backward distance, with sequence evidence still tight, is a
	// conclusive wrap-around. Either way an excessive sequence gap makes
	// the decision unsafe.
	back := int64(u.lastRaw) - int64(raw)
	if gap > MaxSeqGap || back < AmbiguityWindow {
		return 0, &AmbiguityError{
			Offset: offset, PrevSeq: u.lastSeq, Seq: seq,
			PrevRaw: u.lastRaw, Raw: raw, SeqGap: gap, BackDistance: back,
		}
	}
	ext := u.cycleBase() + RolloverMod + int64(raw)
	u.advance(seq, raw, ext, true)
	return ext, nil
}

// cycleBase is the extended value corresponding to raw timestamp 0 in the
// current wrap cycle (rollovers full 32-bit cycles behind raw zero).
func (u *Unwrapper) cycleBase() int64 { return u.rollovers * RolloverMod }

func (u *Unwrapper) advance(seq uint16, raw uint32, ext int64, wrapped bool) {
	if wrapped {
		u.rollovers++
	}
	u.lastSeq = seq
	u.lastRaw = raw
	u.lastExt = ext
}

// reconstructOrdered maps a reordered (older) packet's raw timestamp into
// the current extended space. seqBack is how many packets behind it is.
func (u *Unwrapper) reconstructOrdered(raw uint32, seqBack int64) int64 {
	// Older packet normally lives in the same cycle as the last packet;
	// if its raw value is greater than the last raw value it must be from
	// the previous cycle.
	if raw <= u.lastRaw {
		return u.cycleBase() + int64(raw)
	}
	return (u.rollovers-1)*RolloverMod + int64(raw)
}

// IsBackward reports whether an extended timestamp moves backwards relative
// to the previous accepted packet (used by timeline conflict marking).
func (u *Unwrapper) IsBackward(ext int64) bool {
	return u.initialized && ext < u.lastExt
}
