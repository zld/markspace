package internal

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// NS is the canonical integer nanosecond time base used by every boundary.
type NS int64

const (
	NSPerMicrosecond NS = 1_000
	NSPerMillisecond NS = 1_000_000
	NSPerSecond      NS = 1_000_000_000
)

// SecondsToNS converts whole and fractional seconds without floating point loss.
func SecondsToNS(seconds string) (NS, error) {
	ns, err := ParseDecimalSeconds(seconds)
	if err != nil {
		return 0, NewFault("timebase", "", 0, "%w", err)
	}
	return ns, nil
}

// ParseDecimalSeconds parses values such as 6, 6.000 and 4.1234567895.
// Fractions beyond nine digits are rounded to the nearest nanosecond.
func ParseDecimalSeconds(s string) (NS, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	negative := false
	if s[0] == '-' || s[0] == '+' {
		negative = s[0] == '-'
		s = s[1:]
	}
	if s == "" {
		return 0, fmt.Errorf("invalid duration %q", s)
	}

	whole := s
	frac := ""
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		whole = s[:dot]
		frac = s[dot+1:]
		if strings.IndexByte(frac, '.') >= 0 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
	}
	if whole == "" {
		whole = "0"
	}
	sec, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || sec < 0 {
		return 0, fmt.Errorf("invalid whole seconds %q", whole)
	}
	if sec > math.MaxInt64/int64(NSPerSecond) {
		return 0, fmt.Errorf("duration overflow: %q", s)
	}

	nanos, err := fractionalToNS(frac)
	if err != nil {
		return 0, err
	}
	total := sec*int64(NSPerSecond) + nanos
	if total < 0 {
		return 0, fmt.Errorf("duration overflow: %q", s)
	}
	if negative {
		total = -total
	}
	return NS(total), nil
}

func fractionalToNS(frac string) (int64, error) {
	if frac == "" {
		return 0, nil
	}
	if len(frac) > 18 {
		frac = frac[:18] // enough precision for correct nearest-ns rounding
	}
	for _, r := range frac {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid fractional duration %q", frac)
		}
	}
	if len(frac) <= 9 {
		v, err := strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid fractional duration %q", frac)
		}
		for i := len(frac); i < 9; i++ {
			v *= 10
		}
		return v, nil
	}
	head := frac[:9]
	tail := frac[9:]
	base, _ := strconv.ParseInt(head, 10, 64)
	firstTail := int64(tail[0] - '0')
	for i := len(head); i < 9; i++ {
		base *= 10
	}
	if firstTail >= 5 {
		base++
	}
	if base == int64(NSPerSecond) {
		// Rounding 999999999.9... up rolls into one second. Caller adds this to
		// whole seconds, so represent it as NSPerSecond rather than wrapping.
		return int64(NSPerSecond), nil
	}
	return base, nil
}

// ScaledToNS converts a value expressed in a manifest timescale to nanoseconds.
func ScaledToNS(value, timescale int64) (NS, error) {
	if timescale <= 0 {
		return 0, fmt.Errorf("timescale must be positive, got %d", timescale)
	}
	if value < 0 {
		return 0, fmt.Errorf("negative scaled time %d", value)
	}
	num := new(big.Int).Mul(big.NewInt(value), big.NewInt(int64(NSPerSecond)))
	q := new(big.Int).Quo(num, big.NewInt(timescale))
	if !q.IsInt64() {
		return 0, fmt.Errorf("scaled time overflow: value=%d timescale=%d", value, timescale)
	}
	return NS(q.Int64()), nil
}

// AddNS returns a checked sum in the shared integer nanosecond base.
func AddNS(a, b NS) (NS, error) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, fmt.Errorf("nanosecond addition overflow: %d + %d", a, b)
	}
	if b < 0 && a < math.MinInt64-b {
		return 0, fmt.Errorf("nanosecond addition underflow: %d + %d", a, b)
	}
	return a + b, nil
}

// MulNS returns a checked positive multiplication in nanoseconds.
func MulNS(a NS, factor int64) (NS, error) {
	if a == 0 || factor == 0 {
		return 0, nil
	}
	if a < 0 || factor < 0 {
		return 0, fmt.Errorf("negative multiplication is not supported: %d * %d", a, factor)
	}
	if int64(a) > math.MaxInt64/factor {
		return 0, fmt.Errorf("nanosecond multiplication overflow: %d * %d", a, factor)
	}
	return a * NS(factor), nil
}

// Milliseconds renders nanoseconds as a float for reporting only.
func (n NS) Milliseconds() float64 {
	return float64(n) / float64(NSPerMillisecond)
}
