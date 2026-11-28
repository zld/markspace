// Package timeline provides the exact rational media time base shared by every
// subsystem. All times are carried as integer ticks in a declared track time
// base and converted to seconds only through exact rational arithmetic; no
// floating point value is ever used to decide alignment or ordering.
package timeline

import (
	"fmt"
	"math/big"
)

// Rational is an exact fraction. The denominator is always positive and the
// value is always reduced, so equal values compare equal by construction.
type Rational struct {
	num int64
	den int64
}

// NewRational builds num/den exactly. A zero denominator is an error, as is a
// value that cannot be represented in 64 bits after reduction.
func NewRational(num, den int64) (Rational, error) {
	if den == 0 {
		return Rational{}, newLocated(CodeInvalidTime, "", 0, 0, "rational denominator is zero")
	}
	r := new(big.Rat).SetFrac(big.NewInt(num), big.NewInt(den))
	if !r.Num().IsInt64() || !r.Denom().IsInt64() {
		return Rational{}, newLocated(CodeInvalidTime, "", 0, 0, "rational %d/%d overflows int64", num, den)
	}
	return Rational{num: r.Num().Int64(), den: r.Denom().Int64()}, nil
}

// MustRational is NewRational for compile-time constants and tests.
func MustRational(num, den int64) Rational {
	r, err := NewRational(num, den)
	if err != nil {
		panic(err)
	}
	return r
}

// Num returns the numerator.
func (r Rational) Num() int64 { return r.num }

// Den returns the positive denominator.
func (r Rational) Den() int64 { return r.den }

// IsZero reports whether the value is exactly zero.
func (r Rational) IsZero() bool { return r.num == 0 }

// Neg returns -r.
func (r Rational) Neg() Rational { return Rational{num: -r.num, den: r.den} }

func ratOf(r Rational) *big.Rat { return new(big.Rat).SetFrac64(r.num, r.den) }

func fromRat(r *big.Rat) (Rational, error) {
	if !r.Num().IsInt64() || !r.Denom().IsInt64() {
		return Rational{}, newLocated(CodeInvalidTime, "", 0, 0, "rational result overflows int64")
	}
	return Rational{num: r.Num().Int64(), den: r.Denom().Int64()}, nil
}

// Add returns r+o exactly.
func (r Rational) Add(o Rational) (Rational, error) {
	return fromRat(new(big.Rat).Add(ratOf(r), ratOf(o)))
}

// Sub returns r-o exactly.
func (r Rational) Sub(o Rational) (Rational, error) {
	return fromRat(new(big.Rat).Sub(ratOf(r), ratOf(o)))
}

// MulInt multiplies by an integer exactly.
func (r Rational) MulInt(n int64) (Rational, error) {
	return fromRat(new(big.Rat).Mul(ratOf(r), new(big.Rat).SetInt64(n)))
}

// Cmp returns -1, 0 or +1 as r is less than, equal to or greater than o.
func (r Rational) Cmp(o Rational) int { return ratOf(r).Cmp(ratOf(o)) }

// Abs returns the absolute value.
func (r Rational) Abs() Rational {
	if r.num < 0 {
		return r.Neg()
	}
	return r
}

// Float64 is for reporting only. Alignment and hashing never use it.
func (r Rational) Float64() float64 {
	f, _ := ratOf(r).Float64()
	return f
}

// String renders the exact fraction as "num/den".
func (r Rational) String() string { return fmt.Sprintf("%d/%d", r.num, r.den) }

// Millis rounds the value to the nearest millisecond, half away from zero.
func (r Rational) Millis() (int64, error) { return r.scaled(1000) }

// Micros rounds the value to the nearest microsecond, half away from zero.
// Microseconds are the canonical integer representation used in manifests and
// digests; rounding is deterministic and does not depend on floating point.
func (r Rational) Micros() (int64, error) { return r.scaled(1_000_000) }

func (r Rational) scaled(scale int64) (int64, error) {
	// round(num*scale/den) with halves away from zero.
	n := new(big.Int).Mul(big.NewInt(r.num), big.NewInt(scale))
	d := big.NewInt(r.den)
	q, rem := new(big.Int).QuoRem(n, d, new(big.Int))
	rem.Abs(rem)
	rem.Mul(rem, big.NewInt(2))
	if rem.Cmp(d) >= 0 {
		if n.Sign() < 0 {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	if !q.IsInt64() {
		return 0, newLocated(CodeInvalidTime, "", 0, 0, "scaled time overflows int64")
	}
	return q.Int64(), nil
}

// Base is a track time base: a tick rate plus an optional wrap modulus for
// 32-bit timestamp domains (RTP, 32-bit MP4 clocks).
type Base struct {
	Name      string
	TimeScale uint32
	// WrapAt is the modulus of the raw timestamp domain; 0 means the domain
	// does not wrap and raw ticks are absolute.
	WrapAt int64
}

// Validate checks the base can represent time.
func (b Base) Validate() error {
	if b.TimeScale == 0 {
		return newLocated(CodeInvalidTime, "", 0, 0, "time base %q has zero time scale", b.Name)
	}
	if b.WrapAt < 0 || (b.WrapAt > 0 && b.WrapAt <= int64(b.TimeScale)) {
		return newLocated(CodeInvalidTime, "", 0, 0, "time base %q has invalid wrap modulus %d", b.Name, b.WrapAt)
	}
	return nil
}

// Timestamp is a tick value interpreted in a track time base. This is the only
// representation stored in events, samples and manifests.
type Timestamp struct {
	Ticks int64
	Base  Base
}

// Seconds converts the timestamp to an exact rational number of seconds.
func (t Timestamp) Seconds() (Rational, error) {
	if err := t.Base.Validate(); err != nil {
		return Rational{}, err
	}
	r, err := NewRational(t.Ticks, int64(t.Base.TimeScale))
	if err != nil {
		return Rational{}, &LocatedError{Err: err, Issue: Issue{
			Severity:  SeverityError,
			Code:      CodeInvalidTime,
			Ticks:     t.Ticks,
			TimeScale: t.Base.TimeScale,
			Message:   fmt.Sprintf("convert ticks %d at scale %d (base %q)", t.Ticks, t.Base.TimeScale, t.Base.Name),
		}}
	}
	return r, nil
}

// String renders both the tick value and its exact seconds value.
func (t Timestamp) String() string {
	r, err := t.Seconds()
	if err != nil {
		return fmt.Sprintf("%d/%dtick(%s)", t.Ticks, t.Base.TimeScale, t.Base.Name)
	}
	return fmt.Sprintf("%d/%dtick(%s)=%ss", t.Ticks, t.Base.TimeScale, t.Base.Name, r)
}

// TickTo converts ticks from this base into a timestamp in target, keeping the
// exact seconds value. It is used to place every rendition on one timeline.
func (b Base) TickTo(ticks int64, target Base) (Timestamp, error) {
	if err := b.Validate(); err != nil {
		return Timestamp{}, err
	}
	if err := target.Validate(); err != nil {
		return Timestamp{}, err
	}
	if b.TimeScale == target.TimeScale {
		return Timestamp{Ticks: ticks, Base: target}, nil
	}
	// ticks/b.TimeScale == out/target.TimeScale
	n := new(big.Int).Mul(big.NewInt(ticks), big.NewInt(int64(target.TimeScale)))
	d := big.NewInt(int64(b.TimeScale))
	q, rem := new(big.Int).QuoRem(n, d, new(big.Int))
	if rem.Sign() != 0 {
		// Round half away from zero so mapping is deterministic; the exact
		// seconds value remains recoverable through Seconds() at either base.
		r2 := new(big.Int).Abs(rem)
		r2.Mul(r2, big.NewInt(2))
		if r2.Cmp(d) >= 0 {
			if n.Sign() < 0 {
				q.Sub(q, big.NewInt(1))
			} else {
				q.Add(q, big.NewInt(1))
			}
		}
	}
	if !q.IsInt64() {
		return Timestamp{}, newLocated(CodeInvalidTime, "", 0, 0, "tick conversion overflows int64")
	}
	return Timestamp{Ticks: q.Int64(), Base: target}, nil
}

// WrapStatus describes how a raw timestamp was interpreted by Unwrapper.
type WrapStatus int

const (
	// WrapStatusNormal means the timestamp advanced in arrival order.
	WrapStatusNormal WrapStatus = iota
	// WrapStatusWrapped means a forward wrap of the raw domain was detected.
	WrapStatusWrapped
	// WrapStatusReordered means the timestamp moved backwards by less than
	// half the domain; the event is kept at its exact time and flagged.
	WrapStatusReordered
	// WrapStatusSameTime means the timestamp equals the previous one; several
	// samples or packets may legitimately share a timestamp.
	WrapStatusSameTime
)

func (s WrapStatus) String() string {
	switch s {
	case WrapStatusNormal:
		return "normal"
	case WrapStatusWrapped:
		return "wrapped"
	case WrapStatusReordered:
		return "reordered"
	case WrapStatusSameTime:
		return "same-time"
	default:
		return "unknown"
	}
}

// Unwrapper turns raw wrapping timestamps into a continuous 64-bit tick line.
// It never rewrites the raw tick value; callers keep the original value for
// source reporting and the continuous value for ordering.
type Unwrapper struct {
	Base Base

	seen    bool
	last    int64 // last continuous tick in arrival order
	lastRaw int64
	wraps   int64

	WrapCount      int
	ReorderCount   int
	DuplicateCount int
}

// NewUnwrapper validates the base and returns an unwrapper.
func NewUnwrapper(base Base) (*Unwrapper, error) {
	if err := base.Validate(); err != nil {
		return nil, err
	}
	return &Unwrapper{Base: base}, nil
}

// Push consumes a raw timestamp in arrival order and returns its continuous
// tick value. An ambiguous jump (exactly half the wrap domain) is reported as
// a located error instead of being guessed.
func (u *Unwrapper) Push(raw int64) (int64, WrapStatus, error) {
	if err := u.Base.Validate(); err != nil {
		return 0, WrapStatusNormal, err
	}
	if u.Base.WrapAt > 0 && (raw < 0 || raw >= u.Base.WrapAt) {
		return 0, WrapStatusNormal, newLocated(CodeInvalidTime, "", raw, 0,
			"timestamp %d outside wrap domain [0,%d) for base %q", raw, u.Base.WrapAt, u.Base.Name)
	}
	if !u.seen {
		u.seen, u.lastRaw, u.last = true, raw, raw
		return raw, WrapStatusNormal, nil
	}
	delta := raw - u.lastRaw
	if u.Base.WrapAt > 0 {
		half := u.Base.WrapAt / 2
		switch {
		case delta == half || delta == -half:
			return 0, WrapStatusNormal, newLocated(CodeAmbiguousWrap, "", raw, 0,
				"ambiguous timestamp jump of %d ticks (half of wrap domain %d)", delta, u.Base.WrapAt)
		case delta < -half:
			u.wraps += u.Base.WrapAt
			u.WrapCount++
			u.lastRaw, u.last = raw, raw+u.wraps
			return u.last, WrapStatusWrapped, nil
		case delta > half:
			// A packet from before the most recent wrap arrived late. Map it
			// back onto the previous epoch instead of treating it as the future.
			u.ReorderCount++
			u.lastRaw = raw
			return raw + u.wraps - u.Base.WrapAt, WrapStatusReordered, nil
		}
	}
	cont := raw + u.wraps
	switch {
	case delta == 0:
		u.DuplicateCount++
		return cont, WrapStatusSameTime, nil
	case delta < 0:
		u.ReorderCount++
		u.lastRaw = raw
		return cont, WrapStatusReordered, nil
	default:
		u.lastRaw, u.last = raw, cont
		return cont, WrapStatusNormal, nil
	}
}

// LastContinuous returns the most recent continuous tick (arrival order).
func (u *Unwrapper) LastContinuous() int64 { return u.last }
