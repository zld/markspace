package manifest

import (
	"fmt"
	"strings"
	"time"
)

// parseRFC3339Nanos parses an RFC 3339 timestamp into Unix nanoseconds. The
// standard time.Parse accepts at most nanosecond precision and silently
// truncates further digits, so medtrace rejects more than nine fractional
// digits instead of losing track of where a segment truly begins.
func parseRFC3339Nanos(s string) (int64, error) {
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		// Count fractional digits up to the timezone designator.
		j := i + 1
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j-i-1 > 9 {
			return 0, fmt.Errorf("timestamp %q has more than nanosecond precision", s)
		}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, fmt.Errorf("invalid program date-time %q: %v", s, err)
	}
	return t.UnixNano(), nil
}
