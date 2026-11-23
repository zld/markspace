package subtitle

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"mediqc/internal/fault"
)

// ParseVTT parses a WebVTT stream into a Timeline.
func ParseVTT(r io.Reader) (*Timeline, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNo := 0
	next := func() (string, bool) {
		if sc.Scan() {
			lineNo++
			return strings.TrimRight(sc.Text(), "\r"), true
		}
		return "", false
	}
	header, ok := next()
	if !ok || !strings.HasPrefix(header, "WEBVTT") {
		return nil, fault.New(fault.CodeBadTimecode, lineNo, -1,
			"missing WEBVTT header", "file must start with WEBVTT")
	}
	tl := &Timeline{Format: "vtt"}
	idx := 0
	for {
		line, ok := next()
		if !ok {
			break
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Optional cue identifier: a line without "-->" precedes the timing.
		timing := line
		if !strings.Contains(line, "-->") {
			timing, ok = next()
			if !ok {
				return nil, fault.New(fault.CodeTruncatedInput, lineNo, -1,
					"cue identifier without timing line",
					"re-deliver the complete subtitle file")
			}
		}
		start, end, err := parseVTTTiming(lineNo, timing)
		if err != nil {
			return nil, err
		}
		if err := checkRange(lineNo, start, end); err != nil {
			return nil, err
		}
		var text []string
		for {
			l, ok := next()
			if !ok || strings.TrimSpace(l) == "" {
				if !ok && len(text) == 0 {
					return nil, fault.New(fault.CodeTruncatedInput, lineNo, -1,
						"cue with no payload before EOF",
						"re-deliver the complete subtitle file")
				}
				break
			}
			text = append(text, l)
		}
		idx++
		tl.Cues = append(tl.Cues, Cue{Index: idx, Start: start, End: end,
			Text: strings.Join(text, "\n")})
	}
	if err := sc.Err(); err != nil {
		return nil, fault.New(fault.CodeTruncatedInput, lineNo, -1,
			"read error: "+err.Error(), "check the input file")
	}
	return tl, nil
}

// parseVTTTiming parses "HH:MM:SS.mmm --> HH:MM:SS.mmm" (hours optional).
func parseVTTTiming(line int, s string) (int64, int64, error) {
	parts := strings.Split(s, "-->")
	if len(parts) != 2 {
		return 0, 0, fault.New(fault.CodeBadTimecode, line, -1,
			"malformed timing line "+s, "want 'MM:SS.mmm --> MM:SS.mmm'")
	}
	parse := func(raw string) (int64, error) {
		raw = strings.TrimSpace(raw)
		// Drop cue settings after the end timestamp.
		if i := strings.IndexAny(raw, " \t"); i >= 0 {
			raw = raw[:i]
		}
		var hh, mm, ss, ms int
		var n int
		var err error
		if strings.Count(raw, ":") == 2 {
			n, err = fmt.Sscanf(raw, "%d:%d:%d.%d", &hh, &mm, &ss, &ms)
		} else {
			n, err = fmt.Sscanf(raw, "%d:%d.%d", &mm, &ss, &ms)
		}
		if err != nil || n != 3 && n != 4 {
			return 0, fault.New(fault.CodeBadTimecode, line, -1,
				"malformed timestamp "+raw, "want [HH:]MM:SS.mmm")
		}
		return hmsToTicks(line, hh, mm, ss, ms, raw)
	}
	start, err := parse(parts[0])
	if err != nil {
		return 0, 0, err
	}
	end, err := parse(parts[1])
	if err != nil {
		return 0, 0, err
	}
	return start, end, nil
}
