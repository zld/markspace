// Package clock provides the unified rational time base used by every
// module: timestamps are integer ticks on a rational scale, raw source
// timestamps are always preserved, and conversions are exact.
package clock

import (
	"fmt"
	"math/big"
)

// Scale is a rational time scale: Num/Den ticks per second.
// A nanosecond scale is Scale{1_000_000_000, 1}.
type Scale struct {
	Num int64 `json:"num"`
	Den int64 `json:"den"`
}

// NanoScale is the unified pipeline scale (1 tick = 1 nanosecond).
var NanoScale = Scale{Num: 1_000_000_000, Den: 1}

// Valid reports whether the scale is well-formed.
func (s Scale) Valid() bool { return s.Num > 0 && s.Den > 0 }

// String renders the rational scale as num/den.
func (s Scale) String() string {
	return fmt.Sprintf("%d/%d", s.Num, s.Den)
}

// ToNanos converts ticks on this scale to nanoseconds, rounding to the
// nearest nanosecond. The computation uses big integers so it is exact
// and cannot overflow for any realistic tick value.
func (s Scale) ToNanos(ticks int64) int64 {
	num := new(big.Int).SetInt64(ticks)
	num.Mul(num, big.NewInt(s.Den))
	num.Mul(num, big.NewInt(1_000_000_000))
	den := big.NewInt(s.Num)
	// round half away from zero
	half := new(big.Int).Rsh(new(big.Int).Abs(den), 1)
	if num.Sign() >= 0 {
		num.Add(num, half)
	} else {
		num.Sub(num, half)
	}
	return num.Quo(num, den).Int64()
}

// FromNanos converts nanoseconds to ticks on this scale, rounded.
func (s Scale) FromNanos(nanos int64) int64 {
	num := new(big.Int).SetInt64(nanos)
	num.Mul(num, big.NewInt(s.Num))
	den := new(big.Int).Mul(big.NewInt(s.Den), big.NewInt(1_000_000_000))
	half := new(big.Int).Rsh(new(big.Int).Abs(den), 1)
	if num.Sign() >= 0 {
		num.Add(num, half)
	} else {
		num.Sub(num, half)
	}
	return num.Quo(num, den).Int64()
}

// Stamp is a timestamp on the unified timeline. The raw source value
// and the clock it came from are always preserved alongside the
// corrected value so downstream stages never lose provenance.
type Stamp struct {
	Ticks  int64  `json:"ticks"`  // corrected ticks on Scale
	Scale  Scale  `json:"scale"`  // scale of Ticks
	Raw    int64  `json:"raw"`    // original DTS/PTS as read from the source
	Source string `json:"source"` // source clock id, e.g. "rtp:32000" or "mp4:44100"
}

// Nanos returns the corrected timestamp in nanoseconds.
func (s Stamp) Nanos() int64 { return s.Scale.ToNanos(s.Ticks) }

// Span is a half-open time range [Start, End) in nanoseconds.
type Span struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// Overlaps reports whether two spans share any time.
func (s Span) Overlaps(o Span) bool { return s.Start < o.End && o.Start < s.End }

// Duration returns End-Start (may be <= 0 for invalid spans).
func (s Span) Duration() int64 { return s.End - s.Start }

// Millis converts nanoseconds to fractional milliseconds for reporting.
func Millis(nanos int64) float64 { return float64(nanos) / 1e6 }
