// Package timebase provides the shared rational time scale used by media
// PTS, event timestamps and RTP timestamps.
package timebase

import (
	"fmt"
	"math/big"
	"sort"
)

// Nanosecond is the common pipeline time base: one tick is one nanosecond.
var Nanosecond = Rational{Num: 1, Den: 1_000_000_000}

// Rational is a numerator/denominator time scale. It describes the duration
// of one source tick in seconds as Num/Den.
type Rational struct {
	Num int64
	Den int64
}

// Validate reports whether the scale can be used for conversion.
func (r Rational) Validate() error {
	if r.Num <= 0 || r.Den <= 0 {
		return fmt.Errorf("invalid rational time base %d/%d", r.Num, r.Den)
	}
	return nil
}

// ScaleToNano converts ticks in r seconds-per-tick to integer nanoseconds,
// rounding to nearest. The second return is the absolute rounding residual
// in source-independent nanosecond numerator units and is normally zero for
// fixture streams.
func (r Rational) ScaleToNano(ticks int64) (int64, error) {
	if err := r.Validate(); err != nil {
		return 0, err
	}
	// ticks * Num / Den seconds, then * 1e9. Use big integers to make
	// overflow an explicit mapping failure instead of wrapped time.
	n := new(big.Int).Mul(big.NewInt(ticks), big.NewInt(r.Num))
	n.Mul(n, big.NewInt(1_000_000_000))
	den := big.NewInt(r.Den)
	q, rem := new(big.Int).QuoRem(n, den, new(big.Int))
	if rem.Sign() < 0 {
		// QuoRem truncates toward zero; normalize residual for negatives.
		q.Sub(q, big.NewInt(1))
		rem.Add(rem, den)
	}
	half := new(big.Int).Rsh(den, 1)
	if rem.Cmp(half) >= 0 {
		if ticks >= 0 {
			q.Add(q, big.NewInt(1))
		} else {
			q.Sub(q, big.NewInt(1))
		}
	}
	if !q.IsInt64() {
		return 0, fmt.Errorf("mapped time %s does not fit int64", q.String())
	}
	return q.Int64(), nil
}

// MustNano is intended for constants and test fixtures.
func MustNano(ticks int64, r Rational) int64 {
	v, err := r.ScaleToNano(ticks)
	if err != nil {
		panic(err)
	}
	return v
}

// Milliseconds converts a millisecond count to nanoseconds.
func Milliseconds(v int64) int64 { return v * 1_000_000 }

// Seconds converts seconds to nanoseconds.
func Seconds(v int64) int64 { return v * 1_000_000_000 }

// Interval is a half-open integer-nanosecond interval [Lo, Hi).
type Interval struct {
	Lo int64 `json:"start_ns"`
	Hi int64 `json:"end_ns"`
}

// NewInterval validates and constructs an interval.
func NewInterval(lo, hi int64) (Interval, error) {
	if hi < lo {
		return Interval{}, fmt.Errorf("interval end %d before start %d", hi, lo)
	}
	return Interval{Lo: lo, Hi: hi}, nil
}

// Duration returns the interval length in nanoseconds.
func (i Interval) Duration() int64 { return i.Hi - i.Lo }

// Contains reports whether t is in [Lo, Hi).
func (i Interval) Contains(t int64) bool { return t >= i.Lo && t < i.Hi }

// Intersects reports strict overlap. Adjacent intervals [a,b) and [b,c) do
// not intersect because neither contains a shared instant.
func (i Interval) Intersects(o Interval) bool {
	return i.Lo < o.Hi && o.Lo < i.Hi
}

// Intersection returns the strict overlap.
func (i Interval) Intersection(o Interval) (Interval, bool) {
	lo := max64(i.Lo, o.Lo)
	hi := min64(i.Hi, o.Hi)
	if lo >= hi {
		return Interval{}, false
	}
	return Interval{Lo: lo, Hi: hi}, true
}

// MergeOverlapping sorts and merges only strictly overlapping intervals.
// Adjacent but non-overlapping ranges remain separate so segment count is
// stable and reviewable.
func MergeOverlapping(in []Interval) []Interval {
	if len(in) == 0 {
		return nil
	}
	xs := append([]Interval(nil), in...)
	sort.Slice(xs, func(i, j int) bool {
		if xs[i].Lo != xs[j].Lo {
			return xs[i].Lo < xs[j].Lo
		}
		return xs[i].Hi < xs[j].Hi
	})
	out := []Interval{xs[0]}
	for _, x := range xs[1:] {
		last := &out[len(out)-1]
		if x.Lo < last.Hi {
			if x.Hi > last.Hi {
				last.Hi = x.Hi
			}
			continue
		}
		out = append(out, x)
	}
	return out
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
