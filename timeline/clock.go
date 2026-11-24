// Package timeline defines the single time base shared by every subsystem,
// timestamp conversion helpers and deterministic rounding rules.
package timeline

import "math/bits"

// ClockRate is the common time base: all timestamps are expressed in
// nanoseconds since the start of the input stream.
const ClockRate int64 = 1_000_000_000

// Rational is a non-negative exact ratio Num/Den. Den is always > 0.
type Rational struct {
	Num int64
	Den int64
}

func (r Rational) valid() bool { return r.Den > 0 && r.Num >= 0 }

// Seconds returns the ratio as a floating point second value.
func (r Rational) Seconds() float64 {
	if !r.valid() {
		return 0
	}
	return float64(r.Num) / float64(r.Den)
}

// Mul counts units: how many units of length r fit into n units.
// The exact product is rounded half away from zero so repeated conversions
// are deterministic and do not drift toward zero.
func (r Rational) Mul(n int64) (int64, bool) {
	if !r.valid() || n < 0 {
		return 0, false
	}
	p, ok := mulCheck(r.Num, n)
	if !ok {
		return 0, false
	}
	return divRound(p, r.Den)
}

// NsFromUnits converts a tick count expressed in ticksPerSecond into
// nanoseconds on the common clock.
func NsFromUnits(units, ticksPerSecond int64) (int64, bool) {
	if ticksPerSecond <= 0 || units < 0 {
		return 0, false
	}
	return Rational{Num: ClockRate, Den: ticksPerSecond}.Mul(units)
}

// UnitsFromNs converts nanoseconds into ticks of the given rate, rounded
// half away from zero.
func UnitsFromNs(ns, ticksPerSecond int64) (int64, bool) {
	if ticksPerSecond <= 0 || ns < 0 {
		return 0, false
	}
	return Rational{Num: ticksPerSecond, Den: ClockRate}.Mul(ns)
}

// NsToMillis renders a nanosecond duration as whole milliseconds, rounded
// half away from zero.
func NsToMillis(ns int64) int64 {
	out, ok := divRound(ns, 1_000_000)
	if !ok {
		return 0
	}
	return out
}

// NsToMillisFloat renders a nanosecond duration as fractional milliseconds.
func NsToMillisFloat(ns int64) float64 { return float64(ns) / 1_000_000.0 }

// mulCheck reports signed multiplication without overflow. Inputs are
// guaranteed non-negative by callers.
func mulCheck(a, b int64) (int64, bool) {
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	if hi != 0 {
		return 0, false
	}
	return int64(lo), true
}

// divRound divides n/d rounding half away from zero; d must be positive.
func divRound(n, d int64) (int64, bool) {
	if d <= 0 {
		return 0, false
	}
	q, rem := n/d, n%d
	if rem*2 >= d {
		if q == (1<<63 - 1) {
			return 0, false
		}
		q++
	}
	return q, true
}

// AbsInt64 returns the absolute magnitude, reporting overflow for min int64.
func AbsInt64(v int64) (int64, bool) {
	if v == -1<<63 {
		return 0, false
	}
	if v < 0 {
		return -v, true
	}
	return v, true
}
