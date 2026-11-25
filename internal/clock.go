// Package continuity implements a local HLS/DASH continuity engine: manifest
// parsing, CMAF demuxing, stable track mapping, an event-time timeline with
// deterministic late-data recomputation, lossless remuxing, bounded streaming
// and checkpointed recovery.
package continuity

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
	"strings"
)

// BaseTimescale is the single time base used by the timeline, manifests and
// every cross-track comparison. 90000 is exact for 90 kHz video and for the
// common 48 kHz/1024-sample AAC frame cadence (1920 ticks per frame).
const BaseTimescale uint32 = 90000

// Ticks is an integer count of 1/BaseTimescale seconds unless explicitly
// documented otherwise. All conversions are integer-only and deterministic.
type Ticks int64

var (
	// ErrZeroTimescale is returned when a timescale of zero is supplied.
	ErrZeroTimescale = errors.New("timescale must be positive")
	// ErrTickOverflow is returned when an exact integer conversion does not fit
	// into int64.
	ErrTickOverflow = errors.New("tick conversion overflows int64")
)

// Rescale converts v from the from timescale to the to timescale using exact
// integer arithmetic. The result is rounded half away from zero. Conversions
// never use floating point, so repeated runs produce identical ticks.
func Rescale(v int64, from, to uint32) (int64, error) {
	if from == 0 || to == 0 {
		return 0, ErrZeroTimescale
	}
	if from == to || v == 0 {
		return v, nil
	}
	// Magnitude without overflowing on math.MinInt64.
	var mag uint64
	neg := v < 0
	if neg {
		mag = uint64(-(v + 1)) + 1
	} else {
		mag = uint64(v)
	}
	hi, lo := bits.Mul64(mag, uint64(to))
	if hi >= uint64(from) {
		return 0, ErrTickOverflow
	}
	q, r := bits.Div64(hi, lo, uint64(from))
	if r != 0 && r >= uint64(from)-r { // 2*r >= from, without overflow
		q++
	}
	if neg {
		if q > uint64(math.MaxInt64)+1 {
			return 0, ErrTickOverflow
		}
		if q == uint64(math.MaxInt64)+1 {
			return math.MinInt64, nil
		}
		return -int64(q), nil
	}
	if q > uint64(math.MaxInt64) {
		return 0, ErrTickOverflow
	}
	return int64(q), nil
}

// MustRescale is Rescale with a panic on invalid input. It is intended for
// compile-time constants inside the engine.
func MustRescale(v int64, from, to uint32) int64 {
	t, err := Rescale(v, from, to)
	if err != nil {
		panic(err)
	}
	return t
}

// Millis converts ticks to whole milliseconds, rounded half away from zero.
func (t Ticks) Millis() int64 {
	ms, err := Rescale(int64(t), BaseTimescale, 1000)
	if err != nil {
		panic(err)
	}
	return ms
}

// FromMillis converts whole milliseconds to ticks.
func FromMillis(ms int64) (Ticks, error) {
	t, err := Rescale(ms, 1000, BaseTimescale)
	return Ticks(t), err
}

// ParseDecimalTicks parses a non-negative decimal number of seconds (as used
// by HLS EXTINF) into integer ticks with half-away-from-zero rounding. It
// accepts at most nine fractional digits and rejects exponents and signs
// other than an optional leading '+'.
func ParseDecimalTicks(s string, timescale uint32) (Ticks, error) {
	if timescale == 0 {
		return 0, ErrZeroTimescale
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty decimal")
	}
	if s[0] == '+' {
		s = s[1:]
	}
	if s == "" || s[0] == '-' {
		return 0, fmt.Errorf("invalid decimal %q: must be non-negative", s)
	}
	intPart, fracPart := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, fracPart = s[:i], s[i+1:]
		if intPart == "" {
			return 0, fmt.Errorf("invalid decimal %q: missing integer digits", s)
		}
		if fracPart == "" {
			return 0, fmt.Errorf("invalid decimal %q: missing fraction digits", s)
		}
	}
	if len(fracPart) > 9 {
		return 0, fmt.Errorf("invalid decimal %q: more than 9 fractional digits", s)
	}
	whole, err := parseDigits(intPart)
	if err != nil {
		return 0, fmt.Errorf("invalid decimal %q: %w", s, err)
	}
	var frac uint64
	if fracPart != "" {
		frac, err = parseDigits(fracPart)
		if err != nil {
			return 0, fmt.Errorf("invalid decimal %q: %w", s, err)
		}
	}
	pow := uint64(1)
	for range fracPart {
		if pow > math.MaxUint64/10 {
			return 0, ErrTickOverflow
		}
		pow *= 10
	}
	hi, lo := bits.Mul64(whole, uint64(timescale))
	if hi != 0 {
		return 0, ErrTickOverflow
	}
	if frac != 0 {
		fhi, flo := bits.Mul64(frac, uint64(timescale))
		if fhi != 0 {
			return 0, ErrTickOverflow
		}
		// (flo + pow/2) / pow, with overflow check.
		half := pow / 2
		if flo > math.MaxUint64-half {
			return 0, ErrTickOverflow
		}
		q, r := bits.Div64(0, flo+half, pow)
		_ = r
		if lo > math.MaxUint64-q {
			return 0, ErrTickOverflow
		}
		lo += q
	}
	if lo > uint64(math.MaxInt64) {
		return 0, ErrTickOverflow
	}
	return Ticks(lo), nil
}

func parseDigits(s string) (uint64, error) {
	var v uint64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("non-digit %q", c)
		}
		if v > (math.MaxUint64-uint64(c-'0'))/10 {
			return 0, ErrTickOverflow
		}
		v = v*10 + uint64(c-'0')
	}
	return v, nil
}

// UnwrapTimestamp maps v into the tick window nearest to ref for a modular
// timestamp space of size mod. It is used for 32/33-bit wraparound sources:
// UnwrapTimestamp(1, max, 1<<32) returns max+2 (a small forward step).
// The result shares ref's epoch, so ref must already be unwrapped.
func UnwrapTimestamp(v, ref, mod int64) (int64, error) {
	if mod <= 0 {
		return 0, errors.New("modulus must be positive")
	}
	if v < 0 || v >= mod {
		v %= mod
		if v < 0 {
			v += mod
		}
	}
	base := ref - ((ref%mod)+mod)%mod // epoch-aligned window start
	vm := base + v                    // v now provably inside [0, mod)
	half := mod / 2
	switch {
	case vm-ref > half:
		vm -= mod
	case ref-vm > half:
		vm += mod
	}
	return vm, nil
}

// Span is a half-open media interval [Start, End).
type Span struct {
	Start Ticks
	End   Ticks
}

// NewSpan validates a span. Zero-length spans are legal (End == Start).
func NewSpan(start, end Ticks) (Span, error) {
	if end < start {
		return Span{}, fmt.Errorf("span end %d precedes start %d", end, start)
	}
	return Span{Start: start, End: end}, nil
}

// Duration returns End-Start.
func (s Span) Duration() Ticks { return s.End - s.Start }

// Contains reports whether t lies in [Start, End).
func (s Span) Contains(t Ticks) bool { return t >= s.Start && t < s.End }

// Overlaps reports whether the interiors of the two half-open spans intersect.
// A zero-length span never overlaps.
func (s Span) Overlaps(o Span) bool {
	return s.Start < o.End && o.Start < s.End
}

// GapTo returns the non-negative gap between two spans, or zero when they
// overlap or touch.
func (s Span) GapTo(o Span) Ticks {
	if s.End <= o.Start {
		return o.Start - s.End
	}
	if o.End <= s.Start {
		return s.Start - o.End
	}
	return 0
}
