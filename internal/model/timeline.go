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
	"math"
	"strings"
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

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func abs64(v int64) int64 {
	if v < 0 {
		if v == math.MinInt64 {
			return math.MaxInt64
		}
		return -v
	}
	return v
}

// mulNoOverflow computes (a*b, c*d) while detecting int64 overflow.
func mulNoOverflow(a, b, c, d int64) (int64, int64, bool) {
	ah, al := mulFull(a, b)
	ch, cl := mulFull(c, d)
	if !fitsInt64(ah, al) || !fitsInt64(ch, cl) {
		return 0, 0, false
	}
	return int64(al), int64(cl), true
}

// addRat computes a/b + c/d with intermediate-overflow detection. The
// reduction mirrors math/big.Rat.Add: g1=gcd(a,d), g2=gcd(c,b); the result
// is (a/g1*d/g2 + c/g2*b/g1) / ((b/g1)*(d/g2)), which keeps the unsigned
// products minimal for long sample timelines.
func addRat(an, ad, cn, cd int64) (int64, int64, bool) {
	g1 := gcd(abs64(an), abs64(cd))
	g2 := gcd(abs64(cn), abs64(ad))
	an /= g1
	cd /= g1
	cn /= g2
	ad /= g2
	hi1, lo1 := mulFull(an, cd)
	hi2, lo2 := mulFull(cn, ad)
	numHi, numLo := add128(hi1, lo1, hi2, lo2)
	if !fitsInt64(numHi, numLo) {
		return 0, 0, false
	}
	denHi, denLo := mulFull(ad, cd)
	if !fitsInt64(denHi, denLo) || denLo == 0 {
		return 0, 0, false
	}
	num, den := int64(numLo), int64(denLo)
	if g := gcd(abs64(num), den); g > 1 {
		num /= g
		den /= g
	}
	return num, den, true
}

// mulFull returns the signed 128-bit product of a and b as unsigned
// high/low words in two's-complement representation. It multiplies the
// absolute magnitudes (which always fit the unsigned 128-bit result) and
// applies sign correction afterwards, so negative inputs cannot create
// spurious high words.
func mulFull(a, b int64) (hi, lo uint64) {
	neg := (a < 0) != (b < 0)
	au := uint64(abs64(a))
	bu := uint64(abs64(b))
	al := au & 0xffffffff
	ah := au >> 32
	bl := bu & 0xffffffff
	bh := bu >> 32
	ll := al * bl
	lh := al * bh
	hl := ah * bl
	hh := ah * bh
	mid := (ll >> 32) + (lh & 0xffffffff) + (hl & 0xffffffff)
	lo = (mid << 32) | (ll & 0xffffffff)
	hi = hh + (lh >> 32) + (hl >> 32) + (mid >> 32)
	if neg {
		// two's-complement negate of the unsigned 128-bit magnitude
		lo = ^lo + 1
		hi = ^hi
		if lo == 0 {
			hi++
		}
	}
	return hi, lo
}

// add128 adds two unsigned 128-bit values and returns the unsigned
// overflow carry (always 0 or 1).
func add128(ahi, alo, bhi, blo uint64) (hi, lo uint64) {
	lo = alo + blo
	carry := uint64(0)
	if lo < alo {
		carry = 1
	}
	hi = ahi + bhi + carry
	return hi, lo
}

// fitsInt64 reports whether the 128-bit two's-complement value fits in int64.
func fitsInt64(hi, lo uint64) bool {
	switch hi {
	case 0:
		return lo <= math.MaxInt64
	case math.MaxUint64:
		return lo >= math.MaxInt64+1
	default:
		return false
	}
}

// JoinRanges merges overlapping/adjacent ranges into a minimal sorted list.
func JoinRanges(in []Range) []Range {
	if len(in) == 0 {
		return nil
	}
	out := make([]Range, 0, len(in))
	out = append(out, in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].Start.Cmp(out[j].Start) > 0; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	merged := out[:1]
	for _, rg := range out[1:] {
		last := &merged[len(merged)-1]
		if rg.Start.Cmp(last.End) <= 0 {
			if rg.End.Cmp(last.End) > 0 {
				last.End = rg.End
			}
			continue
		}
		merged = append(merged, rg)
	}
	return merged
}

// DescribeRanges renders ranges for deterministic JSON/text reports.
func DescribeRanges(rs []Range) string {
	parts := make([]string, len(rs))
	for i, r := range rs {
		parts[i] = r.String()
	}
	return strings.Join(parts, ",")
}
