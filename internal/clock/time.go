// Package clock defines the rational media clock shared by the container
// parser, the event aligner and the clip cutter. All timestamps stay in the
// originating track's integer time base; conversions between clocks use
// exact rational arithmetic instead of an average frame rate.
package clock

import (
	"errors"
	"fmt"
)

// Ratio is an exact rational number num/den (den > 0). Values are kept in
// normalized form so that equal values compare bit-for-bit equal.
type Ratio struct {
	num int64
	den int64
}

// NewRatio builds a normalized ratio. It panics on a zero or negative
// denominator because every media clock in this project has a positive
// frequency; callers validate configuration before reaching this point.
func NewRatio(num, den int64) Ratio {
	if den <= 0 {
		panic(fmt.Sprintf("clock: invalid denominator %d", den))
	}
	if num == 0 {
		return Ratio{0, 1}
	}
	g := gcd(abs64(num), den)
	return Ratio{num / g, den / g}
}

// Num returns the numerator.
func (r Ratio) Num() int64 { return r.num }

// Den returns the denominator (always positive).
func (r Ratio) Den() int64 { return r.den }

// Mul scales the ratio by an integer without introducing rounding.
func (r Ratio) Mul(n int64) Ratio { return NewRatio(r.num*n, r.den) }

// Add returns r+o, reduced.
func (r Ratio) Add(o Ratio) Ratio {
	return NewRatio(r.num*o.den+o.num*r.den, r.den*o.den)
}

// Sub returns r-o, reduced.
func (r Ratio) Sub(o Ratio) Ratio {
	return NewRatio(r.num*o.den-o.num*r.den, r.den*o.den)
}

// Cmp returns -1, 0 or 1 like bytes.Compare.
func (r Ratio) Cmp(o Ratio) int {
	l := r.num * o.den
	right := o.num * r.den
	switch {
	case l < right:
		return -1
	case l > right:
		return 1
	default:
		return 0
	}
}

// Float64 is only used for human-readable reports and JSON output.
func (r Ratio) Float64() float64 { return float64(r.num) / float64(r.den) }

// String renders the exact fraction and its decimal approximation.
func (r Ratio) String() string {
	if r.den == 1 {
		return fmt.Sprintf("%d", r.num)
	}
	return fmt.Sprintf("%d/%d(%.6f)", r.num, r.den, r.Float64())
}

// MediaTime is a point on a specific track clock: Ticks counted in
// Timescale units per second. It never stores a float or a converted
// "seconds" value, so timeline construction stays lossless.
type MediaTime struct {
	Ticks     int64
	Timescale uint32
}

// NewMediaTime validates the time base at construction.
func NewMediaTime(ticks int64, timescale uint32) (MediaTime, error) {
	if timescale == 0 {
		return MediaTime{}, &ClockError{Op: "new-time", Reason: "zero timescale", Timescale: 0}
	}
	return MediaTime{Ticks: ticks, Timescale: timescale}, nil
}

// Seconds expresses the time as an exact rational number of seconds.
func (m MediaTime) Seconds() Ratio {
	return NewRatio(m.Ticks, int64(m.Timescale))
}

// ToScale resamples the time onto another time base, rounding to the
// nearest representable tick (half away from zero).
func (m MediaTime) ToScale(timescale uint32) (MediaTime, error) {
	if timescale == 0 {
		return MediaTime{}, &ClockError{Op: "resample", Reason: "zero target timescale", Timescale: timescale}
	}
	if timescale == m.Timescale {
		return m, nil
	}
	num := m.Ticks * int64(timescale)
	q := num / int64(m.Timescale)
	rem := num % int64(m.Timescale)
	if abs64(rem)*2 >= int64(m.Timescale) {
		if rem > 0 {
			q++
		} else {
			q--
		}
	}
	return MediaTime{Ticks: q, Timescale: timescale}, nil
}

// Cmp compares two points after exact resampling.
func (m MediaTime) Cmp(o MediaTime) int {
	return m.Seconds().Cmp(o.Seconds())
}

// AddTicks returns m plus a duration given in the same time base.
func (m MediaTime) AddTicks(ticks int64) MediaTime {
	return MediaTime{Ticks: m.Ticks + ticks, Timescale: m.Timescale}
}

// Sub returns the signed duration m-o in ticks of m's time base. The
// operands must share a time base; cross-scale callers resample first.
func (m MediaTime) Sub(o MediaTime) (int64, error) {
	if m.Timescale != o.Timescale {
		return 0, &ClockError{
			Op: "subtract", Reason: "timescale mismatch",
			Timescale: m.Timescale, Detail: fmt.Sprintf("other=%d", o.Timescale),
		}
	}
	return m.Ticks - o.Ticks, nil
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

// AsClockError unwraps a structured clock error, if present.
func AsClockError(err error) (*ClockError, bool) {
	var ce *ClockError
	if errors.As(err, &ce) {
		return ce, true
	}
	return nil, false
}
