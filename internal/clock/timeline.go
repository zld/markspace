// Package clock maps transport event times and container track ticks onto a
// single rational timeline. All arithmetic is exact: any moment that cannot
// be represented without rounding is reported as ERR_UNREPRESENTABLE_TIME
// instead of being silently approximated.
package clock

import (
	"math/big"

	"mediakernel/internal/fault"
)

// Rational is an exact moment in seconds, Num/Den with Den > 0, kept
// normalized (lowest terms, non-negative denominator).
type Rational struct {
	Num int64
	Den int64
}

func (r Rational) big() *big.Rat {
	return big.NewRat(r.Num, r.Den)
}

func normalize(num, den int64) (Rational, error) {
	if den == 0 {
		return Rational{}, fault.New(fault.ErrUnrepresentable, fault.NA,
			"zero denominator in time value")
	}
	if den < 0 {
		num, den = -num, -den
	}
	g := gcd(abs(num), den)
	return Rational{Num: num / g, Den: den / g}, nil
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// TicksToSeconds converts track ticks at the given timescale (ticks per
// second) into an exact rational number of seconds.
func TicksToSeconds(ticks int64, timescale uint32) (Rational, error) {
	if timescale == 0 {
		return Rational{}, fault.New(fault.ErrUnrepresentable, fault.NA,
			"zero timescale for %d ticks", ticks)
	}
	return normalize(ticks, int64(timescale))
}

// MapTicks converts ticks expressed in srcScale into an integer number of
// ticks in dstScale. The conversion must be exact; otherwise an
// ERR_UNREPRESENTABLE_TIME error is returned.
func MapTicks(ticks int64, srcScale, dstScale uint32) (int64, error) {
	if srcScale == 0 || dstScale == 0 {
		return 0, fault.New(fault.ErrUnrepresentable, fault.NA,
			"zero timescale in mapping %d/%d -> %d", ticks, srcScale, dstScale)
	}
	num := big.NewInt(ticks)
	num.Mul(num, big.NewInt(int64(dstScale)))
	den := big.NewInt(int64(srcScale))
	q, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	if rem.Sign() != 0 {
		return 0, fault.New(fault.ErrUnrepresentable, fault.NA,
			"%d ticks at scale %d not integral at scale %d", ticks, srcScale, dstScale)
	}
	if !q.IsInt64() {
		return 0, fault.New(fault.ErrUnrepresentable, fault.NA,
			"%d ticks at scale %d overflows scale %d", ticks, srcScale, dstScale)
	}
	return q.Int64(), nil
}

// Add returns a + b exactly.
func Add(a, b Rational) (Rational, error) {
	s := new(big.Rat).Add(a.big(), b.big())
	return fromBig(s)
}

// Sub returns a - b exactly.
func Sub(a, b Rational) (Rational, error) {
	s := new(big.Rat).Sub(a.big(), b.big())
	return fromBig(s)
}

// Cmp compares a and b exactly: -1, 0 or +1.
func Cmp(a, b Rational) int {
	return a.big().Cmp(b.big())
}

// ToTicks converts a rational number of seconds into an integer number of
// ticks at the given timescale, requiring exactness.
func (r Rational) ToTicks(timescale uint32) (int64, error) {
	if timescale == 0 {
		return 0, fault.New(fault.ErrUnrepresentable, fault.NA,
			"zero timescale for %d/%d s", r.Num, r.Den)
	}
	p := new(big.Rat).Mul(r.big(), big.NewRat(int64(timescale), 1))
	if !p.IsInt() {
		return 0, fault.New(fault.ErrUnrepresentable, fault.NA,
			"%d/%d s not integral at timescale %d", r.Num, r.Den, timescale)
	}
	q := p.Num()
	if !q.IsInt64() {
		return 0, fault.New(fault.ErrUnrepresentable, fault.NA,
			"%d/%d s overflows timescale %d", r.Num, r.Den, timescale)
	}
	return q.Int64(), nil
}

func fromBig(r *big.Rat) (Rational, error) {
	if !r.Num().IsInt64() || !r.Denom().IsInt64() {
		return Rational{}, fault.New(fault.ErrUnrepresentable, fault.NA,
			"time value %s overflows int64", r.RatString())
	}
	return Rational{Num: r.Num().Int64(), Den: r.Denom().Int64()}, nil
}

// Zero is the origin of every timeline.
func Zero() Rational { return Rational{Num: 0, Den: 1} }
