// Package cli parses the command line of the hlsforge subcommands.
//
// Durations accept integer milliseconds ("12000ms") or whole/decimal
// seconds ("12s", "0.5s"). Everything is reduced to integer milliseconds so
// the rest of the pipeline never performs floating-point time arithmetic.
package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ParseDuration converts an hlsforge duration argument to milliseconds.
func ParseDuration(s string) (int64, error) {
	if s == "" {
		return 0, errors.New("empty duration")
	}
	switch {
	case strings.HasSuffix(s, "ms"):
		v, err := strconv.ParseInt(strings.TrimSuffix(s, "ms"), 10, 64)
		if err != nil || v < 0 {
			return 0, fmt.Errorf("invalid millisecond duration %q", s)
		}
		return v, nil
	case strings.HasSuffix(s, "s"):
		num := strings.TrimSuffix(s, "s")
		parts := strings.Split(num, ".")
		if len(parts) > 2 {
			return 0, fmt.Errorf("invalid second duration %q", s)
		}
		whole, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || whole < 0 {
			return 0, fmt.Errorf("invalid second duration %q", s)
		}
		ms := whole * 1000
		if len(parts) == 2 {
			frac := parts[1]
			if len(frac) > 3 {
				return 0, fmt.Errorf("duration %q has sub-millisecond precision", s)
			}
			frac += strings.Repeat("0", 3-len(frac))
			f, err := strconv.ParseInt(frac, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid second duration %q", s)
			}
			ms += f
		}
		return ms, nil
	default:
		return 0, fmt.Errorf("duration %q must end in s or ms", s)
	}
}

// ParseBitrates parses a comma separated bitrate list in bits per second.
func ParseBitrates(s string) ([]int, error) {
	var out []int
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		v, err := strconv.Atoi(item)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("invalid bitrate %q", item)
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, errors.New("at least one bitrate is required")
	}
	return out, nil
}

// ParseFaults parses the comma separated fault-injection selector.
func ParseFaults(s string) ([]string, error) {
	allowed := map[string]bool{"missing": true, "order": true, "drift": true}
	var out []string
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if !allowed[item] {
			return nil, fmt.Errorf("unknown fault %q (want missing|order|drift)", item)
		}
		out = append(out, item)
	}
	return out, nil
}
