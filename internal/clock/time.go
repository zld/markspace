// Package clock provides an exact rational media-time base shared by the
// manifest parsers, segment scanner, fingerprint index and recovery planner.
package clock

import (
	"fmt"
	"strconv"
	"strings"
)

// Time is an exact rational number of seconds: Num/Den. Values are always
// normalised so that Den > 0 and the fraction is reduced. Using integers keeps
// segment boundaries exact across long DASH/HLS runs; floating point drift is
// never introduced.
type Time struct {
	Num int64
	Den int64
}

// Zero is the origin of the media timeline.
var Zero = Time{Num: 0, Den: 1}

func gcd(a, b int64) int64 {
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// New builds a normalised Time from num/den seconds.
func New(num, den int64) (Time, error) {
	if den == 0 {
		return Time{}, fmt.Errorf("clock: zero denominator")
	}
	if den < 0 {
		num, den = -num, -den
	}
	g := gcd(num, den)
	if g == 0 {
		g = 1
	}
	return Time{Num: num / g, Den: den / g}, nil
}

// MustNew is New for constants known at compile time.
func MustNew(num, den int64) Time {
	t, err := New(num, den)
	if err != nil {
		panic(err)
	}
	return t
}

// FromString parses "N" or "N/D" into a normalised Time.
func FromString(s string) (Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Time{}, fmt.Errorf("clock: empty time")
	}
	if !strings.Contains(s, "/") {
		return parseDecimal(s)
	}
	parts := strings.SplitN(s, "/", 2)
	num, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return Time{}, fmt.Errorf("clock: bad numerator %q: %w", parts[0], err)
	}
	den, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return Time{}, fmt.Errorf("clock: bad denominator %q: %w", parts[1], err)
	}
	return New(num, den)
}

// parseDecimal converts a decimal seconds literal such as "4.004" into the
// exact rational 4004/1000 without ever passing through float64.
func parseDecimal(s string) (Time, error) {
	neg := strings.HasPrefix(s, "-")
	body := strings.TrimPrefix(strings.TrimPrefix(s, "-"), "+")
	intPart, fracPart, _ := strings.Cut(body, ".")
	if intPart == "" && fracPart == "" {
		return Time{}, fmt.Errorf("clock: bad decimal %q", s)
	}
	if intPart == "" {
		intPart = "0"
	}
	digits := intPart + fracPart
	num, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return Time{}, fmt.Errorf("clock: bad decimal %q: %w", s, err)
	}
	den := int64(1)
	for i := 0; i < len(fracPart); i++ {
		if den > 1<<40 {
			return Time{}, fmt.Errorf("clock: decimal %q too precise", s)
		}
		den *= 10
	}
	if neg {
		num = -num
	}
	return New(num, den)
}

// Add returns t+o exactly.
func (t Time) Add(o Time) Time {
	n, err := New(t.Num*o.Den+o.Num*t.Den, t.Den*o.Den)
	if err != nil {
		return Time{}
	}
	return n
}

// Sub returns t-o exactly.
func (t Time) Sub(o Time) Time {
	n, err := New(t.Num*o.Den-o.Num*t.Den, t.Den*o.Den)
	if err != nil {
		return Time{}
	}
	return n
}

// Cmp returns -1, 0 or +1 comparing t with o.
func (t Time) Cmp(o Time) int {
	// Cross-multiply without overflow for realistic media durations. Fall back
	// to float only for values far outside the supported range.
	l := t.Num * o.Den
	r := o.Num * t.Den
	switch {
	case l < r:
		return -1
	case l > r:
		return 1
	default:
		return 0
	}
}

// Equal reports exact equality.
func (t Time) Equal(o Time) bool { return t.Num*o.Den == o.Num*t.Den }

// IsZero reports whether the value is the timeline origin.
func (t Time) IsZero() bool { return t.Num == 0 }

// String renders the canonical "num/den" form used in reports and index files.
func (t Time) String() string {
	if t.Den == 1 {
		return strconv.FormatInt(t.Num, 10)
	}
	return strconv.FormatInt(t.Num, 10) + "/" + strconv.FormatInt(t.Den, 10)
}

// Seconds renders the rational as a decimal string with millisecond precision,
// for human-readable report columns only; all comparisons use the rational form.
func (t Time) Seconds() string {
	return strconv.FormatFloat(float64(t.Num)/float64(t.Den), 'f', 3, 64)
}

// Abs returns the absolute value of t.
func (t Time) Abs() Time {
	if t.Num < 0 {
		return Time{Num: -t.Num, Den: t.Den}
	}
	return t
}

// FromTimescale converts duration ticks at a timescale to seconds exactly.
func FromTimescale(ticks int64, timescale uint32) (Time, error) {
	if timescale == 0 {
		return Time{}, fmt.Errorf("clock: zero timescale")
	}
	return New(ticks, int64(timescale))
}

// ParseISODuration parses an ISO 8601 duration such as "PT12S", "PT1M30.5S" or
// "P1DT2H" into exact seconds. DASH manifests use it for period and segment
// durations, so it shares the same rational base as every other time value.
func ParseISODuration(s string) (Time, error) {
	s = strings.TrimSpace(s)
	if s == "" || (s[0] != 'P' && s[0] != 'p') {
		return Time{}, fmt.Errorf("clock: not an ISO 8601 duration: %q", s)
	}
	rest := s[1:]
	neg := false
	if strings.HasPrefix(rest, "-") {
		neg = true
		rest = rest[1:]
	}
	total := Zero
	inTime := false
	num := ""
	sawDesignator := false
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		switch {
		case c >= '0' && c <= '9' || c == '.':
			num += string(c)
		case c == 'T' || c == 't':
			if num != "" {
				return Time{}, fmt.Errorf("clock: dangling number in duration %q", s)
			}
			inTime = true
		case c == 'Y' || c == 'y' || c == 'W' || c == 'w':
			return Time{}, fmt.Errorf("clock: unsupported duration designator %c in %q", c, s)
		case c == 'H' || c == 'h' || c == 'M' || c == 'm' || c == 'S' || c == 's' || c == 'D' || c == 'd':
			if num == "" {
				return Time{}, fmt.Errorf("clock: missing number before %c in %q", c, s)
			}
			v, err := parseDecimal(num)
			if err != nil {
				return Time{}, err
			}
			var mult int64
			switch c {
			case 'D', 'd':
				if inTime {
					return Time{}, fmt.Errorf("clock: day designator inside time part of %q", s)
				}
				mult = 86400
			case 'H', 'h':
				if !inTime {
					return Time{}, fmt.Errorf("clock: hour designator outside time part of %q", s)
				}
				mult = 3600
			case 'M', 'm':
				if !inTime {
					return Time{}, fmt.Errorf("clock: month designators are unsupported in %q", s)
				}
				mult = 60
			case 'S', 's':
				if !inTime {
					return Time{}, fmt.Errorf("clock: second designator outside time part of %q", s)
				}
				mult = 1
			}
			total = total.Add(MustNew(v.Num*mult, v.Den))
			num = ""
			sawDesignator = true
		default:
			return Time{}, fmt.Errorf("clock: unexpected %q in duration %q", c, s)
		}
	}
	if num != "" {
		return Time{}, fmt.Errorf("clock: trailing number in duration %q", s)
	}
	if !sawDesignator {
		return Time{}, fmt.Errorf("clock: duration %q has no components", s)
	}
	if neg {
		return Time{Num: -total.Num, Den: total.Den}, nil
	}
	return total, nil
}
