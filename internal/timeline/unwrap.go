// Package timeline turns per-frame event timestamps into a monotonically
// comparable time line. It handles modular wrap-around only when the wrap
// direction and range are uniquely decidable, and it opens a fresh boundary on
// source switches so distinct tracks/sources are never merged.
package timeline

import "medcheck/internal/model"

// Boundary is one continuous segment produced by a single (track, source).
// Crossing a boundary resets wrap state instead of silently shifting time.
type Boundary struct {
	Track    uint32
	Source   uint32
	Modulus  uint64 // 0 means timestamps are already absolute (no wrap)
	Base     model.Rational
	StartSeq uint32
}

// Unwrapper keeps the running wrap state for one boundary.
type Unwrapper struct {
	boundary Boundary
	lastRaw  uint64
	lastAbs  int64
	rolls    uint64
	hasLast  bool
}

// NewUnwrapper starts a boundary. A modulus of 0 disables wrap handling and
// treats every tick as already absolute.
func NewUnwrapper(b Boundary) *Unwrapper {
	return &Unwrapper{boundary: b}
}

// Boundary returns the boundary this unwrapper serves.
func (u *Unwrapper) Boundary() Boundary { return u.boundary }

// Unwrap converts a raw modular tick into an absolute tick within the
// boundary. It returns a KindTimeBase error when the jump is ambiguous (it
// could equally be a forward wrap or a backward jump), so callers never get a
// silent, undecidable time shift.
func (u *Unwrapper) Unwrap(raw uint64) (int64, *model.CodeError) {
	if u.boundary.Modulus == 0 {
		abs := int64(raw)
		u.lastRaw, u.lastAbs, u.hasLast = raw, abs, true
		return abs, nil
	}
	m := u.boundary.Modulus
	if raw >= m {
		return 0, u.timeError(raw, "timestamp outside declared modulus")
	}
	if !u.hasLast {
		abs := int64(raw)
		u.lastRaw, u.lastAbs, u.hasLast = raw, abs, true
		return abs, nil
	}
	// Candidate deltas relative to the previous raw value. fwd is the forward
	// cyclic distance in [0,m) and works for any modulus, not only powers of 2.
	fwd := int64((raw + m - u.lastRaw) % m)
	switch {
	case fwd == 0:
		// Equal raw tick: no movement, absolute value unchanged.
		return u.lastAbs, nil
	case m%2 == 0 && fwd == int64(m/2):
		// Forward and backward moves are equidistant: not uniquely decidable.
		return 0, u.timeError(raw, "ambiguous timestamp jump at half modulus")
	case raw > u.lastRaw:
		// No wrap: a plain forward jump.
		abs := u.lastAbs + fwd
		u.advance(raw, abs)
		return abs, nil
	default:
		// raw < lastRaw: a forward wrap is the closer, unique interpretation.
		u.rolls++
		abs := int64(u.rolls)*int64(m) + int64(raw)
		u.advance(raw, abs)
		return abs, nil
	}
}

func (u *Unwrapper) advance(raw uint64, abs int64) {
	u.lastRaw, u.lastAbs = raw, abs
}

// Rolls reports how many modulus wrap-arounds have been observed.
func (u *Unwrapper) Rolls() uint64 { return u.rolls }

// LastAbsolute returns the most recent absolute tick and whether any frame has
// been processed.
func (u *Unwrapper) LastAbsolute() (int64, bool) {
	return u.lastAbs, u.hasLast
}

func (u *Unwrapper) timeError(raw uint64, msg string) *model.CodeError {
	return &model.CodeError{
		Kind:     model.KindTimeBase,
		Track:    u.boundary.Track,
		Source:   u.boundary.Source,
		Message:  msg,
		Offset:   model.RawOffset{},
		FirstPos: int64(raw),
	}
}

// SameBoundary reports whether a frame belongs to the unwrapper's boundary.
func SameBoundary(b Boundary, track, source uint32) bool {
	return b.Track == track && b.Source == source
}
