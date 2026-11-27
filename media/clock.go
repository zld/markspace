// Package media provides the unified nanosecond media time base and the
// shared error model used by every subsystem of the continuity kernel.
package media

import (
	"math/big"
)

var nanosPerSec = big.NewInt(1_000_000_000)

// Scale converts units ticks of the given timescale (ticks per second) into
// nanoseconds. The conversion uses exact rational arithmetic so that
// per-frame rounding never accumulates drift across a track. Any overflow
// of the int64 nanosecond range is reported as an error, never wrapped.
func Scale(units uint64, timescale uint32) (int64, error) {
	if timescale == 0 {
		return 0, &Error{Kind: KindBadTimescale, Op: "clock.Scale",
			Msg: "timescale is zero"}
	}
	v := new(big.Int).SetUint64(units)
	v.Mul(v, nanosPerSec)
	v.Quo(v, new(big.Int).SetUint64(uint64(timescale)))
	if !v.IsInt64() {
		return 0, &Error{Kind: KindOverflow, Op: "clock.Scale",
			Msg: "tick-to-nanosecond conversion overflows int64"}
	}
	return v.Int64(), nil
}

// Tick returns the exact duration of one unified clock tick — 1/gcd of the
// given timescales, expressed in nanoseconds — as a rational number. It is
// the finest time quantum representable exactly in every given timescale.
func Tick(timescales ...uint32) (*big.Rat, error) {
	g := uint64(0)
	for _, ts := range timescales {
		if ts == 0 {
			return nil, &Error{Kind: KindBadTimescale, Op: "clock.Tick",
				Msg: "timescale is zero"}
		}
		g = gcd(g, uint64(ts))
	}
	if g == 0 {
		return nil, &Error{Kind: KindBadTimescale, Op: "clock.Tick",
			Msg: "no timescales given"}
	}
	return new(big.Rat).SetFrac(nanosPerSec, new(big.Int).SetUint64(g)), nil
}

// WithinTick reports whether |deltaNanos| is at most one unified clock tick
// for the given timescales. The comparison is exact (rational).
func WithinTick(deltaNanos int64, timescales ...uint32) (bool, error) {
	tick, err := Tick(timescales...)
	if err != nil {
		return false, err
	}
	d := new(big.Rat).SetInt64(deltaNanos)
	if d.Sign() < 0 {
		d.Neg(d)
	}
	return d.Cmp(tick) <= 0, nil
}

// ExtendTimestamp maps a 32-bit media timestamp (e.g. an RTP timestamp)
// onto a 64-bit extended timeline, given the previously extended value.
// Wraparound of the 32-bit space is resolved by choosing the candidate
// closest to the previous value.
func ExtendTimestamp(prevExt uint64, ts uint32) uint64 {
	const half = uint64(1) << 31
	base := prevExt &^ 0xffffffff
	cand := base | uint64(ts)
	switch {
	case cand > prevExt && cand-prevExt > half:
		return cand - (1 << 32) // timestamp from before the wrap
	case prevExt > cand && prevExt-cand > half:
		return cand + (1 << 32) // timestamp wrapped forward
	default:
		return cand
	}
}

func gcd(a, b uint64) uint64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
