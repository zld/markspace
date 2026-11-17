// Package parser contains self-contained parsers for SRT, WebVTT and ASS.
// Every parse error carries the source file path and a 1-based line number so
// problems can be traced back to the exact cue in the input.
package parser

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Error describes a parse failure at a known location.
type Error struct {
	Path string
	Line int
	Msg  string
}

func (e *Error) Error() string {
	if e.Path == "" {
		return fmt.Sprintf("line %d: %s", e.Line, e.Msg)
	}
	return fmt.Sprintf("%s:%d: %s", e.Path, e.Line, e.Msg)
}

func fail(path string, line int, format string, args ...any) error {
	return &Error{Path: path, Line: line, Msg: fmt.Sprintf(format, args...)}
}

// splitLines breaks decoded text into lines, accepting CRLF and LF and
// dropping a trailing empty line produced by a final newline.
func splitLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func parseClockNumber(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// parseSRTTime parses "HH:MM:SS,mmm" or the VTT form "HH:MM:SS.mmm" (hours
// may exceed two digits in VTT).
func parseSRTTime(s string) (time.Duration, error) {
	comma := strings.IndexByte(s, ',')
	if comma >= 0 {
		s = s[:comma] + "." + s[comma+1:]
	}
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("invalid timestamp %q", s)
	}
	h, ok1 := parseClockNumber(parts[0])
	m, ok2 := parseClockNumber(parts[1])
	secParts := strings.SplitN(parts[2], ".", 2)
	sec, ok3 := parseClockNumber(secParts[0])
	if !ok1 || !ok2 || !ok3 || m > 59 || sec > 59 {
		return 0, fmt.Errorf("invalid timestamp %q", s)
	}
	ms := 0
	if len(secParts) == 2 {
		frac := secParts[1]
		if len(frac) > 3 {
			frac = frac[:3]
		}
		for len(frac) < 3 {
			frac += "0"
		}
		ms, _ = parseClockNumber(frac)
	}
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute +
		time.Duration(sec)*time.Second + time.Duration(ms)*time.Millisecond, nil
}

// parseASSTime parses ASS "H:MM:SS.cc" centisecond timestamps.
func parseASSTime(s string) (time.Duration, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("invalid ASS timestamp %q", s)
	}
	h, ok1 := parseClockNumber(parts[0])
	m, ok2 := parseClockNumber(parts[1])
	secParts := strings.SplitN(parts[2], ".", 2)
	sec, ok3 := parseClockNumber(secParts[0])
	if !ok1 || !ok2 || !ok3 || m > 59 || sec > 59 || len(secParts) != 2 {
		return 0, fmt.Errorf("invalid ASS timestamp %q", s)
	}
	cs := secParts[1]
	if len(cs) != 2 {
		return 0, fmt.Errorf("invalid ASS centiseconds %q", s)
	}
	c, ok := parseClockNumber(cs)
	if !ok {
		return 0, fmt.Errorf("invalid ASS centiseconds %q", s)
	}
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute +
		time.Duration(sec)*time.Second + time.Duration(c)*10*time.Millisecond, nil
}
