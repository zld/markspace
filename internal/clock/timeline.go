// Package clock defines the only time representation used across avpipe:
// an integer tick count paired with an explicit timescale (ticks/second).
package clock

import (
	"fmt"
	"math/big"
)

// Ticks is an integer position or duration in units of a Timebase.
type Ticks int64

// CanonicalScale is a high-resolution common timescale used for sorting and
// de-duplicating events without floating-point comparisons.
const CanonicalScale uint32 = 1_000_000_000

// Timebase is an explicit timescale: Timescale ticks elapse per second.
type Timebase struct {
	Timescale uint32 `json:"timescale"`
}

// NewTimebase returns a timebase and rejects a zero timescale.
func NewTimebase(timescale uint32) (Timebase, error) {
	if timescale == 0 {
		return Timebase{}, fmt.Errorf("clock: timescale must be positive")
	}
	return Timebase{Timescale: timescale}, nil
}

// FromMillis converts an integer millisecond value to this timebase using
// round-to-nearest, ties-up rounding.
func (tb Timebase) FromMillis(ms int64) Ticks {
	return ConvertRound(Ticks(ms), 1000, tb.Timescale)
}

// ToMillis converts ticks to integer milliseconds using round-to-nearest.
func (tb Timebase) ToMillis(t Ticks) int64 {
	return int64(ConvertRound(t, tb.Timescale, 1000))
}

// Timestamp is a media time on a named, explicit timescale.
type Timestamp struct {
	Ticks     Ticks  `json:"ticks"`
	Timescale uint32 `json:"timescale"`
}

// NewTimestamp constructs a timestamp.
func NewTimestamp(ticks Ticks, timescale uint32) Timestamp {
	return Timestamp{Ticks: ticks, Timescale: timescale}
}

// Valid reports whether the timestamp has a non-zero timescale.
func (t Timestamp) Valid() bool { return t.Timescale != 0 }

// In converts t to another timescale using reproducible integer rounding.
func (t Timestamp) In(timescale uint32) Timestamp {
	if t.Timescale == 0 || timescale == 0 {
		return Timestamp{Ticks: t.Ticks, Timescale: timescale}
	}
	return Timestamp{Ticks: ConvertRound(t.Ticks, t.Timescale, timescale), Timescale: timescale}
}

// Canonical converts the timestamp to CanonicalScale.
func (t Timestamp) Canonical() Timestamp { return t.In(CanonicalScale) }

// Compare compares two timestamps on a common exact rational timeline.
// It returns -1, 0, or 1.
func Compare(a, b Timestamp) int {
	if a.Timescale == 0 || b.Timescale == 0 {
		switch {
		case a.Ticks < b.Ticks:
			return -1
		case a.Ticks > b.Ticks:
			return 1
		default:
			return 0
		}
	}
	an := new(big.Int).Mul(big.NewInt(int64(a.Ticks)), big.NewInt(int64(b.Timescale)))
	bn := new(big.Int).Mul(big.NewInt(int64(b.Ticks)), big.NewInt(int64(a.Timescale)))
	return an.Cmp(bn)
}

// AbsDiffMillis returns the absolute distance between two timestamps in
// floating-point milliseconds. It is intended for reporting only; ordering
// and de-duplication use Compare and integer canonical ticks.
func AbsDiffMillis(a, b Timestamp) float64 {
	ca := a.Canonical()
	cb := b.Canonical()
	d := ca.Ticks - cb.Ticks
	if d < 0 {
		d = -d
	}
	return float64(d) * 1000.0 / float64(CanonicalScale)
}

// ConvertRound converts ticks from one timescale to another. Rounding is
// half-up and fully deterministic across machines and Go versions.
func ConvertRound(ticks Ticks, fromScale, toScale uint32) Ticks {
	if fromScale == toScale {
		return ticks
	}
	if fromScale == 0 || toScale == 0 {
		panic("clock: zero timescale")
	}

	// q = ticks * toScale / fromScale, rounded half up.
	num := new(big.Int).Mul(big.NewInt(int64(ticks)), big.NewInt(int64(toScale)))
	den := big.NewInt(int64(fromScale))

	neg := num.Sign() < 0
	if neg {
		num.Abs(num)
	}
	q, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	// Round halves away from zero.
	if twiceRem := new(big.Int).Lsh(rem, 1); twiceRem.Cmp(den) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if neg {
		q.Neg(q)
	}
	return Ticks(q.Int64())
}
