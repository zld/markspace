package timeline

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
)

// Ticks is an integer position or duration in some media scale. It is
// deliberately not a time.Duration: every value belongs to an explicit Scale.
type Ticks int64

// Rational is an exact positive-denominator ratio. Media scales and rates are
// represented here so no subsystem has to use floating point for position
// arithmetic.
type Rational struct {
	Num int64
	Den int64
}

// NewRational normalizes the sign onto Num and rejects a zero denominator.
func NewRational(num, den int64) (Rational, error) {
	if den == 0 {
		return Rational{}, errors.New("timeline: zero denominator")
	}
	if den < 0 {
		num, den = -num, -den
	}
	g := gcd(abs64(num), den)
	if g > 1 {
		num /= g
		den /= g
	}
	return Rational{Num: num, Den: den}, nil
}

// MustRational is NewRational for compile-time constants.
func MustRational(num, den int64) Rational {
	r, err := NewRational(num, den)
	if err != nil {
		panic(err)
	}
	return r
}

// Scale is a fixed tick rate in ticks per second.
type Scale struct {
	Units int64
}

// NewScale validates a tick rate. 1..1e9 ticks/s covers every audio timescale
// we accept while staying far away from multiplication overflow.
func NewScale(units int64) (Scale, error) {
	if units <= 0 || units > 1_000_000_000 {
		return Scale{}, fmt.Errorf("timeline: invalid scale %d", units)
	}
	return Scale{Units: units}, nil
}

// MustScale is NewScale for compile-time constants.
func MustScale(units int64) Scale {
	s, err := NewScale(units)
	if err != nil {
		panic(err)
	}
	return s
}

func (s Scale) Valid() bool        { return s.Units > 0 }
func (s Scale) Rational() Rational { return Rational{Num: s.Units, Den: 1} }

// Seconds expresses this scale's unit as an exact fraction of a second.
func (s Scale) Seconds() Rational {
	return Rational{Num: 1, Den: s.Units}
}

// Rounding mode is fixed for the whole kernel: nearest integer, ties away
// from zero. Every conversion in every subsystem uses mulDivRound so results
// never depend on float64 or on the order conversions are applied.
func mulDivRound(a, b, d int64) (int64, bool) {
	if d <= 0 {
		return 0, false
	}
	if a == 0 || b == 0 {
		return 0, true
	}
	neg := (a < 0) != (b < 0)
	hi, lo := bits.Mul64(uint64(abs64(a)), uint64(abs64(b)))
	den := uint64(d)
	if hi >= den {
		return 0, false
	}
	q, r := bits.Div64(hi, lo, den)
	// 2r >= den spells "round half away from zero"; r < den <= 2^63 so the
	// doubling cannot overflow.
	if r >= den-r {
		q++
	}
	if q > math.MaxInt64 {
		return 0, false
	}
	if neg {
		return -int64(q), true
	}
	return int64(q), true
}

// Convert rescales a count from one media scale to another with the kernel's
// deterministic rounding rule.
func Convert(v Ticks, from, to Scale) (Ticks, error) {
	if !from.Valid() || !to.Valid() {
		return 0, errors.New("timeline: convert with invalid scale")
	}
	r, ok := mulDivRound(int64(v), to.Units, from.Units)
	if !ok {
		return 0, fmt.Errorf("timeline: convert overflow %d %d/%d", v, to.Units, from.Units)
	}
	return Ticks(r), nil
}

// MustConvert is Convert for callers that have already validated scales.
func MustConvert(v Ticks, from, to Scale) Ticks {
	r, err := Convert(v, from, to)
	if err != nil {
		panic(err)
	}
	return r
}

// ToMillis converts a tick count to integer milliseconds, rounding once.
func ToMillis(v Ticks, s Scale) int64 {
	r, ok := mulDivRound(int64(v), 1000, s.Units)
	if !ok {
		return 0
	}
	return r
}

// FromMillis converts integer milliseconds into ticks, rounding once.
func FromMillis(ms int64, s Scale) (Ticks, error) {
	r, ok := mulDivRound(ms, s.Units, 1000)
	if !ok {
		return 0, fmt.Errorf("timeline: millisecond overflow %d", ms)
	}
	return Ticks(r), nil
}

// SecondsFloat is for reporting only. Position math must use Convert.
func SecondsFloat(v Ticks, s Scale) float64 {
	return float64(v) / float64(s.Units)
}

// Rebase translates a source tick position into the output grid while keeping
// the source origin explicit. Anchoring every value at origin (rather than
// accumulating per-sample deltas) bounds rounding error to half a tick and
// makes the result independent of sample count.
func Rebase(v, origin Ticks, from, to Scale) (Ticks, error) {
	d, ok := sub64(int64(v), int64(origin))
	if !ok {
		return 0, errors.New("timeline: rebase overflow")
	}
	return Convert(Ticks(d), from, to)
}

func sub64(a, b int64) (int64, bool) {
	c := a - b
	if (b > 0 && c > a) || (b < 0 && c < a) {
		return 0, false
	}
	return c, true
}

func abs64(v int64) int64 {
	if v == math.MinInt64 {
		return math.MaxInt64
	}
	if v < 0 {
		return -v
	}
	return v
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	if a == 0 {
		return 1
	}
	return a
}
