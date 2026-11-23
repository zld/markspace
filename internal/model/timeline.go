// Package model defines the shared media timeline, track graph and error
// model used by the probe, repair and remux subsystems.
//
// All time arithmetic is performed with explicit rational numbers so that
// tracks with different timescales can be compared without floating point
// accumulation error.
package model

import (
	"errors"
	"fmt"
)

// Hard safety limits mandated by the delivery specification.
const (
	MaxFileSize  int64 = 2 << 30 // 2 GiB
	MaxTracks          = 32
	MaxSamples         = 5_000_000
)

// Rat is an exact rational number. Den is always positive after NewRat.
type Rat struct {
	Num int64
	Den int64
}

// NewRat builds a normalized rational. A zero denominator is rejected.
func NewRat(num, den int64) (Rat, error) {
	if den == 0 {
		return Rat{}, errors.New("rational with zero denominator")
	}
	if den < 0 {
		num, den = -num, -den
	}
	g := gcd(abs64(num), den)
	if g > 1 {
		num /= g
		den /= g
	}
	return Rat{Num: num, Den: den}, nil
}

// MustRat panics on an invalid denominator; use only with literal constants.
func MustRat(num, den int64) Rat {
	r, err := NewRat(num, den)
	if err != nil {
		panic(err)
	}
	return r
}

// FromUnits converts track-scaled units (e.g. mdhd duration) into a Rat.
func FromUnits(units int64, timescale uint32) (Rat, error) {
	return NewRat(units, int64(timescale))
}

func (r Rat) valid() error {
	if r.Den <= 0 {
		return errors.New("invalid rational (non-positive denominator)")
	}
	return nil
}

// Cmp returns -1, 0 or 1 like strings.Compare.
func (r Rat) Cmp(o Rat) int {
	if err := r.valid(); err != nil {
		panic(err)
	}
	if err := o.valid(); err != nil {
		panic(err)
	}
	lh, rh, ok := mulNoOverflow(r.Num, o.Den, o.Num, r.Den)
	if !ok {
		panic("integer overflow while comparing rationals")
	}
	switch {
	case lh < rh:
		return -1
	case lh > rh:
		return 1
	default:
		return 0
	}
}

// Zero returns the canonical rational zero. Always prefer it over the Rat
// struct zero value, whose denominator is invalid.
func Zero() Rat { return Rat{Num: 0, Den: 1} }

// Add returns r+o without floating point conversion.
func (r Rat) Add(o Rat) Rat {
	if err := r.valid(); err != nil {
		panic(err)
	}
	if err := o.valid(); err != nil {
		panic(err)
	}
	num, den, ok := addRat(r.Num, r.Den, o.Num, o.Den)
	if !ok {
		panic("integer overflow while adding rationals")
	}
	out, err := NewRat(num, den)
	if err != nil {
		panic(err)
	}
	return out
}

// Sub returns r-o.
func (r Rat) Sub(o Rat) Rat {
	if err := r.valid(); err != nil {
		panic(err)
	}
	if err := o.valid(); err != nil {
		panic(err)
	}
	num, den, ok := addRat(r.Num, r.Den, -o.Num, o.Den)
	if !ok {
		panic("integer overflow while subtracting rationals")
	}
	out, err := NewRat(num, den)
	if err != nil {
		panic(err)
	}
	return out
}

// Seconds is only used for human-readable reports, never for comparisons.
func (r Rat) Seconds() float64 {
	return float64(r.Num) / float64(r.Den)
}

func (r Rat) String() string {
	return fmt.Sprintf("%d/%d(%.6fs)", r.Num, r.Den, r.Seconds())
}

// Range is a half-open time interval [Start, End) on the unified timeline.
type Range struct {
	Start Rat
	End   Rat
}

// NewRange validates that End is not before Start.
func NewRange(start, end Rat) (Range, error) {
	if start.Cmp(end) > 0 {
		return Range{}, fmt.Errorf("inverted time range: start %s after end %s", start, end)
	}
	return Range{Start: start, End: end}, nil
}

// SafeRange never returns an invalid range: an inverted interval collapses
// to a zero-width point. Use it at boundaries where defect handling must
// continue rather than fail.
func SafeRange(start, end Rat) Range {
	if start.Cmp(end) > 0 {
		return Range{Start: start, End: start}
	}
	return Range{Start: start, End: end}
}

// Overlaps reports whether two ranges share any timeline coverage.
func (r Range) Overlaps(o Range) bool {
	return r.Start.Cmp(o.End) < 0 && o.Start.Cmp(r.End) < 0
}

// Duration returns End-Start.
func (r Range) Duration() Rat { return r.End.Sub(r.Start) }

func (r Range) String() string {
	return fmt.Sprintf("[%s,%s)", r.Start, r.End)
}
