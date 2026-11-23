// Package model contains the integer media-time primitives shared by the
// DASH, HLS, segment and alignment subsystems.
package model

import (
	"fmt"
	"math"
	"math/big"
	"math/bits"
	"strings"
)

// NanosPerSecond is the single output scale used by every subsystem.
const NanosPerSecond int64 = 1_000_000_000

// Rational is an exact, non-negative media time base (ticks per second).
// It is kept in lowest terms so equal time bases compare and serialise
// deterministically.
type Rational struct {
	Num int64 `json:"num"`
	Den int64 `json:"den"`
}

// NewRational validates and normalises a time base. A zero or negative
// denominator is rejected; a negative numerator is rejected as well.
func NewRational(num, den int64) (Rational, error) {
	if den <= 0 {
		return Rational{}, newError(CodeZeroTimescale, "", NoOffset, StageParse,
			true, fmt.Sprintf("invalid timescale %d", den))
	}
	if num < 0 {
		return Rational{}, newError(CodeNegativeDuration, "", NoOffset, StageParse,
			false, fmt.Sprintf("negative time base %d/%d", num, den))
	}
	g := gcd64(num, den)
	if g > 1 {
		num, den = num/g, den/g
	}
	if num == 0 {
		den = 1
	}
	return Rational{Num: num, Den: den}, nil
}

// Scale converts ticks in this time base to nanoseconds. Multiplication is
// performed in 128 bits and overflow is reported instead of wrapping.
func (r Rational) Scale(ticks uint64) (int64, error) {
	if r.Den <= 0 || r.Num < 0 {
		return 0, newError(CodeZeroTimescale, "", NoOffset, StageParse,
			true, "uninitialised time base")
	}
	return scaleToNanos(ticks, uint64(r.Num), uint64(r.Den))
}

// TicksToNanos converts ticks/timescale to nanoseconds. Every caller
// converts original timestamps (rather than accumulated durations), so the
// truncation error is bounded by one nanosecond per value.
func TicksToNanos(ticks uint64, timescale uint32) (int64, error) {
	if timescale == 0 {
		return 0, newError(CodeZeroTimescale, "", NoOffset, StageParse,
			true, "timescale must be greater than zero")
	}
	return scaleToNanos(ticks, 1, uint64(timescale))
}

// DurationToNanos is TicksToNanos with an explicit non-zero check.
func DurationToNanos(duration uint64, timescale uint32) (int64, error) {
	if duration == 0 {
		return 0, newError(CodeNegativeDuration, "", NoOffset, StageParse,
			false, "duration must be greater than zero")
	}
	return TicksToNanos(duration, timescale)
}

// scaleToNanos computes ticks * num * 1e9 / den with exact 128-bit
// intermediate values and explicit overflow detection.
func scaleToNanos(ticks, num, den uint64) (int64, error) {
	if ticks == 0 || num == 0 {
		return 0, nil
	}
	hi, lo := bits.Mul64(ticks, num)
	// Multiply the 128-bit value (hi:lo) by NanosPerSecond.
	upHi, upLo := bits.Mul64(lo, uint64(NanosPerSecond))
	if hi != 0 && uint64(NanosPerSecond) > math.MaxUint64/hi {
		return 0, overflowError(ticks, num, den)
	}
	prodHi := hi*uint64(NanosPerSecond) + upHi
	if prodHi < upHi { // wrapping addition
		return 0, overflowError(ticks, num, den)
	}
	if prodHi >= den { // quotient does not fit in 64 bits
		return 0, overflowError(ticks, num, den)
	}
	q, _ := bits.Div64(prodHi, upLo, den)
	if q > math.MaxInt64 {
		return 0, overflowError(ticks, num, den)
	}
	return int64(q), nil
}

func overflowError(ticks, num, den uint64) error {
	return newError(CodeTimeOverflow, "", NoOffset, StageParse, false,
		fmt.Sprintf("media conversion overflow: ticks=%d scale=%d/%d", ticks, num, den))
}

// AddNanos adds two non-negative nanosecond values without wrapping.
func AddNanos(a, b int64) (int64, error) {
	if a < 0 || b < 0 {
		return 0, newError(CodeNegativeDuration, "", NoOffset, StageParse,
			false, "cannot add negative media time")
	}
	if b > math.MaxInt64-a {
		return 0, newError(CodeTimeOverflow, "", NoOffset, StageParse,
			false, "media time addition overflow")
	}
	return a + b, nil
}

// ParseISODuration converts an ISO-8601 duration (for example PT8.000S or
// PT1H2M3.5S) into nanoseconds. Weeks and days are supported; months and
// years are rejected because they have no fixed length.
func ParseISODuration(s string) (int64, error) {
	orig := s
	if len(s) < 2 || s[0] != 'P' {
		return 0, invalidDuration(orig)
	}
	s = s[1:]
	total := new(big.Int)
	var number strings.Builder
	inTime, seen := false, false
	flush := func(nanosPerUnit int64) error {
		if number.Len() == 0 {
			return invalidDuration(orig)
		}
		v, ok := new(big.Rat).SetString(number.String())
		if !ok || v.Sign() < 0 {
			return invalidDuration(orig)
		}
		// value = v * nanosPerUnit, rounded half away from zero.
		exact := new(big.Rat).Mul(v, new(big.Rat).SetInt64(nanosPerUnit))
		q, r := new(big.Int).QuoRem(exact.Num(), exact.Denom(), new(big.Int))
		if new(big.Int).Lsh(new(big.Int).Abs(r), 1).Cmp(exact.Denom()) >= 0 {
			q.Add(q, big.NewInt(1))
		}
		total.Add(total, q)
		number.Reset()
		seen = true
		return nil
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == 'T':
			if inTime || number.Len() > 0 {
				return 0, invalidDuration(orig)
			}
			inTime = true
		case c >= '0' && c <= '9' || c == '.':
			number.WriteByte(c)
		case c == 'W' || c == 'D':
			if inTime {
				return 0, invalidDuration(orig)
			}
			hours := int64(24)
			if c == 'W' {
				hours = 7 * 24
			}
			if err := flush(hours * 3600 * NanosPerSecond); err != nil {
				return 0, err
			}
		case c == 'H' || c == 'M' || c == 'S':
			if !inTime {
				return 0, invalidDuration(orig)
			}
			unit := NanosPerSecond
			switch c {
			case 'H':
				unit *= 3600
			case 'M':
				unit *= 60
			}
			if err := flush(unit); err != nil {
				return 0, err
			}
		default:
			return 0, invalidDuration(orig)
		}
	}
	if number.Len() != 0 || !seen {
		return 0, invalidDuration(orig)
	}
	if !total.IsInt64() || total.Sign() < 0 {
		return 0, invalidDuration(orig)
	}
	return total.Int64(), nil
}

// ParseDecimalSeconds converts a decimal seconds string (HLS EXTINF) into
// nanoseconds exactly, rounding half away from zero at nanosecond precision.
func ParseDecimalSeconds(s string) (int64, error) {
	orig := s
	s = strings.TrimSpace(s)
	if s == "" || s[0] == '-' || strings.ContainsAny(s, "eE+") {
		return 0, invalidDuration(orig)
	}
	if strings.Count(s, ".") > 1 {
		return 0, invalidDuration(orig)
	}
	intPart, fracPart := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, fracPart = s[:i], s[i+1:]
	}
	if intPart == "" {
		intPart = "0"
	}
	if !allDigits(intPart) || (fracPart != "" && !allDigits(fracPart)) {
		return 0, invalidDuration(orig)
	}
	whole, err := parseNonNegativeInt(intPart)
	if err != nil {
		return 0, invalidDuration(orig)
	}
	ns := new(big.Int).Mul(whole, big.NewInt(NanosPerSecond))
	if fracPart != "" {
		frac, ok := new(big.Int).SetString(fracPart, 10)
		if !ok {
			return 0, invalidDuration(orig)
		}
		den := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(len(fracPart))), nil)
		num := new(big.Int).Mul(frac, big.NewInt(NanosPerSecond))
		q, r := new(big.Int).QuoRem(num, den, new(big.Int))
		if new(big.Int).Lsh(r, 1).Cmp(den) >= 0 {
			q.Add(q, big.NewInt(1))
		}
		ns.Add(ns, q)
	}
	if !ns.IsInt64() || ns.Sign() <= 0 {
		return 0, invalidDuration(orig)
	}
	return ns.Int64(), nil
}

func parseNonNegativeInt(s string) (*big.Int, error) {
	v := new(big.Int)
	if _, ok := v.SetString(s, 10); !ok || v.Sign() < 0 {
		return nil, fmt.Errorf("not a non-negative integer")
	}
	return v, nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func invalidDuration(s string) error {
	return newError(CodeNegativeDuration, "", NoOffset, StageParse, false,
		fmt.Sprintf("invalid or non-positive duration %q", s))
}

func gcd64(a, b int64) int64 {
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	for b != 0 {
		a, b = b, a%b
	}
	if a == 0 {
		return 1
	}
	return a
}
