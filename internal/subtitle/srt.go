package subtitle

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"mediqc/internal/fault"
)

// ParseSRT parses an SRT stream into a Timeline. Every error carries the
// 1-based source line and a fault code.
func ParseSRT(r io.Reader) (*Timeline, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	tl := &Timeline{Format: "srt"}
	lineNo := 0
	next := func() (string, bool) {
		if sc.Scan() {
			lineNo++
			return strings.TrimRight(sc.Text(), "\r"), true
		}
		return "", false
	}
	for {
		line, ok := next()
		if !ok {
			break
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Sequence number.
		var idx int
		if _, err := fmt.Sscanf(strings.TrimSpace(line), "%d", &idx); err != nil {
			return nil, fault.New(fault.CodeBadTimecode, lineNo, -1,
				"expected cue sequence number, got "+line,
				"SRT cues start with a numeric index")
		}
		// Timing line.
		timing, ok := next()
		if !ok {
			return nil, fault.New(fault.CodeTruncatedInput, lineNo, -1,
				fmt.Sprintf("cue %d: missing timing line", idx),
				"re-deliver the complete subtitle file")
		}
		start, end, err := parseSRTTiming(lineNo, timing)
		if err != nil {
			return nil, err
		}
		if err := checkRange(lineNo, start, end); err != nil {
			return nil, err
		}
		// Payload lines until blank or EOF.
		var text []string
		for {
			l, ok := next()
			if !ok || strings.TrimSpace(l) == "" {
				if !ok && len(text) == 0 {
					return nil, fault.New(fault.CodeTruncatedInput, lineNo, -1,
						fmt.Sprintf("cue %d: no payload before EOF", idx),
						"re-deliver the complete subtitle file")
				}
				break
			}
			text = append(text, l)
		}
		tl.Cues = append(tl.Cues, Cue{Index: idx, Start: start, End: end,
			Text: strings.Join(text, "\n")})
	}
	if err := sc.Err(); err != nil {
		return nil, fault.New(fault.CodeTruncatedInput, lineNo, -1,
			"read error: "+err.Error(), "check the input file")
	}
	return tl, nil
}

// parseSRTTiming parses "HH:MM:SS,mmm --> HH:MM:SS,mmm".
func parseSRTTiming(line int, s string) (int64, int64, error) {
	parts := strings.Split(s, "-->")
	if len(parts) != 2 {
		return 0, 0, fault.New(fault.CodeBadTimecode, line, -1,
			"malformed timing line "+s, "want 'HH:MM:SS,mmm --> HH:MM:SS,mmm'")
	}
	parse := func(raw string) (int64, error) {
		raw = strings.TrimSpace(raw)
		var hh, mm, ss, ms int
		n, err := fmt.Sscanf(raw, "%d:%d:%d,%d", &hh, &mm, &ss, &ms)
		if err != nil || n != 4 {
			return 0, fault.New(fault.CodeBadTimecode, line, -1,
				"malformed timestamp "+raw, "want HH:MM:SS,mmm")
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
