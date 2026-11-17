package parser

import (
	"strings"

	"subtitle/internal/cue"
)

// ParseVTT parses WebVTT. It accepts cue identifiers (which may themselves
// look like text), per-cue settings after the timing line, NOTE/REGION/STYLE
// blocks and multi-line cue text.
func ParseVTT(path string, text string) (*cue.Document, error) {
	lines := splitLines(text)
	if len(lines) == 0 || !strings.HasPrefix(strings.TrimSpace(lines[0]), "WEBVTT") {
		return nil, fail(path, 1, "missing WEBVTT file header")
	}
	doc := &cue.Document{Format: cue.FormatVTT}
	i := 1
	for i < len(lines) {
		// Skip blank lines and block headers such as REGION / STYLE.
		for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
			i++
		}
		if i >= len(lines) {
			break
		}
		header := strings.TrimSpace(lines[i])
		if strings.HasPrefix(header, "NOTE") || strings.HasPrefix(header, "REGION") ||
			strings.HasPrefix(header, "STYLE") {
			i++
			for i < len(lines) && strings.TrimSpace(lines[i]) != "" {
				i++
			}
			continue
		}
		blockStart := i + 1
		id := ""
		if !strings.Contains(lines[i], "-->") {
			// A cue identifier line: anything that is not a timing line.
			id = header
			i++
			if i >= len(lines) {
				return nil, fail(path, blockStart, "cue identifier %q has no timing line", id)
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
