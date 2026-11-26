package manifest

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"medtrace/internal/core"
)

// parseISODuration converts an ISO-8601 duration (as used by MPD
// mediaPresentationDuration and segment-template durations) to integer
// nanoseconds. The accepted grammar is the subset DASH uses:
//
//	P[nD][T[nH][nM][nS]]
//
// with an optional decimal fraction on any component. Calendar years and
// months are rejected rather than approximated: an MPD producer that uses them
// did not pin a duration medtrace could reconstruct without desynchronising
// the canonical timeline.
func parseISODuration(s string) (core.Nanos, error) {
	orig := s
	if s == "" || (s[0] != 'P' && s[0] != 'p') {
		return 0, fmt.Errorf("duration %q does not start with P", orig)
	}
	s = s[1:]
	var total int64
	inTime := false
	seen := false
	sawTime := false
	lastUnit := 0 // ordering: D=1,H=2,M/m=3,S=4
	for s != "" {
		if s[0] == 'T' || s[0] == 't' {
			if inTime {
				return 0, fmt.Errorf("duration %q has a second T designator", orig)
			}
			inTime, sawTime = true, true
			s = s[1:]
			continue
		}
		i := 0
		for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.' || s[i] == ',') {
			i++
		}
		if i == 0 || i == len(s) {
			return 0, fmt.Errorf("duration %q has a missing component value or designator", orig)
		}
		numStr := strings.Replace(s[:i], ",", ".", 1)
		unit := s[i]
		s = s[i+1:]
		seen = true
		order := unitOrder(unit, inTime)
		if order <= lastUnit {
			return 0, fmt.Errorf("duration %q repeats or reorders component %q", orig, string(unit))
		}
		lastUnit = order
		nanos, err := componentNanos(numStr, unit, inTime)
		if err != nil {
			return 0, fmt.Errorf("duration %q: %w", orig, err)
		}
		if nanos > math.MaxInt64-total {
			return 0, fmt.Errorf("duration %q overflows int64 nanoseconds", orig)
		}
		total += nanos
	}
	if !seen {
		return 0, fmt.Errorf("duration %q has no components", orig)
	}
	if sawTime && total == 0 && strings.HasSuffix(strings.ToUpper(orig), "T") {
		return 0, fmt.Errorf("duration %q ends with an empty time section", orig)
	}
	return core.Nanos(total), nil
}

// unitOrder ranks a designator so a repeated or out-of-order component can be
// rejected; ISO-8601 requires each unit at most once in decreasing magnitude.
func unitOrder(unit byte, inTime bool) int {
	switch unit {
	case 'D', 'd':
		return 1
	case 'H', 'h':
		return 2
	case 'M', 'm':
		return 3
	case 'S', 's':
		return 4
	}
	return 0
}

// componentNanos converts one "<value><designator>" component to nanoseconds.
func componentNanos(num string, unit byte, inTime bool) (int64, error) {
	val, err := parseDecimalDuration(num)
	if err != nil {
		return 0, err
	}
	var factor int64
	switch unit {
	case 'Y', 'y':
		return 0, fmt.Errorf("calendar year component is not convertible to an exact duration")
	case 'M': // upper-case M is months in the date section, minutes in time
		if !inTime {
			return 0, fmt.Errorf("calendar month component is not convertible to an exact duration")
		}
		factor = 60
	case 'm':
		if !inTime {
			return 0, fmt.Errorf("calendar month component is not convertible to an exact duration")
		}
		factor = 60
	case 'D', 'd':
		if inTime {
			return 0, fmt.Errorf("day component inside the time section")
		}
		factor = 86400
	case 'H', 'h':
		if !inTime {
			return 0, fmt.Errorf("hour component outside the time section")
		}
		factor = 3600
	case 'S', 's':
		if !inTime {
			return 0, fmt.Errorf("second component outside the time section")
		}
		factor = 1
	default:
		return 0, fmt.Errorf("unknown duration designator %q", string(unit))
	}
	if factor > 1 {
		if val > math.MaxInt64/factor {
			return 0, fmt.Errorf("component overflows int64 nanoseconds")
		}
		val *= factor
	}
	return val, nil
}

// parseUint is a strict unsigned decimal parser used for MPD numeric
// attributes.
func parseUint(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" || !allDigits(s) {
		return 0, fmt.Errorf("expected an unsigned integer, got %q", s)
	}
	return strconv.ParseUint(s, 10, 64)
}
