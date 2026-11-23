// Package model defines the shared integer timeline and cross-module faults
// used by the archive recovery kernel.
//
// All media times are expressed in integer ticks of a shared timebase. A
// conversion never uses floating point: it returns the integer quotient and
// the exact fractional remainder, and callers carry that remainder forward
// when accumulating a timeline.
package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"math/bits"
)

// MaxTimebase caps the shared timebase. A set of representation timescales
// whose least common multiple exceeds this value cannot be aligned exactly.
const MaxTimebase int64 = 1 << 40

// MaxTicks caps absolute integral tick counts to stay far away from int64
// overflow when a Time is added to another Time.
const MaxTicks int64 = 1 << 62

var (
	// ErrInexact is returned when a conversion would drop a non-zero remainder.
	ErrInexact = errors.New("timebase conversion is not exact")
	// ErrTimebaseTooLarge is returned when a shared timebase exceeds MaxTimebase.
	ErrTimebaseTooLarge = errors.New("shared timebase exceeds cap")
	// ErrBadTimebase is returned for a zero or negative timebase.
	ErrBadTimebase = errors.New("timebase must be positive")
	// ErrTimeOverflow is returned when a time value cannot fit in int64 ticks.
	ErrTimeOverflow = errors.New("time value overflows int64 ticks")
)

// Rational is an exact reduced signed fraction. Den is always positive.
type Rational struct {
	Num int64 `json:"num"`
	Den int64 `json:"den"`
}

// NewRational builds a reduced fraction.
func NewRational(num, den int64) (Rational, error) {
	if den == 0 {
		return Rational{}, errors.New("rational: zero denominator")
	}
	if den < 0 {
		num, den = -num, -den
	}
	if num == 0 {
		return Rational{Num: 0, Den: 1}, nil
	}
	g := gcd(absInt(num), den)
	return Rational{Num: num / g, Den: den / g}, nil
}

// MustRational is NewRational for constants known to be valid.
func MustRational(num, den int64) Rational {
	r, err := NewRational(num, den)
	if err != nil {
		panic(err)
	}
	return r
}

// IsZero reports whether r is exactly zero.
func (r Rational) IsZero() bool { return r.Num == 0 }

// Add returns r+o exactly, or an error on int64 overflow.
func (r Rational) Add(o Rational) (Rational, error) {
	if r.IsZero() {
		return o, nil
	}
	if o.IsZero() {
		return r, nil
	}
	g := gcd(r.Den, o.Den)
	lcm := r.Den / g * o.Den
	if lcm <= 0 {
		return Rational{}, ErrTimeOverflow
	}
	a, ok1 := mulInt64(r.Num, lcm/r.Den)
	b, ok2 := mulInt64(o.Num, lcm/o.Den)
	if !ok1 || !ok2 {
		return Rational{}, ErrTimeOverflow
	}
	sum, ok := addInt64(a, b)
	if !ok {
		return Rational{}, ErrTimeOverflow
	}
	return NewRational(sum, lcm)
}

// Cmp compares r and o exactly.
func (r Rational) Cmp(o Rational) int {
	if r.Den == o.Den {
		return cmpInt64(r.Num, o.Num)
	}
	a, ok1 := mulInt64(r.Num, o.Den)
	b, ok2 := mulInt64(o.Num, r.Den)
	if !ok1 || !ok2 {
		// Fall back to big integers for extreme values.
		ba := new(big.Int).Mul(big.NewInt(r.Num), big.NewInt(o.Den))
		bb := new(big.Int).Mul(big.NewInt(o.Num), big.NewInt(r.Den))
		return ba.Cmp(bb)
	}
	return cmpInt64(a, b)
}

// String renders an exact fraction, e.g. "1/90000" or "3".
func (r Rational) String() string {
	if r.Den == 1 {
		return fmt.Sprintf("%d", r.Num)
	}
	return fmt.Sprintf("%d/%d", r.Num, r.Den)
}

// Time is an exact instant or duration: integral ticks plus a fractional tick
// in [0,1). Time values are normalized by Add, Sub and NewTime.
type Time struct {
	Ticks int64    `json:"ticks"`
	Rem   Rational `json:"rem"`
}

// NewTime builds a normalized Time. The remainder may be any rational; whole
// ticks are folded into Ticks.
func NewTime(ticks int64, rem Rational) (Time, error) {
	if rem.Den == 0 {
		rem = Rational{Den: 1}
	}
	if rem.Den < 0 {
		rem.Num, rem.Den = -rem.Num, -rem.Den
	}
	if rem.Num != 0 {
		q := rem.Num / rem.Den // truncated toward zero
		if rem.Num < 0 && rem.Num%rem.Den != 0 {
			q--
		}
		var ok bool
		ticks, ok = addInt64(ticks, q)
		if !ok || ticks > MaxTicks || ticks < -MaxTicks {
			return Time{}, ErrTimeOverflow
		}
		rem = Rational{Num: rem.Num - q*rem.Den, Den: rem.Den}
		if rem.Num != 0 {
			g := gcd(rem.Num, rem.Den)
			rem = Rational{Num: rem.Num / g, Den: rem.Den / g}
		} else {
			rem = Rational{Den: 1}
		}
	} else {
		rem = Rational{Den: 1}
	}
	return Time{Ticks: ticks, Rem: rem}, nil
}

// TimeOfTicks returns a whole-tick Time.
func TimeOfTicks(ticks int64) Time { return Time{Ticks: ticks, Rem: Rational{Den: 1}} }

// Add returns t+o exactly carrying fractional ticks into whole ticks.
func (t Time) Add(o Time) (Time, error) {
	if t.Ticks > MaxTicks || t.Ticks < -MaxTicks || o.Ticks > MaxTicks || o.Ticks < -MaxTicks {
		return Time{}, ErrTimeOverflow
	}
	sum, ok := addInt64(t.Ticks, o.Ticks)
	if !ok {
		return Time{}, ErrTimeOverflow
	}
	rem, ok := addRational(ratOrZero(t.Rem), ratOrZero(o.Rem))
	if !ok {
		return Time{}, ErrTimeOverflow
	}
	return NewTime(sum, rem)
}

// Sub returns t-o exactly.
func (t Time) Sub(o Time) (Time, error) {
	neg := Time{Ticks: o.Ticks, Rem: Rational{Num: -o.Rem.Num, Den: o.Rem.Den}}
	if neg.Rem.Den == 0 {
		neg.Rem.Den = 1
	}
	return t.Add(neg)
}

// Cmp compares t and o exactly.
func (t Time) Cmp(o Time) int {
	if t.Ticks != o.Ticks {
		return cmpInt64(t.Ticks, o.Ticks)
	}
	return ratOrZero(t.Rem).Cmp(ratOrZero(o.Rem))
}

// IsZero reports whether t is exactly zero.
func (t Time) IsZero() bool {
	return t.Ticks == 0 && (t.Rem.Num == 0 || t.Rem.Den == 0)
}

// Floor returns the largest integer tick count not greater than t.
func (t Time) Floor() int64 { return t.Ticks }

// Ceil returns the smallest integer tick count not less than t.
func (t Time) Ceil() int64 {
	if t.Rem.Num > 0 {
		return t.Ticks + 1
	}
	return t.Ticks
}

// String renders the exact time as ticks and an optional remainder.
func (t Time) String() string {
	if t.Rem.Num == 0 || t.Rem.Den == 0 {
		return fmt.Sprintf("%d ticks", t.Ticks)
	}
	return fmt.Sprintf("%d+%s ticks", t.Ticks, t.Rem.String())
}

type timeWire struct {
	Ticks int64    `json:"ticks"`
	Rem   Rational `json:"rem"`
}

// MarshalJSON emits the exact representation; field order is fixed so output
// digests stay byte-stable across runs.
func (t Time) MarshalJSON() ([]byte, error) {
	r := t.Rem
	if r.Den == 0 {
		r = Rational{Den: 1}
	}
	return json.Marshal(timeWire{Ticks: t.Ticks, Rem: r})
}

// UnmarshalJSON reads the exact representation produced by MarshalJSON.
func (t *Time) UnmarshalJSON(b []byte) error {
	var w timeWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	if w.Rem.Den == 0 {
		w.Rem = Rational{Den: 1}
	}
	v, err := NewTime(w.Ticks, w.Rem)
	if err != nil {
		return err
	}
	*t = v
	return nil
}

// ConvertTicks converts v ticks at source timebase `from` into `to` ticks per
// second, returning the exact quotient and fractional remainder of one
// destination tick. The remainder is never dropped.
func ConvertTicks(v int64, from, to int64) (Time, error) {
	if from <= 0 || to <= 0 {
		return Time{}, ErrBadTimebase
	}
	num := new(big.Int).Mul(big.NewInt(v), big.NewInt(to))
	den := big.NewInt(from)
	q, r := new(big.Int).QuoRem(num, den, new(big.Int))
	if !q.IsInt64() {
		return Time{}, ErrTimeOverflow
	}
	if !r.IsInt64() {
		return Time{}, ErrTimeOverflow
	}
	return NewTime(q.Int64(), Rational{Num: r.Int64(), Den: from})
}

// ConvertExact is ConvertTicks that rejects any non-zero remainder.
func ConvertExact(v int64, from, to int64) (int64, error) {
	t, err := ConvertTicks(v, from, to)
	if err != nil {
		return 0, err
	}
	if t.Rem.Num != 0 {
		return 0, fmt.Errorf("%w: %d ticks at %d/s is %s at %d/s", ErrInexact, v, from, t.Rem.String(), to)
	}
	return t.Ticks, nil
}

// SecondsToTime converts a decimal-second value (num/den seconds) to ticks of
// the given timebase, exactly, retaining the fractional remainder.
func SecondsToTime(num, den int64, base int64) (Time, error) {
	if den == 0 {
		return Time{}, errors.New("seconds: zero denominator")
	}
	if base <= 0 {
		return Time{}, ErrBadTimebase
	}
	if den < 0 {
		num, den = -num, -den
	}
	// ticks = num * base / den
	n := new(big.Int).Mul(big.NewInt(num), big.NewInt(base))
	q, r := new(big.Int).QuoRem(n, big.NewInt(den), new(big.Int))
	if !q.IsInt64() || !r.IsInt64() {
		return Time{}, ErrTimeOverflow
	}
	return NewTime(q.Int64(), Rational{Num: r.Int64(), Den: den})
}

// Milliseconds converts t at the given timebase to exact milliseconds. The
// integer part and any sub-millisecond remainder are returned separately.
func (t Time) Milliseconds(base int64) (int64, Rational, error) {
	if base <= 0 {
		return 0, Rational{}, ErrBadTimebase
	}
	msNum := new(big.Int).Mul(big.NewInt(t.Ticks), big.NewInt(1000))
	// add remainder * 1000 / rem.Den, expressed over base
	remNum := big.NewInt(0)
	remDen := int64(1)
	if t.Rem.Num != 0 && t.Rem.Den > 0 {
		remNum.Mul(big.NewInt(t.Rem.Num), big.NewInt(1000))
		remDen = t.Rem.Den
	}
	// total = msNum/base + remNum/remDen
	lcm := lcmInt64(base, remDen)
	if lcm <= 0 {
		return 0, Rational{}, ErrTimeOverflow
	}
	a := new(big.Int).Mul(msNum, big.NewInt(lcm/base))
	b := new(big.Int).Mul(remNum, big.NewInt(lcm/remDen))
	total := new(big.Int).Add(a, b)
	q, r := new(big.Int).QuoRem(total, big.NewInt(lcm), new(big.Int))
	if !q.IsInt64() || !r.IsInt64() {
		return 0, Rational{}, ErrTimeOverflow
	}
	rem, err := NewRational(r.Int64(), lcm)
	if err != nil {
		return 0, Rational{}, err
	}
	return q.Int64(), rem, nil
}

// LCM returns the least common multiple of a and b, capped by MaxTimebase.
func LCM(a, b int64) (int64, error) {
	if a <= 0 || b <= 0 {
		return 0, ErrBadTimebase
	}
	g := gcd(a, b)
	q := a / g
	if q > MaxTimebase/b {
		return 0, ErrTimebaseTooLarge
	}
	v := q * b
	if v > MaxTimebase {
		return 0, ErrTimebaseTooLarge
	}
	return v, nil
}

// LCMAll returns the LCM of all values, or ErrTimebaseTooLarge.
func LCMAll(values []int64) (int64, error) {
	if len(values) == 0 {
		return 0, ErrBadTimebase
	}
	acc := int64(1)
	for _, v := range values {
		next, err := LCM(acc, v)
		if err != nil {
			return 0, err
		}
		acc = next
	}
	return acc, nil
}

// CheckSharedTimebase verifies that every representation timescale divides the
// shared base exactly.
func CheckSharedTimebase(base int64, timescales []int64) error {
	if base <= 0 {
		return ErrBadTimebase
	}
	for _, ts := range timescales {
		if ts <= 0 {
			return fmt.Errorf("%w: representation timescale %d", ErrBadTimebase, ts)
		}
		if base%ts != 0 {
			return fmt.Errorf("%w: timescale %d does not divide shared base %d", ErrInexact, ts, base)
		}
	}
	return nil
}

// FrameDuration returns the median of positive durations, using the exact
// lower median for an even count. It returns zero for an empty input.
func FrameDuration(durations []Time) Time {
	if len(durations) == 0 {
		return Time{}
	}
	cp := make([]Time, 0, len(durations))
	for _, d := range durations {
		if d.Cmp(Time{}) > 0 {
			cp = append(cp, d)
		}
	}
	if len(cp) == 0 {
		return Time{}
	}
	// Insertion sort keeps this allocation-light for the small batches used
	// by the archive kernel.
	for i := 1; i < len(cp); i++ {
		for j := i; j > 0 && cp[j].Cmp(cp[j-1]) < 0; j-- {
			cp[j], cp[j-1] = cp[j-1], cp[j]
		}
	}
	return cp[(len(cp)-1)/2]
}

func ratOrZero(r Rational) Rational {
	if r.Den == 0 {
		return Rational{Den: 1}
	}
	return r
}

func addRational(a, b Rational) (Rational, bool) {
	if a.Num == 0 {
		return b, true
	}
	if b.Num == 0 {
		return a, true
	}
	if a.Den <= 0 || b.Den <= 0 {
		return Rational{}, false
	}
	g := gcd(a.Den, b.Den)
	lcm := a.Den / g * b.Den
	if lcm <= 0 {
		return Rational{}, false
	}
	x, ok1 := mulInt64(a.Num, lcm/a.Den)
	y, ok2 := mulInt64(b.Num, lcm/b.Den)
	if !ok1 || !ok2 {
		return Rational{}, false
	}
	s, ok := addInt64(x, y)
	if !ok {
		return Rational{}, false
	}
	r, err := NewRational(s, lcm)
	return r, err == nil
}

func lcmInt64(a, b int64) int64 {
	if a <= 0 || b <= 0 {
		return 0
	}
	g := gcd(a, b)
	if a/g > math.MaxInt64/b {
		return 0
	}
	return a / g * b
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

func absInt(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func mulInt64(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	hi, lo := bits.Mul64(uint64(absInt(a)), uint64(absInt(b)))
	if hi != 0 || lo > math.MaxInt64 {
		return 0, false
	}
	if (a < 0) != (b < 0) {
		return -int64(lo), true
	}
	return int64(lo), true
}

func addInt64(a, b int64) (int64, bool) {
	s := a + b
	if (b > 0 && s < a) || (b < 0 && s > a) {
		return 0, false
	}
	return s, true
}

func cmpInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
