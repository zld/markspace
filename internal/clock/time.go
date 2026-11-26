// Package clock implements the integer rational media clock shared by every
// subsystem. All timeline math stays in exact rational numbers (int64
// numerator/denominator) until an explicit integer conversion; no float is
// used anywhere on the media timeline.
package clock

import (
	"errors"
)

// Rat is an exact rational number with a strictly positive denominator.
// Values are kept normalized except for the zero value, whose Den is 0.
type Rat struct {
	Num int64 // numerator
	Den int64 // denominator, always > 0 for a valid (non-zero-value) Rat
}

// R constructs a normalized Rat. It returns an overflow error if the
// normalization multiplication overflows int64.
func R(num, den int64) (Rat, error) {
	if den == 0 {
		return Rat{}, ErrZeroDenominator
	}
	if den < 0 {
		num, den = -num, -den
	}
	if num == 0 {
		return Rat{Num: 0, Den: den}, nil
	}
	g := gcd(abs64(num), den)
	num, den = num/g, den/g
	return Rat{Num: num, Den: den}, nil
}

// MustR is like R but panics on error. Use only for compile-time-known inputs.
func MustR(num, den int64) Rat {
	r, err := R(num, den)
	if err != nil {
		panic(err)
	}
	return r
}

// FromInt creates a Rat from an integer with denominator 1.
func FromInt(n int64) Rat { return Rat{Num: n, Den: 1} }

// Valid reports whether r was constructed through R/FromInt.
func (r Rat) Valid() bool { return r.Den > 0 }

// IsZero reports whether the value is zero.
func (r Rat) IsZero() bool { return r.Num == 0 }

// Cmp returns -1, 0 or 1 like strings.Compare.
func (r Rat) Cmp(o Rat) (int, error) {
	if !r.Valid() || !o.Valid() {
		return 0, ErrInvalidRat
	}
	ln, err := mul64(r.Num, o.Den)
	if err != nil {
		return 0, overflowf("cross multiply %d*%d", r.Num, o.Den)
	}
	rn, err := mul64(o.Num, r.Den)
	if err != nil {
		return 0, overflowf("cross multiply %d*%d", o.Num, r.Den)
	}
	switch {
	case ln < rn:
		return -1, nil
	case ln > rn:
		return 1, nil
	default:
		return 0, nil
	}
}

// Add returns r+o, returning an overflow error on int64 exhaustion.
func (r Rat) Add(o Rat) (Rat, error) {
	if !r.Valid() || !o.Valid() {
		return Rat{}, ErrInvalidRat
	}
	// a/b + c/d = (a*d + c*b)/(b*d), computed with 128-bit intermediates.
	num, den, err := addFrac(r.Num, r.Den, o.Num, o.Den)
	if err != nil {
		return Rat{}, err
	}
	return normalize(num, den)
}

// Sub returns r-o.
func (r Rat) Sub(o Rat) (Rat, error) {
	neg, err := R(-o.Num, o.Den)
	if err != nil {
		return Rat{}, err
	}
	return r.Add(neg)
}

// Mul returns r*o.
func (r Rat) Mul(o Rat) (Rat, error) {
	if !r.Valid() || !o.Valid() {
		return Rat{}, ErrInvalidRat
	}
	num, err := mul64(r.Num, o.Num)
	if err != nil {
		return Rat{}, overflowf("multiply numerators %d*%d", r.Num, o.Num)
	}
	den, err := mul64(r.Den, o.Den)
	if err != nil {
		return Rat{}, overflowf("multiply denominators %d*%d", r.Den, o.Den)
	}
	return normalize(num, den)
}

// Div returns r/o; division by zero returns ErrZeroDenominator.
func (r Rat) Div(o Rat) (Rat, error) {
	if !r.Valid() || !o.Valid() {
		return Rat{}, ErrInvalidRat
	}
	if o.Num == 0 {
		return Rat{}, ErrZeroDenominator
	}
	inv, err := R(o.Den, o.Num)
	if err != nil {
		return Rat{}, err
	}
	return r.Mul(inv)
}

// Floor returns the greatest integer <= r.
func (r Rat) Floor() (int64, error) {
	if !r.Valid() {
		return 0, ErrInvalidRat
	}
	// Go truncates toward zero; for negative non-integers adjust by -1.
	q := r.Num / r.Den
	if r.Num < 0 && r.Num%r.Den != 0 {
		var ok bool
		q, ok = add64(q, -1)
		if !ok {
			return 0, overflowf("floor %d/%d", r.Num, r.Den)
		}
	}
	return q, nil
}

// Round returns the nearest integer, halves away from zero.
func (r Rat) Round() (int64, error) {
	if !r.Valid() {
		return 0, ErrInvalidRat
	}
	if r.Num >= 0 {
		n, ok := add64(r.Num, r.Den/2)
		if !ok {
			return 0, overflowf("round %d/%d", r.Num, r.Den)
		}
		return n / r.Den, nil
	}
	n, ok := add64(r.Num, -r.Den/2)
	if !ok {
		return 0, overflowf("round %d/%d", r.Num, r.Den)
	}
	return n / r.Den, nil
}

// Scale multiplies r by the integer n without losing precision.
func (r Rat) Scale(n int64) (Rat, error) { return r.Mul(FromInt(n)) }

// TicksToRat converts a raw tick count at ticksPerSecond into a Rat in
// seconds, e.g. 90000 ticks at 90kHz == 1.
func TicksToRat(ticks, ticksPerSecond int64) (Rat, error) {
	if ticksPerSecond <= 0 {
		return Rat{}, ErrZeroDenominator
	}
	return R(ticks, ticksPerSecond)
}

// ToNanoseconds converts a Rat expressed in seconds into an integer
// nanosecond value, erroring if the result does not fit int64.
func (r Rat) ToNanoseconds() (int64, error) {
	scaled, err := r.Scale(1_000_000_000)
	if err != nil {
		return 0, err
	}
	return scaled.Round()
}

// Nanoseconds wraps the common int64ns -> Rat(seconds) conversion.
func Nanoseconds(ns int64) (Rat, error) {
	r, err := R(ns, 1_000_000_000)
	if err != nil {
		return Rat{}, overflowf("Nanoseconds(%d)", ns)
	}
	return r, nil
}

// Equal compares two rationals for exact equality.
func (r Rat) Equal(o Rat) (bool, error) {
	c, err := r.Cmp(o)
	return c == 0, err
}

// String renders r as "n/d" (or just "n" when the denominator is 1).
func (r Rat) String() string {
	if !r.Valid() {
		return "<invalid-rat>"
	}
	if r.Den == 1 {
		return itoa(r.Num)
	}
	return itoa(r.Num) + "/" + itoa(r.Den)
}

// Min returns the smaller of two rationals.
func Min(a, b Rat) (Rat, error) {
	c, err := a.Cmp(b)
	if err != nil {
		return Rat{}, err
	}
	if c <= 0 {
		return a, nil
	}
	return b, nil
}

// Max returns the larger of two rationals.
func Max(a, b Rat) (Rat, error) {
	c, err := a.Cmp(b)
	if err != nil {
		return Rat{}, err
	}
	if c >= 0 {
		return a, nil
	}
	return b, nil
}

// ---- internal checked arithmetic ---------------------------------------

func normalize(num, den int64) (Rat, error) {
	if den == 0 {
		return Rat{}, ErrZeroDenominator
	}
	if den < 0 {
		var ok bool
		num, ok = add64(0, -num)
		if !ok {
			return Rat{}, overflowf("negate numerator %d", num)
		}
		den = -den
	}
	if num == 0 {
		return Rat{Num: 0, Den: den}, nil
	}
	g := gcd(abs64(num), den)
	return Rat{Num: num / g, Den: den / g}, nil
}

func addFrac(an, ad, bn, bd int64) (int64, int64, error) {
	// a/b + c/d = (a*d + c*b)/(b*d); each product is overflow-checked.
	p1, err := mul64(an, bd)
	if err != nil {
		return 0, 0, err
	}
	p2, err := mul64(bn, ad)
	if err != nil {
		return 0, 0, err
	}
	num, ok := add64(p1, p2)
	if !ok {
		return 0, 0, overflowf("fraction add overflow")
	}
	den, err := mul64(ad, bd)
	if err != nil {
		return 0, 0, err
	}
	return num, den, nil
}

func mul64(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	// Go guarantees two's-complement wraparound on overflow.
	r := a * b
	// Includes the MinInt64 * -1 corner case (r/a == 1 != b == -1).
	if r/a != b {
		return 0, overflowf("multiply %d*%d overflows int64", a, b)
	}
	return r, nil
}

func add64(a, b int64) (int64, bool) {
	s := a + b
	if (a >= 0 && b >= 0 && s < 0) || (a < 0 && b < 0 && s >= 0) {
		return 0, false
	}
	return s, true
}

func mulOverflows(a, b int64) bool {
	_, err := mul64(a, b)
	return err != nil
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	if a < 0 {
		return -a
	}
	return a
}

func abs64(v int64) int64 {
	if v >= 0 {
		return v
	}
	return -v
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := v < 0
	u := uint64(v)
	if neg {
		u = uint64(-v)
	}
	for u > 0 {
		i--
		buf[i] = byte('0' + u%10)
		u /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// IsOverflow reports whether err (or something it wraps) is an overflow.
func IsOverflow(err error) bool {
	var oe *OverflowError
	return errors.As(err, &oe)
}
