// Package clock implements an exact integer-rational media clock.
//
// DASH timescales, MP4 track timescales and RTP clock rates are expressed as
// integer ticks-per-second ratios. All cross-timebase comparisons and
// conversions go through math/big so no floating point rounding is ever
// introduced into timeline decisions.
package clock

import (
	"errors"
	"math/big"
	"time"
)

// Rational is a reduced integer ratio Num/Den, Den always > 0.
type Rational struct {
	Num int64
	Den int64
}

// NewRational builds and reduces a ratio. A non-positive denominator is an
// error so a zero timescale can never be represented as a Rational.
func NewRational(num, den int64) (Rational, error) {
	if den <= 0 {
		return Rational{}, errors.New("clock: denominator must be positive")
	}
	if num == 0 {
		return Rational{Num: 0, Den: den}, nil
	}
	g := gcd(abs64(num), den)
	return Rational{Num: num / g, Den: den / g}, nil
}

func (r Rational) String() string { return big.NewRat(r.Num, r.Den).RatString() }

// Float64 is for reporting/metrics only, never for ordering decisions.
func (r Rational) Float64() float64 {
	f, _ := new(big.Rat).SetFrac(big.NewInt(r.Num), big.NewInt(r.Den)).Float64()
	return f
}

// Timebase is a valid tick rate: Valid() == false means a zero/invalid source
// timescale was rejected at construction and must be reported as an error.
type Timebase struct {
	rate Rational
}

// New creates a timebase of ticksPerSecond ticks per second.
func New(ticksPerSecond int64) (Timebase, error) {
	if ticksPerSecond <= 0 {
		return Timebase{}, errors.New("clock: zero or negative timescale")
	}
	r, err := NewRational(ticksPerSecond, 1)
	if err != nil {
		return Timebase{}, err
	}
	return Timebase{rate: r}, nil
}

// NewFromRational accepts fractional tick rates (e.g. 44100/1000 style
// specifications) and still rejects a zero numerator.
func NewFromRational(r Rational) (Timebase, error) {
	if r.Num <= 0 || r.Den <= 0 {
		return Timebase{}, errors.New("clock: invalid rational timescale")
	}
	return Timebase{rate: r}, nil
}

// Valid reports whether the timebase is usable.
func (t Timebase) Valid() bool { return t.rate.Num > 0 && t.rate.Den > 0 }

// Rate returns the reduced ticks-per-second ratio.
func (t Timebase) Rate() Rational { return t.rate }

// Instant is a timestamp bound to a specific timebase.
type Instant struct {
	tb    Timebase
	ticks int64
}

// At stamps a raw source tick value. Negative ticks are allowed so non-zero
// (and pre-roll) start times can be expressed.
func (tb Timebase) At(ticks int64) Instant {
	return Instant{tb: tb, ticks: ticks}
}

// Ticks returns the raw tick value in the originating timebase.
func (i Instant) Ticks() int64 { return i.ticks }

// Timebase returns the owning timebase.
func (i Instant) Timebase() Timebase { return i.tb }

// Master is the shared 1 ns-tick timeline every track is projected onto.
var Master = Timebase{rate: Rational{Num: 1_000_000_000, Den: 1}}

// bigNanos converts an instant to an exact nanosecond numerator over a
// denominator: nanos = ticks * tb.Den * 1e9 / tb.Num.
func (i Instant) bigNanos() *big.Rat {
	num := big.NewInt(i.ticks)
	num.Mul(num, big.NewInt(i.tb.rate.Den))
	num.Mul(num, big.NewInt(1_000_000_000))
	return new(big.Rat).SetFrac(num, big.NewInt(i.tb.rate.Num))
}

// Convert projects the instant onto another timebase, rounding half away from
// zero so repeated conversion does not silently lose sub-tick durations.
func (i Instant) Convert(to Timebase) Instant {
	if !i.tb.Valid() || !to.Valid() {
		return Instant{tb: to}
	}
	num := big.NewInt(i.ticks)
	num.Mul(num, big.NewInt(i.tb.rate.Den))
	num.Mul(num, big.NewInt(to.rate.Num))
	den := big.NewInt(i.tb.rate.Num)
	den.Mul(den, big.NewInt(to.rate.Den))
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(num, den, rem)
	twice := new(big.Int).Mul(rem, big.NewInt(2))
	cmp := twice.Cmp(new(big.Int).Abs(den))
	if cmp > 0 || (cmp == 0 && q.Sign() >= 0) {
		if num.Sign() >= 0 {
			q.Add(q, big.NewInt(1))
		} else {
			q.Sub(q, big.NewInt(1))
		}
	}
	return Instant{tb: to, ticks: q.Int64()}
}

// Cmp compares two instants on the master timeline: -1, 0 or +1.
func Cmp(a, b Instant) int {
	return a.bigNanos().Cmp(b.bigNanos())
}

// DeltaNanos returns b-a in nanoseconds. The bool is false when the result
// overflows int64, which must be surfaced as a timestamp-overflow error.
func DeltaNanos(a, b Instant) (int64, bool) {
	d := new(big.Rat).Sub(b.bigNanos(), a.bigNanos())
	whole := new(big.Int).Quo(d.Num(), d.Denom())
	if whole.IsInt64() {
		return whole.Int64(), true
	}
	return 0, false
}

// DeltaMillis returns b-a in milliseconds exactly (float is for metrics).
func DeltaMillis(a, b Instant) float64 {
	d := new(big.Rat).Sub(b.bigNanos(), a.bigNanos())
	millis := new(big.Rat).Quo(d, big.NewRat(1_000_000, 1))
	f, _ := millis.Float64()
	return f}

// Since returns b-a as a standard library duration.
func Since(a, b Instant) (time.Duration, bool) {
	ns, ok := DeltaNanos(a, b)
	return time.Duration(ns), ok
}

// Wraparound unwraps fixed-width source counters (RTP uint32 timestamps and
// sequence numbers) into a monotonically increasing int64 stream.
type Wraparound struct {
	mod      uint64
	half     uint64
	started  bool
	lastRaw  uint64
	extended int64
}

// NewWraparound builds an unwrapper for the given counter width in bits
// (e.g. 32 for RTP timestamps, 16 for RTP sequence numbers).
func NewWraparound(bits uint) (*Wraparound, error) {
	if bits == 0 || bits > 63 {
		return nil, errors.New("clock: wraparound width out of range")
	}
	mod := uint64(1) << bits
	return &Wraparound{mod: mod, half: mod / 2}, nil
}

// Extend maps a raw counter value to an unwrapped absolute value.
func (w *Wraparound) Extend(raw uint64) int64 {
	raw %= w.mod
	if !w.started {
		w.started = true
		w.lastRaw = raw
		w.extended = int64(raw)
		return w.extended
	}
	delta := int64((raw - w.lastRaw) % w.mod)
	if uint64(delta) > w.half { // closer backwards: counter wrapped
		delta -= int64(w.mod)
	}
	w.extended += delta
	w.lastRaw = raw
	return w.extended
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
