package clock

import "fmt"

// Rat is an exact rational number with a positive denominator.
type Rat struct {
	Num int64
	Den int64
}

// NewRat normalizes num/den; den must be non-zero.
func NewRat(num, den int64) Rat {
	if den == 0 {
		panic("clock: zero denominator")
	}
	if den < 0 {
		num, den = -num, -den
	}
	g := gcd(abs(num), den)
	return Rat{Num: num / g, Den: den / g}
}

func abs(v int64) int64 {
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

// Add returns r+o.
func (r Rat) Add(o Rat) Rat { return NewRat(r.Num*o.Den+o.Num*r.Den, r.Den*o.Den) }

// Sub returns r-o.
func (r Rat) Sub(o Rat) Rat { return NewRat(r.Num*o.Den-o.Num*r.Den, r.Den*o.Den) }

// Cmp returns -1, 0 or 1 comparing r to o.
func (r Rat) Cmp(o Rat) int {
	l, rr := r.Num*o.Den, o.Num*r.Den
	switch {
	case l < rr:
		return -1
	case l > rr:
		return 1
	default:
		return 0
	}
}

// Float64 returns the rounded floating-point value.
func (r Rat) Float64() float64 { return float64(r.Num) / float64(r.Den) }

// String renders the rational in n/d form.
func (r Rat) String() string { return fmt.Sprintf("%d/%d", r.Num, r.Den) }

// Time is an exact media instant: Ticks units at Rate per second.
type Time struct {
	Ticks int64
	Rate  int64
}

// NewTime builds a media time; rate must be positive.
func NewTime(ticks, rate int64) Time {
	if rate <= 0 {
		panic("clock: non-positive rate")
	}
	return Time{Ticks: ticks, Rate: rate}
}

// Seconds converts the instant to an exact rational number of seconds.
func (t Time) Seconds() Rat { return NewRat(t.Ticks, t.Rate) }

// AddTicks returns t advanced by n ticks.
func (t Time) AddTicks(n int64) Time { return Time{Ticks: t.Ticks + n, Rate: t.Rate} }

// Sub returns the exact difference t-o in seconds; both must share a rate unit.
func (t Time) Sub(o Time) Rat { return t.Seconds().Sub(o.Seconds()) }

// CmpTime compares two instants exactly.
func (t Time) CmpTime(o Time) int { return t.Seconds().Cmp(o.Seconds()) }

// SamplesAt maps the instant to an output sample index by exact rational
// scaling and round-half-away-from-zero. The boundary error is at most
// half an output sample.
func (t Time) SamplesAt(rate int64) int64 {
	return MulDivRound(t.Ticks, rate, t.Rate)
}

// MulDivRound computes round(x*num/den) without floating point; den must be positive.
func MulDivRound(x, num, den int64) int64 {
	if den <= 0 {
		panic("clock: non-positive denominator")
	}
	p := x * num
	q := p / den
	r := p % den
	if r == 0 {
		return q
	}
	if r < 0 {
		r = -r
	}
	if 2*r >= den {
		if p < 0 {
			q--
		} else {
			q++
		}
	}
	return q
}

// Unwrapper32 turns a wrapping 32-bit RTP timestamp into a monotonic
// 64-bit tick count. Packets must be pushed in timeline order.
type Unwrapper32 struct {
	ext int64
	raw uint32
	set bool
}

// Push extends raw and returns the unwrapped tick value.
func (u *Unwrapper32) Push(raw uint32) int64 {
	if !u.set {
		u.ext, u.raw, u.set = 0, raw, true
		return 0
	}
	u.ext += int64(int32(raw - u.raw))
	u.raw = raw
	return u.ext
}

// Last returns the most recently unwrapped tick value.
func (u *Unwrapper32) Last() int64 { return u.ext }

// Delta32 returns the signed 16-bit/32-bit style difference cur-prev in ticks.
// It expands a single wrap of the 32-bit timestamp space.
func Delta32(prev, cur uint32) int32 { return int32(cur - prev) }

// Delta16 returns the signed sequence-number difference cur-prev, expanding a
// single wrap of the 16-bit sequence space.
func Delta16(prev, cur uint16) int16 { return int16(cur - prev) }
