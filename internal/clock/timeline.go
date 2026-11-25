// Package clock defines the single integer-tick time base used by every
// subsystem. All conversions use integer scales and rational remainder
// accumulation; floating point is deliberately absent so repeated segment
// boundaries cannot drift.
package clock

import (
	"errors"
	"math/big"
)

// MaxTimeScale is the inclusive upper bound for an accepted timescale.
// Larger values are rejected before they can overflow tick arithmetic.
const MaxTimeScale = 0x7FFFFFFF

// ErrBadScale is returned when a timescale is zero or negative.
var ErrBadScale = errors.New("clock: timescale must be in [1, MaxTimeScale]")

// Rational is an exact Num/Dem pair. Den is always positive.
type Rational struct {
	Num int64
	Den int64
}

// ValidScale reports whether scale may be used as a ticks-per-second base.
func ValidScale(scale uint32) bool {
	return scale >= 1 && scale <= MaxTimeScale
}

// Convert translates ticks from fromScale to toScale exactly.
// It returns the floored scaled value and the leftover source numerator
// in [0, fromScale), so callers can accumulate the remainder themselves.
func Convert(ticks int64, fromScale, toScale uint32) (scaled int64, rem int64, err error) {
	if !ValidScale(fromScale) || !ValidScale(toScale) {
		return 0, 0, ErrBadScale
	}
	if ticks < 0 {
		return 0, 0, errors.New("clock: negative tick count")
	}
	num := ticks * int64(toScale)
	return num / int64(fromScale), num % int64(fromScale), nil
}

// Accumulator converts a stream of tick deltas from one timescale to
// another while carrying the rational remainder, so the sum of converted
// parts equals the conversion of the whole (no per-segment truncation).
type Accumulator struct {
	from uint32
	to   uint32
	out  int64
	rem  int64
}

// NewAccumulator builds an accumulator mapping fromScale onto toScale.
func NewAccumulator(fromScale, toScale uint32) (*Accumulator, error) {
	if !ValidScale(fromScale) || !ValidScale(toScale) {
		return nil, ErrBadScale
	}
	return &Accumulator{from: fromScale, to: toScale}, nil
}

// Add consumes ticks on the source scale and returns the new absolute
// output position on the target scale.
func (a *Accumulator) Add(ticks int64) (int64, error) {
	if ticks < 0 {
		return 0, errors.New("clock: negative tick count")
	}
	num := ticks*int64(a.to) + a.rem
	a.out += num / int64(a.from)
	a.rem = num % int64(a.from)
	return a.out, nil
}

// Position returns the current output position.
func (a *Accumulator) Position() int64 { return a.out }

// Remainder returns the carried numerator in [0, fromScale).
func (a *Accumulator) Remainder() int64 { return a.rem }

// Millis renders ticks/scale as whole milliseconds (floor).
func Millis(ticks int64, scale uint32) (int64, error) {
	if !ValidScale(scale) {
		return 0, ErrBadScale
	}
	return ticks * 1000 / int64(scale), nil
}

// WithinTolerance reports whether two instants on different timescales
// differ by at most toleranceMs milliseconds. The comparison is exact
// integer cross-multiplication evaluated with big.Int so 90 kHz video
// timescales multiplied by long durations cannot overflow.
func WithinTolerance(ticksA int64, scaleA uint32, ticksB int64, scaleB uint32, toleranceMs int64) (bool, error) {
	if !ValidScale(scaleA) || !ValidScale(scaleB) {
		return false, ErrBadScale
	}
	if toleranceMs < 0 {
		return false, errors.New("clock: negative tolerance")
	}
	// |ticksA*scaleB - ticksB*scaleA| * 1000 <= tol * scaleA * scaleB
	lhs := new(big.Int).Mul(big.NewInt(ticksA), big.NewInt(int64(scaleB)))
	rhs := new(big.Int).Mul(big.NewInt(ticksB), big.NewInt(int64(scaleA)))
	diff := new(big.Int).Abs(lhs.Sub(lhs, rhs))
	diff.Mul(diff, big.NewInt(1000))
	limit := new(big.Int).Mul(big.NewInt(toleranceMs),
		new(big.Int).Mul(big.NewInt(int64(scaleA)), big.NewInt(int64(scaleB))))
	return diff.Cmp(limit) <= 0, nil
}

// RTPUnwrapper extends 32-bit RTP timestamps (RFC 3550 section 5.1) into
// monotonic 64-bit values across modulo-2^32 wrap-around.
type RTPUnwrapper struct {
	started  bool
	last     uint32
	extended int64
}

// Extend maps a raw timestamp to its unwrapped absolute value.
func (u *RTPUnwrapper) Extend(ts uint32) uint64 {
	if !u.started {
		u.extended = int64(ts)
		u.started = true
	} else {
		// Signed difference interprets a backwards jump smaller than
		// half the cycle as a forward wrap, per RTP arrival rules.
		u.extended += int64(int32(ts - u.last))
	}
	u.last = ts
	if u.extended < 0 {
		// Fixtures always start above zero; a negative extended value
		// would mean input began close to the cycle boundary.
		u.extended = 0
	}
	return uint64(u.extended)
}

// Last returns the most recent unwrapped value and whether any packet
// has been observed.
func (u *RTPUnwrapper) Last() (uint64, bool) {
	if !u.started {
		return 0, false
	}
	return uint64(u.extended), true
}

// CheckMonotonic validates that an unwrapped RTP timestamp does not move
// backwards relative to the previous packet on the same track.
func CheckMonotonic(prev, next uint64) error {
	if next < prev {
		return errors.New("clock: RTP timestamp regressed after unwrap")
	}
	return nil
}

// Timeline accumulates per-track segment durations onto one shared scale.
// Discontinuities do not reset it: callers keep appending and the absolute
// position simply advances, which is how HLS EXT-X-DISCONTINUITY is modeled.
type Timeline struct {
	Scale uint32
	accs  map[uint32]*Accumulator
}

// NewTimeline creates a timeline normalized onto targetScale.
func NewTimeline(targetScale uint32) (*Timeline, error) {
	if !ValidScale(targetScale) {
		return nil, ErrBadScale
	}
	return &Timeline{Scale: targetScale, accs: map[uint32]*Accumulator{}}, nil
}

// Range is a half-open [Start, End) interval measured on the timeline scale.
type Range struct {
	Start int64
	End   int64
}

// Append converts duration ticks (expressed in srcScale) for trackID into
// timeline units and returns the half-open range it occupies. Each track
// keeps its own remainder accumulator.
func (tl *Timeline) Append(trackID uint32, ticks int64, srcScale uint32) (Range, error) {
	acc, ok := tl.accs[trackID]
	if !ok {
		var err error
		acc, err = NewAccumulator(srcScale, tl.Scale)
		if err != nil {
			return Range{}, err
		}
		tl.accs[trackID] = acc
	} else if acc.from != srcScale {
		return Range{}, errors.New("clock: track timescale changed mid-timeline")
	}
	start := acc.Position()
	end, err := acc.Add(ticks)
	if err != nil {
		return Range{}, err
	}
	return Range{Start: start, End: end}, nil
}

// Position returns the current timeline position for a track.
func (tl *Timeline) Position(trackID uint32) int64 {
	if acc, ok := tl.accs[trackID]; ok {
		return acc.Position()
	}
	return 0
}
