// Package media holds the codec-independent media kernel: rational time
// bases, normalized frame events and the layered error model.
package media

import (
	"fmt"
	"math/big"
)

// Rational is a time base expressed as ticks per second Num/Den.
type Rational struct {
	Num uint64
	Den uint64
}

// Clock converts codec-local tick counts onto a shared rational time base.
type Clock struct {
	Rate Rational
}

// NewClock validates and returns a clock with the given tick rate.
func NewClock(num, den uint64) (Clock, error) {
	if num == 0 || den == 0 {
		return Clock{}, fmt.Errorf("%w: clock rate %d/%d has zero component",
			ErrInvalidArgument, num, den)
	}
	return Clock{Rate: Rational{Num: num, Den: den}}, nil
}

// ToRational maps an integer tick count onto the clock time base.
func (c Clock) ToRational(ticks int64) big.Rat {
	num := new(big.Int).Mul(big.NewInt(ticks), big.NewInt(int64(c.Rate.Den)))
	var r big.Rat
	r.SetFrac(num, big.NewInt(int64(c.Rate.Num)))
	return r
}

// CmpTick compares two tick counts without floating point conversion.
func (c Clock) CmpTick(a, b int64) int {
	// (a-b) * Den compared against zero; Num is positive.
	d := big.NewInt(a - b)
	d.Mul(d, big.NewInt(int64(c.Rate.Den)))
	return d.Sign()
}

// CmpRational compares a tick count against an already-normalized rational
// value. Returns -1, 0 or 1.
func (c Clock) CmpRational(ticks int64, r *big.Rat) int {
	left := c.ToRational(ticks)
	return left.Cmp(r)
}

// FrameEvent is the normalized unit handed by codec adapters to the kernel.
// PTS values are raw source ticks; repaired ticks are filled later.
type FrameEvent struct {
	Index        int   // ordinal among complete units, 0-based
	Offset       int64 // absolute byte offset of the start code / OBU header
	Length       int64 // full on-wire length including start code / header
	IsFrame      bool  // VCL/frame unit rather than parameter set / metadata
	IsKeyframe   bool  // random-access point
	RawPTS       int64 // source presentation tick; -1 when absent
	HasPTS       bool
	RepairedPTS  int64 // monotonic output ticks
	RepairReason string
}

// ByteRange is the inclusive source range backing an indexed entry.
type ByteRange struct {
	Offset int64
	Length int64
}

// Contains reports whether off falls inside the range.
func (r ByteRange) Contains(off int64) bool {
	return off >= r.Offset && off < r.Offset+r.Length
}
