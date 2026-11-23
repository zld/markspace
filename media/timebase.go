// Package media defines the single integer time base used by every other
// package. Times are exact integer tick counts, and conversions that do not
// land on a tick keep the leftover as an exact Ratio instead of dropping into
// floating point. Nothing here accumulates float error.
package media

import (
	"fmt"
	"strconv"
	"strings"
)

// Timescale is the number of ticks per second. It is always positive.
type Timescale int64

// Valid reports whether the timescale can be used as a divisor.
func (ts Timescale) Valid() bool { return ts > 0 }

// String renders the raw tick rate.
func (ts Timescale) String() string { return strconv.FormatInt(int64(ts), 10) }

// Common timescales used by the fixtures and HLS/DASH manifests.
const (
	TimescaleMS  Timescale = 1000
	Timescale90K Timescale = 90000
)

// Time is an exact position or duration at a fixed timescale.
type Time struct {
	Value     int64     `json:"value"`
	Timescale Timescale `json:"timescale"`
}

// NewTime builds a Time, clamping an invalid timescale to 1 so arithmetic can
// never divide by zero. Callers that need validation use Validate.
func NewTime(value int64, ts Timescale) Time {
	if !ts.Valid() {
		ts = 1
	}
	return Time{Value: value, Timescale: ts}
}

// Validate reports an unusable time.
func (t Time) Validate() error {
	if !t.Timescale.Valid() {
		return fmt.Errorf("media: non-positive timescale %d", t.Timescale)
	}
	return nil
}

// Seconds returns the exact duration as a Ratio of seconds.
func (t Time) Seconds() Ratio { return NewRatio(t.Value, int64(t.Timescale)) }

// Scale converts to target ticks. The integer result truncates toward zero;
// the second result is the exact leftover expressed in seconds (that is,
// leftover ticks divided by the target timescale). The identity
//
//	exact target ticks == value + leftover*target
//
// holds exactly.
func (t Time) Scale(target Timescale) (int64, Ratio, error) {
	if err := t.Validate(); err != nil {
		return 0, Ratio{}, err
	}
	if !target.Valid() {
		return 0, Ratio{}, fmt.Errorf("media: invalid target timescale %d", target)
	}
	ticks := NewRatio(t.Value, int64(t.Timescale)).MulInt(int64(target))
	q := ticks.Num / ticks.Den // Num/Den is normalised, Den > 0
	left := NewRatio(ticks.Num-q*ticks.Den, ticks.Den)
	// left is in target ticks; convert to seconds for a stable unit.
	return q, NewRatio(left.Num, left.Den*int64(target)), nil
}

// Convert returns the truncated time at target ticks plus the exact leftover
// seconds, so repeated conversions can be reconciled.
func (t Time) Convert(target Timescale) (Time, Ratio, error) {
	q, rem, err := t.Scale(target)
	if err != nil {
		return Time{}, Ratio{}, err
	}
	return NewTime(q, target), rem, nil
}

// Add returns t+o on t's timescale. The residual (in seconds) expresses
// everything that did not fit into a whole tick, exactly.
func (t Time) Add(o Time) (Time, Ratio, error) {
	if err := t.Validate(); err != nil {
		return Time{}, Ratio{}, err
	}
	if err := o.Validate(); err != nil {
		return Time{}, Ratio{}, err
	}
	sum := t.Seconds().Add(o.Seconds()).MulInt(int64(t.Timescale))
	q := sum.Num / sum.Den
	rem := NewRatio(sum.Num-q*sum.Den, sum.Den*int64(t.Timescale))
	return NewTime(q, t.Timescale), rem, nil
}

// Ratio is a normalised exact rational number. Den is always positive and
// Num/Den is reduced, so equality and summation are exact.
type Ratio struct {
	Num int64 `json:"num"`
	Den int64 `json:"den"`
}

// NewRatio normalises num/den. A zero denominator degrades to the zero
// rational rather than panicking, so corrupted manifests stay diagnosable.
func NewRatio(num, den int64) Ratio {
	if den == 0 {
		return Ratio{}
	}
	if den < 0 {
		num, den = -num, -den
	}
	if num == 0 {
		return Ratio{Num: 0, Den: 1}
	}
	g := gcd(abs(num), den)
	return Ratio{Num: num / g, Den: den / g}
}

// RatioOfSeconds turns whole seconds into a Ratio.
func RatioOfSeconds(s int64) Ratio { return Ratio{Num: s, Den: 1} }

// RatioOfMS turns whole milliseconds into an exact seconds Ratio.
func RatioOfMS(ms int64) Ratio { return NewRatio(ms, 1000) }

// Add returns r+o exactly. The zero value of Ratio is the additive identity.
func (r Ratio) Add(o Ratio) Ratio {
	if r.Den == 0 {
		return o
	}
	if o.Den == 0 {
		return r
	}
	return NewRatio(r.Num*o.Den+o.Num*r.Den, r.Den*o.Den)
}

// Sub returns r-o exactly.
func (r Ratio) Sub(o Ratio) Ratio {
	if o.Den == 0 {
		return r
	}
	return NewRatio(r.Num*o.Den-o.Num*r.Den, r.Den*o.Den)
}

// MulInt multiplies by an integer exactly.
func (r Ratio) MulInt(k int64) Ratio { return NewRatio(r.Num*k, r.Den) }

// Abs returns the magnitude.
func (r Ratio) Abs() Ratio {
	if r.Num < 0 {
		return Ratio{Num: -r.Num, Den: r.Den}
	}
	return r
}

// IsZero reports whether the rational is exactly zero.
func (r Ratio) IsZero() bool { return r.Num == 0 }

// Cmp compares r and o: -1, 0 or 1.
func (r Ratio) Cmp(o Ratio) int {
	if r.Den == 0 {
		r = Ratio{}
	}
	if o.Den == 0 {
		o = Ratio{}
	}
	l, rr := r.Num*o.Den, o.Num*r.Den
	switch {
	case l < rr:
		return -1
	case l > rr:
		return 1
	default:
		return 0
	}
}

// MS returns the exact value in milliseconds as a Ratio.
func (r Ratio) MS() Ratio { return r.MulInt(1000) }

// CeilMS returns the value in milliseconds rounded away from zero. It is used
// for comparison thresholds so a nonzero exact remainder never reports zero.
// The zero Ratio reports zero.
func (r Ratio) CeilMS() int64 {
	if r.Den == 0 {
		return 0
	}
	ms := r.MS()
	q := ms.Num / ms.Den
	if ms.Num%ms.Den != 0 {
		if ms.Num > 0 {
			q++
		} else {
			q--
		}
	}
	return q
}

// String renders "num/den".
func (r Ratio) String() string { return fmt.Sprintf("%d/%d", r.Num, r.Den) }

// ParseDecimalSeconds parses a non-negative decimal duration such as "6",
// "6.006" or "0.0334" into an exact seconds Ratio. Scientific notation and
// signs are rejected: manifests express durations as plain decimals.
func ParseDecimalSeconds(s string) (Ratio, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Ratio{}, fmt.Errorf("media: empty duration")
	}
	if strings.ContainsAny(s, "eE+-") {
		return Ratio{}, fmt.Errorf("media: unsupported duration %q", s)
	}
	intPart, fracPart, hasFrac := strings.Cut(s, ".")
	if intPart == "" {
		intPart = "0"
	}
	whole, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil || whole < 0 {
		return Ratio{}, fmt.Errorf("media: invalid duration %q", s)
	}
	if !hasFrac || fracPart == "" {
		return Ratio{Num: whole, Den: 1}, nil
	}
	if len(fracPart) > 9 {
		return Ratio{}, fmt.Errorf("media: duration %q has too many fraction digits", s)
	}
	for _, c := range fracPart {
		if c < '0' || c > '9' {
			return Ratio{}, fmt.Errorf("media: invalid duration %q", s)
		}
	}
	scale := int64(1)
	for range fracPart {
		scale *= 10
	}
	frac, err := strconv.ParseInt(fracPart, 10, 64)
	if err != nil {
		return Ratio{}, fmt.Errorf("media: duration overflow %q", s)
	}
	return NewRatio(whole*scale+frac, scale), nil
}

// FormatMS renders an exact Ratio of seconds as milliseconds with three
// decimals, truncating only for display.
func FormatMS(r Ratio) string {
	ms := r.MS()
	sign := ""
	num := ms.Num
	if num < 0 {
		sign, num = "-", -num
	}
	whole := num / ms.Den
	frac := num % ms.Den
	// frac/Den is in thousandths of a millisecond; scale to 3 places.
	thousandths := frac * 1000 / ms.Den
	return fmt.Sprintf("%s%d.%03d", sign, whole, thousandths)
}

// AlignAccumulator sums exact rational alignment residuals and remembers the
// largest single residual observed. It is the only place drift is totalled.
type AlignAccumulator struct {
	Sum     Ratio `json:"sum"`
	MaxAbs  Ratio `json:"max_abs"`
	Samples int64 `json:"samples"`
}

// Observe records one residual (declared minus actual, in seconds).
func (a *AlignAccumulator) Observe(r Ratio) {
	a.Sum = a.Sum.Add(r)
	abs := r.Abs()
	if abs.Cmp(a.MaxAbs) > 0 {
		a.MaxAbs = abs
	}
	a.Samples++
}

// ErrorMS is the absolute accumulated error in milliseconds, rounded up.
func (a AlignAccumulator) ErrorMS() int64 { return a.Sum.Abs().CeilMS() }

// MaxMS is the largest single residual in milliseconds, rounded up.
func (a AlignAccumulator) MaxMS() int64 { return a.MaxAbs.CeilMS() }

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	if a < 0 {
		return -a
	}
	return a
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
