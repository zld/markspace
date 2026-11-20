package format

import (
	"fmt"
	"regexp"
	"strings"

	"subx/internal/cue"
)

// VTT timestamps allow optional hours: [HH:]MM:SS.mmm
// 1:hh(opt) 2:mm mm 3:ss 4:ms | 5:hh 6:mm 7:ss 8:ms 9:settings
var vttTimingRe = regexp.MustCompile(`^(?:(\d+):)?(\d{2}):(\d{2})\.(\d{3})\s+-->\s+(?:(\d+):)?(\d{2}):(\d{2})\.(\d{3})(?:[ \t]+(.*))?$`)

// ParseVTT parses a WebVTT file. NOTE comment blocks are skipped; every
// other malformed block aborts parsing with a file/line error.
func ParseVTT(text, file string) ([]cue.Cue, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "WEBVTT") {
		return nil, &ParseError{file, 1, "missing WEBVTT header"}
	}
	if rest := strings.TrimPrefix(lines[0], "WEBVTT"); rest != "" && !strings.HasPrefix(rest, " ") && !strings.HasPrefix(rest, "\t") {
		return nil, &ParseError{file, 1, "malformed WEBVTT header"}
	}
	var cues []cue.Cue
	i := 1
	for i < len(lines) {
		line := lines[i]
		i++
		if strings.TrimSpace(line) == "" {
			continue
		}
		if line == "NOTE" || strings.HasPrefix(line, "NOTE ") || strings.HasPrefix(line, "NOTE\t") {
			for i < len(lines) && strings.TrimSpace(lines[i]) != "" {
				i++
			}
			continue
		}
		var c cue.Cue
		timing := line
		timingLn := i
		if !strings.Contains(line, "-->") {
			c.ID = line
			if i >= len(lines) {
				return nil, &ParseError{file, i, "unexpected end of file after cue identifier"}
			}
			timing = lines[i]
			i++
			timingLn = i
		}
		m := vttTimingRe.FindStringSubmatch(timing)
		if m == nil {
			return nil, &ParseError{file, timingLn, fmt.Sprintf("expected timing line '[HH:]MM:SS.mmm --> [HH:]MM:SS.mmm', got %q", timing)}
		}
		start, end, err := parseVTTTiming(m)
		if err != nil {
			return nil, &ParseError{file, timingLn, err.Error()}
		}
		c.StartMs, c.EndMs, c.Settings = start, end, m[9]
		for i < len(lines) && strings.TrimSpace(lines[i]) != "" {
			c.Lines = append(c.Lines, lines[i])
			i++
		}
		if err := c.Validate(); err != nil {
			return nil, &ParseError{file, timingLn, err.Error()}
		}
		cues = append(cues, c)
	}
	if len(cues) == 0 {
		return nil, &ParseError{file, 1, "no cues found"}
	}
	return cues, nil
}

func parseVTTTiming(m []string) (int64, int64, error) {
	var h, mm, s, ms int64
	fmt.Sscanf(m[1], "%d", &h)
	fmt.Sscanf(m[2]+":"+m[3]+":"+m[4], "%d:%d:%d", &mm, &s, &ms)
	start, err := cue.FromParts(h, mm, s, ms)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid start time: %v", err)
	}
	h = 0
	fmt.Sscanf(m[5], "%d", &h)
	fmt.Sscanf(m[6]+":"+m[7]+":"+m[8], "%d:%d:%d", &mm, &s, &ms)
	end, err := cue.FromParts(h, mm, s, ms)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid end time: %v", err)
	}
	return start, end, nil
}

// SerializeVTT renders cues as WebVTT, preserving identifiers and settings.
func SerializeVTT(cues []cue.Cue) []byte {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for _, c := range cues {
		if c.ID != "" {
			b.WriteString(c.ID)
			b.WriteByte('\n')
		}
		b.WriteString(cue.FormatVTT(c.StartMs) + " --> " + cue.FormatVTT(c.EndMs))
		if c.Settings != "" {
			b.WriteByte(' ')
			b.WriteString(c.Settings)
		}
		b.WriteByte('\n')
		for _, l := range c.Lines {
			b.WriteString(l)
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	return []byte(b.String())
}
