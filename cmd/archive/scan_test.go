package main

import (
	"fmt"
	"strings"
)

// fmtSscan is a tiny wrapper so test helpers can parse digits without pulling
// formatting into the production binary's imports.
func fmtSscan(s string, out any) (int, error) {
	s = strings.TrimSpace(s)
	switch v := out.(type) {
	case *float64:
		var f float64
		if _, err := fmt.Sscanf(s, "%g", &f); err != nil {
			return 0, err
		}
		*v = f
		return 1, nil
	case *int:
		var n int
		if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
			return 0, err
		}
		*v = n
		return 1, nil
	}
	return 0, fmt.Errorf("unsupported target %T", out)
}
