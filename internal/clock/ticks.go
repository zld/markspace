// Package clock defines the unified integer time base shared by all
// subsystems. All timeline arithmetic is done in integer ticks to avoid
// floating-point accumulation across discontinuities.
package clock

import (
	"fmt"
	"strconv"
	"strings"
)

// MasterTimescale is the single time base of the pipeline (90 kHz, the
// MPEG/RTP clock rate). Every manifest, fragment and index timestamp is
// converted to ticks of this clock exactly once, at the boundary.
const MasterTimescale int64 = 90000

// Ticks is a position or duration on the master timeline.
type Ticks int64

// ToMaster converts v units of the given timescale to master ticks,
// rounding to nearest. Timescale must be positive.
func ToMaster(v int64, timescale int64) (Ticks, error) {
	if timescale <= 0 {
		return 0, fmt.Errorf("clock: invalid timescale %d", timescale)
	}
	// Decompose to keep intermediate products small:
	// v*M/ts = (v/ts)*M + (v%ts)*M/ts, and |v%ts|*M < ts*M which for any
	// realistic timescale (<= 2^32) stays far below int64 overflow.
	q := v / timescale
	r := v % timescale
	rem := r * MasterTimescale
	// Round half away from zero.
	var adj int64
	if rem >= 0 {
		adj = timescale / 2
	} else {
		adj = -timescale / 2
	}
	return Ticks(q*MasterTimescale + (rem+adj)/timescale), nil
}

// MustToMaster is ToMaster for call sites that have already validated the
// timescale; it panics on a non-positive timescale.
func MustToMaster(v int64, timescale int64) Ticks {
	t, err := ToMaster(v, timescale)
	if err != nil {
		panic(err)
	}
	return t
}

// To converts master ticks back to units of the given timescale.
func (t Ticks) To(timescale int64) int64 {
	if timescale <= 0 {
		panic("clock: invalid timescale")
	}
	return mulDivRound(int64(t), timescale, MasterTimescale)
}

// Millis returns the tick count as whole milliseconds, rounded to nearest.
func (t Ticks) Millis() int64 {
	return mulDivRound(int64(t), 1000, MasterTimescale)
}

// FromMillis converts milliseconds to master ticks.
func FromMillis(ms int64) Ticks {
	return Ticks(mulDivRound(ms, MasterTimescale, 1000))
}

// mulDivRound computes a*b/c rounded half away from zero, using the same
// quotient/remainder decomposition as ToMaster to avoid overflow.
func mulDivRound(a, b, c int64) int64 {
	if c == 0 {
		panic("clock: division by zero")
	}
	q := a / c
	r := a % c
	rem := r * b
	var adj int64
	if rem >= 0 {
		adj = c / 2
	} else {
		adj = -c / 2
	}
	return q*b + (rem+adj)/c
}

// ParseDecimal parses a non-negative decimal string (e.g. "9.976" from an
// HLS EXTINF tag) into an exact numerator/denominator pair without ever
// going through floating point.
func ParseDecimal(s string) (num, den int64, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, fmt.Errorf("clock: empty decimal")
	}
	neg := false
	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	}
	parts := strings.SplitN(s, ".", 2)
	intPart, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("clock: bad decimal %q: %w", s, err)
	}
	num, den = intPart, 1
	if len(parts) == 2 {
		frac := parts[1]
		if frac == "" {
			return 0, 0, fmt.Errorf("clock: bad decimal %q", s)
		}
		fv, err := strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("clock: bad decimal %q: %w", s, err)
		}
		for range len(frac) {
			den *= 10
		}
		num = num*den + fv
	}
	if neg {
		num = -num
	}
	return num, den, nil
}

// ParseDuration parses a decimal number of seconds into exact master ticks.
func ParseDuration(s string) (Ticks, error) {
	num, den, err := ParseDecimal(s)
	if err != nil {
		return 0, err
	}
	return Ticks(mulDivRound(num, MasterTimescale, den)), nil
}
