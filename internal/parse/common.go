// Package parse implements hand-written SRT, WebVTT and ASS parsers. No
// third-party subtitle library is used; every grammar rule lives here so that
// error line numbers stay under our control.
package parse

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"subsync/internal/model"
)

// Parser converts decoded UTF-8 text into the unified Document model.
type Parser interface {
	Format() model.Format
	Parse(text, track string, mode model.ParseMode) (*model.Document, error)
}

// ForFormat returns the parser for a supported format.
func ForFormat(f model.Format) (Parser, error) {
	switch f {
	case model.FormatSRT:
		return srtParser{}, nil
	case model.FormatVTT:
		return vttParser{}, nil
	case model.FormatASS:
		return assParser{}, nil
	default:
		return nil, fmt.Errorf("no parser registered for format %q", f)
	}
}

// parseTimestamp accepts "HH:MM:SS.mmm" (SRT/VTT, 3 fractional digits) and
// "H:MM:SS.cc" (ASS, 2 digits). Either separator '.' or ',' is accepted.
func parseTimestamp(token string) (time.Duration, error) {
	parts := strings.FieldsFunc(token, func(r rune) bool { return r == ':' })
	if len(parts) != 3 {
		return 0, fmt.Errorf("bad timestamp %q", token)
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("bad hours in %q", token)
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return 0, fmt.Errorf("bad minutes in %q", token)
	}
	secParts := strings.FieldsFunc(parts[2], func(r rune) bool { return r == '.' || r == ',' })
	if len(secParts) != 1 && len(secParts) != 2 {
		return 0, fmt.Errorf("bad seconds in %q", token)
	}
	s, err := strconv.Atoi(secParts[0])
	if err != nil || s < 0 || s > 59 {
		return 0, fmt.Errorf("bad seconds in %q", token)
	}
	var fracNS int64
	if len(secParts) == 2 {
		digits := secParts[1]
		if len(digits) > 9 {
			return 0, fmt.Errorf("fractional seconds too precise in %q", token)
		}
		n, err := strconv.Atoi(digits)
		if err != nil {
			return 0, fmt.Errorf("bad fractional seconds in %q", token)
		}
		// Normalize any digit count to nanoseconds.
		for i := len(digits); i < 9; i++ {
			n *= 10
		}
		fracNS = int64(n)
	}
	return time.Duration(h)*time.Hour +
		time.Duration(m)*time.Minute +
		time.Duration(s)*time.Second +
		time.Duration(fracNS), nil
}

// splitLines normalizes CRLF/CR input into clean LF lines while remembering
// the 1-based line number of each element.
func splitLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.Split(text, "\n")
}

// blank reports whether a line contains only whitespace.
func blank(s string) bool { return strings.TrimSpace(s) == "" }

// errf builds a line-anchored parse error.
func errf(line int, format string, args ...any) *model.ParseError {
	return &model.ParseError{Line: line, Msg: fmt.Sprintf(format, args...)}
}
