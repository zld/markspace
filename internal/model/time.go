package model

import (
	"errors"
	"fmt"
	"math/bits"
)

// Base is an integer tick rate (ticks per second).
type Base uint32

func (b Base) Valid() bool { return b > 0 }

// Rational is an exact non-negative rational number of seconds.
// It is the only representation used for media-time arithmetic; no
// floating point value ever participates in accumulation.
type Rational struct {
	Num int64 `json:"num"`
	Den int64 `json:"den"`
}

var (
	ErrNegativeTime = errors.New("media time must be non-negative")
	ErrZeroDen      = errors.New("rational denominator must be positive")
	ErrTimeOverflow = errors.New("media time arithmetic overflow")
	ErrBackwards    = errors.New("timestamp went backwards")
)

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	if a < 0 {
		return -a
	}
	return a
}

// NewRational validates and reduces num/den.
func NewRational(num, den int64) (Rational, error) {
	if num < 0 {
		return Rational{}, ErrNegativeTime
	}
	if den <= 0 {
		return Rational{}, ErrZeroDen
	}
	g := gcd(num, den)
	if g == 0 {
		g = 1
	}
	return Rational{Num: num / g, Den: den / g}, nil
}

func MustRational(num, den int64) Rational {
	r, err := NewRational(num, den)
	if err != nil {
		panic(err)
	}
	return r
}

func (r Rational) IsZero() bool { return r.Num == 0 }

func (r Rational) Validate() error {
	if r.Num < 0 {
		return ErrNegativeTime
	}
	if r.Den <= 0 {
		return ErrZeroDen
	}
	return nil
}

func cmp128(ah, al, bh, bl uint64) int {
	switch {
	case ah < bh:
		return -1
	case ah > bh:
		return 1
	case al < bl:
		return -1
	case al > bl:
		return 1
	default:
		return 0
	}
}

// Cmp compares two rationals exactly using 128-bit integer products.
func (r Rational) Cmp(o Rational) int {
	ah, al := bits.Mul64(uint64(r.Num), uint64(o.Den))
	bh, bl := bits.Mul64(uint64(o.Num), uint64(r.Den))
	return cmp128(ah, al, bh, bl)
}

// Sub returns r-o. Negative results are rejected rather than clamped.
func (r Rational) Sub(o Rational) (Rational, error) {
	if r.Cmp(o) < 0 {
		return Rational{}, ErrNegativeTime
	}
	// The products are bounded by fixture-scale media times. Reject
	// overflow instead of silently wrapping.
	n1, ok1 := mulInt64(r.Num, o.Den)
	n2, ok2 := mulInt64(o.Num, r.Den)
	d, ok3 := mulInt64(r.Den, o.Den)
	if !ok1 || !ok2 || !ok3 || n1 < n2 {
		return Rational{}, ErrTimeOverflow
	}
	return NewRational(n1-n2, d)
}

func mulInt64(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	if hi != 0 || lo > uint64(^uint64(0)>>1) {
		return 0, false
	}
	return int64(lo), true
}

// Rescale converts this rational into ticks at base. It returns the exact
// floor and the non-negative remainder (in units of the denominator), so
// callers can report a non-integral track boundary instead of hiding it.
func (r Rational) Rescale(base Base) (ticks int64, remainder int64, err error) {
	if err := r.Validate(); err != nil {
		return 0, 0, err
	}
	if !base.Valid() {
		return 0, 0, fmt.Errorf("invalid time base %d", base)
	}
	hi, lo := bits.Mul64(uint64(r.Num), uint64(base))
	if hi >= uint64(r.Den) {
		return 0, 0, ErrTimeOverflow
	}
	q, rem := bits.Div64(hi, lo, uint64(r.Den))
	if q > uint64(^uint64(0)>>1) {
		return 0, 0, ErrTimeOverflow
	}
	return int64(q), int64(rem), nil
}

// RescaleTicks converts integer ticks from one base to another.
func RescaleTicks(ticks int64, from, to Base) (int64, int64, error) {
	if ticks < 0 {
		return 0, 0, ErrNegativeTime
	}
	if !from.Valid() || !to.Valid() {
		return 0, 0, fmt.Errorf("invalid time base")
	}
	return Rational{Num: ticks, Den: int64(from)}.Rescale(to)
}

// Duration returns the duration represented by ticks at base.
func Duration(ticks int64, base Base) (Rational, error) {
	return NewRational(ticks, int64(base))
}

// AddTicks adds delta ticks to ticks, rejecting overflow and negative time.
func AddTicks(ticks, delta int64) (int64, error) {
	if delta < 0 && -delta > ticks {
		return 0, ErrNegativeTime
	}
	if delta > 0 && ticks > int64(^uint64(0)>>1)-delta {
		return 0, ErrTimeOverflow
	}
	return ticks + delta, nil
}

// UnwrapState expands 32-bit RTP timestamps into a monotonic 64-bit value.
// A modular jump larger than half the 32-bit space is reported as backwards,
// never silently treated as a wrap.
type UnwrapState struct {
	last  uint32
	wraps uint64
	have  bool
}

// UnwrapResult describes one timestamp expansion.
type UnwrapResult struct {
	Value     uint64 `json:"value"`
	Wraps     uint64 `json:"wraps"`
	Wrapped   bool   `json:"wrapped"`
	Backwards bool   `json:"backwards"`
	Delta     uint32 `json:"delta"`
}

// Init establishes the stream's epoch without counting it as a wrap.
func (u *UnwrapState) Init(ts uint32) {
	u.last = ts
	u.wraps = 0
	u.have = true
}

// Push expands ts relative to the initialized epoch.
func (u *UnwrapState) Push(ts uint32) (UnwrapResult, error) {
	if !u.have {
		u.Init(ts)
		return UnwrapResult{Value: uint64(ts)}, nil
	}
	delta := uint32(ts - u.last)
	if delta > 1<<31 {
		return UnwrapResult{Backwards: true, Delta: delta}, ErrBackwards
	}
	wrapped := ts < u.last
	if wrapped {
		u.wraps++
	}
	u.last = ts
	return UnwrapResult{
		Value:   (u.wraps << 32) | uint64(ts),
		Wraps:   u.wraps,
		Wrapped: wrapped,
		Delta:   delta,
	}, nil
}

// wraps is exposed for diagnostics/tests.
func (u *UnwrapState) Wraps() uint64 { return u.wraps }
