package format

import (
	"fmt"
	"regexp"
	"strings"

	"subx/internal/cue"
)

// 1:hh 2:mm 3:ss 4:ms  5:hh 6:mm 7:ss 8:ms 9:trailing
var srtTimingRe = regexp.MustCompile(`^(\d+):(\d{2}):(\d{2})[,.](\d{3})\s+-->\s+(\d+):(\d{2}):(\d{2})[,.](\d{3})\s*$`)

// ParseSRT parses SubRip text. Blocks are separated by blank lines; a
// numeric index line is optional. Malformed blocks are never skipped:
// they abort parsing with a file/line error.
func ParseSRT(text, file string) ([]cue.Cue, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var cues []cue.Cue
	i := 0
	next := func() (string, int, bool) { // logical line, 1-based number, ok
		if i >= len(lines) {
			return "", 0, false
		}
		s := lines[i]
		i++
		return s, i, true
	}
	for {
		line, ln, ok := next()
		if !ok {
			break
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		var c cue.Cue
		if isIndexLine(line) {
			c.ID = strings.TrimSpace(line)
			line, ln, ok = next()
			if !ok {
				return nil, &ParseError{file, ln - 1, "unexpected end of file after cue index"}
			}
		}
		m := srtTimingRe.FindStringSubmatch(line)
		if m == nil {
			return nil, &ParseError{file, ln, fmt.Sprintf("expected timing line 'HH:MM:SS,mmm --> HH:MM:SS,mmm', got %q", line)}
		}
		start, end, err := parseTimingParts(m)
		if err != nil {
			return nil, &ParseError{file, ln, err.Error()}
		}
		c.StartMs, c.EndMs = start, end
		for {
			line, _, ok = next()
			if !ok || strings.TrimSpace(line) == "" {
				break
			}
			c.Lines = append(c.Lines, line)
		}
		if err := c.Validate(); err != nil {
			return nil, &ParseError{file, ln, err.Error()}
		}
		cues = append(cues, c)
		if !ok {
			break
		}
	}
	if len(cues) == 0 {
		return nil, &ParseError{file, 1, "no cues found"}
	}
	return cues, nil
}

func isIndexLine(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 12 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func parseTimingParts(m []string) (int64, int64, error) {
	var h, mm, s, ms int64
	fmt.Sscanf(m[1]+":"+m[2]+":"+m[3]+":"+m[4], "%d:%d:%d:%d", &h, &mm, &s, &ms)
	start, err := cue.FromParts(h, mm, s, ms)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid start time: %v", err)
	}
	fmt.Sscanf(m[5]+":"+m[6]+":"+m[7]+":"+m[8], "%d:%d:%d:%d", &h, &mm, &s, &ms)
	end, err := cue.FromParts(h, mm, s, ms)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid end time: %v", err)
	}
	return start, end, nil
}

// SerializeSRT renders cues as SubRip with sequential 1-based numbering.
func SerializeSRT(cues []cue.Cue) []byte {
	var b strings.Builder
	for i, c := range cues {
		fmt.Fprintf(&b, "%d\n%s --> %s\n", i+1, cue.FormatSRT(c.StartMs), cue.FormatSRT(c.EndMs))
		for _, l := range c.Lines {
			b.WriteString(l)
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	return []byte(b.String())
}
