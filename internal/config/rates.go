package config

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseRates parses a comma-separated rate list, rejecting blanks, non-positive
// values, duplicates and non-numeric entries.
func ParseRates(s string) ([]int, error) {
	parts := strings.Split(s, ",")
	rates := make([]int, 0, len(parts))
	seen := map[int]bool{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("--rates contains an empty entry")
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("--rates entry %q is not an integer", part)
		}
		if n <= 0 {
			return nil, fmt.Errorf("--rates entry %d must be positive", n)
		}
		if seen[n] {
			return nil, fmt.Errorf("--rates contains duplicate bitrate %d", n)
		}
		seen[n] = true
		rates = append(rates, n)
	}
	if len(rates) == 0 {
		return nil, fmt.Errorf("--rates is required")
	}
	return rates, nil
}

// Usage returns the usage text for a command, empty when unknown.
func Usage(command string) string {
	switch command {
	case "build":
		return buildUsage
	case "fixture":
		return fixtureUsage
	}
	return ""
}

func usageErr(command string, err error, usage string) error {
	return &UsageError{Command: command, Err: err, Usage: usage}
}
