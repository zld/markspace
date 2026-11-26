// Package clock defines the authoritative 90 kHz integer media timebase.
package clock

import (
	"errors"
	"fmt"
	"math/big"
)

// Hz is the only timeline timebase used by compiled releases.
const Hz int64 = 90000

// Ticks is an integer count of 1/90000-second units.
type Ticks int64

// Seconds converts ticks to seconds. It is intended only for final display.
func (t Ticks) Seconds() float64 { return float64(t) / float64(Hz) }

// Rational is an exact multiplier: mapped = value * Num / Den.
type Rational struct {
	Num int64
	Den int64
}

// Timescale maps a source timestamp expressed in unitsPerSecond to 90k ticks.
func Timescale(unitsPerSecond int64) Rational { return Rational{Num: Hz, Den: unitsPerSecond} }

// Common source timebases.
var (
	Timebase90k = Timescale(Hz)
	Timebase48k = Timescale(48000)
	Frame24fps  = Timescale(24)
	Frame12fps  = Timescale(12)
	RTPVideo    = Timebase90k
	RTPOpus48k  = Timebase48k
)

// Error locates a failure by track, pipeline stage, and input offset.
type Error struct {
	Track  string
	Stage  string
	Offset int64
	Err    error
}

func (e *Error) Error() string {
	return fmt.Sprintf("track=%q stage=%s offset=%d: %v", e.Track, e.Stage, e.Offset, e.Err)
}
func (e *Error) Unwrap() error { return e.Err }

// Wrap creates a located error while preserving errors.Is/As.
func Wrap(track, stage string, offset int64, err error) error {
	if err == nil {
		return nil
	}
	var le *Error
	if errors.As(err, &le) {
		return err
	}
	return &Error{Track: track, Stage: stage, Offset: offset, Err: err}
}

// Range is a left-closed/right-open tick interval [From, To).
type Range struct {
	From Ticks `json:"from"`
	To   Ticks `json:"to"`
}

func (r Range) Length() Ticks { return r.To - r.From }

// Validate checks canonical interval order and optional asset duration.
func (r Range) Validate(duration Ticks) error {
	if r.From < 0 || r.To < 0 {
		return fmt.Errorf("negative range %d:%d", r.From, r.To)
	}
	if r.From >= r.To {
		return fmt.Errorf("non-increasing range %d:%d", r.From, r.To)
	}
	if duration > 0 && r.To > duration {
		return fmt.Errorf("range end %d exceeds duration %d", r.To, duration)
	}
	return nil
}

// Mapping is an exact rational conversion. Ticks is floor(Value*Num/Den);
// RemNum/RemDen describe the sub-tick remainder [0, one tick).
type Mapping struct {
	Value  int64    `json:"value"`
	Base   Rational `json:"base"`
	Ticks  Ticks    `json:"ticks"`
	RemNum int64    `json:"rem_num"`
	RemDen int64    `json:"rem_den"`
	Exact  Rational `json:"-"`
}

// Aligned reports whether conversion landed exactly on a 90 kHz tick.
func (m Mapping) Aligned() bool { return m.RemNum == 0 }

// Map converts a non-negative source value using exact integer arithmetic.
func Map(track, stage string, offset, value int64, base Rational) (Mapping, error) {
	if base.Num <= 0 || base.Den <= 0 {
		return Mapping{}, Wrap(track, stage, offset, fmt.Errorf("invalid rational %d/%d", base.Num, base.Den))
	}
	if value < 0 {
		return Mapping{}, Wrap(track, stage, offset, fmt.Errorf("negative timestamp value %d", value))
	}
	n := new(big.Int).Mul(big.NewInt(value), big.NewInt(base.Num))
	d := big.NewInt(base.Den)
	q, rem := new(big.Int).QuoRem(n, d, new(big.Int))
	if !q.IsInt64() {
		return Mapping{}, Wrap(track, stage, offset, fmt.Errorf("mapped ticks overflow for value %d", value))
	}
	rem64 := rem.Int64()
	g := gcd(rem64, base.Den)
	m := Mapping{
		Value:  value,
		Base:   base,
		Ticks:  Ticks(q.Int64()),
		RemNum: rem64 / g,
		RemDen: base.Den / g,
		Exact:  Rational{Num: value * base.Num, Den: base.Den},
	}
	return m, nil
}

// MustMap is a convenience for compile-time-known valid timebases.
func MustMap(value int64, base Rational) Ticks {
	m, err := Map("", "map", value, value, base)
	if err != nil {
		panic(err)
	}
	return m.Ticks
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
