package parser

import (
	"strings"
	"time"

	"subtitle/internal/cue"
)

// ParseSRT parses SubRip text. Cues are separated by blank lines; the numeric
// index line is optional in practice but, when present, must be numeric.
func ParseSRT(path string, text string) (*cue.Document, error) {
	lines := splitLines(text)
	doc := &cue.Document{Format: cue.FormatSRT}
	i := 0
	for i < len(lines) {
		if strings.TrimSpace(lines[i]) == "" {
			i++
			continue
		}
		blockStart := i + 1
		// Optional numeric index.
		id := ""
		if idx := strings.TrimSpace(lines[i]); isDigits(idx) {
			id = idx
			i++
			if i >= len(lines) {
				return nil, fail(path, blockStart, "dangling cue index %q", id)
			}
		}
		timeLine := i + 1
		start, end, err := parseSRTTiming(lines[i])
		if err != nil {
			return nil, fail(path, timeLine, "%v", err)
		}
		i++
		var body []string
		for i < len(lines) && strings.TrimSpace(lines[i]) != "" {
			body = append(body, lines[i])
			i++
		}
		if len(body) == 0 {
			return nil, fail(path, timeLine, "cue has no text")
		}
		doc.Cues = append(doc.Cues, cue.Cue{
			ID:         id,
			Start:      start,
			End:        end,
			Text:       strings.Join(body, "\n"),
			SourceLine: blockStart,
		})
	}
	return doc, nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parseSRTTiming parses "00:00:01,000 --> 00:00:02,500" and any trailing
// VTT-style settings, which are ignored by SRT but tolerated here.
func parseSRTTiming(line string) (time.Duration, time.Duration, error) {
	arrow := strings.Index(line, "-->")
	if arrow == -1 {
		return 0, 0, &parseMsg{msg: "missing \"-->\" timing separator"}
	}
	left := strings.TrimSpace(line[:arrow])
	right := strings.TrimSpace(line[arrow+3:])
	// Drop settings after the end timestamp (VTT cue settings, SRT position).
	if sp := strings.IndexAny(right, " \t"); sp >= 0 {
		right = right[:sp]
	}
	right = strings.TrimSpace(right)
	start, err := parseSRTTime(left)
	if err != nil {
		return 0, 0, err
	}
	end, err := parseSRTTime(right)
	if err != nil {
		return 0, 0, err
	}
	return start, end, nil
}

type parseMsg struct{ msg string }

func (e *parseMsg) Error() string { return e.msg }
