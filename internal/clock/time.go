// Package clock defines the shared media time base.
//
// All time conversions use integer rational arithmetic so that repeated runs
// produce identical results and floating point rounding never leaks into the
// manifest. Every multiplication is checked for overflow; callers receive a
// fault.Error naming the field involved instead of a wrapped value.
package clock

import (
	"fmt"
	"math"

	"rtptrace/internal/fault"
)

// Rational is an exact numerator/denominator pair. Denominator is always
// strictly positive; zero is rejected.
type Rational struct {
	Num uint64
	Den uint64
}

// Duration is an exact time span expressed as value*Num/Den base units.
// The fraction is kept un-normalized on purpose: the source timescale stays
// visible in error messages.
type Duration struct {
	Value uint64
	Rate  Rational
}

// maxU64 is typed so comparisons stay in uint64 arithmetic (an untyped
// MaxUint64 constant would otherwise default to int and overflow).
const maxU64 uint64 = math.MaxUint64

// NewRational validates a timescale pair.
func NewRational(num, den uint64, field string) (Rational, error) {
	if den == 0 {
		return Rational{}, fault.New(fault.KindOverflow, field, fault.NoOffset,
			fmt.Sprintf("zero denominator in rational %d/%d", num, den))
	}
	return Rational{Num: num, Den: den}, nil
}

// mul64 returns a*b with ok=false on overflow (ok=true means success).
func mul64(a, b uint64) (uint64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	if a > maxU64/b {
		return 0, false
	}
	return a * b, true
}

// Scale converts value*inNum/inDen into outNum/outDen base units, truncating
// toward zero like integer division. field names the offending sample table
// field so errors stay locatable.
func Scale(value uint64, in, out Rational, field string) (uint64, error) {
	if in.Den == 0 || out.Den == 0 {
		return 0, fault.New(fault.KindOverflow, field, fault.NoOffset,
			"rational scale with zero denominator")
	}
	num, ok := mul64(value, in.Num)
	if !ok {
		return 0, fault.New(fault.KindOverflow, field, fault.NoOffset,
			fmt.Sprintf("overflow multiplying value=%d by num=%d", value, in.Num))
	}
	num, ok = mul64(num, out.Num)
	if !ok {
		return 0, fault.New(fault.KindOverflow, field, fault.NoOffset,
			fmt.Sprintf("overflow scaling to output rate %d/%d", out.Num, out.Den))
	}
	den, ok := mul64(in.Den, out.Den)
	if !ok {
		return 0, fault.New(fault.KindOverflow, field, fault.NoOffset,
			"overflow multiplying denominators")
	}
	if den == 0 {
		return 0, fault.New(fault.KindOverflow, field, fault.NoOffset, "zero scale denominator")
	}
	return num / den, nil
}

// ToRTPTimestamp maps a media duration to a 32-bit RTP clock value. The
// high bits are folded away by modular arithmetic, which is exactly how RTP
// timestamps wrap; an error is only returned when the intermediate product
// overflows uint64.
func ToRTPTimestamp(d Duration, rtpClock uint32, field string) (uint32, error) {
	out := Rational{Num: uint64(rtpClock), Den: 1}
	v, err := Scale(d.Value, d.Rate, out, field)
	if err != nil {
		return 0, err
	}
	return uint32(v), nil
}

// Microseconds renders a duration in integer microseconds.
func (d Duration) Microseconds(field string) (uint64, error) {
	return Scale(d.Value, d.Rate, Rational{Num: 1_000_000, Den: 1}, field)
}

// Add accumulates two durations sharing the same rate, rejecting overflow.
func Add(a, b Duration, field string) (Duration, error) {
	if a.Rate != b.Rate {
		return Duration{}, fault.New(fault.KindOverflow, field, fault.NoOffset,
			"cannot add durations on different time bases")
	}
	if a.Value > maxU64-b.Value {
		return Duration{}, fault.New(fault.KindOverflow, field, fault.NoOffset,
			fmt.Sprintf("overflow adding durations %d + %d", a.Value, b.Value))
	}
	return Duration{Value: a.Value + b.Value, Rate: a.Rate}, nil
}
