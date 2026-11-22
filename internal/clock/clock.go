// Package clock implements the unified media time base.
//
// Internal media instants are integer nanoseconds. Any sub-nanosecond part
// of a source timescale conversion is retained as a rational remainder so
// repeated conversions across representations never lose precision. The
// package also provides counter wraparound expansion (RTP sequence numbers
// and 32-bit timestamps), linear clock-drift fitting and a VFR-preserving
// monotonic enforcer.
package clock

import (
	"fmt"
	"math/big"

	"mediasync/internal/mederr"
)

// NanosPerSecond is the resolution of the unified time base.
const NanosPerSecond int64 = 1_000_000_000

// Fraction is the residual of a ticks->nanoseconds conversion:
// the exact instant equals Ns + Num/Den nanoseconds, 0 <= Num < Den.
type Fraction struct {
	Num int64
	Den int64
}

// RatNanos is an exact media instant in the unified time base.
type RatNanos struct {
	Ns  int64
	Rem Fraction
}

// TimeBase converts ticks of one representation to unified nanoseconds.
type TimeBase struct {
	timescale int64
}

// NewTimeBase rejects a zero timescale, which would make conversion
// undefined (DASH mdhd timescale == 0 is invalid input).
func NewTimeBase(timescale uint32) (*TimeBase, error) {
	if timescale == 0 {
		return nil, mederr.New(mederr.CodeZeroTimescale,
			"representation declares zero timescale")
	}
	return &TimeBase{timescale: int64(timescale)}, nil
}

// Timescale returns the ticks-per-second value.
func (tb *TimeBase) Timescale() uint32 { return uint32(tb.timescale) }

// mul64 returns a*b with an overflow check.
func mul64(a, b int64) (int64, error) {
	r := a * b
	if a != 0 && r/a != b {
		return 0, mederr.New(mederr.CodeArithmeticOverflow,
			fmt.Sprintf("overflow multiplying %d*%d", a, b))
	}
	return r, nil
}

func add64(a, b int64) (int64, error) {
	r := a + b
	if (b > 0 && r < a) || (b < 0 && r > a) {
		return 0, mederr.New(mederr.CodeArithmeticOverflow,
			fmt.Sprintf("overflow adding %d+%d", a, b))
	}
	return r, nil
}

// ToNanos converts a tick count to exact unified nanoseconds.
func (tb *TimeBase) ToNanos(ticks int64) (RatNanos, error) {
	scaled, err := mul64(ticks, NanosPerSecond)
	if err != nil {
		return RatNanos{}, err
	}
	// Euclidean division so negative ticks get a non-negative remainder.
	q := scaled / tb.timescale
	r := scaled % tb.timescale
	if r < 0 {
		r += tb.timescale
		q--
	}
	return RatNanos{Ns: q, Rem: Fraction{Num: r, Den: tb.timescale}}, nil
}

// Compare orders two exact instants.
func (r RatNanos) Compare(o RatNanos) int {
	if r.Ns != o.Ns {
		if r.Ns < o.Ns {
			return -1
		}
		return 1
	}
	l := big.NewInt(r.Rem.Num * o.Rem.Den)
	rr := big.NewInt(o.Rem.Num * r.Rem.Den)
	return l.Cmp(rr)
}

// Unwrapper expands a bounded counter (16/32-bit) into a monotonic int64
// stream, adding whole cycles whenever the new raw value jumps backwards
// by more than half the counter's modulus.
type Unwrapper struct {
	modulus  int64
	last     int64
	haveLast bool
	wraps    int
}

// NewUnwrapper builds an Unwrapper. bits must be 16 or 32.
func NewUnwrapper(bits int) (*Unwrapper, error) {
	if bits != 16 && bits != 32 {
		return nil, mederr.New(mederr.CodeInternal,
			fmt.Sprintf("unsupported wrap width %d", bits))
	}
	return &Unwrapper{modulus: int64(1) << bits}, nil
}

// Expand returns the unwrapped value and reports whether this step wrapped.
func (u *Unwrapper) Expand(raw uint64) (int64, bool, error) {
	if raw >= uint64(u.modulus) {
		return 0, false, mederr.New(mederr.CodeArithmeticOverflow,
			fmt.Sprintf("raw counter %d exceeds modulus %d", raw, u.modulus))
	}
	v := int64(raw)
	if !u.haveLast {
		u.haveLast, u.last = true, v
		return v, false, nil
	}
	delta := v - (u.last % u.modulus)
	rawJump := delta
	if delta > u.modulus/2 {
		delta -= u.modulus
	} else if delta < -u.modulus/2 {
		delta += u.modulus
	}
	// A wrap is a raw jump that crossed a modulus boundary in either
	// direction; delta folding leaves its sign intact.
	wrapped := rawJump > u.modulus/2 || rawJump < -u.modulus/2
	next, err := add64(u.last, delta)
	if err != nil {
		return 0, false, err
	}
	if wrapped {
		u.wraps++
	}
	u.last = next
	return next, wrapped, nil
}

// Wraps returns the number of backward wraparounds observed so far.
func (u *Unwrapper) Wraps() int { return u.wraps }
