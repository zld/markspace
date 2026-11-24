// Package timeline establishes one integer media clock (see
// media.ClockRate) and maps every track's own timebase onto it. All
// conversions use integer/rational arithmetic; float is never used for
// timestamps, so segment boundaries cannot drift through accumulation.
package timeline

import (
	"math/big"

	"archivecheck/internal/media"
	"archivecheck/internal/merr"
)

// Clock converts a track's native time units to unified-clock ticks.
// A track time unit lasts (TimebaseDen/TimebaseNum) seconds.
type Clock struct {
	TrackID     uint32
	TimebaseNum uint32
	TimebaseDen uint32
}

// NewClock validates a track timebase.
func NewClock(trackID uint32, num, den uint32) (Clock, error) {
	if num == 0 || den == 0 {
		return Clock{}, merr.New(merr.KindTimeline, "timeline",
			"track:"+u32(trackID), "timebase numerator and denominator must be positive")
	}
	return Clock{TrackID: trackID, TimebaseNum: num, TimebaseDen: den}, nil
}

// ToTicks converts n track units into unified-clock ticks, rounding to
// the nearest tick (half-up). The exact rounding error is returned in
// nanoseconds (always <= half a tick ~5555ns), allowing the composer to
// report the conversion error without any float arithmetic.
func (c Clock) ToTicks(n int64) (ticks int64, errNanos int64) {
	// Exact tick count = n * ClockRate * Den / Num.
	scaled := new(big.Int).Mul(big.NewInt(n), big.NewInt(media.ClockRate))
	scaled.Mul(scaled, big.NewInt(int64(c.TimebaseDen)))
	num := big.NewInt(int64(c.TimebaseNum))
	q, r := new(big.Int), new(big.Int)
	q.QuoRem(scaled, num, r)
	roundedUp := new(big.Int).Lsh(r, 1).Cmp(num) >= 0
	if roundedUp {
		q.Add(q, big.NewInt(1))
	}
	// Error in ticks = r/Num (or (Num-r)/Num when rounded up); one tick
	// equals 1e9/ClockRate nanoseconds.
	errTicksNum := new(big.Int).Set(r)
	if roundedUp {
		errTicksNum.Sub(num, r)
	}
	nanos := new(big.Int).Mul(errTicksNum, big.NewInt(1_000_000_000))
	nanos.Quo(nanos, new(big.Int).Mul(num, big.NewInt(media.ClockRate)))
	return q.Int64(), nanos.Int64()
}

// SecondsToTicks converts an exact rational number of seconds to ticks.
func SecondsToTicks(sec *big.Rat) int64 {
	scaled := new(big.Rat).Mul(sec, big.NewRat(media.ClockRate, 1))
	return roundRat(scaled)
}

func roundRat(r *big.Rat) int64 {
	q := new(big.Int)
	rem := new(big.Int)
	q.QuoRem(r.Num(), r.Denom(), rem)
	two := new(big.Int).Lsh(rem, 1)
	if two.Cmp(r.Denom()) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	return q.Int64()
}

func u32(v uint32) string {
	if v == 0 {
		return "0"
	}
	var buf [10]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
