package clock

import (
	"fmt"
	"math/big"
)

// Rational is an immutable normalized rational number. In media code it is
// used both for time bases (seconds per tick) and absolute stream time.
type Rational struct {
	Num int64 `json:"num"`
	Den int64 `json:"den"`
}

// New returns a normalized rational. A zero denominator is rejected.
func New(num, den int64) Rational {
	if den == 0 {
		panic("clock: zero denominator")
	}
	if den < 0 {
		num, den = -num, -den
	}
	if num == 0 {
		return Rational{Num: 0, Den: 1}
	}
	g := gcd(abs64(num), den)
	return Rational{Num: num / g, Den: den / g}
}

// Timebase is expressed as the duration of one tick in seconds. For example,
// a 48 kHz clock is New(1, 48000).
type Timebase = Rational

func TickRate(timescale uint32) Timebase { return New(1, int64(timescale)) }

func (r Rational) Valid() bool { return r.Den > 0 }

func (r Rational) Validate() error {
	if r.Den <= 0 {
		return fmt.Errorf("clock: invalid rational %d/%d", r.Num, r.Den)
	}
	return nil
}

func (r Rational) Big() *big.Rat {
	if r.Den == 0 {
		return new(big.Rat)
	}
	return big.NewRat(r.Num, r.Den)
}

func (r Rational) Float64() float64 {
	if r.Den == 0 {
		return 0
	}
	v, _ := new(big.Rat).SetFrac64(r.Num, r.Den).Float64()
	return v
}

func (r Rational) String() string { return fmt.Sprintf("%d/%d", r.Num, r.Den) }

func (r Rational) Cmp(o Rational) int { return compareRat(r, o) }
func (r Rational) Equal(o Rational) bool {
	return r.Valid() && o.Valid() && r.Cmp(o) == 0
}
func (r Rational) Add(o Rational) Rational { return fromBig(new(big.Rat).Add(r.Big(), o.Big())) }
func (r Rational) Sub(o Rational) Rational { return fromBig(new(big.Rat).Sub(r.Big(), o.Big())) }
func (r Rational) Mul(o Rational) Rational { return fromBig(new(big.Rat).Mul(r.Big(), o.Big())) }

// Ticks returns the rational time represented by n ticks of tb.
func Ticks(n int64, tb Timebase) Rational {
	if err := tb.Validate(); err != nil {
		panic(err)
	}
	return New(n*tb.Num, tb.Den) // New normalizes possible reduction; product is small in this kernel
}

// ConvertTicks converts n source ticks to the destination time base. The
// result is rounded to the nearest destination tick. Use ExactTicks when an
// integral conversion is required.
func ConvertTicks(n int64, from, to Timebase) int64 {
	secs := Ticks(n, from)
	rate := New(to.Den, to.Num) // destination ticks per second
	return secs.Mul(rate).roundedInt()
}

// ExactTicks is like ConvertTicks but reports an error if rounding is needed.
func ExactTicks(n int64, from, to Timebase) (int64, error) {
	x := secsToTicksRat(Ticks(n, from), to)
	if !x.IsInt() {
		return 0, fmt.Errorf("clock: %d ticks %s is not integral in %s", n, from, to)
	}
	return x.Num().Int64(), nil
}

// AlignmentErrorMS compares two rational timestamps and returns their absolute
// difference in milliseconds.
func AlignmentErrorMS(want, got Rational) float64 {
	d := want.Sub(got)
	if d.Num < 0 {
		d = New(-d.Num, d.Den)
	}
	return d.Mul(New(1000, 1)).Float64()
}

// CommonTimebase returns the finest exact time base in which all supplied
// bases can be represented integrally. If tb is Num/Den seconds per tick, its
// tick rate is Den/Num; the common rate is the LCM of the reduced rate
// numerators.
func CommonTimebase(bases ...Timebase) (Timebase, error) {
	if len(bases) == 0 {
		return New(1, 1_000_000_000), nil
	}
	commonRate := int64(1)
	for _, tb := range bases {
		if err := tb.Validate(); err != nil {
			return Rational{}, err
		}
		rate := New(tb.Den, tb.Num)
		commonRate = lcm(commonRate, rate.Num)
	}
	return New(1, commonRate), nil
}

// ToTicks converts an absolute rational time to rounded ticks in tb.
func ToTicks(t Rational, tb Timebase) int64 {
	return t.Mul(New(tb.Den, tb.Num)).roundedInt()
}

func secsToTicksRat(secs Rational, tb Timebase) *big.Rat {
	return secs.Mul(New(tb.Den, tb.Num)).Big()
}

func (r Rational) roundedInt() int64 {
	x := r.Big()
	n := new(big.Int)
	rem := new(big.Int)
	q, rem := n.QuoRem(x.Num(), x.Denom(), rem)
	if rem.Sign() != 0 {
		twice := new(big.Int).Mul(rem, big.NewInt(2))
		if twice.Cmp(x.Denom()) >= 0 {
			if x.Num().Sign() >= 0 {
				q.Add(q, big.NewInt(1))
			} else {
				q.Sub(q, big.NewInt(1))
			}
		}
	}
	return q.Int64()
}

func fromBig(x *big.Rat) Rational {
	return New(x.Num().Int64(), x.Denom().Int64())
}
func compareRat(a, b Rational) int { return a.Big().Cmp(b.Big()) }

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
func lcm(a, b int64) int64 { return a / gcd(a, b) * b }
func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
