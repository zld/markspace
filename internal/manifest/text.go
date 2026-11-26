package manifest

import (
	"strconv"
	"strings"
)

// lines splits text into lines while remembering the 1-based number of each.
// It accepts CRLF and a missing trailing newline.
func lines(text string) []line {
	raw := strings.Split(text, "\n")
	out := make([]line, 0, len(raw))
	for i, s := range raw {
		out = append(out, line{Num: i + 1, Text: strings.TrimSuffix(s, "\r")})
	}
	// A trailing newline produces one empty final element; drop it so callers
	// do not report a phantom error on the last line.
	if n := len(out); n > 0 && out[n-1].Text == "" {
		out = out[:n-1]
	}
	return out
}

type line struct {
	Num  int
	Text string
}

// trim is the shared whitespace rule: leading/trailing ASCII space and tab.
func (l line) trim() string { return strings.TrimSpace(l.Text) }

// attr parses an HLS attribute list of the form KEY=VALUE,KEY="VALUE",...
// It returns the values with surrounding double quotes removed. Duplicate
// keys keep the last value. Malformed entries without '=' are ignored; the
// caller decides whether an attribute is required.
func attr(s string) (map[string]string, error) {
	out := make(map[string]string, 8)
	for {
		s = strings.TrimLeft(s, " \t")
		if s == "" {
			return out, nil
		}
		eq := strings.IndexByte(s, '=')
		if eq <= 0 {
			return nil, errBadAttr
		}
		key := strings.TrimSpace(s[:eq])
		if strings.ContainsAny(key, " \t") || key == "" {
			return nil, errBadAttr
		}
		rest := s[eq+1:]
		var val string
		if strings.HasPrefix(rest, `"`) {
			end := strings.IndexByte(rest[1:], '"')
			if end < 0 {
				return nil, errBadAttr
			}
			val = rest[1 : 1+end]
			rest = rest[1+end+1:]
			if rest != "" {
				if rest[0] != ',' {
					return nil, errBadAttr
				}
				rest = rest[1:]
			}
		} else {
			comma := strings.IndexByte(rest, ',')
			if comma < 0 {
				val, rest = rest, ""
			} else {
				val, rest = rest[:comma], rest[comma+1:]
			}
			val = strings.TrimSpace(val)
		}
		out[strings.ToUpper(key)] = val
		s = rest
	}
}

// errBadAttr marks a malformed attribute list; callers wrap it with a position.
var errBadAttr = &attrError{}

type attrError struct{}

func (*attrError) Error() string { return "malformed attribute list" }

// parseDecimalDuration parses an HLS duration (decimal seconds, e.g.
// "9.009") into integer microseconds without floating point, then converts to
// nanoseconds. At most 9 fractional digits are accepted; extra digits are
// rejected rather than silently truncated so the caller sees the problem.
func parseDecimalDuration(s string) (coreNanos int64, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errBadDuration
	}
	neg := false
	switch s[0] {
	case '-':
		neg = true
		s = s[1:]
	case '+':
		s = s[1:]
	}
	if s == "" {
		return 0, errBadDuration
	}
	intPart := s
	fracPart := ""
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		intPart = s[:dot]
		fracPart = s[dot+1:]
		if fracPart == "" {
			return 0, errBadDuration
		}
	}
	if intPart == "" {
		intPart = "0"
	}
	if !allDigits(intPart) || (fracPart != "" && !allDigits(fracPart)) {
		return 0, errBadDuration
	}
	if len(fracPart) > 9 {
		return 0, errBadDuration
	}
	secs, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, errBadDuration
	}
	var frac int64
	if fracPart != "" {
		frac, err = strconv.ParseInt(fracPart, 10, 64)
		if err != nil {
			return 0, errBadDuration
		}
		for i := len(fracPart); i < 9; i++ {
			frac *= 10
		}
	}
	const maxSec = int64(1<<63-1) / int64(1_000_000_000)
	if secs > maxSec {
		return 0, errBadDuration
	}
	n := secs*int64(1_000_000_000) + frac
	if neg {
		n = -n
	}
	return n, nil
}

type durationError struct{}

func (*durationError) Error() string { return "malformed duration" }

var errBadDuration = &durationError{}

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
